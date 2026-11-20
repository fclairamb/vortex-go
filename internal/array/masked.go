package array

import "fmt"

// decodeMasked decodes vortex.masked: child 0 is the values, child 1 the validity.
func decodeMasked(n *Node, t DType, rows int) (*Array, error) {
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 {
		return nil, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	if c := n.NumChildren(); c != 1 && c != 2 {
		return nil, fmt.Errorf("expected 1 or 2 children, got %d", c)
	}
	values, err := n.Child(0, t.NonNullable(), rows)
	if err != nil {
		return nil, err
	}
	out := *values
	out.Type = t
	out.Validity = nil
	if n.NumChildren() == 2 {
		if out.Validity, err = n.Validity(1, rows); err != nil {
			return nil, err
		}
	}
	return &out, nil
}
