package encode

import (
	"bytes"
	"math"
	"math/bits"
	"slices"
)

// OnPair: a dictionary of at most 4096 tokens of 1 to 16 bytes, trained on the strings of a
// column, and each string written as the codes (16 bits) of the longest tokens that cover it.
//
// It follows the onpair crate (0.3.0, Apache-2.0, the one vortex-onpair 0.93.0 calls with its
// DEFAULT_CONFIG): the 256 bytes are the first tokens, and a token is made by merging two
// tokens that follow each other in the strings often enough, a threshold that adapts so that
// the dictionary fills while a sample of 15% of the bytes is read. Then the tokens are sorted
// and the strings are written with the longest token at each position. The sample is not the
// crate's (its random generator is not ported), so the dictionary that comes out is not always
// the crate's; the encoding of a string with a given dictionary is.

const (
	onPairMaxBits   = 12 // DEFAULT_CONFIG.max_dict_bits
	onPairMaxTokens = 1 << onPairMaxBits
	onPairMaxToken  = 16   // MAX_TOKEN_SIZE
	onPairFraction  = 0.15 // DEFAULT_CONFIG.threshold: the share of the bytes the training reads
	// OnPairSampleFraction is the share of the bytes the training reads on the sample of a
	// compressor (SAMPLE_TRAINING_FRACTION of vortex-btrblocks): 15% of a sample of about a
	// thousand rows trains a dictionary too poor to rank OnPair against FSST.
	OnPairSampleFraction = 0.5
	onPairSeed           = 42 // DEFAULT_CONFIG.seed
	onPairSlots          = 4 * onPairMaxTokens
	onPairPairsMin       = 1 << 12
)

// onPairPair is a slot of the table of the pairs of tokens the training counts.
type onPairPair struct {
	key   uint32
	count uint8
	used  bool
}

// onPairState is what the training and the encoding of OnPair need, in the memory of an Encoder.
type onPairState struct {
	bytes  []byte       // the bytes of the tokens by id, end to end
	pairs  []onPairPair // the counts of the pairs of tokens the training sees
	order  []uint32     // the rows of the sample
	rows   [][]byte
	codes  []uint16 // the codes of the strings, and where the codes of each end
	ends32 []int

	pairCount int
	n         int
	rng       uint64
	w0, w1    [onPairMaxTokens]uint64 // the words of the bytes of each token
	ends      [onPairMaxTokens + 1]uint32
	lens      [onPairLensSlots]uint32 // bit l is set when a token of l bytes starts with the two bytes of the slot
	single    [256]uint16
	slots     [onPairSlots]uint16 // the id + 1 of the token of each slot of a hash of its words
	rank      [onPairMaxTokens]uint16
	perm      [onPairMaxTokens]uint16
	tlen      [onPairMaxTokens]uint8
}

func (e *Encoder) onPairState() *onPairState {
	if e.op == nil {
		e.op = onPairStates.Get().(*onPairState)
	}
	return e.op
}

// onPairLensSlots is the size of the table of the lengths of the tokens by their first two bytes.
const onPairLensSlots = 1 << 12

// onPairLensKey is the slot of the tokens that start with the bytes a and b.
func onPairLensKey(a, b byte) uint32 {
	return (uint32(a)<<8 | uint32(b)) * 2654435761 >> 20
}

func onPairHash(a, b uint64, l int) uint64 {
	h := a*0x9e3779b97f4a7c15 ^ b*0xc2b2ae3d27d4eb4f ^ uint64(l)*0x165667b19e3779f9
	h ^= h >> 32
	h *= 0xd6e8feb86659fd93
	return h ^ h>>29
}

// onPairWords is the bytes of b (at most 16) as two little-endian words, zero past the end.
func onPairWords(b []byte, l int) (uint64, uint64) {
	a, c := wordAt(b, 0), uint64(0)
	if l > 8 {
		c = wordAt(b, 8)
		return a, c & lowBytes(l-8)
	}
	return a & lowBytes(l), 0
}

// lowBytes is the mask of the first n (0 to 8) bytes of a word.
func lowBytes(n int) uint64 {
	if n >= 8 {
		return math.MaxUint64
	}
	return 1<<(8*n) - 1
}

