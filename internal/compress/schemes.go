package compress

import (
	"math/bits"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// cands is a list of schemes to try, in order: on a tie the first one wins.
type cands struct {
	s [10]scheme
	n int
}

func (l *cands) add(s scheme, ok bool) {
	if ok {
		l.s[l.n] = s
		l.n++
	}
}

// applyFn builds the array of scheme s, its children compressed.
type applyFn[T any] func(x at, s scheme, v []T, nulls []bool) *encode.Node

// pick is the array of the best candidate, nil when none beats the plain array, which costs
// plain bytes for the rows v, and is measured by plainCost on a sample.
func pick[T any](c *Compressor, x at, v []T, nulls []bool, l *cands, plainCost func(v []T, nulls []bool) int,
	sample func(c *Compressor, v []T) []T, apply applyFn[T],
) *encode.Node {
	plain := plainCost(v, nulls)
	if x.trial {
		var best *encode.Node
		for _, s := range l.s[:l.n] {
			if n := apply(x, s, v, nulls); n != nil && cost(n) < plain {
				best, plain = n, cost(n)
			}
		}
		return best
	}
	raw, bools, bins := c.raw.mark(), c.nulls.mark(), c.bins.mark()
	sv, sn := sample(c, v), c.sampleNulls(nulls, sampleRuns(len(v), func(int, int) {}))
	best, bestCost := sPlain, plainCost(sv, sn)
	trial := at{e: c.trial, depth: x.depth, trial: true, parent: x.parent}
	for _, s := range l.s[:l.n] {
		if n := apply(trial, s, sv, sn); n != nil && cost(n) < bestCost {
			best, bestCost = s, cost(n)
		}
		c.trial.Reset()
	}
	c.raw.restore(raw)
	c.nulls.restore(bools)
	c.bins.restore(bins)
	if best == sPlain {
		return nil
	}
	if n := apply(x, best, v, nulls); n != nil && cost(n) < plain {
		return n
	}
	return nil
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
	negative bool // the smallest valid value is negative
	minZero  bool // the smallest valid value is zero
	packW    int  // the bits of the largest valid value
	forW     int  // the bits of the largest minus the smallest valid value
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
	if !c.limit.NoSequence {
		if n, ok := encode.Sequence(x.e, v, nulls); ok && cost(n) < numberCost(v, nulls) {
			return n
		}
	}
	if n, ok := constantNumbers(c, x, v, nulls); ok && cost(n) < numberCost(v, nulls) {
		return n
	}
	l := intCands(c, statsOfInts(v, nulls), x, p, len(v), zigzag != nil)
	apply := func(x at, s scheme, v []T, nulls []bool) *encode.Node { return applyInt(c, x, p, s, v, nulls, zigzag) }
	return pick(c, x, v, nulls, &l, numberCost[T], sampleNumbers[T], apply)
}

// intCands is the schemes to try on integers, in the order of the reference
// (vortex-btrblocks/src/session.rs), which wins the ties.
func intCands(c *Compressor, st intStats, x at, p PType, rows int, signed bool) cands {
	valid := st.valid > 0
	runs := st.long(rows) && x.parent != sRunEnd && x.parent != sRLE
	cascade := x.cascades()
	var l cands
	l.add(sFoR, cascade && st.framed())
	l.add(sZigZag, cascade && signed && st.negative && x.parent != sZigZag)
	l.add(sBitPack, !st.negative && st.packW < 8*p.Width())
	l.add(sSparse, x.parent != sSparse && valid)
	l.add(sDict, cascade && x.parent != sDict && valid)
	l.add(sRunEnd, cascade && runs)
	l.add(sRLE, cascade && runs && !c.limit.NoRLE)
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
	l.add(sDict, cascade && x.parent != sDict && valid > 0)
	l.add(sSparse, x.parent != sSparse && valid > 0 && 10*(len(v)-valid) > 9*len(v)) // dominated by nulls
	l.add(sRLE, cascade && valid > 0 && len(v) >= runThreshold*runs && x.parent != sRLE && !c.limit.NoRLE)
	l.add(sPco, valid > 0 && c.pco(p.Width()))
	l.add(sZstd, c.zstd())
	apply := func(x at, s scheme, v []T, nulls []bool) *encode.Node { return applyFloat(c, x, p, s, v, nulls) }
	return pick(c, x, v, nulls, &l, numberCost[T], sampleNumbers[T], apply)
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
