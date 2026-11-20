package array

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/fclairamb/vortex-go/internal/scalar"
)

// decodeConstant decodes vortex.constant: buffer 0 is a protobuf ScalarValue. The numbers,
// the booleans and null are supported, the other types join with their own encodings.
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
		return nullConstant(a, t, rows)
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
	default:
		return nil, fmt.Errorf("a constant of type %s: %w", t, errors.ErrUnsupported)
	}
	return a, nil
}

// nullConstant is an array of rows nulls.
func nullConstant(a *Array, t DType, rows int) (*Array, error) {
	switch t.Kind {
	case Null:
		return a, nil
	case Bool, Prim:
	default:
		return nil, fmt.Errorf("a constant of type %s: %w", t, errors.ErrUnsupported)
	}
	if !t.Nullable {
		return nil, fmt.Errorf("a null value cannot be a %s", t)
	}
	a.Validity = &Bitmap{Bytes: make([]byte, (rows+7)/8)}
	if t.Kind == Bool {
		a.Bits = Bitmap{Bytes: make([]byte, (rows+7)/8)}
	} else {
		a.Data = make([]byte, rows*t.PType.Width())
	}
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
