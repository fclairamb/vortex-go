package array

import (
	"fmt"
	"math"
)

// elemType is the element type of a list type, or an error.
func elemType(t DType, k Kind) (DType, error) {
	if t.Kind != k || t.Elem == nil {
		return DType{}, fmt.Errorf("cannot be a %s", t)
	}
	return *t.Elem, nil
}

// decodeList decodes vortex.list: metadata {elements_len, offset_ptype}, children: the
// elements, the offsets (rows + 1) and the validity.
func decodeList(n *Node, t DType, rows int) (*Array, error) {
	et, err := elemType(t, List)
	if err != nil {
		return nil, err
	}
	meta, err := varints(n.Metadata(), 2)
	if err != nil {
		return nil, err
	}
	elen, err := count("elements_len", meta[0])
	if err != nil {
		return nil, err
	}
	p, err := intType("offset_ptype", meta[1])
	if err != nil {
		return nil, err
	}
	if c := n.NumChildren(); c != 2 && c != 3 {
		return nil, fmt.Errorf("expected 2 or 3 children, got %d", c)
	}
	a := &Array{Type: t, Len: rows}
	if a.Elems, err = n.Child(0, et, elen); err != nil {
		return nil, fmt.Errorf("elements: %w", err)
	}
	offs, err := n.ints(1, "offsets", p, rows+1)
	if err != nil {
		return nil, err
	}
	if err := checkOffsets(offs, elen); err != nil {
		return nil, err
	}
	a.Starts, a.Ends = offs[:rows], offs[1:]
	if n.NumChildren() == 3 {
		if a.Validity, err = n.Validity(2, rows); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// decodeListView decodes vortex.listview: metadata {elements_len, offset_ptype, size_ptype},
// children: the elements, the offsets, the sizes (rows each) and the validity. The views may
// overlap and come in any order.
func decodeListView(n *Node, t DType, rows int) (*Array, error) {
	et, err := elemType(t, List)
	if err != nil {
		return nil, err
	}
	meta, err := varints(n.Metadata(), 3)
	if err != nil {
		return nil, err
	}
	elen, err := count("elements_len", meta[0])
	if err != nil {
		return nil, err
	}
	op, err := intType("offset_ptype", meta[1])
	if err != nil {
		return nil, err
	}
	sp, err := intType("size_ptype", meta[2])
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 {
		return nil, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	if c := n.NumChildren(); c != 3 && c != 4 {
		return nil, fmt.Errorf("expected 3 or 4 children, got %d", c)
	}
	a := &Array{Type: t, Len: rows}
	if a.Elems, err = n.Child(0, et, elen); err != nil {
		return nil, fmt.Errorf("elements: %w", err)
	}
	if a.Starts, err = n.ints(1, "offsets", op, rows); err != nil {
		return nil, err
	}
	sizes, err := n.ints(2, "sizes", sp, rows)
	if err != nil {
		return nil, err
	}
	if err := viewEnds(a.Starts, sizes, elen); err != nil {
		return nil, err
	}
	a.Ends = sizes
	if n.NumChildren() == 4 {
		if a.Validity, err = n.Validity(3, rows); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// viewEnds checks that every view fits in the elen elements, and turns the sizes into ends.
func viewEnds(starts, sizes []int, elen int) error {
	for i, off := range starts {
		if off > elen || sizes[i] > elen-off {
			return fmt.Errorf("row %d is %d elements from %d, the list has %d elements", i, sizes[i], off, elen)
		}
		sizes[i] += off
	}
	return nil
}

// decodeFixedSizeList decodes vortex.fixed_size_list: child 0 the elements (rows times the
// size), child 1 the validity.
func decodeFixedSizeList(n *Node, t DType, rows int) (*Array, error) {
	et, err := elemType(t, FixedSizeList)
	if err != nil {
		return nil, err
	}
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 {
		return nil, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	if c := n.NumChildren(); c != 1 && c != 2 {
		return nil, fmt.Errorf("expected 1 or 2 children, got %d", c)
	}
	if t.Size > 0 && rows > math.MaxInt/t.Size {
		return nil, fmt.Errorf("%d lists of %d elements are too many", rows, t.Size)
	}
	a := &Array{Type: t, Len: rows}
	if a.Elems, err = n.Child(0, et, rows*t.Size); err != nil {
		return nil, fmt.Errorf("elements: %w", err)
	}
	if n.NumChildren() == 2 {
		if a.Validity, err = n.Validity(1, rows); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// decodeMap decodes vortex.map: one child, a vortex.listview of the entries, structs of key
// and value.
func decodeMap(n *Node, t DType, rows int) (*Array, error) {
	et, err := elemType(t, Map)
	if err != nil {
		return nil, err
	}
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 {
		return nil, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	if n.NumChildren() != 1 {
		return nil, fmt.Errorf("expected 1 child, got %d", n.NumChildren())
	}
	entries, err := n.Child(0, DType{Kind: List, Elem: &et, Nullable: t.Nullable}, rows)
	if err != nil {
		return nil, fmt.Errorf("entries: %w", err)
	}
	if id := n.ChildEncoding(0); id != "vortex.listview" {
		return nil, fmt.Errorf("entries are %s, want vortex.listview", id)
	}
	out := *entries
	out.Type = t
	return &out, nil
}
