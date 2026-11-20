package protowire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Type is a protobuf wire type.
type Type uint8

// The wire types a message may use. Groups (3 and 4) are rejected, 6 and 7 do not exist.
const (
	Varint  Type = 0
	Fixed64 Type = 1
	Bytes   Type = 2
	Fixed32 Type = 5
)

// maxVarintLen is the longest valid varint, for a 64-bit value.
const maxVarintLen = 10

// ErrTruncated reports an input that ends inside a value.
var ErrTruncated = errors.New("protowire: truncated input")

// ErrVarint reports a varint longer than 10 bytes, or one that overflows 64 bits.
var ErrVarint = errors.New("protowire: varint overflows 64 bits")

// ReadVarint decodes the varint at the start of b and returns its value and its length.
func ReadVarint(b []byte) (v uint64, n int, err error) {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if i == maxVarintLen-1 && c > 1 {
			return 0, 0, ErrVarint
		}
		v |= uint64(c&0x7f) << (7 * uint(i))
		if c < 0x80 {
			return v, i + 1, nil
		}
	}
	return 0, 0, ErrTruncated
}

// Zigzag decodes a sint32 or sint64 value.
func Zigzag(v uint64) int64 {
	return int64(v>>1) ^ -int64(v&1)
}

// Iter walks the fields of one message. The zero value is not usable, use NewIter.
//
//	it := protowire.NewIter(b)
//	for it.Next() {
//		switch it.Num() { ... }
//	}
//	if err := it.Err(); err != nil { ... }
type Iter struct {
	err  error
	rest []byte
	data []byte
	val  uint64
	num  int
	typ  Type
}

// NewIter returns an iterator over the fields of the message b.
func NewIter(b []byte) *Iter {
	return &Iter{rest: b}
}

// Next advances to the next field. It returns false at the end of the message or on error.
func (it *Iter) Next() bool {
	if it.err != nil || len(it.rest) == 0 {
		return false
	}
	tag, n, err := ReadVarint(it.rest)
	if err != nil {
		return it.fail(err)
	}
	it.rest = it.rest[n:]
	num := tag >> 3
	if num == 0 || tag>>32 != 0 {
		return it.fail(fmt.Errorf("protowire: invalid field number %d", num))
	}
	it.num = int(num)
	it.typ = Type(tag & 7)
	switch it.typ {
	case Varint:
		it.val, n, err = ReadVarint(it.rest)
	case Fixed64:
		it.val, n, err = it.fixed(8)
	case Fixed32:
		it.val, n, err = it.fixed(4)
	case Bytes:
		n, err = it.lengthDelimited()
	default:
		err = fmt.Errorf("protowire: unsupported wire type %d for field %d", it.typ, it.num)
	}
	if err != nil {
		return it.fail(err)
	}
	it.rest = it.rest[n:]
	return true
}

// fixed reads a little-endian value of size bytes.
func (it *Iter) fixed(size int) (v uint64, n int, err error) {
	if len(it.rest) < size {
		return 0, 0, ErrTruncated
	}
	for i := size - 1; i >= 0; i-- {
		v = v<<8 | uint64(it.rest[i])
	}
	return v, size, nil
}

// lengthDelimited reads the length prefix and exposes the payload in it.data.
func (it *Iter) lengthDelimited() (int, error) {
	l, n, err := ReadVarint(it.rest)
	if err != nil {
		return 0, err
	}
	if l > uint64(len(it.rest)-n) {
		return 0, fmt.Errorf("protowire: field %d has length %d, %d bytes left: %w",
			it.num, l, len(it.rest)-n, ErrTruncated)
	}
	it.data = it.rest[n : n+int(l)]
	return n + int(l), nil
}

func (it *Iter) fail(err error) bool {
	it.err = err
	it.rest = nil
	return false
}

// Err returns the error that stopped the iteration, nil at the end of the message.
func (it *Iter) Err() error { return it.err }

// Num is the field number of the current field.
func (it *Iter) Num() int { return it.num }

// Type is the wire type of the current field.
func (it *Iter) Type() Type { return it.typ }

// Uint is the value of a Varint, Fixed32 or Fixed64 field (the raw bits for the fixed ones).
func (it *Iter) Uint() uint64 { return it.val }

// Bytes is the payload of a Bytes field. It aliases the input.
func (it *Iter) Bytes() []byte { return it.data }

// Float32 is the value of a Fixed32 field read as a float.
func (it *Iter) Float32() float32 { return math.Float32frombits(uint32(it.val)) }

// Float64 is the value of a Fixed64 field read as a double.
func (it *Iter) Float64() float64 { return math.Float64frombits(it.val) }

// PackedVarints decodes the payload of a packed repeated varint field.
func PackedVarints(b []byte) ([]uint64, error) {
	var out []uint64
	for len(b) > 0 {
		v, n, err := ReadVarint(b)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		b = b[n:]
	}
	return out, nil
}

// PackedFixed32 decodes the payload of a packed repeated fixed32 (or float) field, as raw bits.
func PackedFixed32(b []byte) ([]uint32, error) {
	if len(b)%4 != 0 {
		return nil, ErrTruncated
	}
	out := make([]uint32, len(b)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(b[4*i:])
	}
	return out, nil
}

// PackedFixed64 decodes the payload of a packed repeated fixed64 (or double) field, as raw bits.
func PackedFixed64(b []byte) ([]uint64, error) {
	if len(b)%8 != 0 {
		return nil, ErrTruncated
	}
	out := make([]uint64, len(b)/8)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(b[8*i:])
	}
	return out, nil
}