// reset empties the dictionary and makes it the 256 bytes, in the order of their values.
func (s *onPairState) reset() {
	s.bytes = s.bytes[:0]
	s.n, s.lens = 0, [onPairLensSlots]uint32{}
	clear(s.slots[:])
	s.ends[0] = 0
	for b := range 256 {
		s.single[b] = uint16(b)
		s.add([]byte{byte(b)})
	}
}

// add appends a token, which is not in the dictionary, and returns its id.
func (s *onPairState) add(tok []byte) int {
	id, l := s.n, len(tok)
	s.n++
	s.bytes = append(s.bytes, tok...)
	s.ends[id+1] = uint32(len(s.bytes))
	s.tlen[id] = uint8(l)
	s.w0[id], s.w1[id] = onPairWords(tok, l)
	if l > 1 {
		s.lens[onPairLensKey(tok[0], tok[1])] |= 1 << l
	}
	i := onPairHash(s.w0[id], s.w1[id], l) & (onPairSlots - 1)
	for s.slots[i] != 0 {
		i = (i + 1) & (onPairSlots - 1)
	}
	s.slots[i] = uint16(id + 1)
	return id
}

func (s *onPairState) token(id int) []byte { return s.bytes[s.ends[id]:s.ends[id+1]] }

// match is the id and the length of the longest token that starts b, which is not empty.
func (s *onPairState) match(b []byte) (id, l int) {
	w0, w1 := wordAt(b, 0), uint64(0)
	if len(b) > 8 {
		w1 = wordAt(b, 8)
	}
	// the lengths of the tokens that start with these two bytes (or collide with them), up to the bytes left
	var cand uint32
	if len(b) > 1 {
		cand = s.lens[onPairLensKey(b[0], b[1])] & (1<<(min(len(b), onPairMaxToken)+1) - 1)
	}
	for cand != 0 {
		l = bits.Len32(cand) - 1
		cand &^= 1 << l
		a, c := w0, w1
		if l > 8 {
			c &= lowBytes(l - 8)
		} else {
			a, c = a&lowBytes(l), 0
		}
		for i := onPairHash(a, c, l) & (onPairSlots - 1); s.slots[i] != 0; i = (i + 1) & (onPairSlots - 1) {
			if t := int(s.slots[i]) - 1; int(s.tlen[t]) == l && s.w0[t] == a && s.w1[t] == c {
				return t, l
			}
		}
	}
	return int(s.single[b[0]]), 1
}

// increment counts the pair of tokens key and returns its count, which stops at 255.
func (s *onPairState) increment(key uint32) uint8 {
	if 2*s.pairCount >= len(s.pairs) {
		s.growPairs()
	}
	mask := len(s.pairs) - 1
	for i := int(onPairHash(uint64(key), 0, 0)) & mask; ; i = (i + 1) & mask {
		p := &s.pairs[i]
		if !p.used {
			*p = onPairPair{key: key, count: 1, used: true}
			s.pairCount++
			return 1
		}
		if p.key == key {
			if p.count < math.MaxUint8 {
				p.count++
			}
			return p.count
		}
	}
}

// forget resets the count of the pair key, which is in the table.
func (s *onPairState) forget(key uint32) {
	mask := len(s.pairs) - 1
	for i := int(onPairHash(uint64(key), 0, 0)) & mask; s.pairs[i].used; i = (i + 1) & mask {
		if s.pairs[i].key == key {
			s.pairs[i].count = 0
			return
		}
	}
}

func (s *onPairState) growPairs() {
	old := s.pairs
	size := max(2*len(old), onPairPairsMin)
	s.pairs = make([]onPairPair, size)
	for _, p := range old {
		if !p.used {
			continue
		}
		mask := size - 1
		i := int(onPairHash(uint64(p.key), 0, 0)) & mask
		for s.pairs[i].used {
			i = (i + 1) & mask
		}
		s.pairs[i] = p
	}
}

// onPairController adapts the threshold of the merges so that the dictionary fills while the
// sample is read: a merge is made at the second time a pair is seen, and the threshold goes up
// when the dictionary fills faster than the budget of bytes allows.
type onPairController struct {
	capacity, budget, interval     int
	threshold                      uint8
	created, scanned               int
	createdAtCheck, scannedAtCheck int
	nextCheck                      int
}

