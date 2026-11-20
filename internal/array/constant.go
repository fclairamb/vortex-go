package array

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/fclairamb/vortex-go/internal/scalar"
)

// decodeConstant decodes vortex.constant: buffer 0 is a protobuf ScalarValue. The numbers,
// booleans, strings, binaries, decimals, lists and null are supported, structs and maps are not.
func decodeConstant(n *Node, t DType, rows int) (*Array, error) {
	if n.NumBuffers() != 1 {
		return nil, fmt.Errorf("expected 1 buffer, got %d", n.NumBuffers())
	}
	buf, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	v, err := scalar.Decode(buf)
	if err != nil {
		return nil, err
	}
	return constantArray(&v, t, rows)
}

func constantArray(v *scalar.Value, t DType, rows int) (*Array, error) {
	if t.Kind == Ext {
		return constantArray(v, *t.Storage, rows)
	}
	a := &Array{Type: t, Len: rows}
	if v.Kind == scalar.Null {
		return nullConstant(t, rows)
	}
	switch t.Kind {
	case Bool:
		if v.Kind != scalar.Bool {
			return nil, fmt.Errorf("a value of kind %d cannot be a bool", v.Kind)
		}
		a.Bits = Bitmap{Bytes: make([]byte, (rows+7)/8)}
		if v.Bool {
			for i := range a.Bits.Bytes {
				a.Bits.Bytes[i] = 0xff
			}
		}
	case Prim:
		one, err := primBytes(v, t.PType)
		if err != nil {
			return nil, err
		}
		a.Data = make([]byte, 0, rows*len(one))
		for range rows {
			a.Data = append(a.Data, one...)
		}
	case Utf8, Binary, Decimal:
		return bytesConstant(a, v, t, rows)
	case List, FixedSizeList:
		return listConstant(a, v, t, rows)
	default:
		return nil, fmt.Errorf("a constant of type %s: %w", t, errors.ErrUnsupported)
	}
	return a, nil
}

// bytesConstant is rows copies of a string, a binary or a decimal.
func bytesConstant(a *Array, v *scalar.Value, t DType, rows int) (*Array, error) {
	if t.Kind == Decimal {
		if v.Kind != scalar.Bytes || !slices.Contains(decimalWidths[:], len(v.Bytes)) {
			return nil, fmt.Errorf("a value of kind %d and %d bytes cannot be a decimal", v.Kind, len(v.Bytes))
		}
		a.Width = len(v.Bytes)
		a.Data = bytes.Repeat(v.Bytes, rows)
		return a, nil
	}
	data, err := stringBytes(v, t)
	if err != nil {
		return nil, err
	}
	a.Data = data
	a.Starts, a.Ends = make([]int, rows), repeat(rows, len(data))
	return a, nil
}

// stringBytes is the bytes of a string or binary scalar for type t.
func stringBytes(v *scalar.Value, t DType) ([]byte, error) {
	switch {
	case v.Kind == scalar.String && t.Kind == Utf8:
		return []byte(v.Str), nil
	case v.Kind == scalar.Bytes && t.Kind == Binary:
		return v.Bytes, nil
	case v.Kind == scalar.Bytes && utf8.Valid(v.Bytes):
		return v.Bytes, nil
	}
	return nil, fmt.Errorf("a value of kind %d cannot be a %s", v.Kind, t)
}

