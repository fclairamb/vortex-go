package scalar

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// Kind is the arm of the ScalarValue oneof that was set.
type Kind uint8

// The arms of the oneof, in field number order.
const (
	Null   Kind = iota + 1 // null_value = 1
	Bool                   // bool_value = 2
	Int                    // int64_value = 3, a sint64 (zigzag)
	Uint                   // uint64_value = 4
	F32                    // f32_value = 5
	F64                    // f64_value = 6
	String                 // string_value = 7
	Bytes                  // bytes_value = 8, also a decimal: little-endian at its storage width
	List                   // list_value = 9
	F16                    // f16_value = 10, the 16 bits of the half float
)

// The arms of the oneof this package does not decode yet.
const (
	fieldVariant = 11
	fieldUnion   = 12
)

// maxDepth bounds the nesting of list values.
const maxDepth = 64

// ErrNoKind reports a message with none of the arms set.
var ErrNoKind = errors.New("scalar: no value kind set")

// Value is a decoded ScalarValue. Only the field of its Kind is set.
type Value struct {
	Str   string
	Bytes []byte // aliases the input
	List  []Value
	Int   int64
	Uint  uint64 // Uint, and the bits of an F16
	F64   float64
	F32   float32
	Kind  Kind
	Bool  bool
}

// Decode decodes the bytes of a ScalarValue message. As in protobuf, when several arms of the
// oneof appear the last one wins, and unknown fields are skipped.
func Decode(b []byte) (Value, error) {
	return decode(b, 0)
}

// wire is the wire type each arm must have, by field number.
var wire = map[int]protowire.Type{
	1: protowire.Varint, 2: protowire.Varint, 3: protowire.Varint, 4: protowire.Varint,
	5: protowire.Fixed32, 6: protowire.Fixed64, 7: protowire.Bytes, 8: protowire.Bytes,
	9: protowire.Bytes, 10: protowire.Varint,
}

func decode(b []byte, depth int) (Value, error) {
	if depth > maxDepth {
		return Value{}, fmt.Errorf("scalar: lists nested deeper than %d", maxDepth)
	}
	var v Value
	it := protowire.NewIter(b)
	for it.Next() {
		if it.Num() == fieldVariant || it.Num() == fieldUnion {
			return Value{}, fmt.Errorf("scalar: field %d (variant or union value) is not supported yet", it.Num())
		}
		want, known := wire[it.Num()]
		if !known {
			continue // unknown field, or an arm we reject below
		}
		if it.Type() != want {
			return Value{}, fmt.Errorf("scalar: field %d has wire type %d, want %d", it.Num(), it.Type(), want)
		}
		var err error
		if v, err = arm(it, depth); err != nil {
			return Value{}, err
		}
	}
	if err := it.Err(); err != nil {
		return Value{}, err
	}
	if v.Kind == 0 {
		return Value{}, ErrNoKind
	}
	return v, nil
}

// arm builds the Value of the field the iterator is on.
func arm(it *protowire.Iter, depth int) (Value, error) {
	k := Kind(it.Num())
	v := Value{Kind: k}
	switch k {
	case Null:
	case Bool:
		v.Bool = it.Uint() != 0
	case Int:
		v.Int = protowire.Zigzag(it.Uint())
	case Uint, F16:
		v.Uint = it.Uint()
	case F32:
		v.F32 = it.Float32()
	case F64:
		v.F64 = it.Float64()
	case String:
		if !utf8.Valid(it.Bytes()) {
			return Value{}, errors.New("scalar: string value is not valid UTF-8")
		}
		v.Str = string(it.Bytes())
	case Bytes:
		v.Bytes = it.Bytes()
	default: // List
		list, err := decodeList(it.Bytes(), depth)
		if err != nil {
			return Value{}, err
		}
		v.List = list
	}
	return v, nil
}

// decodeList decodes a ListValue: repeated ScalarValue values = 1.
func decodeList(b []byte, depth int) ([]Value, error) {
	var out []Value
	it := protowire.NewIter(b)
	for it.Next() {
		if it.Num() != 1 {
			continue
		}
		if it.Type() != protowire.Bytes {
			return nil, fmt.Errorf("scalar: list value has wire type %d", it.Type())
		}
		v, err := decode(it.Bytes(), depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, it.Err()
}
