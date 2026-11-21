package array

import (
	"errors"
	"fmt"
)

// decodeDecimalBytePartsV1 decodes vortex.decimal_byte_parts as the v1 format writes it:
// metadata {zeroth_child_ptype, lower_part_count}, one child, the most significant part, a
// signed integer array that carries the nulls. With no lower part, the decimal is that integer
// sign-extended: the values are kept at the width of the part, which every consumer widens.
// Lower parts belong to the v2 format, under another identifier.
func decodeDecimalBytePartsV1(n *Node, t DType, rows int) (*Array, error) {
	p, err := n.decimalPartsType(t)
	if err != nil {
		return nil, err
	}
	msp, err := n.primChild(0, p, t, rows)
	if err != nil {
		return nil, fmt.Errorf("most significant part: %w", err)
	}
	return &Array{Type: t, Len: rows, Data: msp.Data, Width: p.Width(), Validity: msp.Validity}, nil
}

// decimalPartsType reads a vortex.decimal_byte_parts node of an array of type t, and returns
// the type of its most significant part.
func (n *Node) decimalPartsType(t DType) (PType, error) {
	if t.Kind != Decimal {
		return 0, fmt.Errorf("cannot be a %s", t)
	}
	meta, err := varints(n.Metadata(), 2)
	if err != nil {
		return 0, err
	}
	p, err := intType("zeroth_child_ptype", meta[0])
	if err != nil {
		return 0, err
	}
	if !p.Signed() {
		return 0, fmt.Errorf("most significant part is %s, not a signed integer", p)
	}
	if meta[1] != 0 {
		return 0, fmt.Errorf("lower_part_count is %d: lower parts are not supported: %w", meta[1], errors.ErrUnsupported)
	}
	if n.NumChildren() != 1 {
		return 0, fmt.Errorf("expected 1 child, got %d", n.NumChildren())
	}
	if n.NumBuffers() != 0 {
		return 0, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	return p, nil
}