// repeat is n copies of v.
func repeat(n, v int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// listConstant is rows copies of the list of a list scalar.
func listConstant(a *Array, v *scalar.Value, t DType, rows int) (*Array, error) {
	if v.Kind != scalar.List {
		return nil, fmt.Errorf("a value of kind %d cannot be a %s", v.Kind, t)
	}
	n := len(v.List)
	if t.Kind == FixedSizeList && n != t.Size {
		return nil, fmt.Errorf("a list of %d elements cannot be a %s", n, t)
	}
	one := make([]*Array, n)
	for i := range v.List {
		var err error
		if one[i], err = constantArray(&v.List[i], *t.Elem, 1); err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
	}
	elems := Concat(*t.Elem, one)
	if n > 0 && rows > math.MaxInt/n {
		return nil, fmt.Errorf("%d lists of %d elements are too many", rows, n)
	}
	copies := make([]*Array, rows)
	for i := range copies {
		copies[i] = elems
	}
	a.Elems = Concat(*t.Elem, copies)
	if t.Kind == List {
		a.Starts, a.Ends = make([]int, rows), make([]int, rows)
		for i := range rows {
			a.Starts[i], a.Ends[i] = i*n, (i+1)*n
		}
	}
	return a, nil
}

// zeroArray is n rows of zero values, the content of a null constant. Structs are not
// supported yet.
func zeroArray(t DType, n int) (*Array, error) {
	a := &Array{Type: t, Len: n}
	switch t.Kind {
	case Null:
	case Bool:
		a.Bits = Bitmap{Bytes: make([]byte, (n+7)/8)}
	case Prim:
		a.Data = make([]byte, n*t.PType.Width())
	case Decimal:
		a.Width, a.Data = 1, make([]byte, n)
	case Utf8, Binary:
		a.Starts, a.Ends = make([]int, n), make([]int, n)
	case List, Map:
		a.Starts, a.Ends = make([]int, n), make([]int, n)
		return withElems(a, 0)
	case FixedSizeList:
		if t.Size > 0 && n > math.MaxInt/t.Size {
			return nil, fmt.Errorf("%d lists of %d elements are too many", n, t.Size)
		}
		return withElems(a, n*t.Size)
	case Ext:
		return zeroArray(*t.Storage, n)
	case Variant:
		a.Var = &VariantData{}
	default:
		return nil, fmt.Errorf("a constant of type %s: %w", t, errors.ErrUnsupported)
	}
	return a, nil
}

// withElems gives a the n zero elements of its type.
func withElems(a *Array, n int) (*Array, error) {
	var err error
	a.Elems, err = zeroArray(*a.Type.Elem, n)
	return a, err
}

// nullConstant is an array of rows nulls.
func nullConstant(t DType, rows int) (*Array, error) {
	a, err := zeroArray(t, rows)
	if err != nil || t.Kind == Null {
		return a, err
	}
	if !t.Nullable {
		return nil, fmt.Errorf("a null value cannot be a %s", t)
	}
	a.Validity = &Bitmap{Bytes: make([]byte, (rows+7)/8)}
	return a, nil
}

// primBytes is the little-endian encoding of v as a value of type p.
func primBytes(v *scalar.Value, p PType) ([]byte, error) {
	if p.Float() {
		return floatBytes(v, p)
	}
	var u uint64
	neg := false
	switch v.Kind {
	case scalar.Int:
		u, neg = uint64(v.Int), v.Int < 0
	case scalar.Uint:
		u = v.Uint
	default:
		return nil, fmt.Errorf("a value of kind %d cannot be a %s", v.Kind, p)
	}
	bits := uint(8 * p.Width())
	if p.Signed() {
		s := int64(u)
		if (!neg && u > math.MaxInt64) || s<<(64-bits)>>(64-bits) != s {
			return nil, fmt.Errorf("the value does not fit in %s", p)
		}
	} else if neg || (bits < 64 && u>>bits != 0) {
		return nil, fmt.Errorf("the value does not fit in %s", p)
	}
	return binary.LittleEndian.AppendUint64(nil, u)[:p.Width()], nil
}

func floatBytes(v *scalar.Value, p PType) ([]byte, error) {
	switch {
	case v.Kind == scalar.F16 && p == F16:
		return binary.LittleEndian.AppendUint16(nil, uint16(v.Uint)), nil
	case v.Kind == scalar.F32 && p == F32:
		return binary.LittleEndian.AppendUint32(nil, math.Float32bits(v.F32)), nil
	case v.Kind == scalar.F64 && p == F64:
		return binary.LittleEndian.AppendUint64(nil, math.Float64bits(v.F64)), nil
	}
	return nil, fmt.Errorf("a value of kind %d cannot be a %s", v.Kind, p)
}
