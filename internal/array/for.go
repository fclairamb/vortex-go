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
	width, ref, err := n.forParts(t)
	if err != nil {
		return nil, err
	}
	enc, err := n.Child(0, t, rows)
	if err != nil {
		return nil, err
	}
	out := *enc
	out.Data = addConst(n.target(enc), enc.Data, width, ref)
	out.fresh, out.bound = true, 0
	return &out, nil
}

// forParts reads a fastlanes.for node of an array of type t: the width of its integers, and
// the reference, as the bits of a 64-bit integer.
func (n *Node) forParts(t DType) (width int, ref uint64, err error) {
	if width, err = intPrim(t); err != nil {
		return 0, 0, err
	}
	if n.NumBuffers() != 0 {
		return 0, 0, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	if n.NumChildren() != 1 {
		return 0, 0, fmt.Errorf("expected 1 child, got %d", n.NumChildren())
	}
	v, err := scalar.Decode(n.Metadata())
	if err != nil {
		return 0, 0, fmt.Errorf("reference: %w", err)
	}
	b, err := primBytes(&v, t.PType)
	if err != nil {
		return 0, 0, fmt.Errorf("reference: %w", err)
	}
	return width, binary.LittleEndian.Uint64(append(b, 0, 0, 0, 0, 0, 0, 0, 0)), nil
}

// zigZagParts checks a vortex.zigzag node of an array of type t, and returns the width of its
// integers.
func (n *Node) zigZagParts(t DType) (int, error) {
	width, err := intPrim(t)
	if err != nil {
		return 0, err
	}
	if !t.PType.Signed() {
		return 0, fmt.Errorf("cannot be a %s", t)
	}
	return width, n.oneChild()
}

// decodeZigZag decodes vortex.zigzag: child 0 is the unsigned encoded integers.
func decodeZigZag(n *Node, t DType, rows int) (*Array, error) {
	width, err := n.zigZagParts(t)
	if err != nil {
		return nil, err
	}
	enc, err := n.primChild(0, unsignedOf(t.PType), t, rows)
	if err != nil {
		return nil, err
	}
	out := *enc
	out.Type = t
	out.Data = unzigzag(n.target(enc), enc.Data, width)
	out.fresh, out.bound = true, 0
	return &out, nil
}
