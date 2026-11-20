package array

import "fmt"

// decodePrimitive decodes vortex.primitive: buffer 0 is the values, little-endian, child 0
// the validity.
func decodePrimitive(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Prim {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	buf, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	if want := rows * t.PType.Width(); len(buf) != want {
		return nil, fmt.Errorf("buffer of %d bytes, want %d for %d %s values", len(buf), want, rows, t.PType)
	}
	valid, err := n.optionalValidity(rows)
	if err != nil {
		return nil, err
	}
	return &Array{Type: t, Len: rows, Data: buf, Validity: valid}, nil
}
