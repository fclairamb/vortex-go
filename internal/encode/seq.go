package encode

import (
	"bytes"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// checkedAdd is a+b, and false when it does not fit in T.
func checkedAdd[T Int](a, b T) (T, bool) {
	s := a + b
	if signed[T]() {
		return s, (b <= 0 || s >= a) && (b >= 0 || s <= a)
	}
	return s, s >= a
}

// checkedSub is a-b, and false when it does not fit in T.
func checkedSub[T Int](a, b T) (T, bool) {
	d := a - b
	if signed[T]() {
		return d, (b <= 0 || d <= a) && (b >= 0 || d >= a)
	}
	return d, a >= b
}

// Sequence is the array of v when every value is the one before plus the same step, which is
// not zero and does not make a value overflow: the metadata holds the first value and the
// step (a signed integer), and there is no buffer. One value is a sequence of step 0. It returns false for an
// empty array, a null, or any other array.
func Sequence[T Int](e *Encoder, v []T, nulls []bool) (*Node, bool) {
	if len(v) == 0 || hasNull(nulls) {
		return nil, false
	}
	var step T
	if len(v) > 1 {
		var ok bool
		if step, ok = checkedSub(v[1], v[0]); !ok || step == 0 {
			return nil, false
		}
	}
	for i := 1; i < len(v); i++ {
		if next, ok := checkedAdd(v[i-1], step); !ok || next != v[i] {
			return nil, false
		}
	}
	n := e.node(IDSequence)
	var step8 [16]byte
	e.tmp2 = appendScalar(e.tmp2[:0], v[0])
	m := protowire.AppendBytes(e.tmp[:0], 1, e.tmp2)
	// the step is signed, whatever T is
	stepMsg := protowire.AppendSint(step8[:0], scalarInt, int64(step))
	e.tmp = protowire.AppendBytes(m, 2, stepMsg)
	n.Meta = e.keep(e.tmp)
	return n, true
}

// Constant is the array of v when every row has the same value, a float compared by its bits,
// or when every row is null: no metadata, and the value as a ScalarValue in the one buffer. It
// returns false for an empty array or any other array, a null among values included.
func Constant[T Number](e *Encoder, v []T, nulls []bool) (*Node, bool) {
	if len(v) == 0 {
		return nil, false
	}
	n := e.node(IDConstant)
	if hasNull(nulls) {
		for _, null := range nulls {
			if !null {
				return nil, false
			}
		}
		e.tmp = protowire.AppendUint(e.tmp[:0], scalarNull, 0)
		n.add(e.keep(e.tmp), 1)
		return n, true
	}
	w := unsafex.Size[T]()
	raw := unsafex.Bytes(v)
	for i := 1; i < len(v); i++ {
		if getWidth(raw[i*w:], w) != getWidth(raw, w) {
			return nil, false
		}
	}
	e.tmp = appendScalar(e.tmp[:0], v[0])
	n.add(e.keep(e.tmp), 1)
	return n, true
}

// The fields of the ScalarValue message of strings and bytes.
const (
	scalarString = 7
	scalarBytes  = 8
)

// ConstantBytes is Constant for byte slices: the value is a string when utf8 is set, else bytes.
func ConstantBytes(e *Encoder, v [][]byte, nulls []bool, utf8 bool) (*Node, bool) {
	if len(v) == 0 || hasNull(nulls) && !allNull(nulls) {
		return nil, false
	}
	n := e.node(IDConstant)
	if hasNull(nulls) {
		e.tmp = protowire.AppendUint(e.tmp[:0], scalarNull, 0)
		n.add(e.keep(e.tmp), 1)
		return n, true
	}
	for _, x := range v[1:] {
		if !bytes.Equal(x, v[0]) {
			return nil, false
		}
	}
	field := scalarBytes
	if utf8 {
		field = scalarString
	}
	e.tmp = protowire.AppendBytes(e.tmp[:0], field, v[0])
	n.add(e.keep(e.tmp), 1)
	return n, true
}

// ConstantBools is Constant for booleans.
func ConstantBools(e *Encoder, v []bool, nulls []bool) (*Node, bool) {
	if len(v) == 0 || hasNull(nulls) && !allNull(nulls) {
		return nil, false
	}
	n := e.node(IDConstant)
	if hasNull(nulls) {
		e.tmp = protowire.AppendUint(e.tmp[:0], scalarNull, 0)
		n.add(e.keep(e.tmp), 1)
		return n, true
	}
	for _, x := range v[1:] {
		if x != v[0] {
			return nil, false
		}
	}
	e.tmp = protowire.AppendUint(e.tmp[:0], scalarBool, uint64(boolInt(v[0])))
	n.add(e.keep(e.tmp), 1)
	return n, true
}
