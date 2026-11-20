package array

import "fmt"

// decodeNull decodes vortex.null: no buffer, no child.
func decodeNull(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Null {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	return &Array{Type: t, Len: rows}, nil
}
