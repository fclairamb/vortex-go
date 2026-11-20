package array

import "fmt"

// decodeDict decodes vortex.dict: metadata {values_len, codes_ptype, is_nullable_codes?,
// all_values_referenced?}, children the codes and the values. A null code is a null row. The
// values are decoded once, whatever the number of rows.
func decodeDict(n *Node, t DType, rows int) (*Array, error) {
	meta, err := readMessage(n.Metadata())
	if err != nil {
		return nil, err
	}
	vlen, err := count("values_len", meta.varint[1])
	if err != nil {
		return nil, err
	}
	p, err := intType("codes_ptype", meta.varint[2])
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 || n.NumChildren() != 2 {
		return nil, fmt.Errorf("expected no buffer and 2 children, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	ct := DType{Kind: Prim, PType: p, Nullable: t.Nullable}
	if meta.hasVarint[3] {
		ct.Nullable = meta.varint[3] != 0
	}
	codes, err := n.Child(0, ct, rows)
	if err != nil {
		return nil, fmt.Errorf("codes: %w", err)
	}
	values, err := n.Child(1, t, vlen)
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}
	idx, err := toInts(codes, "codes", p, codes.Validity)
	if err != nil {
		return nil, err
	}
	for i, c := range idx {
		switch {
		case codes.Null(i):
			idx[i] = -1
		case c >= vlen:
			return nil, fmt.Errorf("code %d at row %d is past the %d values", c, i, vlen)
		}
	}
	return withType(take(values, idx), t), nil
}
