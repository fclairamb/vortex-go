package encode

import "github.com/fclairamb/vortex-go/internal/unsafex"

// Signed is the signed integer types, the ones ZigZag takes.
type Signed interface {
	~int8 | ~int16 | ~int32 | ~int64
}

// FoR is the frame of reference of the integers of v: the reference is the smallest valid
// value, and the child holds each value minus the reference (wrapping), zero for a null row.
// A column with no valid value has the reference 0.
func FoR[T Int](e *Encoder, v []T, nulls []bool) *Node {
	w := unsafex.Size[T]()
	var ref T
	found := false
	for i, x := range v {
		if !isNull(nulls, i) && (!found || x < ref) {
			ref, found = x, true
		}
	}
	out := e.allocDirty(len(v) * w)
	for i, x := range v {
		d := ubits(x - ref)
		if isNull(nulls, i) {
			d = 0
		}
		putWidth(out[i*w:], w, d)
	}
	n := e.node(IDFoR)
	e.tmp = appendScalar(e.tmp[:0], ref)
	n.Meta = e.keep(e.tmp)
	n.kid(e.leaf(out, w, nulls, len(v)))
	return n
}

// ZigZag maps the signed integers of v to unsigned ones that are small when the values are
// close to zero: 0, -1, 1, -2 become 0, 1, 2, 3. The slot of a null row is mapped too.
func ZigZag[T Signed](e *Encoder, v []T, nulls []bool) *Node {
	w := unsafex.Size[T]()
	out := e.allocDirty(len(v) * w)
	for i, x := range v {
		y := int64(x)
		putWidth(out[i*w:], w, uint64(y<<1)^uint64(y>>63))
	}
	n := e.node(IDZigZag)
	n.kid(e.leaf(out, w, nulls, len(v)))
	return n
}
