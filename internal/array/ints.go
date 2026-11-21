package array

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// varints reads the varint fields 1 to n of a protobuf message, 0 for the ones it lacks.
// Fields of other numbers and other wire types are skipped.
func varints(meta []byte, n int) ([]uint64, error) {
	out := make([]uint64, n)
	it := protowire.NewIter(meta)
	for it.Next() {
		if num := it.Num(); num >= 1 && num <= n && it.Type() == protowire.Varint {
			out[num-1] = it.Uint()
		}
	}
	return out, it.Err()
}

// message is the fields 1 to 7 of a protobuf message that are varints, and the ones that are
// length-delimited. Fields of other numbers are skipped.
type message struct {
	blob               [8][]byte
	varint             [8]uint64
	hasVarint, hasBlob [8]bool
}

// readMessage reads the fields of a protobuf message.
func readMessage(meta []byte) (message, error) {
	var m message
	it := protowire.NewIter(meta)
	for it.Next() {
		num := it.Num()
		if num < 1 || num > 7 {
			continue
		}
		switch it.Type() {
		case protowire.Varint:
			m.varint[num], m.hasVarint[num] = it.Uint(), true
		case protowire.Bytes:
			m.blob[num], m.hasBlob[num] = it.Bytes(), true
		}
	}
	return m, it.Err()
}

// intType checks that v, a PType of a metadata, is an integer type.
func intType(what string, v uint64) (PType, error) {
	if v > uint64(I64) {
		return 0, fmt.Errorf("%s is %d, not an integer type", what, v)
	}
	return PType(v), nil
}

// count converts a length of a metadata to an int.
func count(what string, v uint64) (int, error) {
	if v > math.MaxInt {
		return 0, fmt.Errorf("%s is %d, too large", what, v)
	}
	return int(v), nil
}

// ints decodes child i of the node as rows non-null integers of type p, as ints. A value
// that is negative, or does not fit in an int, is an error.
func (n *Node) ints(i int, what string, p PType, rows int) ([]int, error) {
	a, err := n.Child(i, DType{Kind: Prim, PType: p}, rows)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if a.Validity != nil {
		return nil, fmt.Errorf("%s: has nulls", what)
	}
	return toInts(n.ctx.opt.Arena, a, what, p, nil)
}

// fillInts sets out to the values of data, integers of type p, and reports whether every one
// of them is an int that is not negative. It stops at nothing: the caller that gets false reads
// the values again, to name the first one that is wrong.
func fillInts(out []int, data []byte, p PType) bool {
	switch p {
	case U8:
		for i, v := range data[:len(out)] {
			out[i] = int(v)
		}
		return true
	case U16:
		return widen(out, words[uint16](data))
	case U32:
		return widen(out, words[uint32](data))
	case U64:
		return widen(out, words[uint64](data))
	case I8:
		return widen(out, words[int8](data))
	case I16:
		return widen(out, words[int16](data))
	case I32:
		return widen(out, words[int32](data))
	}
	return widen(out, words[int64](data))
}

// widen sets out to the values of src, and reports whether none is negative or past an int.
func widen[T integer](out []int, src []T) bool {
	var bad int
	for i, v := range src[:len(out)] {
		out[i] = int(v)
		bad |= int(v)
	}
	return bad >= 0
}

// integer is an integer of a file.
type integer interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32 | ~uint64
}

// toInts reads the values of a, an array of integers of type p, as ints. A value that is
// negative, or does not fit in an int, is an error. The rows that valid marks null are 0:
// valid nil means every row is valid.
func toInts(ar *Arena, a *Array, what string, p PType, valid *Bitmap) ([]int, error) {
	w := p.Width()
	out := ar.Ints(a.Len, false)
	if fillInts(out, a.Data, p) {
		for k := 0; valid != nil && k < len(out); k++ {
			if !valid.Get(k) {
				out[k] = 0
			}
		}
		return out, nil
	}
	for k := range out {
		if valid != nil && !valid.Get(k) {
			out[k] = 0
			continue
		}
		var u uint64
		switch w {
		case 1:
			u = uint64(a.Data[k])
		case 2:
			u = uint64(binary.LittleEndian.Uint16(a.Data[2*k:]))
		case 4:
			u = uint64(binary.LittleEndian.Uint32(a.Data[4*k:]))
		default:
			u = binary.LittleEndian.Uint64(a.Data[8*k:])
		}
		if p.Signed() {
			u = uint64(int64(u) << (64 - 8*w) >> (64 - 8*w)) // sign-extend
			if int64(u) < 0 {
				return nil, fmt.Errorf("%s: negative value %d at row %d", what, int64(u), k)
			}
		}
		if u > math.MaxInt {
			return nil, fmt.Errorf("%s: value %d at row %d is too large", what, u, k)
		}
		out[k] = int(u)
	}
	return out, nil
}
