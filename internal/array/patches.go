package array

import "fmt"

// patches are values that replace the ones of another array: Values[k] is the value at row
// Index[k] - offset.
type patches struct {
	values *Array
	index  []int
	offset int
	// children is the number of children the patches take, from the first one.
	children int
}

// patchesMeta is the PatchesMetadata message.
type patchesMeta struct {
	length, offset int
	indices        PType
	chunkOffsets   bool // the chunk offsets are the third child
}

// parsePatchesMeta reads {len, offset, indices_ptype, chunk_offsets_len?, chunk_offsets_ptype?,
// offset_within_chunk?}.
func parsePatchesMeta(b []byte) (m patchesMeta, err error) {
	f, err := readMessage(b)
	if err != nil {
		return m, err
	}
	if m.length, err = count("patches len", f.varint[1]); err != nil {
		return m, err
	}
	if m.offset, err = count("patches offset", f.varint[2]); err != nil {
		return m, err
	}
	if m.indices, err = intType("patches indices_ptype", f.varint[3]); err != nil {
		return m, err
	}
	if m.indices.Signed() {
		return m, fmt.Errorf("patch indices are %s, not unsigned", m.indices)
	}
	m.chunkOffsets = f.hasVarint[5]
	if m.chunkOffsets {
		_, err = intType("patches chunk_offsets_ptype", f.varint[5])
	}
	return m, err
}

// patchesChild decodes the patches of the message m, whose indices are child first of the
// node, then the values (of type t, rows of the array), then the chunk offsets if m has some.
// The chunk offsets only speed up a lookup, they are not read. Every index is checked against
// the rows of the array.
func (n *Node) patchesChild(m patchesMeta, first int, t DType, rows int) (*patches, error) {
	p := &patches{offset: m.offset, children: 2}
	if m.chunkOffsets {
		p.children = 3
	}
	if n.NumChildren() < first+p.children {
		return nil, fmt.Errorf("patches need %d children from child %d, the node has %d", p.children, first, n.NumChildren())
	}
	var err error
	if p.index, err = n.ints(first, "patch indices", m.indices, m.length); err != nil {
		return nil, err
	}
	for k, i := range p.index {
		if i < m.offset || i-m.offset >= rows {
			return nil, fmt.Errorf("patch %d is at index %d, offset %d, in an array of %d rows", k, i, m.offset, rows)
		}
	}
	if p.values, err = n.Child(first+1, t, m.length); err != nil {
		return nil, fmt.Errorf("patch values: %w", err)
	}
	return p, nil
}

// row is the row of the array patch k replaces.
func (p *patches) row(k int) int { return p.index[k] - p.offset }

// apply overwrites the rows of the integers in data, w bytes each, with the patch values. A
// patch value must not be null.
func (p *patches) apply(data []byte, w int) error {
	for k := range p.index {
		if p.values.Null(k) {
			return fmt.Errorf("patch %d is null", k)
		}
		copy(data[p.row(k)*w:], p.values.Data[k*w:(k+1)*w])
	}
	return nil
}
