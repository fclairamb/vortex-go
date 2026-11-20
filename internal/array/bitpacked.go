package array

import "fmt"

// decodeBitPacked decodes fastlanes.bitpacked: metadata {bit_width, offset, patches?}, buffer 0
// the packed blocks, children the patches (indices, values, and their chunk offsets if the
// metadata has some) then the validity. Signed types are packed as unsigned.
func decodeBitPacked(n *Node, t DType, rows int) (*Array, error) {
	width, err := intPrim(t)
	if err != nil {
		return nil, err
	}
	meta, err := readMessage(n.Metadata())
	if err != nil {
		return nil, err
	}
	w, off := meta.varint[1], meta.varint[2]
	if w > uint64(8*width) {
		return nil, fmt.Errorf("bit width %d is past the %d bits of %s", w, 8*width, t)
	}
	if off >= blockSize {
		return nil, fmt.Errorf("offset %d is not under %d", off, blockSize)
	}
	packed, err := n.packedBlocks(int(w), int(off), rows)
	if err != nil {
		return nil, err
	}
	var pt *patches
	validity := 0
	if meta.hasBlob[3] {
		m, err := parsePatchesMeta(meta.blob[3])
		if err != nil {
			return nil, err
		}
		if pt, err = n.patchesChild(m, 0, t, rows); err != nil {
			return nil, err
		}
		validity = pt.children
	}
	a := &Array{Type: t, Len: rows, Data: make([]byte, rows*width)}
	if a.Validity, err = n.validityFrom(validity, rows); err != nil {
		return nil, err
	}
	unpackWidth(width, a.Data, packed, int(w), int(off), rows)
	if pt != nil {
		if err := pt.apply(a.Data, width); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// packedBlocks is buffer 0 of the node, the blocks of rows values of w bits from value off of
// the first block.
func (n *Node) packedBlocks(w, off, rows int) ([]byte, error) {
	if n.NumBuffers() != 1 {
		return nil, fmt.Errorf("expected 1 buffer, got %d", n.NumBuffers())
	}
	packed, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	blocks := (rows + off + blockSize - 1) / blockSize
	if want := blocks * 128 * w; len(packed) != want {
		return nil, fmt.Errorf("buffer of %d bytes, want %d for %d values of %d bits from offset %d",
			len(packed), want, rows, w, off)
	}
	return packed, nil
}

// validityFrom reads the validity, which is child i if the node has one more than i.
func (n *Node) validityFrom(i, rows int) (*Bitmap, error) {
	switch c := n.NumChildren(); c {
	case i:
		return nil, nil
	case i + 1:
		return n.Validity(i, rows)
	default:
		return nil, fmt.Errorf("expected %d or %d children, got %d", i, i+1, c)
	}
}