func newOnPairController(capacity, total int, fraction float64) onPairController {
	interval := max(capacity/128, 64)
	return onPairController{
		capacity: capacity, budget: int(float64(total) * fraction), interval: interval,
		threshold: 2, nextCheck: interval,
	}
}

func (c *onPairController) onEntry() {
	c.created++
	if c.created < c.nextCheck {
		return
	}
	recent := 1e9
	if db := c.scanned - c.scannedAtCheck; db > 0 {
		recent = float64(c.created-c.createdAtCheck) / float64(db)
	}
	eRem, bRem := 1, 1
	if c.capacity > c.created {
		eRem = c.capacity - c.created
	}
	if c.budget > c.scanned {
		bRem = c.budget - c.scanned
	}
	target := float64(eRem) / float64(bRem)
	ratio := 1e9
	if target > 0 {
		ratio = recent / target
	}
	if ratio > 2 && c.threshold < 255 {
		c.threshold++
	} else if ratio < 0.5 && c.threshold > 2 {
		c.threshold--
	}
	c.createdAtCheck, c.scannedAtCheck = c.created, c.scanned
	c.nextCheck = c.created + c.interval
}

func (s *onPairState) next() uint64 { // splitmix64
	s.rng += 0x9e3779b97f4a7c15
	z := s.rng
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	return z ^ z>>31
}

// sample picks the rows the training reads: a random part of the rows (twice the share of the
// bytes it reads, and 1024 more), cut where their bytes pass the budget. A column whose rows
// are skewed in length takes more rows until they hold the budget.
func (s *onPairState) sample(vals [][]byte, nulls []bool, total int, fraction float64) {
	n := len(vals)
	budget := int(float64(total) * fraction)
	k := min(int(min(2*fraction, 1)*float64(n))+1024, n)
	for {
		s.order = slices.Grow(s.order[:0], n)[:n]
		for i := range s.order {
			s.order[i] = uint32(i)
		}
		s.rng = onPairSeed
		for i := range k {
			j := i + int(s.next()%uint64(n-i))
			s.order[i], s.order[j] = s.order[j], s.order[i]
		}
		held := 0
		for _, r := range s.order[:k] {
			if !isNull(nulls, int(r)) {
				held += len(vals[r])
			}
		}
		if held > budget || k == n {
			break
		}
		k = min(2*k, n)
	}
	s.rows = s.rows[:0]
	cut := budget <= total/2
	held := 0
	for _, r := range s.order[:k] {
		if cut && held > budget {
			break
		}
		if isNull(nulls, int(r)) {
			s.rows = append(s.rows, nil)
			continue
		}
		s.rows = append(s.rows, vals[r])
		held += len(vals[r])
	}
}

// train makes the dictionary from the valid strings, which hold total bytes, reading the given
// share of them.
func (s *onPairState) train(vals [][]byte, nulls []bool, total int, fraction float64) {
	s.reset()
	s.sample(vals, nulls, total, fraction)
	ctrl := newOnPairController(onPairMaxTokens-256, total, fraction)
	threshold := ctrl.threshold
	if cap(s.pairs) == 0 {
		s.pairs = make([]onPairPair, onPairPairsMin)
	}
	clear(s.pairs)
	s.pairCount = 0
	for _, row := range s.rows {
		if len(row) == 0 {
			continue
		}
		prevID, prevLen := s.match(row)
		pos := prevLen
		if ctrl.scanned += prevLen; ctrl.scanned > ctrl.budget {
			break
		}
		for pos < len(row) {
			curID, curLen := s.match(row[pos:])
			if ctrl.scanned += curLen; ctrl.scanned > ctrl.budget {
				s.finish()
				return
			}
			if pairLen := prevLen + curLen; pairLen <= onPairMaxToken {
				key := uint32(prevID)<<16 | uint32(curID)
				if s.increment(key) >= threshold {
					id := s.add(row[pos-prevLen : pos+curLen])
					if s.n == onPairMaxTokens {
						s.finish()
						return
					}
					ctrl.onEntry()
					threshold = ctrl.threshold
					s.forget(key)
					prevID, prevLen = id, pairLen
					pos += curLen
					continue
				}
			}
			prevID, prevLen = curID, curLen
			pos += curLen
		}
	}
	s.finish()
}

