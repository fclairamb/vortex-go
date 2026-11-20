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
	patch := func(i int) bool {
		if fillNull {
			return !isNull(nulls, i)
		}
		return isNull(nulls, i) || v[i] != fill
	}
	count := 0
	for i := range v {
		if patch(i) {
			count++
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
	idx, vals, patchNulls := e.alloc(count*iw), e.alloc(count*w), e.flags(count)
	validPatches := 0
	for i := range v {
		if patch(i) && !isNull(nulls, i) {
			validPatches++
		}
	}
	k := 0
	for i := range v {
		if !patch(i) {
			continue
		}
		putWidth(idx[k*iw:], iw, uint64(i))
		// the slots of null patches are kept, unless there is no valid patch
		patchNulls[k] = isNull(nulls, i)
		if validPatches > 0 || !patchNulls[k] {
			copy(vals[k*w:], raw[i*w:(i+1)*w])
		}
		k++
	}
	n := e.node(IDSparse)
	pm := protowire.AppendUint(e.tmp2[:0], 1, uint64(count))
	pm = putUint(pm, 3, uint64(ptype))
	e.tmp2 = pm
	n.Meta = e.keep(protowire.AppendBytes(e.tmp[:0], 1, pm))
	if fillNull {
		e.tmp = protowire.AppendUint(e.tmp[:0], scalarNull, 0)
	} else {
		e.tmp = appendScalar(e.tmp[:0], fill)
	}
	n.add(e.keep(e.tmp), 1)
	n.kid(e.leaf(idx, iw, nil, count))
	n.kid(e.leaf(vals, w, patchNulls, count))
	return n, true
}

// fillValue is the most frequent valid value of v, a zero when the fill is null.
func fillValue[T Number](e *Encoder, v []T, nulls []bool, fillNull bool) T {
	var fill T
	if fillNull {
		return fill
	}
	k := keys{raw: unsafex.Bytes(v), w: unsafex.Size[T](), kind: keyNumbers}
	distinct, nullCode := e.classify(&k, len(v), nulls)
	best := -1
	for c := range distinct {
		if c != nullCode && (best < 0 || e.count[c] > e.count[best]) {
			best = c
		}
	}
	return v[e.first[best]]
}
