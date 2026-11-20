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

// Masked is the array of values under the validity of nulls: child 0 the values, child 1 the
// validity, no metadata and no buffer. values is of the non-nullable type. With no null row it
// is values itself.
func Masked(e *Encoder, values *Node, nulls []bool, rows int) *Node {
	v := e.validity(nulls, rows, rows)
	if v == nil {
		return values
	}
	n := e.node(IDMasked)
	n.kid(values)
	n.kid(v)
	return n
}

// ConstantMasked is Constant, and also the array of a chunk whose valid values are all equal
// and some rows are null: the constant of that value under the validity. It returns false for
// an empty array or one with two distinct valid values.
func ConstantMasked[T Number](e *Encoder, v []T, nulls []bool) (*Node, bool) {
	if n, ok := Constant(e, v, nulls); ok {
		return n, true
	}
	first := firstValid(nulls, len(v))
	if first < 0 {
		return nil, false
	}
	w := unsafex.Size[T]()
	raw := unsafex.Bytes(v)
	for i := first + 1; i < len(v); i++ {
		if !nulls[i] && getWidth(raw[i*w:], w) != getWidth(raw[first*w:], w) {
			return nil, false
		}
	}
	n := e.node(IDConstant)
	e.tmp = appendScalar(e.tmp[:0], v[first])
	n.add(e.keep(e.tmp), 1)
	return Masked(e, n, nulls, len(v)), true
}

// ConstantBytesMasked is ConstantMasked for byte slices.
func ConstantBytesMasked(e *Encoder, v [][]byte, nulls []bool, utf8 bool) (*Node, bool) {
	if n, ok := ConstantBytes(e, v, nulls, utf8); ok {
		return n, true
	}
	first := firstValid(nulls, len(v))
	if first < 0 {
		return nil, false
	}
	for i := first + 1; i < len(v); i++ {
		if !nulls[i] && !bytes.Equal(v[i], v[first]) {
			return nil, false
		}
	}
	field := scalarBytes
	if utf8 {
		field = scalarString
	}
	n := e.node(IDConstant)
	e.tmp = protowire.AppendBytes(e.tmp[:0], field, v[first])
	n.add(e.keep(e.tmp), 1)
	return Masked(e, n, nulls, len(v)), true
}

// ConstantBoolsMasked is ConstantMasked for booleans.
func ConstantBoolsMasked(e *Encoder, v []bool, nulls []bool) (*Node, bool) {
	if n, ok := ConstantBools(e, v, nulls); ok {
		return n, true
	}
	first := firstValid(nulls, len(v))
	if first < 0 {
		return nil, false
	}
	for i := first + 1; i < len(v); i++ {
		if !nulls[i] && v[i] != v[first] {
			return nil, false
		}
	}
	n := e.node(IDConstant)
	e.tmp = protowire.AppendUint(e.tmp[:0], scalarBool, uint64(boolInt(v[first])))
	n.add(e.keep(e.tmp), 1)
	return Masked(e, n, nulls, len(v)), true
}

// firstValid is the first row that is not null, or -1 when the array is empty, has no null
// (the constants handle it) or has no valid row.
func firstValid(nulls []bool, rows int) int {
	if rows == 0 || len(nulls) < rows {
		return -1
	}
	for i := range rows {
		if !nulls[i] {
			return i
		}
	}
	return -1
}
