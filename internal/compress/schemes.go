package compress

import (
	"encoding/binary"
	"hash/maphash"
	"math/bits"
	"sync"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// cands is a list of schemes to try, in order: on a tie the first one wins.
type cands struct {
	s [10]scheme
	n int
}

// has says whether s is in the list.
func (l *cands) has(s scheme) bool {
	for _, t := range l.s[:l.n] {
		if t == s {
			return true
		}
	}
	return false
}

func (l *cands) add(s scheme, ok bool) {
	if ok {
		l.s[l.n] = s
		l.n++
	}
}

// applyFn builds the array of scheme s, its children compressed.
type applyFn[T any] func(x at, s scheme, v []T, nulls []bool) *encode.Node

// rowsOf is what pick needs to know of the rows of one type.
type rowsOf[T any] struct {
	cost   func(v []T, nulls []bool) int // the cost of the plain array
	sample func(c *Compressor, v []T) []T
	digest func(v []T, nulls []bool) uint64 // a hash of the rows and of their nulls
	// dict, when set, says whether the dictionary is built on all the rows to measure it, after
	// the other candidates, rather than on their sample (see dictAdmit)
	dict func(c *Compressor, v []T, nulls []bool, plain int, best ratio) admission
}

// numbersOf is rowsOf for numbers.
func numbersOf[T encode.Number]() rowsOf[T] {
	return rowsOf[T]{cost: numberCost[T], sample: sampleNumbers[T], digest: digestNumbers[T]}
}

// binsOf is rowsOf for strings.
var binsOf = rowsOf[[]byte]{cost: binsCost, sample: sampleBins, digest: digestBins, dict: dictAdmit}

// pick is the array of the best candidate, nil when none beats the plain array, which costs
// plain bytes for the rows v, and is measured on a sample.
func pick[T any](c *Compressor, x at, v []T, nulls []bool, l *cands, r rowsOf[T], apply applyFn[T]) *encode.Node {
	plain := r.cost(v, nulls)
	if x.trial {
		var best *encode.Node
		for _, s := range l.s[:l.n] {
			if n := apply(x, s, v, nulls); n != nil && cost(n) < plain {
				best, plain = n, cost(n)
			}
		}
		return best
	}
	best, known := take(&c.chosen, x, v, nulls, r.digest)
	var trial *encode.Node // the dictionary built on all the rows, when there is one
	if !known {
		best, trial = choose(c, x, v, nulls, plain, l, r, apply)
	}
	if x.probe && best != sDict && len(v) > 0 {
		// the writer compresses the same rows next, when they are not a dictionary
		c.chosen = choice{n: len(v), sum: r.digest(v, nulls), s: best}
	}
	if best == sPlain || x.probe && best != sDict {
		return nil // a probe only asks whether it is a dictionary
	}
	if n := applyBest(x, best, v, nulls, trial, apply); n != nil && cost(n) < plain {
		return n
	}
	return nil
}

// applyBest is the array of scheme best for the rows v, or trial, the dictionary built on all
// of them, when it is the best or smaller: the winner was chosen on a sample, the dictionary is
// a measure, and the reference keeps it so too (TrialOutput).
func applyBest[T any](x at, best scheme, v []T, nulls []bool, trial *encode.Node, apply applyFn[T]) *encode.Node {
	if best == sDict && trial != nil {
		return trial
	}
	n := apply(x, best, v, nulls)
	if trial != nil && (n == nil || cost(trial) < cost(n)) {
		return trial
	}
	return n
}

// ratio is a compression ratio, num/den, as the reference ranks its estimates.
type ratio struct{ num, den int }

// beats says whether r is a higher ratio than o.
func (r ratio) beats(o ratio) bool { return r.num*o.den > o.num*r.den }

// choose is the best scheme for the rows v, which cost plain bytes as a plain array, and the
// dictionary built on all of them when it was measured so. The candidates are measured on the
// sample of the rows, the dictionary last, when rowsOf has dict: like the dictionary of strings
// of the reference, it is then built on all the rows when it may beat the best ratio of the
// sample, and measured on the sample only when it would be too large for that.
func choose[T any](
	c *Compressor, x at, v []T, nulls []bool, plain int, l *cands, r rowsOf[T], apply applyFn[T],
) (scheme, *encode.Node) {
	deferred := r.dict != nil && l.has(sDict)
	best, bestCost, samplePlain := sampled(c, x, v, nulls, l, r, apply, deferred)
	if !deferred {
		return best, nil
	}
	var n *encode.Node
	a := r.dict(c, v, nulls, plain, ratio{samplePlain, max(bestCost, 1)})
	if a == admitTrial {
		// over the bounds of the trial, it is measured on the sample
		if n = apply(at{e: x.e, depth: x.depth, parent: x.parent, excl: x.excl, full: true}, sDict, v, nulls); n == nil {
			a = admitTooLarge
		}
	}
	switch {
	case a == admitTooLarge:
		// on the same sample, after the others: the dictionary has to cost less than the best
		one := cands{s: [10]scheme{sDict}, n: 1}
		if s, k, _ := sampled(c, x, v, nulls, &one, r, apply, false); s == sDict && k < bestCost {
			best = sDict
		}
	case n != nil && ratio{plain, max(cost(n), 1)}.beats(ratio{samplePlain, max(bestCost, 1)}):
		best = sDict
	}
	return best, n
}

// sampled is the best scheme for the rows v, measured on their sample, its cost there and the
// cost of the plain sample. A deferred dictionary is left out.
func sampled[T any](
	c *Compressor, x at, v []T, nulls []bool, l *cands, r rowsOf[T], apply applyFn[T], deferred bool,
) (scheme, int, int) {
	raw, bools, bins := c.raw.mark(), c.nulls.mark(), c.bins.mark()
	sv, sn := r.sample(c, v), c.sampleNulls(nulls, sampleRuns(len(v), func(int, int) {}))
	plain := r.cost(sv, sn)
	best, bestCost := sPlain, plain
	trial := at{e: c.trial, depth: x.depth, trial: true, parent: x.parent, excl: x.excl}
	for _, s := range l.s[:l.n] {
		if deferred && s == sDict {
			continue
		}
		if n := apply(trial, s, sv, sn); n != nil {
			k := amortized(n, len(sv), len(v))
			if k < bestCost {
				best, bestCost = s, k
			}
		}
		c.trial.Reset()
	}
	c.raw.restore(raw)
	c.nulls.restore(bools)
	c.bins.restore(bins)
	return best, bestCost, plain
}

// amortized is the cost of n, the array of a sample of rows of the rows rows, with its bytes
// that do not grow with the rows (the symbol table of FSST, the dictionary of OnPair) charged at
// the share of the rows the sample holds, as the reference does (Scheme::fixed_nbytes): the
// whole array spreads them over all its rows.
func amortized(n *encode.Node, rows, all int) int {
	total := cost(n)
	if rows >= all {
		return total
	}
	var fixed int
	switch n.ID {
	case encode.IDFSST:
		fixed = len(n.Bufs[0].Data) + len(n.Bufs[1].Data)
	case encode.IDOnPair:
		fixed = len(n.Bufs[0].Data) + n.Kids[0].Size()
	}
	return total - fixed + (fixed*rows+all-1)/all
}

// choice is the scheme that the sample of a probe chose for the array of a chunk: the writer
// compresses the same rows next, with the same compressor, unless they are a dictionary, and
// the sample would choose the same again. The rows are known by their number and a hash of
// their values, which holds no reference to them.
type choice struct {
	n   int    // the rows, 0 when there is no choice
	sum uint64 // the digest of the rows and of their nulls
	s   scheme
}

// take returns the scheme chosen for the rows v at the top of a chunk, when the last probe, m,
// chose it, and forgets it: it serves once.
func take[T any](m *choice, x at, v []T, nulls []bool, digest func(v []T, nulls []bool) uint64) (scheme, bool) {
	if x.depth > 0 || x.probe || m.n == 0 {
		return 0, false
	}
	hit := m.n == len(v) && m.sum == digest(v, nulls)
	s := m.s
	*m = choice{}
	return s, hit
}

// seed is the seed of the digests of the rows.
var seed = maphash.MakeSeed()

// digestNulls adds the nulls to h.
func digestNulls(h *maphash.Hash, nulls []bool) {
	if nulls == nil {
		return
	}
	var word [8]byte
	for i, null := range nulls {
		if null {
			word[i&7] |= 1 << (i >> 3 & 7)
		}
		if i&63 == 63 || i == len(nulls)-1 {
			_, _ = h.Write(word[:])
			word = [8]byte{}
		}
	}
	_ = h.WriteByte(1) // no null is not the same as no row null
}

// digestNumbers is the digest of the numbers v and of their nulls.
func digestNumbers[T encode.Number](v []T, nulls []bool) uint64 {
	var h maphash.Hash
	h.SetSeed(seed)
	_, _ = h.Write(unsafex.Bytes(v))
	digestNulls(&h, nulls)
	return h.Sum64()
}

// digestBins is the digest of the strings v and of their nulls.
func digestBins(v [][]byte, nulls []bool) uint64 {
	var h maphash.Hash
	h.SetSeed(seed)
	var size [4]byte
	for _, b := range v {
		binary.LittleEndian.PutUint32(size[:], uint32(len(b)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(b)
	}
	digestNulls(&h, nulls)
	return h.Sum64()
}

// sampleNumbers is the sample of the numbers v.
func sampleNumbers[T encode.Number](c *Compressor, v []T) []T {
	rows := sampleRuns(len(v), func(int, int) {})
	out := unsafex.Slice[T](c.raw.get(rows * unsafex.Size[T]()))[:0]
	sampleRuns(len(v), func(start, size int) { out = append(out, v[start:start+size]...) })
	return out
}

// sampleBins is the sample of the strings v.
func sampleBins(c *Compressor, v [][]byte) [][]byte {
	out := c.bins.get(sampleRuns(len(v), func(int, int) {}))[:0]
	sampleRuns(len(v), func(start, size int) { out = append(out, v[start:start+size]...) })
	return out
}

// numberCost is the cost of a plain array of numbers.
func numberCost[T encode.Number](v []T, nulls []bool) int {
	return nodeOverhead + bufferOverhead + len(v)*unsafex.Size[T]() + validityCost(nulls, len(v))
}

// intStats is what the candidates of integers depend on.
type intStats struct {
	runs     int  // the runs of equal rows
	valid    int  // the rows that are not null
	packW    int  // the bits of the largest valid value
	forW     int  // the bits of the largest minus the smallest valid value
	distinct int  // the distinct valid values, at least as many as counted when it stopped
	top      int  // the rows of the most frequent valid value, 0 when it is under 9/10 of them
	negative bool // the smallest valid value is negative
	minZero  bool // the smallest valid value is zero
	counted  bool // distinct and top are counted
}

// nullDominated says whether more than nine tenths of the rows rows are null: a sparse array
// then has a null fill.
func (st intStats) nullDominated(rows int) bool { return 10*(rows-st.valid) > 9*rows }

// sparse says whether a sparse array is tried. When the values are counted, it is when the
// reference tries one (SparseScheme): the rows are mostly null, or the most frequent value is
// at least nine tenths of the valid ones, and not all of them.
func (st intStats) sparse(rows int) bool {
	return st.valid > 0 && (!st.counted || st.nullDominated(rows) || 10*st.top >= 9*st.valid && st.top < st.valid)
}

// dict says whether a dictionary is tried. When the values are counted, it is when the
// reference tries one (IntDictScheme): at most half of the valid values are distinct.
func (st intStats) dict() bool { return st.valid > 0 && (!st.counted || st.distinct <= st.valid/2) }

// valueSet counts the distinct values of integers. It is taken from valueSets for one count and
// given back at its end: the compressors share as many as there are counts at once, not one each.
type valueSet struct {
	keys  [2][]uint64
	count [2][]int32 // the rows of the value of each slot, 0 for an empty slot
}

var valueSets = sync.Pool{New: func() any { return new(valueSet) }}

// table is table i of the set, of size empty slots.
func (s *valueSet) table(i, size int) ([]uint64, []int32) {
	if cap(s.keys[i]) < size {
		s.keys[i], s.count[i] = make([]uint64, size), make([]int32, size)
	}
	keys, count := s.keys[i][:size], s.count[i][:size]
	clear(count)
	return keys, count
}

// firstSlots is the size of the first table of a count: it doubles as the values come.
const firstSlots = 1 << 10

// spread counts the distinct valid values of v, of which valid are valid, and the rows of the
// most frequent one, into st. It stops when it knows that neither a dictionary nor a sparse
// array is tried: more than half of the values are distinct, and the most frequent one cannot
// be nine tenths of them.
func spread[T encode.Int](v []T, nulls []bool, st *intStats) {
	s := valueSets.Get().(*valueSet)
	defer valueSets.Put(s)
	// the table has an empty slot for each value it holds, so that it always has one: it
	// stops at the value that makes more than half of them distinct, and integers of 8 or 16
	// bits have fewer values than that
	most := st.valid/2 + 1
	if w := unsafex.Size[T](); w <= 2 {
		most = min(most, 1<<(8*w))
	}
	size, cur := min(1<<bits.Len(uint(2*most-1)), firstSlots), 0
	keys, count := s.table(cur, size)
	distinct, top := 0, int32(0)
	for i, x := range v {
		if isNull(nulls, i) {
			continue
		}
		key := uint64(x)
		at := slot(key, size)
		for count[at] != 0 && keys[at] != key {
			at = (at + 1) & uint64(size-1)
		}
		if count[at] == 0 {
			// a new value: once more than half are distinct, the most frequent one is at
			// most the rest of the rows plus one
			if distinct++; 2*distinct > st.valid && 10*(st.valid-distinct+1) < 9*st.valid {
				st.counted, st.distinct, st.top = true, distinct, 0
				return
			}
			keys[at] = key
			if 2*distinct > size {
				count[at] = 1
				top = max(top, 1)
				cur ^= 1
				keys, count = s.grow(keys, count, cur, 2*size)
				size *= 2
				continue
			}
		}
		count[at]++
		top = max(top, count[at])
	}
	st.counted, st.distinct, st.top = true, distinct, int(top)
	if 10*st.top < 9*st.valid {
		st.top = 0
	}
}

// slot is the first slot of key in a table of size slots.
func slot(key uint64, size int) uint64 {
	return key * 0x9e3779b97f4a7c15 >> (64 - bits.TrailingZeros(uint(size)))
}

// grow moves the values of a table to table i, of size slots, and returns it.
func (s *valueSet) grow(keys []uint64, count []int32, i, size int) ([]uint64, []int32) {
	nk, nc := s.table(i, size)
	for j, c := range count {
		if c == 0 {
			continue
		}
		at := slot(keys[j], size)
		for nc[at] != 0 {
			at = (at + 1) & uint64(size-1)
		}
		nk[at], nc[at] = keys[j], c
	}
	return nk, nc
}

// framed says whether a frame of reference packs the values narrower than bit-packing.
func (st intStats) framed() bool {
	return st.valid > 0 && !st.minZero && (st.negative || st.forW < st.packW)
}

// long says whether the runs of rows rows are long enough for run-end and RLE.
func (st intStats) long(rows int) bool { return st.valid > 0 && rows >= runThreshold*st.runs }

func statsOfInts[T encode.Int](v []T, nulls []bool) intStats {
	var st intStats
	var lo, hi T
	for i, x := range v {
		if i == 0 || isNull(nulls, i) != isNull(nulls, i-1) || !isNull(nulls, i) && x != v[i-1] {
			st.runs++
		}
		if isNull(nulls, i) {
			continue
		}
		if st.valid == 0 || x < lo {
			lo = x
		}
		if st.valid == 0 || x > hi {
			hi = x
		}
		st.valid++
	}
	mask := ^uint64(0) >> (64 - 8*unsafex.Size[T]())
	var zero T
	st.negative, st.minZero = lo < zero, lo == zero
	st.packW = bits.Len64(uint64(hi) & mask)
	st.forW = bits.Len64(uint64(hi-lo) & mask)
	return st
}

// constantNumbers is the constant array of the numbers v, under their validity when the compressor
// may mask.
func constantNumbers[T encode.Number](c *Compressor, x at, v []T, nulls []bool) (*encode.Node, bool) {
	if c.limit.NoMasked {
		return encode.Constant(x.e, v, nulls)
	}
	return encode.ConstantMasked(x.e, v, nulls)
}

// zigzagFn is ZigZag for a signed type, nil for an unsigned one.
type zigzagFn[T encode.Int] func(*encode.Encoder, []T, []bool) *encode.Node

// ints is the compressed array of integers.
func ints[T encode.Int](c *Compressor, x at, p PType, v []T, nulls []bool, zigzag zigzagFn[T]) *encode.Node {
	if !c.limit.NoSequence && x.takes(sSequence) {
		if n, ok := encode.Sequence(x.e, v, nulls); ok && cost(n) < numberCost(v, nulls) {
			return n
		}
	}
	if n, ok := constantNumbers(c, x, v, nulls); ok && cost(n) < numberCost(v, nulls) {
		return n
	}
	st := statsOfInts(v, nulls)
	if x.trial && st.valid > 0 && (x.sparse() && !st.nullDominated(len(v)) || x.dict()) {
		spread(v, nulls, &st)
	}
	l := intCands(c, st, x, p, len(v), zigzag != nil)
	apply := func(x at, s scheme, v []T, nulls []bool) *encode.Node { return applyInt(c, x, p, s, v, nulls, zigzag) }
	return pick(c, x, v, nulls, &l, numbersOf[T](), apply)
}

// intCands is the schemes to try on integers, in the order of the reference
// (vortex-btrblocks/src/session.rs), which wins the ties.
func intCands(c *Compressor, st intStats, x at, p PType, rows int, signed bool) cands {
	valid := st.valid > 0
	runs := st.long(rows)
	cascade := x.cascades()
	var l cands
	l.add(sFoR, cascade && st.framed() && x.takes(sFoR))
	l.add(sZigZag, cascade && signed && st.negative && x.takes(sZigZag)) // never under a zigzag
	l.add(sBitPack, !st.negative && st.packW < 8*p.Width())
	l.add(sSparse, x.sparse() && st.sparse(rows))
	l.add(sDict, x.dict() && st.dict())
	l.add(sRunEnd, cascade && runs && x.takes(sRunEnd))
	l.add(sRLE, cascade && runs && !c.limit.NoRLE && x.takes(sRLE))
	l.add(sPco, valid && c.pco(p.Width()))
	l.add(sZstd, c.zstd())
	return l
}

// applyInt is the array of integers of scheme s.
func applyInt[T encode.Int](c *Compressor, x at, p PType, s scheme, v []T, nulls []bool,
	zigzag zigzagFn[T],
) *encode.Node {
	var n *encode.Node
	switch s {
	case sBitPack:
		n, _ = encode.BitPackBest(x.e, v, nulls)
	case sFoR:
		n = encode.FoR(x.e, v, nulls)
	case sZigZag:
		n = zigzag(x.e, v, nulls)
	case sRunEnd:
		n = encode.RunEnd(x.e, v, nulls)
	case sRLE:
		n = encode.RLE(x.e, v, nulls)
	case sDict:
		n = encode.Dict(x.e, v, nulls)
		if beatsBare(x, n, numberCost(v, nulls)) {
			return n
		}
	case sSparse:
		n, _ = encode.Sparse(x.e, v, nulls)
	case sPco:
		n = encode.Pco(x.e, v, nulls)
	default:
		n = encode.Zstd(x.e, v, nulls)
	}
	if n != nil {
		c.kids(x, n, p, s)
	}
	return n
}

// floats is the compressed array of floats.
func floats[T encode.Float](c *Compressor, x at, p PType, v []T, nulls []bool) *encode.Node {
	if n, ok := constantNumbers(c, x, v, nulls); ok && cost(n) < numberCost(v, nulls) {
		return n
	}
	runs, valid := runsOf(v, nulls)
	cascade := x.cascades()
	var l cands
	l.add(sALP, cascade && valid > 0)
	l.add(sALPRD, valid > 0)
	l.add(sDict, x.dict() && valid > 0 && (!x.trial || floatsDict(v, nulls, valid)))
	l.add(sSparse, x.parent != sSparse && valid > 0 && 10*(len(v)-valid) > 9*len(v)) // dominated by nulls
	l.add(sRLE, cascade && valid > 0 && len(v) >= runThreshold*runs && !c.limit.NoRLE && x.takes(sRLE))
	l.add(sPco, valid > 0 && c.pco(p.Width()))
	l.add(sZstd, c.zstd())
	apply := func(x at, s scheme, v []T, nulls []bool) *encode.Node { return applyFloat(c, x, p, s, v, nulls) }
	return pick(c, x, v, nulls, &l, numbersOf[T](), apply)
}

// floatsDict says whether the reference tries a dictionary of the floats v (FloatDictScheme):
// at most half of the valid values are distinct, counted by their bits, as the dictionary does.
func floatsDict[T encode.Float](v []T, nulls []bool, valid int) bool {
	st := intStats{valid: valid}
	if unsafex.Size[T]() == 4 {
		spread(unsafex.Slice[uint32](unsafex.Bytes(v)), nulls, &st)
	} else {
		spread(unsafex.Slice[uint64](unsafex.Bytes(v)), nulls, &st)
	}
	return st.dict()
}

// runsOf is the runs of equal rows of v, and its valid rows.
func runsOf[T encode.Float](v []T, nulls []bool) (runs, valid int) {
	for i := range v {
		if i == 0 || isNull(nulls, i) != isNull(nulls, i-1) || !isNull(nulls, i) && v[i] != v[i-1] {
			runs++
		}
		if !isNull(nulls, i) {
			valid++
		}
	}
	return runs, valid
}

// applyFloat is the array of floats of scheme s.
func applyFloat[T encode.Float](c *Compressor, x at, p PType, s scheme, v []T, nulls []bool) *encode.Node {
	var n *encode.Node
	switch s {
	case sALP:
		n = encode.ALP(x.e, v, nulls)
	case sALPRD:
		n, _ = encode.ALPRD(x.e, v, nulls)
	case sDict:
		n = encode.Dict(x.e, v, nulls)
		if beatsBare(x, n, numberCost(v, nulls)) {
			return n
		}
	case sSparse:
		n, _ = encode.Sparse(x.e, v, nulls) // there is a valid value
	case sRLE:
		n = encode.RLE(x.e, v, nulls)
	case sPco:
		n = encode.Pco(x.e, v, nulls)
	default:
		n = encode.Zstd(x.e, v, nulls)
	}
	c.kids(x, n, p, s)
	return n
}
