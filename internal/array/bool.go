package array

import (
	"fmt"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// boolOffset reads the metadata of vortex.bool: {offset: uint32}, a bit offset under 8.
func boolOffset(meta []byte) (int, error) {
	off := uint64(0)
	it := protowire.NewIter(meta)
	for it.Next() {
		if it.Num() == 1 && it.Type() == protowire.Varint {
			off = it.Uint()
		}
	}
	if err := it.Err(); err != nil {
		return 0, err
	}
	if off >= 8 {
		return 0, fmt.Errorf("bit offset %d, want less than 8", off)
	}
	return int(off), nil
}

// decodeBool decodes vortex.bool: buffer 0 is the bitmap, child 0 the validity.
func decodeBool(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Bool {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	off, err := boolOffset(n.Metadata())
	if err != nil {
		return nil, err
	}
	buf, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	if len(buf)*8 < off+rows {
		return nil, fmt.Errorf("buffer of %d bytes cannot hold %d bits from bit %d", len(buf), rows, off)
	}
	valid, err := n.optionalValidity(rows)
	if err != nil {
		return nil, err
	}
	return &Array{Type: t, Len: rows, Bits: Bitmap{Bytes: buf, Off: off}, Validity: valid}, nil
}
