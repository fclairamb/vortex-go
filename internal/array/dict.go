package array

import (
	"fmt"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// decodeDict decodes vortex.dict: metadata {values_len, codes_ptype, is_nullable_codes?,
// all_values_referenced?}, children the codes and the values. A null code is a null row. The
// values are decoded once, whatever the number of rows.
func decodeDict(n *Node, t DType, rows int) (*Array, error) {
	var valuesLen, codesType, nullable uint64
	nullableSet := false
	it := protowire.NewIter(n.Metadata())
	for it.Next() {
		if it.Type() != protowire.Varint {
			continue
		}
		switch it.Num() {
		case 1:
			valuesLen = it.Uint()
		case 2:
			codesType = it.Uint()
		case 3:
			nullable, nullableSet = it.Uint(), true
		}
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	vlen, err := count("values_len", valuesLen)
	if err != nil {
		return nil, err
	}
	p, err := intType("codes_ptype", codesType)
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 || n.NumChildren() != 2 {
		return nil, fmt.Errorf("expected no buffer and 2 children, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	ct := DType{Kind: Prim, PType: p, Nullable: t.Nullable}
	if nullableSet {
		ct.Nullable = nullable != 0
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
