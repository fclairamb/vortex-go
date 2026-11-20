package array

import "fmt"

// decodeByteBool decodes vortex.bytebool: buffer 0 has one byte per value, child 0 is the
// validity.
func decodeByteBool(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Bool {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	valid, err := n.optionalValidity(rows)
	if err != nil {
		return nil, err
	}
	buf, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	if len(buf) < rows {
		return nil, fmt.Errorf("buffer of %d bytes, want %d", len(buf), rows)
	}
	w := newBitWriter(rows)
	for _, b := range buf[:rows] {
		w.set(b != 0)
	}
	return &Array{Type: t, Len: rows, Bits: Bitmap{Bytes: w.bytes}, Validity: valid}, nil
}
