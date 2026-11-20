package encode

import (
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// RunEnd is the run-end encoding of v: the children are the end of each run, in the smallest
// unsigned type that holds the number of rows, and the value of the run (the last of its rows).
// A run is rows equal to the one before: two nulls are equal, a null and a value are not, and
// the numbers are compared as numbers, so a NaN is a run of its own and 0 and -0 are one.
func RunEnd[T Number](e *Encoder, v []T, nulls []bool) *Node {
	w := unsafex.Size[T]()
	runs := 0
	for i := range v {
		if i == 0 || !sameRow(v, nulls, i-1, i) {
			runs++
		}
	}
	ptype, ew := narrow(uint64(len(v)))
	ends, vals, runNulls := e.alloc(runs*ew), e.alloc(runs*w), e.flags(runs)
	k := 0
	raw := unsafex.Bytes(v)
	for i := range v {
		if i+1 < len(v) && sameRow(v, nulls, i, i+1) {
			continue
		}
		putWidth(ends[k*ew:], ew, uint64(i+1))
		if isNull(nulls, i) {
			runNulls[k] = true
		} else {
			copy(vals[k*w:], raw[i*w:(i+1)*w])
		}
		k++
	}
	n := e.node(IDRunEnd)
	e.tmp = putUint(e.tmp[:0], 1, uint64(ptype))
	e.tmp = putUint(e.tmp, 2, uint64(runs))
	n.Meta = e.keep(e.tmp)
	n.kid(e.leaf(ends, ew, nil, runs))
	n.kid(e.leaf(vals, w, runNulls, runs))
	return n
}

// sameRow says whether rows i and j are in the same run.
func sameRow[T Number](v []T, nulls []bool, i, j int) bool {
	if isNull(nulls, i) || isNull(nulls, j) {
		return isNull(nulls, i) && isNull(nulls, j)
	}
	return v[i] == v[j]
}
