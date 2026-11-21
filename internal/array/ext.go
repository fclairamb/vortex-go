package array

import "fmt"

// decodeExt decodes vortex.ext: child 0 is the storage, which is what the caller gets.
// Date, time and timestamp columns give their storage.
func decodeExt(n *Node, t DType, rows int) (*Array, error) {
	if err := n.extParts(t); err != nil {
		return nil, err
	}
	return n.Child(0, *t.Storage, rows)
}

// extParts checks a vortex.ext node of an array of type t.
func (n *Node) extParts(t DType) error {
	if t.Kind != Ext {
		return fmt.Errorf("cannot be a %s", t)
	}
	if err := n.noMetadata(); err != nil {
		return err
	}
	if n.NumChildren() != 1 {
		return fmt.Errorf("expected 1 child, got %d", n.NumChildren())
	}
	return nil
}
