package array

import (
	"encoding/binary"
	"fmt"

	"github.com/fclairamb/vortex-go/internal/scalar"
)

// primChild decodes child i as the integers of type p with the nullability of t.
func (n *Node) primChild(i int, p PType, t DType, rows int) (*Array, error) {
	return n.Child(i, DType{Kind: Prim, PType: p, Nullable: t.Nullable}, rows)
}

// oneChild checks that the node has no metadata, no buffer and one child.
func (n *Node) oneChild() error {
	if err := n.noMetadata(); err != nil {
		return err
	}
	if n.NumBuffers() != 0 {
		return fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	if n.NumChildren() != 1 {
		return fmt.Errorf("expected 1 child, got %d", n.NumChildren())
	}
	return nil
}

// decodeFoR decodes fastlanes.for: the metadata is the reference, a ScalarValue, child 0 the
// encoded integers. A value is the encoded one plus the reference, wrapping.
func decodeFoR(n *Node, t DType, rows int) (*Array, error) {
	width, err := intPrim(t)
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 {
		return nil, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	if n.NumChildren() != 1 {
		return nil, fmt.Errorf("expected 1 child, got %d", n.NumChildren())
	}
	v, err := scalar.Decode(n.Metadata())
	if err != nil {
		return nil, fmt.Errorf("reference: %w", err)
	}
	ref, err := primBytes(&v, t.PType)
	if err != nil {
		return nil, fmt.Errorf("reference: %w", err)
	}
	enc, err := n.Child(0, t, rows)
	if err != nil {
		return nil, err
	}
	out := *enc
	out.Data = addConst(enc.Data, width, binary.LittleEndian.Uint64(append(ref, 0, 0, 0, 0, 0, 0, 0, 0)))
	return &out, nil
}

// decodeZigZag decodes vortex.zigzag: child 0 is the unsigned encoded integers.
func decodeZigZag(n *Node, t DType, rows int) (*Array, error) {
	width, err := intPrim(t)
	if err != nil {
		return nil, err
	}
	if !t.PType.Signed() {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if err := n.oneChild(); err != nil {
		return nil, err
	}
	enc, err := n.primChild(0, unsignedOf(t.PType), t, rows)
	if err != nil {
		return nil, err
	}
	out := *enc
	out.Type = t
	out.Data = unzigzag(enc.Data, width)
	return &out, nil
}
