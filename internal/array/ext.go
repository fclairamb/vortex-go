package array

import "fmt"

// decodeExt decodes vortex.ext: child 0 is the storage, which is what the caller gets.
// Date, time and timestamp columns give their storage.
func decodeExt(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Ext {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	if n.NumChildren() != 1 {
		return nil, fmt.Errorf("expected 1 child, got %d", n.NumChildren())
	}
	return n.Child(0, *t.Storage, rows)
}