// finish sorts the tokens and gives each id its rank.
func (s *onPairState) finish() {
	perm := s.perm[:s.n]
	for i := range perm {
		perm[i] = uint16(i)
	}
	slices.SortFunc(perm, func(a, b uint16) int { return bytes.Compare(s.token(int(a)), s.token(int(b))) })
	for r, id := range perm {
		s.rank[id] = uint16(r)
	}
}

// load makes the dictionary of the sorted tokens given by their bytes and ends (the first end
// being 0), which must be at most 4096 tokens of 1 to 16 bytes, with the 256 bytes.
func (s *onPairState) load(dict []byte, ends []uint32) bool {
	n := len(ends) - 1
	if n < 256 || n > onPairMaxTokens || ends[0] != 0 || int(ends[n]) > len(dict) {
		return false
	}
	s.bytes = s.bytes[:0]
	s.n, s.lens = 0, [onPairLensSlots]uint32{}
	clear(s.slots[:])
	s.ends[0] = 0
	var seen [256]bool
	for i := range n {
		if ends[i+1] <= ends[i] || ends[i+1]-ends[i] > onPairMaxToken {
			return false
		}
		tok := dict[ends[i]:ends[i+1]]
		if i > 0 && bytes.Compare(s.token(i-1), tok) >= 0 {
			return false
		}
		if len(tok) == 1 {
			s.single[tok[0]], seen[tok[0]] = uint16(i), true
		}
		s.add(tok)
		s.rank[i] = uint16(i)
	}
	return !slices.Contains(seen[:], false)
}

// parse writes the codes of the valid strings to s.codes and the end of each string's codes
// to s.ends32.
func (s *onPairState) parse(vals [][]byte, nulls []bool) {
	s.codes, s.ends32 = s.codes[:0], s.ends32[:0]
	for i, v := range vals {
		if !isNull(nulls, i) {
			for pos := 0; pos < len(v); {
				id, l := s.match(v[pos:])
				s.codes = append(s.codes, s.rank[id])
				pos += l
			}
		}
		s.ends32 = append(s.ends32, len(s.codes))
	}
}

// dictSize is the bytes of the dictionary, with the padding that lets the last token, in the
// order of the tokens, be read as 16 bytes.
func (s *onPairState) dictSize() int {
	last := s.perm[s.n-1]
	return max(len(s.bytes), len(s.bytes)-len(s.token(int(last)))+onPairMaxToken)
}

// OnPairBytes is the OnPair encoding of the byte slices of vals: the dictionary trained on the
// valid ones, in a buffer, and the children that give the offsets of the tokens, the codes, where
// the codes of each string end and its length, then the validity. A column where every row is
// null is a null constant.
func OnPairBytes(e *Encoder, vals [][]byte, nulls []bool) *Node {
	return OnPairBytesTrained(e, vals, nulls, onPairFraction)
}

// OnPairBytesTrained is OnPairBytes with a training that reads the given share of the bytes,
// OnPairSampleFraction for the sample of a compressor.
func OnPairBytesTrained(e *Encoder, vals [][]byte, nulls []bool, fraction float64) *Node {
	if allNull(nulls) {
		n, _ := ConstantBytes(e, vals, nulls, true)
		return n
	}
	s := e.onPairState()
	total := validBytesOf(vals, nulls)
	s.train(vals, nulls, total, fraction)
	return e.onPairColumn(vals, nulls)
}

// OnPairBytesWith is OnPairBytes with the dictionary given, the bytes of the tokens and their
// offsets (the ones of the reference's output): it returns false for a dictionary that is not
// one, which is sorted, has the 256 bytes and at most 4096 tokens of 1 to 16 bytes.
func OnPairBytesWith(e *Encoder, vals [][]byte, nulls []bool, dict []byte, offs []uint32) (*Node, bool) {
	s := e.onPairState()
	if !s.load(dict, offs) {
		return nil, false
	}
	s.perm = [onPairMaxTokens]uint16{}
	for i := range s.n {
		s.perm[i] = uint16(i)
	}
	return e.onPairColumn(vals, nulls), true
}

