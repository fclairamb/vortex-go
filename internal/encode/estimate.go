package encode

import (
	"math"
	"math/bits"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The Estimate functions return the bytes of the buffers of the array an encoder would make
// (what Node.Size counts), from a sample of its rows: sample is the first rows, or any rows,
// of an array of rows rows, and nulls the nulls of the sample. They build nothing, and a
// sample that is the whole array gives the exact size. What a sample cannot tell, the number
// of exceptions, of runs or of values, they scale up from its share of the rows.

// scale is x, counted in a sample of n rows, for rows rows.
func scale(x, n, rows int) int {
	if n == 0 || n == rows {
		return x
	}
	return int((int64(x)*int64(rows) + int64(n)/2) / int64(n))
}

// validBytes is the bytes of the validity of rows rows, from the nulls of a sample.
func validBytes(nulls []bool, rows int) int {
	switch {
	case !hasNull(nulls):
		return 0
	case allNull(nulls):
		return 2 // a constant false
	}
	return (rows + 7) / 8
}

// EstimateBitPack is the size of BitPack at the given width.
func EstimateBitPack[T Int](sample []T, nulls []bool, rows, width int) int {
	w := unsafex.Size[T]()
	exceptions := 0
	for i, x := range sample {
		if !isNull(nulls, i) && bits.Len64(ubits(x)) > width {
			exceptions++
		}
	}
	blocks := (rows + blockSize - 1) / blockSize
	size := blocks*128*width + validBytes(nulls, rows)
	if patches := scale(exceptions, len(sample), rows); patches > 0 {
		_, iw := patchWidth(rows)
		size += patches*(iw+w) + blocks*8
	}
	return size
}

// EstimateFoR is the size of FoR.
func EstimateFoR[T Int](nulls []bool, rows int) int {
	return rows*unsafex.Size[T]() + validBytes(nulls, rows)
}

// EstimateZigZag is the size of ZigZag.
func EstimateZigZag[T Signed](nulls []bool, rows int) int {
	return rows*unsafex.Size[T]() + validBytes(nulls, rows)
}

// EstimateRLE is the size of RLE.
func EstimateRLE[T Number](sample []T, nulls []bool, rows int) int {
	w := unsafex.Size[T]()
	count := 0
	var prev T
	for i, x := range sample {
		if isNull(nulls, i) {
			if i%blockSize == 0 {
				prev = 0
			}
			x = prev
		}
		// a NaN is the same as another one, as the encoder compares the bits
		if i%blockSize == 0 || x != prev && !(math.IsNaN(float64(x)) && math.IsNaN(float64(prev))) {
			count++
		}
		prev = x
	}
	values := scale(count, len(sample), rows)
	blocks := (rows + blockSize - 1) / blockSize
	return values*w + blocks*blockSize*2 + blocks*8 + validBytes(nulls, blocks*blockSize)
}

// EstimateRunEnd is the size of RunEnd.
func EstimateRunEnd[T Number](sample []T, nulls []bool, rows int) int {
	runs := 0
	for i := range sample {
		if i == 0 || !sameRow(sample, nulls, i-1, i) {
			runs++
		}
	}
	if len(sample) > 0 {
		runs = 1 + scale(runs-1, len(sample), rows) // the first run is not a boundary
	}
	_, ew := narrow(uint64(rows))
	return runs*(ew+unsafex.Size[T]()) + validBytes(nulls, runs)
}

// distinctOf classifies a sample, and returns the number of distinct values of the array, at
// most limit: the ones of the sample, plus the ones it has not seen. These are counted with the
// values seen once (f1) and twice (f2) in it: Chao's estimate f1*f1/2*f2 of what a sample
// misses, or, when no value is seen twice, the growth of the values seen once. The other results
// are the classes of the sample.
func (e *Encoder) distinctOf(k *keys, n, rows, limit int, nulls []bool) (d, distinct, nullCode int) {
	distinct, nullCode = e.classify(k, n, nulls)
	f1, f2 := 0, 0
	for c := range distinct {
		switch e.count[c] {
		case 1:
			f1++
		case 2:
			f2++
		}
	}
	d = distinct
	switch {
	case n == rows:
	case f2 > 0:
		d += f1 * f1 / (2 * f2)
	default:
		d += scale(f1, n, rows) - f1
	}
	return min(d, rows, limit), distinct, nullCode
}

// EstimateDict is the size of Dict.
func EstimateDict[T Number](e *Encoder, sample []T, nulls []bool, rows int) int {
	k := keys{raw: unsafex.Bytes(sample), w: unsafex.Size[T](), kind: keyNumbers}
	d, _, _ := e.distinctOf(&k, len(sample), rows, 1<<min(8*k.w, 40)+boolInt(hasNull(nulls)), nulls)
	_, cw := narrow(uint64(max(d-1, 0)))
	return rows*cw + d*k.w + validBytes(nulls, d)
}

// EstimateDictBytes is the size of DictBytes.
func EstimateDictBytes(e *Encoder, sample [][]byte, nulls []bool, rows int) int {
	k := keys{bins: sample, kind: keyBytes}
	return e.estimateVarBin(&k, len(sample), nulls, rows)
}

// EstimateDictStrings is the size of DictStrings.
func EstimateDictStrings(e *Encoder, sample []string, nulls []bool, rows int) int {
	k := keys{strs: sample, kind: keyStrings}
	return e.estimateVarBin(&k, len(sample), nulls, rows)
}

func (e *Encoder) estimateVarBin(k *keys, n int, nulls []bool, rows int) int {
	d, distinct, nullCode := e.distinctOf(k, n, rows, rows, nulls)
	long := 0
	for c := range distinct {
		if size := k.size(int(e.first[c])); c != nullCode && size > maxInline {
			long += size
		}
	}
	_, cw := narrow(uint64(max(d-1, 0)))
	return rows*cw + 16*d + scale(long, max(distinct, 1), max(d, 1)) + validBytes(nulls, d)
}

// EstimateSequence is the size of Sequence, which has no buffer.
func EstimateSequence() int { return 0 }

// EstimateConstant is the size of Constant: the ScalarValue of the first value of the sample.
func EstimateConstant[T Number](sample []T, nulls []bool) int {
	var b [16]byte
	if allNull(nulls) || len(sample) == 0 {
		return 2
	}
	return len(appendScalar(b[:0], sample[0]))
}

// EstimateSparse is the size of Sparse.
func EstimateSparse[T Number](e *Encoder, sample []T, nulls []bool, rows int) int {
	nullCount := 0
	for i := range sample {
		if isNull(nulls, i) {
			nullCount++
		}
	}
	fillNull := 10*nullCount > nullDominated*len(sample)
	fill := fillValue(e, sample, nulls, fillNull)
	patches, nullPatches := 0, 0
	for i, x := range sample {
		switch {
		case fillNull && isNull(nulls, i):
		case fillNull || isNull(nulls, i) || x != fill:
			patches++
			if isNull(nulls, i) {
				nullPatches++
			}
		}
	}
	if patches == 0 {
		return EstimateConstant(sample, nulls)
	}
	patches, nullPatches = scale(patches, len(sample), rows), scale(nullPatches, len(sample), rows)
	iw, fillSize := 8, 2
	if fillNull {
		iw = 4
	} else {
		var b [16]byte
		fillSize = len(appendScalar(b[:0], fill))
	}
	valid := 0
	switch nullPatches {
	case 0:
	case patches:
		valid = 2
	default:
		valid = (patches + 7) / 8
	}
	return fillSize + patches*(iw+unsafex.Size[T]()) + valid
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
