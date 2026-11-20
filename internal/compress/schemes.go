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
type applyFn[T any] func(e *encode.Encoder, trial bool, s scheme, v []T, nulls []bool) *encode.Node

// pick is the array of the best candidate, nil when none beats the plain array, which costs
// plain bytes for the rows v, and is measured by plainCost on a sample.
func pick[T any](c *Compressor, e *encode.Encoder, trial bool, v []T, nulls []bool, l *cands,
	plainCost func(v []T, nulls []bool) int, sample func(c *Compressor, v []T) []T, apply applyFn[T],
) *encode.Node {
	plain := plainCost(v, nulls)
	if trial {
		var best *encode.Node
		for _, s := range l.s[:l.n] {
			if n := apply(e, true, s, v, nulls); n != nil && cost(n) < plain {
				best, plain = n, cost(n)
			}
		}
		return best
	}
	raw, bools, bins := c.raw.mark(), c.nulls.mark(), c.bins.mark()
	sv, sn := sample(c, v), c.sampleNulls(nulls, sampleRuns(len(v), func(int, int) {}))
	best, bestCost := sPlain, plainCost(sv, sn)
	for _, s := range l.s[:l.n] {
		if n := apply(c.trial, true, s, sv, sn); n != nil && cost(n) < bestCost {
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
	if n := apply(e, false, best, v, nulls); n != nil && cost(n) < plain {
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

// ints is the compressed array of integers.
func ints[T encode.Int](c *Compressor, e *encode.Encoder, trial bool, p PType, v []T, nulls []bool, depth int,
	parent scheme, zigzag func(*encode.Encoder, []T, []bool) *encode.Node,
) *encode.Node {
	if n, ok := encode.Sequence(e, v, nulls); ok && cost(n) < numberCost(v, nulls) {
		return n
	}
	if n, ok := encode.Constant(e, v, nulls); ok && cost(n) < numberCost(v, nulls) {
		return n
	}
	st := statsOfInts(v, nulls)
	runs := st.valid > 0 && len(v) >= runThreshold*st.runs && parent != sRunEnd && parent != sRLE
	cascade := depth < maxDepth
	// the order of the reference (vortex-btrblocks/src/session.rs), which wins the ties
	var l cands
	l.add(sFoR, cascade && st.valid > 0 && !st.minZero && (st.negative || st.forW < st.packW))
	l.add(sZigZag, cascade && zigzag != nil && st.negative && parent != sZigZag)
	l.add(sBitPack, !st.negative && st.packW < 8*p.Width())
	l.add(sSparse, parent != sSparse && st.valid > 0)
	l.add(sDict, cascade && parent != sDict && st.valid > 0)
	l.add(sRunEnd, cascade && runs)
	l.add(sRLE, cascade && runs)
	l.add(sZstd, c.mode == Compact)
	return pick(c, e, trial, v, nulls, &l, numberCost[T], sampleNumbers[T], func(e *encode.Encoder, trial bool, s scheme, v []T, nulls []bool) *encode.Node {
		var n *encode.Node
		switch s {
		case sBitPack:
			n, _ = encode.BitPackBest(e, v, nulls)
		case sFoR:
			n = encode.FoR(e, v, nulls)
		case sZigZag:
			n = zigzag(e, v, nulls)
		case sRunEnd:
			n = encode.RunEnd(e, v, nulls)
		case sRLE:
			n = encode.RLE(e, v, nulls)
		case sDict:
			n = encode.Dict(e, v, nulls)
		case sSparse:
			n, _ = encode.Sparse(e, v, nulls)
		default:
			n = encode.Zstd(e, v, nulls)
		}
		if n != nil {
			c.kids(e, trial, n, p, depth, s)
		}
		return n
	})
}

// floats is the compressed array of floats.
func floats[T encode.Float](c *Compressor, e *encode.Encoder, trial bool, p PType, v []T, nulls []bool, depth int,
	parent scheme,
) *encode.Node {
	if n, ok := encode.Constant(e, v, nulls); ok && cost(n) < numberCost(v, nulls) {
		return n
	}
	runs, valid := 0, 0
	for i := range v {
		if i == 0 || isNull(nulls, i) != isNull(nulls, i-1) || !isNull(nulls, i) && v[i] != v[i-1] {
			runs++
		}
		if !isNull(nulls, i) {
			valid++
		}
	}
	cascade := depth < maxDepth
	var l cands
	l.add(sALP, cascade && valid > 0)
	l.add(sALPRD, valid > 0)
	l.add(sDict, cascade && parent != sDict && valid > 0)
	l.add(sSparse, parent != sSparse && valid > 0 && 10*(len(v)-valid) > 9*len(v)) // dominated by nulls
	l.add(sRLE, cascade && valid > 0 && len(v) >= runThreshold*runs && parent != sRLE)
	l.add(sZstd, c.mode == Compact)
	return pick(c, e, trial, v, nulls, &l, numberCost[T], sampleNumbers[T], func(e *encode.Encoder, trial bool, s scheme, v []T, nulls []bool) *encode.Node {
		var n *encode.Node
		switch s {
		case sALP:
			n = encode.ALP(e, v, nulls)
		case sALPRD:
			n, _ = encode.ALPRD(e, v, nulls)
		case sDict:
			n = encode.Dict(e, v, nulls)
		case sSparse:
			n, _ = encode.Sparse(e, v, nulls) // there is a valid value
		case sRLE:
			n = encode.RLE(e, v, nulls)
		default:
			n = encode.Zstd(e, v, nulls)
		}
		c.kids(e, trial, n, p, depth, s)
		return n
	})
}
