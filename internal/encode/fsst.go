package encode

import (
	"encoding/binary"
	"math"
	"math/bits"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// FSST (Fast Static Symbol Table): a table of at most 255 symbols of 1 to 8 bytes, trained on
// the strings of a column, and each string written as the codes of the symbols that cover it,
// the longest first, a byte that no symbol covers being an escape (255) and the byte itself.
//
// The training is the generational algorithm of the paper as the fsst-rs crate (0.7.0) runs
// it: 5 rounds that compress a sample of the strings with the current table, count each symbol
// and each pair of symbols, and make the next table of the best candidates (the symbols and the
// concatenations of two that follow each other). The details that decide which table comes out
// are the ones of the crate, down to the order of the codes; the encoding of a string with a
// given table is the one of its compressor, which a lossy hash table of the symbols of 3
// bytes and more makes (a table of the crate has no two symbols that collide).

const (
	fsstMaxSymbols = 255
	fsstLenShift   = 12
	fsstCodeBase   = 256
	fsstUnused     = 511 + 1<<fsstLenShift // the code of a byte that no symbol covers
	fsstHashSlots  = 1 << 11
	fsstSampleSize = 1 << 14 // the sample of a column too large to be read in full
	fsstSampleLine = 512     // the bytes the sample takes of a string
	fsstMaxEscape  = 2       // the output bytes of an input byte at the worst
)

// fsstGenerations is the share of the sample, out of 128, the rounds of the training read.
var fsstGenerations = [5]int{8, 38, 68, 98, 128}

type fsstEntry struct {
	sym     uint64
	code    uint16
	ignored uint16
}

type fsstCand struct {
	sym  uint64
	gain int
}

// fsstState is what the training and the encoding of FSST need, in the memory of an Encoder.
type fsstState struct {
	c2     []int32 // the counts of the pairs of codes, 512 by 512
	cands  []fsstCand
	table  []int32
	lines  [][]byte
	sample [][]byte

	// the table of the encoder: the symbols and their lengths by code, and the lookups
	sym [fsstMaxSymbols]uint64
	two [1 << 16]uint16
	pht [fsstHashSlots]fsstEntry

	// the table being built, as the training makes it
	bsym  [511]uint64
	bone  [256]uint16
	btwo  [1 << 16]uint16
	bpht  [fsstHashSlots]fsstEntry
	bhist [8]int

	// the counts of the training
	c1    [512]int32
	idx1  [8]uint64
	pairs [512][8]uint64

	n         int
	bn        int
	length    [fsstMaxSymbols]uint8
	hasSuffix uint8
}

func fsstHash(v uint64) uint64 { return v*2971215073 ^ v>>15 }

func symLen(s uint64) int {
	if l := 8 - bits.LeadingZeros64(s)>>3; l > 0 {
		return l
	}
	return 1
}

func newSymbolCode(code uint8, n int) uint16 { return uint16(code) + uint16(n)<<fsstLenShift }

func newBuildingCode(code, n int) uint16 { return uint16(code) + 256 + uint16(n)<<fsstLenShift }

func newEscapeCode(b byte) uint16 { return uint16(b) + 1<<fsstLenShift }

// wordAt is the 8 bytes of b from i, little-endian, the ones past the end being 0.
func wordAt(b []byte, i int) uint64 {
	if i+8 <= len(b) {
		return binary.LittleEndian.Uint64(b[i:])
	}
	var w uint64
	for k := 0; i+k < len(b); k++ {
		w |= uint64(b[i+k]) << (8 * k)
	}
	return w
}

// lineSet is the strings or the byte slices of a column.
type lineSet struct {
	bins [][]byte
	strs []string
}

func (l lineSet) n() int {
	if l.bins != nil {
		return len(l.bins)
	}
	return len(l.strs)
}

func (l lineSet) get(i int) []byte {
	if l.bins != nil {
		return l.bins[i]
	}
	return unsafex.StringBytes(l.strs[i])
}

func (e *Encoder) fsstState() *fsstState {
	if e.fs == nil {
		e.fs = fsstStates.Get().(*fsstState)
	}
	return e.fs
}

// reset empties the table being built: the code of a byte is the escape of the byte.
func (s *fsstState) reset() {
	for b := range 256 {
		s.bsym[b] = uint64(b)
		s.bone[b] = newEscapeCode(byte(b))
	}
	clear(s.bsym[256:])
	for hi := range 256 {
		copy(s.btwo[hi<<8:], s.bone[:])
	}
	for i := range s.bpht {
		s.bpht[i] = fsstEntry{code: fsstUnused, ignored: 64}
	}
	s.bhist = [8]int{}
	s.bn = 0
}

// insert adds a symbol at the end of the table being built. A symbol of 3 bytes or more that
// lands on the slot of another is refused.
func (s *fsstState) insert(sym uint64, n int) bool {
	switch n {
	case 2:
		s.btwo[sym&0xffff] = newBuildingCode(s.bn, 2)
	case 1:
		s.bone[sym&0xff] = newBuildingCode(s.bn, 1)
	default:
		slot := &s.bpht[fsstHash(sym&0xffffff)&(fsstHashSlots-1)]
		if slot.code != fsstUnused {
			return false
		}
		*slot = fsstEntry{sym, newBuildingCode(s.bn, n), uint16(64 - 8*n)}
	}
	s.bhist[n-1]++
	s.bsym[256+s.bn] = sym
	s.bn++
	return true
}

// clearBuilt removes the symbols from the table being built.
func (s *fsstState) clearBuilt() {
	for code := range 256 + s.bn {
		sym := s.bsym[code]
		switch symLen(sym) {
		case 1:
			s.bone[sym&0xff] = newEscapeCode(byte(sym))
		case 2:
			s.btwo[sym&0xffff] = newEscapeCode(byte(sym))
		default:
			s.bpht[fsstHash(sym&0xffffff)&(fsstHashSlots-1)].code = fsstUnused
		}
	}
	s.bhist = [8]int{}
	s.bn = 0
}

// longest is the code of the longest symbol the table being built has at the start of word.
func (s *fsstState) longest(word uint64) uint16 {
	e := &s.bpht[fsstHash(word&0xffffff)&(fsstHashSlots-1)]
	if e.code != fsstUnused && word&(math.MaxUint64>>e.ignored) == e.sym {
		return e.code
	}
	if t := s.btwo[word&0xffff]; t&511 >= fsstCodeBase {
		return t
	}
	return s.bone[word&0xff]
}

func (s *fsstState) count1(c uint16) {
	if s.idx1[c>>6]>>(c&63)&1 == 1 {
		s.c1[c]++
	} else {
		s.c1[c] = 1
		s.idx1[c>>6] |= 1 << (c & 63)
	}
}

func (s *fsstState) count2(a, b uint16) {
	i := int(a)<<9 + int(b)
	if s.pairs[a][b>>6]>>(b&63)&1 == 1 {
		s.c2[i]++
	} else {
		s.c2[i] = 1
		s.pairs[a][b>>6] |= 1 << (b & 63)
	}
}

func (s *fsstState) clearCounts() {
	s.idx1 = [8]uint64{}
	for i := range s.pairs {
		s.pairs[i] = [8]uint64{}
	}
}

// countLine compresses a string with the table being built and counts the codes it uses and
// the pairs of them that follow each other.
func (s *fsstState) countLine(line []byte) {
	prev := uint16(511)
	for pos := 0; pos < len(line); {
		code := s.longest(wordAt(line, pos))
		ext := code & 511
		s.count1(ext)
		s.count2(prev, ext)
		if n := int(code >> fsstLenShift); n > 1 {
			first := uint16(s.bsym[ext] & 0xff)
			s.count1(first)
			s.count2(prev, first)
		}
		pos += int(code >> fsstLenShift)
		prev = ext
	}
}

// each calls f for the codes set in a bitmap of 512 codes, ascending, up to 510.
func each(m *[8]uint64, f func(code uint16)) {
	for i, w := range m {
		for w != 0 {
			code := uint16(i*64 + bits.TrailingZeros64(w))
			w &= w - 1
			if code < 511 {
				f(code)
			}
		}
	}
}

// addCandidate adds the gain of a symbol to the candidates.
func (s *fsstState) addCandidate(sym uint64, gain int) {
	if 2*len(s.cands) >= len(s.table) {
		s.table = make([]int32, max(4096, 2*len(s.table)))
		for i, c := range s.cands {
			s.place(i, c.sym)
		}
	}
	mask := uint64(len(s.table) - 1)
	for h := fsstHash(sym) & mask; ; h = (h + 1) & mask {
		switch c := s.table[h]; {
		case c == 0:
			s.cands = append(s.cands, fsstCand{sym, gain})
			s.table[h] = int32(len(s.cands))
			return
		case s.cands[c-1].sym == sym:
			s.cands[c-1].gain += gain
			return
		}
	}
}

func (s *fsstState) place(i int, sym uint64) {
	mask := uint64(len(s.table) - 1)
	h := fsstHash(sym) & mask
	for s.table[h] != 0 {
		h = (h + 1) & mask
	}
	s.table[h] = int32(i + 1)
}

// optimize makes the next table out of the counts: the symbols seen often enough, and what
// the pairs of them make, best gain first. On the last round, over a sample that is the whole
// column, a symbol that does not save more than it costs is left out.
func (s *fsstState) optimize(share int, prune bool) {
	s.cands = s.cands[:0]
	clear(s.table)
	minCount := 5 * share / 128
	if prune {
		minCount = 1
	}
	each(&s.idx1, func(code1 uint16) {
		sym1 := s.bsym[code1]
		len1 := symLen(sym1)
		count := int(s.c1[code1])
		if count < minCount {
			return
		}
		gain := count * len1
		if len1 == 1 {
			gain *= 8 // fewer exceptions
		}
		s.addCandidate(sym1, gain)
		if share >= 128 || len1 == 8 {
			return
		}
		each(&s.pairs[code1], func(code2 uint16) {
			sym2 := s.bsym[code2]
			if len1+symLen(sym2) > 8 {
				return
			}
			merged := sym1 | sym2<<(8*len1)
			s.addCandidate(merged, int(s.c2[int(code1)<<9+int(code2)])*symLen(merged))
		})
	})
	// The candidates are taken best first, from a heap as the crate does: the table is made of
	// the first few hundred of thousands of them, which a sort would order in full.
	h := s.cands
	for i := len(h)/2 - 1; i >= 0; i-- {
		siftDown(h, i)
	}
	s.clearBuilt()
	inserted := 0
	for len(h) > 0 && inserted < fsstMaxSymbols {
		c := h[0]
		last := len(h) - 1
		h[0] = h[last]
		h = h[:last]
		siftDown(h, 0)
		n := symLen(c.sym)
		if prune {
			saves := c.gain
			if n == 1 {
				saves /= 8 // the boost of the single bytes
			}
			if saves <= n+1 {
				continue
			}
		}
		if s.insert(c.sym, n) {
			inserted++
		}
	}
}

// better says whether candidate a comes before b: the best gain first, then the longest
// symbol, then the largest. No two candidates have the same symbol.
func better(a, b fsstCand) bool {
	if a.gain != b.gain {
		return a.gain > b.gain
	}
	if la, lb := symLen(a.sym), symLen(b.sym); la != lb {
		return la > lb
	}
	return a.sym > b.sym
}

// siftDown moves the candidate at i of the heap h down to its place: each one comes before
// its children.
func siftDown(h []fsstCand, i int) {
	for {
		best := i
		if l := 2*i + 1; l < len(h) && better(h[l], h[best]) {
			best = l
		}
		if r := 2*i + 2; r < len(h) && better(h[r], h[best]) {
			best = r
		}
		if best == i {
			return
		}
		h[i], h[best] = h[best], h[i]
		i = best
	}
}

// makeSample is the strings the training reads: all of them when they are few, else pieces of
// at most 512 bytes of strings picked by a hash, up to 16 KB.
func (s *fsstState) makeSample(lines [][]byte, total int) [][]byte {
	if total < fsstSampleSize {
		return lines
	}
	sample := s.sample[:0]
	rnd := fsstHash(4637947)
	size := 0
	for size < fsstSampleSize {
		rnd = fsstHash(rnd)
		start := int(rnd % uint64(len(lines)))
		var line []byte
		for k := range lines {
			if l := lines[(start+k)%len(lines)]; len(l) > 0 {
				line = l
				break
			}
		}
		// a sample is taken from 16 KB at least: some line is not empty
		chunks := 1 + (len(line)-1)/fsstSampleLine
		rnd = fsstHash(rnd)
		chunk := fsstSampleLine * int(rnd%uint64(chunks))
		n := min(fsstSampleLine, len(line)-chunk)
		sample = append(sample, line[chunk:chunk+n])
		size += n
	}
	s.sample = sample
	return sample
}

// train makes the table of the strings: the symbols in the order of their codes.
func (s *fsstState) train(lines [][]byte) {
	s.reset()
	total := 0
	for _, l := range lines {
		total += len(l)
	}
	if len(lines) > 0 {
		sample := s.makeSample(lines, total)
		for _, share := range fsstGenerations {
			for i, line := range sample {
				if share < 128 && int(fsstHash(uint64(i))&127) > share {
					continue
				}
				s.countLine(line)
			}
			s.optimize(share, share >= 128 && total < fsstSampleSize)
			s.clearCounts()
		}
	}
	s.finalize()
}

// finalize orders the symbols as the crate does: by length, 2 (no longer symbol shares their
// first 2 bytes), 2 (one does), 3, 4 ... 8, then 1; and loads them.
func (s *fsstState) finalize() {
	var byLength [8]int
	byLength[0] = s.bn - s.bhist[0]
	for i := 1; i < 7; i++ {
		byLength[i+1] = byLength[i] + s.bhist[i]
	}
	noSuffix, suffixed := 0, byLength[2]
	for i := range s.bn {
		sym := s.bsym[256+i]
		n := symLen(sym)
		var code int
		if n == 2 {
			shared := false
			for k := range fsstMaxSymbols { // the table, stale entries included
				if k != i && s.bsym[256+k]&0xffff == sym&0xffff {
					shared = true
					break
				}
			}
			if shared {
				suffixed--
				code = suffixed
			} else {
				code = noSuffix
				noSuffix++
			}
		} else {
			code = byLength[n-1]
			byLength[n-1]++
		}
		s.sym[code], s.length[code] = sym, uint8(n)
	}
	s.n = s.bn
	s.load()
}

// load builds the lookups of the table in sym and length, n symbols: the 2 and 1 byte symbols
// in a table of the first 2 bytes, the longer ones in a hash table of their first 3. A symbol
// that lands on the slot of another is not used.
func (s *fsstState) load() {
	var one [256]uint16
	for i := range one {
		one[i] = fsstUnused
	}
	for i := range s.n {
		if s.length[i] == 1 {
			one[s.sym[i]&0xff] = newSymbolCode(uint8(i), 1)
		}
	}
	for hi := range 256 {
		copy(s.two[hi<<8:], one[:])
	}
	for i := range s.pht {
		s.pht[i] = fsstEntry{code: fsstUnused, ignored: 64}
	}
	for i := range s.n {
		sym := s.sym[i]
		switch n := int(s.length[i]); {
		case n == 2:
			s.two[sym&0xffff] = newSymbolCode(uint8(i), 2)
		case n > 2:
			if slot := &s.pht[fsstHash(sym&0xffffff)&(fsstHashSlots-1)]; slot.code == fsstUnused {
				*slot = fsstEntry{sym, newSymbolCode(uint8(i), n), uint16(64 - 8*n)}
			}
		}
	}
	// the codes of the 2-byte symbols that nothing longer extends, which need no probe
	s.hasSuffix = 0
	for int(s.hasSuffix) < s.n && s.length[s.hasSuffix] == 2 && !s.extended(int(s.hasSuffix)) {
		s.hasSuffix++
	}
}

// extended says whether a symbol of the table longer than 2 bytes starts with symbol i.
func (s *fsstState) extended(i int) bool {
	for j := range s.n {
		if s.length[j] > 2 && s.sym[j]&0xffff == s.sym[i]&0xffff {
			return true
		}
	}
	return false
}

// setTable loads a table: the symbols by code and their lengths, which must be the ones of the
// symbols. It returns false for a table that is not one.
func (s *fsstState) setTable(syms []uint64, lens []uint8) bool {
	if len(syms) != len(lens) || len(syms) > fsstMaxSymbols {
		return false
	}
	for i, sym := range syms {
		if int(lens[i]) != symLen(sym) {
			return false
		}
	}
	s.n = copy(s.sym[:], syms)
	copy(s.length[:], lens)
	s.load()
	return true
}

// compress writes the codes of src, which take at most twice its length, to out, and returns
// the bytes it wrote.
func (s *fsstState) compress(src, out []byte) int {
	o := 0
	for pos := 0; pos < len(src); {
		word := wordAt(src, pos)
		t := s.two[word&0xffff]
		if uint8(t) < s.hasSuffix { // no longer symbol to look for
			out[o] = uint8(t)
			o++
			pos += 2
			continue
		}
		p := &s.pht[fsstHash(word&0xffffff)&(fsstHashSlots-1)]
		if p.code != fsstUnused && word&(math.MaxUint64>>p.ignored) == p.sym {
			out[o] = uint8(p.code)
			o++
			pos += int(p.code >> fsstLenShift)
			continue
		}
		out[o] = uint8(t)
		o++
		if t == fsstUnused { // no symbol: an escape and the byte
			out[o] = byte(word)
			o++
		}
		pos += int(t >> fsstLenShift)
	}
	return o
}

// FSSTBytes is the FSST encoding of the byte slices of vals: the table trained on the valid
// ones, 3 buffers (the symbols, their lengths, the codes) and the children that say, for each
// row, the length of the string and where its codes end, then the validity.
func FSSTBytes(e *Encoder, vals [][]byte, nulls []bool) *Node {
	return e.fsstColumn(lineSet{bins: vals}, nulls, len(vals), true)
}

// FSSTStrings is FSSTBytes for strings.
func FSSTStrings(e *Encoder, vals []string, nulls []bool) *Node {
	return e.fsstColumn(lineSet{strs: vals}, nulls, len(vals), true)
}

// FSSTBytesWith is FSSTBytes with the table given, the symbols by code and their lengths
// (those of the crate's compressor): it returns false for a table that is not one.
func FSSTBytesWith(e *Encoder, vals [][]byte, nulls []bool, syms []uint64, lens []uint8) (*Node, bool) {
	if !e.fsstState().setTable(syms, lens) {
		return nil, false
	}
	return e.fsstColumn(lineSet{bins: vals}, nulls, len(vals), false), true
}

// FSSTStringsWith is FSSTBytesWith for strings.
func FSSTStringsWith(e *Encoder, vals []string, nulls []bool, syms []uint64, lens []uint8) (*Node, bool) {
	if !e.fsstState().setTable(syms, lens) {
		return nil, false
	}
	return e.fsstColumn(lineSet{strs: vals}, nulls, len(vals), false), true
}

// fsstColumn encodes the lines; when train is set it trains the table first, else the table
// is the one already loaded.
func (e *Encoder) fsstColumn(ls lineSet, nulls []bool, rows int, train bool) *Node {
	s := e.fsstState()
	defer e.done()
	if train {
		s.trainOn(ls, nulls)
	}
	total := 0
	for i := range rows {
		if !isNull(nulls, i) {
			total += len(ls.get(i))
		}
	}
	ow, optype := fsstOffsetWidth(total)
	ulen, offs := e.alloc(4*rows), e.alloc(ow*(rows+1))
	codes := e.allocDirty(fsstMaxEscape * total) // only the bytes written are kept
	o := 0
	for i := range rows {
		if !isNull(nulls, i) {
			line := ls.get(i)
			putWidth(ulen[4*i:], 4, uint64(len(line)))
			o += s.compress(line, codes[o:])
		}
		putWidth(offs[ow*(i+1):], ow, uint64(o))
	}
	syms64, lens8 := e.allocDirty(8*s.n), e.allocDirty(s.n)
	for i := range s.n {
		putWidth(syms64[8*i:], 8, s.sym[i])
		lens8[i] = s.length[i]
	}
	n := e.node(IDFSST)
	n.add(syms64, 8)
	n.add(lens8, 1)
	n.add(codes[:o:o], 1)
	n.kid(e.leaf(ulen, 4, nil, rows))
	n.kid(e.leaf(offs, ow, nil, rows+1))
	n.kid(e.validity(nulls, rows, rows))
	e.tmp = putUint(putUint(e.tmp[:0], 1, pI32), 2, uint64(optype))
	n.Meta = e.keep(e.tmp)
	return n
}

// fsstOffsetWidth is the bytes and the PType number of the offsets of the codes of total bytes
// of strings: i32, or i64 when the codes, at most twice as many bytes, could pass 2 GiB.
func fsstOffsetWidth(total int) (width, ptype int) {
	if fsstMaxEscape*total > math.MaxInt32 {
		return 8, pI64
	}
	return 4, pI32
}

// trainOn trains the table on the valid strings.
func (s *fsstState) trainOn(ls lineSet, nulls []bool) {
	s.lines = s.lines[:0]
	for i := range ls.n() {
		if !isNull(nulls, i) {
			s.lines = append(s.lines, ls.get(i))
		}
	}
	s.train(s.lines)
}

// EstimateFSSTBytes is the size of FSSTBytes.
func EstimateFSSTBytes(e *Encoder, sample [][]byte, nulls []bool, rows int) int {
	return e.estimateFSST(lineSet{bins: sample}, nulls, rows)
}

// EstimateFSSTStrings is the size of FSSTStrings.
func EstimateFSSTStrings(e *Encoder, sample []string, nulls []bool, rows int) int {
	return e.estimateFSST(lineSet{strs: sample}, nulls, rows)
}

// estimateFSST trains on the sample, compresses it and scales what comes out to the rows.
func (e *Encoder) estimateFSST(ls lineSet, nulls []bool, rows int) int {
	s := e.fsstState()
	defer e.done()
	s.trainOn(ls, nulls)
	n := ls.n()
	total, out := 0, 0
	for i := range n {
		if !isNull(nulls, i) {
			total += len(ls.get(i))
		}
	}
	e.tmp = grow(e.tmp, fsstMaxEscape*total)
	for i := range n {
		if !isNull(nulls, i) {
			out += s.compress(ls.get(i), e.tmp)
		}
	}
	ow, _ := fsstOffsetWidth(scale(total, n, rows))
	return scale(out, n, rows) + 4*rows + ow*(rows+1) + 9*s.n + validBytes(nulls, rows)
}
