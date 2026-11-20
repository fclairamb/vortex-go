package array

import (
	"fmt"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// decodeBitPacked decodes fastlanes.bitpacked: metadata {bit_width, offset, patches?}, buffer 0
// the packed blocks, children the patches (indices, values, and their chunk offsets if the
// metadata has some) then the validity. Signed types are packed as unsigned.
func decodeBitPacked(n *Node, t DType, rows int) (*Array, error) {
	width, err := intPrim(t)
	if err != nil {
		return nil, err
	}
	var bitWidth, offset uint64
	var patchMsg []byte
	hasPatches := false
	it := protowire.NewIter(n.Metadata())
	for it.Next() {
		switch {
		case it.Num() == 1 && it.Type() == protowire.Varint:
			bitWidth = it.Uint()
		case it.Num() == 2 && it.Type() == protowire.Varint:
			offset = it.Uint()
		case it.Num() == 3 && it.Type() == protowire.Bytes:
			patchMsg, hasPatches = it.Bytes(), true
		}
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	if bitWidth > uint64(8*width) {
		return nil, fmt.Errorf("bit width %d is past the %d bits of %s", bitWidth, 8*width, t)
	}
	if offset >= blockSize {
		return nil, fmt.Errorf("offset %d is not under %d", offset, blockSize)
	}
	w, off := int(bitWidth), int(offset)
	if n.NumBuffers() != 1 {
		return nil, fmt.Errorf("expected 1 buffer, got %d", n.NumBuffers())
	}
	packed, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	blocks := (rows + off + blockSize - 1) / blockSize
	if want := blocks * 128 * w; len(packed) != want {
		return nil, fmt.Errorf("buffer of %d bytes, want %d for %d values of %d bits from offset %d", len(packed), want, rows, w, off)
	}
	var pt *patches
	validity := 0
	if hasPatches {
		m, err := parsePatchesMeta(patchMsg)
		if err != nil {
			return nil, err
		}
		if pt, err = n.patchesChild(m, 0, t, rows); err != nil {
			return nil, err
		}
		validity = pt.children
	}
	a := &Array{Type: t, Len: rows, Data: make([]byte, rows*width)}
	switch c := n.NumChildren(); c {
	case validity:
	case validity + 1:
		if a.Validity, err = n.Validity(validity, rows); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("expected %d or %d children, got %d", validity, validity+1, c)
	}
	unpackWidth(width, a.Data, packed, w, off, rows)
	if pt != nil {
		if err := pt.apply(a.Data, width); err != nil {
			return nil, err
		}
	}
	return a, nil
}
