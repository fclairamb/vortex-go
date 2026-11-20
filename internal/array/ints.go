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
	w := p.Width()
	out := make([]int, rows)
	for k := range out {
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
