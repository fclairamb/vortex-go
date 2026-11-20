package array

import "fmt"

// decodeStruct decodes vortex.struct: the validity when there is one, then the fields.
func decodeStruct(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Struct {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	nf := len(t.Fields)
	first := n.NumChildren() - nf
	if first != 0 && first != 1 {
		return nil, fmt.Errorf("%d children, want %d or %d", n.NumChildren(), nf, nf+1)
	}
	a := &Array{Type: t, Len: rows, Fields: make([]*Array, nf)}
	if first == 1 {
		valid, err := n.Validity(0, rows)
		if err != nil {
			return nil, err
		}
		a.Validity = valid
	}
	for i, ft := range t.Fields {
		f, err := n.Field(first+i, ft.Name, ft.Type, rows)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", ft.Name, err)
		}
		a.Fields[i] = f
	}
	return a, nil
}
