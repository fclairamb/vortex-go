package encode

import (
	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// nullDominated is the share of nulls, in tenths, over which a sparse array has a null fill.
const nullDominated = 9

// Sparse is the sparse encoding of the numbers of v: a fill value and the patches, the rows
// that differ from it. The fill is null when more than nine tenths of the rows are null, the
// patches being the valid rows; else it is the most frequent value, the first of them when
// several are, and the patches are the rows that are not equal to it, the null ones included.
// The fill is in the one buffer, as a ScalarValue, and a null patch has the value zero. An array
// that is all equal to its fill is the constant of Constant, as the reference does. It returns
// false when the array is empty or all null.
func Sparse[T Number](e *Encoder, v []T, nulls []bool) (*Node, bool) {
	nullCount := 0
	for i := range v {
		if isNull(nulls, i) {
			nullCount++
		}
	}
	if len(v) == 0 || nullCount == len(v) {
		return nil, false
	}
	fillNull := 10*nullCount > nullDominated*len(v)
	fill := fillValue(e, v, nulls, fillNull)
	count, validPatches := 0, 0
	for i := range v {
		if isPatch(v, nulls, i, fill, fillNull) {
			count++
			if !isNull(nulls, i) {
				validPatches++
			}
		}
	}
	if count == 0 {
		return Constant(e, v, nulls)
	}
	ptype, iw := pU64, 8
	if fillNull {
		ptype, iw = pU32, 4
	}
	w := unsafex.Size[T]()
	raw := unsafex.Bytes(v)
	idx, vals, patchNulls := e.allocDirty(count*iw), e.allocDirty(count*w), e.flags(count)
	k := 0
	for i := range v {
		if !isPatch(v, nulls, i, fill, fillNull) {
			continue
		}
		putWidth(idx[k*iw:], iw, uint64(i))
		// the slots of null patches are kept, unless there is no valid patch
		patchNulls[k] = isNull(nulls, i)
		if validPatches > 0 || !patchNulls[k] {
			copy(vals[k*w:], raw[i*w:(i+1)*w])
		} else {
			clear(vals[k*w : (k+1)*w])
		}
		k++
	}
	if fillNull {
		e.tmp = protowire.AppendUint(e.tmp[:0], scalarNull, 0)
	} else {
		e.tmp = appendScalar(e.tmp[:0], fill)
	}
	n := e.node(IDSparse)
	n.add(e.keep(e.tmp), 1)
	e.tmp2 = putUint(protowire.AppendUint(e.tmp2[:0], 1, uint64(count)), 3, uint64(ptype))
	n.Meta = e.keep(protowire.AppendBytes(e.tmp[:0], 1, e.tmp2))
	n.kid(e.leaf(idx, iw, nil, count))
	n.kid(e.leaf(vals, w, patchNulls, count))
	return n, true
}

// isPatch says whether row i is a patch: with a null fill, the valid rows; else the null rows
// and the ones that are not equal to the fill.
func isPatch[T Number](v []T, nulls []bool, i int, fill T, fillNull bool) bool {
	if fillNull {
		return !isNull(nulls, i)
	}
	return isNull(nulls, i) || v[i] != fill
}

// fillValue is the most frequent valid value of v, a zero when the fill is null.
func fillValue[T Number](e *Encoder, v []T, nulls []bool, fillNull bool) T {
	var fill T
	if fillNull {
		return fill
	}
	k := keys{raw: unsafex.Bytes(v), w: unsafex.Size[T](), kind: keyNumbers}
	distinct, nullCode := e.classify(&k, len(v), nulls)
	defer e.done()
	best := -1
	for c := range distinct {
		if c != nullCode && (best < 0 || e.dt.count[c] > e.dt.count[best]) {
			best = c
		}
	}
	return v[e.dt.first[best]]
}
