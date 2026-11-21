package array

import "fmt"

// decodeBitPacked decodes fastlanes.bitpacked: metadata {bit_width, offset, patches?}, buffer 0
// the packed blocks, children the patches (indices, values, and their chunk offsets if the
// metadata has some) then the validity. Signed types are packed as unsigned.
func decodeBitPacked(n *Node, t DType, rows int) (*Array, error) {
	m, err := n.bitPackedParts(t, rows)
	if err != nil {
		return nil, err
	}
	width := t.PType.Width()
	a := &Array{Type: t, Len: rows, Data: n.ctx.opt.Arena.Bytes(rows*width, false), fresh: true, Validity: m.valid}
	unpackWidth(width, a.Data, m.packed, m.w, m.off, rows)
	if m.patches != nil {
		if err := m.patches.apply(a.Data, width); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// bitPacked is what a fastlanes.bitpacked node holds.
type bitPacked struct {
	patches *patches
	valid   *Bitmap
	packed  []byte
	w, off  int // the bits of a value, and the place of row 0 in the first block
}

// bitPackedParts reads a fastlanes.bitpacked node of an array of type t and of that many rows.
func (n *Node) bitPackedParts(t DType, rows int) (m bitPacked, err error) {
	width, err := intPrim(t)
	if err != nil {
		return m, err
	}
	meta, err := readMessage(n.Metadata())
	if err != nil {
		return m, err
	}
	w, off := meta.varint[1], meta.varint[2]
	if w > uint64(8*width) {
		return m, fmt.Errorf("bit width %d is past the %d bits of %s", w, 8*width, t)
	}
	if off >= blockSize {
		return m, fmt.Errorf("offset %d is not under %d", off, blockSize)
	}
	m.w, m.off = int(w), int(off)
	if m.packed, err = n.packedBlocks(m.w, m.off, rows); err != nil {
		return m, err
	}
	validity := 0
	if meta.hasBlob[3] {
		pm, err := parsePatchesMeta(meta.blob[3])
		if err != nil {
			return m, err
		}
		if m.patches, err = n.patchesChild(pm, 0, t, rows); err != nil {
			return m, err
		}
		validity = m.patches.children
	}
	m.valid, err = n.validityFrom(validity, rows)
	return m, err
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