// onPairColumn parses the strings with the dictionary of the state and makes the node.
func (e *Encoder) onPairColumn(vals [][]byte, nulls []bool) *Node {
	s := e.op
	defer e.done()
	rows := len(vals)
	s.parse(vals, nulls)
	ptype, ow := narrow(uint64(len(s.codes)))
	ptype = max(ptype, pU32) // the reference writes u32, and u64 for more than u32 codes
	ow = max(ow, 4)
	// the dictionary is written in the order of the ranks
	dictBytes, dictOffs := e.alloc(s.dictSize()), e.alloc(4*(s.n+1))
	at := 0
	for r, id := range s.perm[:s.n] {
		at += copy(dictBytes[at:], s.token(int(id)))
		putWidth(dictOffs[4*(r+1):], 4, uint64(at))
	}
	codes := e.allocDirty(2 * len(s.codes))
	for i, c := range s.codes {
		putWidth(codes[2*i:], 2, uint64(c))
	}
	offs := e.alloc(ow * (rows + 1))
	ulen := e.alloc(4 * rows)
	for i, v := range vals {
		putWidth(offs[ow*(i+1):], ow, uint64(s.ends32[i]))
		if !isNull(nulls, i) {
			putWidth(ulen[4*i:], 4, uint64(len(v)))
		}
	}
	n := e.node(IDOnPair)
	n.add(dictBytes, 8)
	n.kid(e.leaf(dictOffs, 4, nil, s.n+1))
	n.kid(e.leaf(codes, 2, nil, len(s.codes)))
	n.kid(e.leaf(offs, ow, nil, rows+1))
	n.kid(e.leaf(ulen, 4, nil, rows))
	n.kid(e.validity(nulls, rows, rows))
	e.tmp = putUint(e.tmp[:0], 1, pU32)
	e.tmp = putUint(e.tmp, 3, uint64(s.n))
	e.tmp = putUint(e.tmp, 4, uint64(len(s.codes)))
	e.tmp = putUint(e.tmp, 5, pU32)
	e.tmp = putUint(e.tmp, 6, pU16)
	e.tmp = putUint(e.tmp, 7, uint64(ptype))
	n.Meta = e.keep(e.tmp)
	return n
}

// EstimateOnPairBytes is the size of OnPairBytes, from the sample of rows rows.
func EstimateOnPairBytes(e *Encoder, sample [][]byte, nulls []bool, rows int) int {
	if allNull(nulls) {
		return 2 // a null constant
	}
	s := e.onPairState()
	defer e.done()
	n := len(sample)
	total := validBytesOf(sample, nulls)
	s.train(sample, nulls, total, onPairFraction)
	s.parse(sample, nulls)
	_, ow := narrow(uint64(scale(len(s.codes), n, rows)))
	ow = max(ow, 4)
	return s.dictSize() + 4*(s.n+1) + 2*scale(len(s.codes), n, rows) + ow*(rows+1) + 4*rows + validBytes(nulls, rows)
}

// NarrowOnPair narrows the four integer children of n, an array of OnPairBytes, each to the
// smallest unsigned integer that holds its values, and writes their types in the metadata, as
// the OnPair scheme of the reference does before it compresses them.
func NarrowOnPair(e *Encoder, n *Node) {
	if n.ID != IDOnPair {
		return // a null constant
	}
	var ptypes [4]int
	for i, k := range n.Kids[:4] {
		b := k.Bufs[0]
		count := len(b.Data) / b.Align
		var top uint64
		for j := range count {
			top = max(top, getWidth(b.Data[j*b.Align:], b.Align))
		}
		ptype, w := narrow(top)
		ptypes[i] = ptype
		if w < b.Align {
			out := e.allocDirty(w * count)
			for j := range count {
				putWidth(out[j*w:], w, getWidth(b.Data[j*b.Align:], b.Align))
			}
			n.Kids[i] = e.leaf(out, w, nil, count)
		}
	}
	e.tmp = putUint(e.tmp[:0], 1, uint64(ptypes[3]))
	e.tmp = putUint(e.tmp, 3, uint64(len(n.Kids[0].Bufs[0].Data)/(1<<ptypes[0])-1))
	e.tmp = putUint(e.tmp, 4, uint64(len(n.Kids[1].Bufs[0].Data)/(1<<ptypes[1])))
	e.tmp = putUint(e.tmp, 5, uint64(ptypes[0]))
	e.tmp = putUint(e.tmp, 6, uint64(ptypes[1]))
	e.tmp = putUint(e.tmp, 7, uint64(ptypes[2]))
	n.Meta = e.keep(e.tmp)
}
