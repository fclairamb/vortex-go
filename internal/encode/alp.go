package encode

import (
	"math"
	"math/bits"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// Float is the float types the encoders of floats take.
type Float interface{ ~float32 | ~float64 }

// Exponents is the pair of powers of ten ALP scales a value by: it encodes round(v * 10^E *
// 10^-F), and decodes n * 10^F * 10^-E.
type Exponents struct{ E, F uint8 }

// alpChunk is the rows per chunk of the patches: one offset into the patches per chunk.
const alpChunk = 1024

// The sample the search of the exponents reads, as the alp crate does: runs of alpBlock rows
// spread over the array, alpSample rows in all.
const (
	alpSample = 64
	alpBlock  = 8
)

// The powers of ten of the alp crate: pow10[i] is 10^i and ipow10[i] is 10^-i, each one
// rounded to the nearest float, which is what the literals of the crate are.
var (
	pow10f64 = [...]float64{
		1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11,
		1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22, 1e23,
	}
	ipow10f64 = [...]float64{
		1e-0, 1e-1, 1e-2, 1e-3, 1e-4, 1e-5, 1e-6, 1e-7, 1e-8, 1e-9, 1e-10, 1e-11,
		1e-12, 1e-13, 1e-14, 1e-15, 1e-16, 1e-17, 1e-18, 1e-19, 1e-20, 1e-21, 1e-22, 1e-23,
	}
	pow10f32  = [...]float32{1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10}
	ipow10f32 = [...]float32{
		1e-0, 1e-1, 1e-2, 1e-3, 1e-4, 1e-5, 1e-6, 1e-7, 1e-8, 1e-9, 1e-10,
	}
)

// alpInt is the integer type an ALP array holds: of the width of the float.
type alpInt interface{ int32 | int64 }

func pow10[F Float](i uint8) F {
	if unsafex.Size[F]() == 4 {
		return F(pow10f32[i])
	}
	return F(pow10f64[i])
}

func ipow10[F Float](i uint8) F {
	if unsafex.Size[F]() == 4 {
		return F(ipow10f32[i])
	}
	return F(ipow10f64[i])
}

// maxExponent is the exclusive bound of the exponents the search tries: the largest power of
// ten an integer of the width holds.
func maxExponent[F Float]() int {
	if unsafex.Size[F]() == 4 {
		return 10
	}
	return 18
}

// floatBits is the bits of x: two floats are the same when these are.
func floatBits[F Float](x F) uint64 {
	if unsafex.Size[F]() == 4 {
		return uint64(math.Float32bits(float32(x)))
	}
	return math.Float64bits(float64(x))
}

// saturate converts x to an integer like Rust's `as`: a NaN is 0, and what is out of range is
// the nearest limit.
func saturate[F Float, I alpInt](x F) I {
	if x != x {
		return 0
	}
	lim := F(math.Ldexp(1, 8*unsafex.Size[I]()-1))
	var one I = 1
	top := one<<(8*unsafex.Size[I]()-1) - 1
	switch {
	case x >= lim:
		return top
	case x <= -lim:
		return -top - 1
	}
	return I(x)
}

// sweet is the float that makes an addition round to an integer: 2^(m) + 2^(m-1) for m
// mantissa bits.
func sweet[F Float]() F {
	if unsafex.Size[F]() == 4 {
		return F(float32(1<<23 + 1<<22))
	}
	return F(float64(1<<52 + 1<<51))
}

// alpEncodeOne is the integer of v under the exponents, whether it decodes back to v or not.
func alpEncodeOne[F Float, I alpInt](v F, x Exponents) I {
	// the explicit conversion keeps the compiler from fusing the product and the sum
	y := F(v*pow10[F](x.E)*ipow10[F](x.F)) + sweet[F]()
	return saturate[F, I](F(y) - sweet[F]())
}

// alpDecodeOne is the float of an integer under the exponents.
func alpDecodeOne[F Float, I alpInt](n I, x Exponents) F {
	return F(n) * pow10[F](x.F) * ipow10[F](x.E)
}

// samplePlan is the rows a search reads: runs of block rows, spread so that they are disjoint
// and the last one reaches the end, or all the rows of an array of at most max.
type samplePlan struct{ block, spacing, count int }

func newSamplePlan(n, maxSample, block int) samplePlan {
	block = min(max(block, 1), max(maxSample, 1))
	if n <= maxSample {
		return samplePlan{block: max(n, 1), count: n}
	}
	blocks := max(maxSample/block, 1)
	spacing := 0
	if blocks > 1 {
		spacing = (n - block) / (blocks - 1)
	}
	return samplePlan{block: block, spacing: spacing, count: blocks * block}
}

// at is the row of the k-th sampled one.
func (p samplePlan) at(k int) int { return k/p.block*p.spacing + k%p.block }

// BestExponents is the pair of exponents that makes the smallest encoding, from a sample of v,
// as the alp crate picks it: an estimate of the bits of the integers, once the range of the
// ones that decode back is taken off, and of the patches, over each pair; of two as good, the
// one that scales least.
func BestExponents[F Float](e *Encoder, v []F) Exponents {
	if unsafex.Size[F]() == 4 {
		return bestExponents[float32, int32](e, unsafex.Slice[float32](unsafex.Bytes(v)))
	}
	return bestExponents[float64, int64](e, unsafex.Slice[float64](unsafex.Bytes(v)))
}

func bestExponents[F Float, I alpInt](e *Encoder, v []F) Exponents {
	p := newSamplePlan(len(v), alpSample, alpBlock)
	sample := unsafex.Slice[F](e.alloc(p.count * unsafex.Size[F]()))
	for k := range sample {
		sample[k] = v[p.at(k)]
	}
	best := Exponents{}
	bestSize, _ := alpEstimateWithin[F, I](sample, best, math.MaxInt)
	for x := maxExponent[F]() - 1; x >= 0; x-- {
		for f := 0; f <= x; f++ {
			cand := Exponents{uint8(x), uint8(f)}
			size, ok := alpEstimateWithin[F, I](sample, cand, bestSize)
			switch {
			case !ok:
			case size < bestSize:
				best, bestSize = cand, size
			case size == bestSize && x-f < int(best.E)-int(best.F):
				best = cand
			}
		}
	}
	return best
}

// alpPatchBytes is what the estimate counts for a patch: its value and a 16-bit position, as
// the chunks of 1024 rows allow.
func alpPatchBytes[F Float]() int { return unsafex.Size[F]() + 2 }

// alpEstimateWithin is the size the integers bit-packed and the patches take, in bytes, or
// false once the patches alone are more than limit.
func alpEstimateWithin[F Float, I alpInt](values []F, x Exponents, limit int) (int, bool) {
	var kept, all [2]I
	var hasKept, hasAll bool
	patches := 0
	for _, v := range values {
		n := alpEncodeOne[F, I](v, x)
		all, hasAll = widen(all, hasAll, n)
		if floatBits(alpDecodeOne[F, I](n, x)) == floatBits(v) {
			kept, hasKept = widen(kept, hasKept, n)
			continue
		}
		patches++
		if patches*alpPatchBytes[F]() > limit {
			return 0, false
		}
	}
	span, has := kept, hasKept
	if patches == len(values) {
		span, has = all, hasAll
	}
	width := 8 * unsafex.Size[I]()
	if has {
		if d := span[1] - span[0]; d >= 0 { // a range that overflows costs all the bits
			width = bits.Len64(uint64(d))
		}
	}
	return (len(values)*width+7)/8 + patches*alpPatchBytes[F](), true
}

// widen adds n to the running (min, max) of the integers seen.
func widen[I alpInt](b [2]I, has bool, n I) ([2]I, bool) {
	if !has {
		return [2]I{n, n}, true
	}
	return [2]I{min(b[0], n), max(b[1], n)}, true
}

// ALP is the ALP encoding of the floats of v, at the exponents BestExponents finds: the
// integers round(v * 10^e * 10^-f) in a child, and in patches (a position, a value and the
// number of patches before each chunk of 1024 rows) the valid values that do not decode back to
// the same bits: a NaN, an infinity, a -0 or a value that needs more digits. The integer of a
// patch is the first one that is not, so that it does not widen the range of the others.
func ALP[F Float](e *Encoder, v []F, nulls []bool) *Node {
	return ALPWith(e, v, nulls, BestExponents(e, v))
}

// ALPWith is ALP at the given exponents, at most 10 for floats of 32 bits and 18 for floats
// of 64: it returns nil for more.
func ALPWith[F Float](e *Encoder, v []F, nulls []bool, x Exponents) *Node {
	if int(x.E) > maxExponent[F]() || int(x.F) > maxExponent[F]() {
		return nil
	}
	if unsafex.Size[F]() == 4 {
		return alpNode[float32, int32](e, unsafex.Slice[float32](unsafex.Bytes(v)), nulls, x)
	}
	return alpNode[float64, int64](e, unsafex.Slice[float64](unsafex.Bytes(v)), nulls, x)
}

func alpNode[F Float, I alpInt](e *Encoder, v []F, nulls []bool, x Exponents) *Node {
	n, w := len(v), unsafex.Size[F]()
	raw := e.alloc(n * w)
	enc := unsafex.Slice[I](raw)
	isPatch := e.flags(n)
	fill, hasFill := I(0), false
	for i, f := range v {
		enc[i] = alpEncodeOne[F, I](f, x)
		if floatBits(alpDecodeOne[F, I](enc[i], x)) != floatBits(f) {
			isPatch[i] = true
		} else if !hasFill {
			fill, hasFill = enc[i], true
		}
	}
	count := 0
	for i := range v {
		if isPatch[i] && hasFill {
			enc[i] = fill
		}
		if isPatch[i] && !isNull(nulls, i) {
			count++
		}
	}
	node := e.node(IDALP)
	node.kid(e.leaf(raw, w, nulls, n))
	e.tmp = putUint(putUint(e.tmp[:0], 1, uint64(x.E)), 2, uint64(x.F))
	if count > 0 {
		chunks := (n + alpChunk - 1) / alpChunk
		alpPatches(e, node, v, nulls, isPatch, count, chunks)
	}
	node.Meta = e.keep(e.tmp)
	return node
}

// alpPatches adds the patches: the three children, and their message to e.tmp, which holds
// the metadata of the array so far.
func alpPatches[F Float](e *Encoder, node *Node, v []F, nulls, isPatch []bool, count, chunks int) {
	w := unsafex.Size[F]()
	idx, vals, offs := e.alloc(8*count), e.alloc(w*count), e.alloc(8*chunks)
	raw := unsafex.Bytes(v)
	k := 0
	for c := range chunks {
		putWidth(offs[8*c:], 8, uint64(k))
		for i := c * alpChunk; i < min(len(v), (c+1)*alpChunk); i++ {
			if isPatch[i] && !isNull(nulls, i) {
				putWidth(idx[8*k:], 8, uint64(i))
				copy(vals[w*k:], raw[w*i:w*(i+1)])
				k++
			}
		}
	}
	m := protowire.AppendUint(e.tmp2[:0], 1, uint64(count))
	m = protowire.AppendUint(m, 3, pU64)
	m = protowire.AppendUint(m, 4, uint64(chunks))
	m = protowire.AppendUint(m, 5, pU64)
	m = protowire.AppendUint(m, 6, 0)
	e.tmp2 = m
	e.tmp = protowire.AppendBytes(e.tmp, 3, m)
	node.kid(e.leaf(idx, 8, nil, count))
	node.kid(e.leaf(vals, w, nil, count))
	node.kid(e.leaf(offs, 8, nil, chunks))
}

// EstimateALP is the size of ALP.
func EstimateALP[F Float](e *Encoder, sample []F, nulls []bool, rows int) int {
	if unsafex.Size[F]() == 4 {
		return estimateALP[float32, int32](e, unsafex.Slice[float32](unsafex.Bytes(sample)), nulls, rows)
	}
	return estimateALP[float64, int64](e, unsafex.Slice[float64](unsafex.Bytes(sample)), nulls, rows)
}

func estimateALP[F Float, I alpInt](e *Encoder, sample []F, nulls []bool, rows int) int {
	x := bestExponents[F, I](e, sample)
	patches := 0
	for i, f := range sample {
		if !isNull(nulls, i) && floatBits(alpDecodeOne[F, I](alpEncodeOne[F, I](f, x), x)) != floatBits(f) {
			patches++
		}
	}
	w := unsafex.Size[F]()
	size := rows*w + validBytes(nulls, rows)
	if patches = scale(patches, len(sample), rows); patches > 0 {
		size += patches*(8+w) + (rows+alpChunk-1)/alpChunk*8
	}
	return size
}
