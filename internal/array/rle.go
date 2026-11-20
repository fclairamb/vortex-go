package array

import (
	"encoding/binary"
	"fmt"
	"slices"
)

// rleMeta is the metadata of fastlanes.rle: {values_len, indices_len, indices_ptype,
// values_idx_offsets_len, values_idx_offsets_ptype, offset}.
type rleMeta struct {
	valuesLen, indicesLen, offsetsLen, offset int
	indices, offsets                          PType
}

func parseRLEMeta(b []byte) (m rleMeta, err error) {
	v, err := varints(b, 6)
	if err != nil {
		return m, err
	}
	if m.valuesLen, err = count("values_len", v[0]); err != nil {
		return m, err
	}
	if m.indicesLen, err = count("indices_len", v[1]); err != nil {
		return m, err
	}
	if m.indices, err = intType("indices_ptype", v[2]); err != nil {
		return m, err
	}
	if m.indices != U8 && m.indices != U16 {
		return m, fmt.Errorf("indices are %s, not u8 or u16", m.indices)
	}
	if m.offsetsLen, err = count("values_idx_offsets_len", v[3]); err != nil {
		return m, err
	}
	if m.offsets, err = intType("values_idx_offsets_ptype", v[4]); err != nil {
		return m, err
	}
	m.offset, err = count("offset", v[5])
	return m, err
}

// check checks the lengths of the metadata against the number of rows of the array.
func (m rleMeta) check(rows int) error {
	if m.indicesLen%blockSize != 0 || m.offset > m.indicesLen || rows > m.indicesLen-m.offset {
		return fmt.Errorf("%d indices cannot hold %d rows from offset %d, in blocks of %d",
			m.indicesLen, rows, m.offset, blockSize)
	}
	if m.offsetsLen != m.indicesLen/blockSize || m.indicesLen < m.valuesLen {
		return fmt.Errorf("%d offsets and %d values for %d indices", m.offsetsLen, m.valuesLen, m.indicesLen)
	}
	return nil
}

// decodeRLE decodes fastlanes.rle: children the values, the indices (u8 or u16, with the
// validity) and the offsets of the values of each block of 1024 indices. The values of block b
// are values[offsets[b]-offsets[0]:] up to the next block's. The array is rows indices from
// index offset.
func decodeRLE(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Prim {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	m, err := parseRLEMeta(n.Metadata())
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 || n.NumChildren() != 3 {
		return nil, fmt.Errorf("expected no buffer and 3 children, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	if err := m.check(rows); err != nil {
		return nil, err
	}
	values, err := n.Child(0, t.NonNullable(), m.valuesLen)
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}
	indices, err := n.primChild(1, m.indices, t, m.indicesLen)
	if err != nil {
		return nil, fmt.Errorf("indices: %w", err)
	}
	offs, err := n.valueOffsets(m)
	if err != nil {
		return nil, err
	}
	a := &Array{Type: t, Len: rows, Data: make([]byte, rows*t.PType.Width())}
	if indices.Validity != nil {
		v := indices.Validity.Slice(m.offset)
		a.Validity = &v
	}
	for c := m.offset / blockSize; c*blockSize < m.offset+rows; c++ {
		end := m.valuesLen
		if c+1 < len(offs) {
			end = offs[c+1] - offs[0]
		}
		if err := expandBlock(a, m, values, indices, c, offs[c]-offs[0], end); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// valueOffsets reads child 2, the offsets of the values of each block: they never decrease, and
// do not span more values than there are.
func (n *Node) valueOffsets(m rleMeta) ([]int, error) {
	offs, err := n.ints(2, "values index offsets", m.offsets, m.offsetsLen)
	if err != nil {
		return nil, err
	}
	if !slices.IsSorted(offs) {
		return nil, fmt.Errorf("values index offsets decrease")
	}
	if len(offs) > 0 && offs[len(offs)-1]-offs[0] > m.valuesLen {
		return nil, fmt.Errorf("values index offsets span %d values, there are %d", offs[len(offs)-1]-offs[0], m.valuesLen)
	}
	return offs, nil
}

// expandBlock writes the rows of block c to a: the index of a row is the position, among the
// values of the block, which are values[start:end], of the value of the row.
func expandBlock(a *Array, m rleMeta, values, indices *Array, c, start, end int) error {
	if start == end {
		return fmt.Errorf("block %d references no values", c)
	}
	w := a.Type.PType.Width()
	lo, hi := max(c*blockSize, m.offset), min((c+1)*blockSize, m.offset+a.Len)
	for p := lo; p < hi; p++ {
		i := 0 // a null row has no index
		switch {
		case indices.Null(p):
		case m.indices == U8:
			i = int(indices.Data[p])
		default:
			i = int(binary.LittleEndian.Uint16(indices.Data[2*p:]))
		}
		if i >= end-start {
			return fmt.Errorf("index %d at %d is past the %d values of block %d", i, p, end-start, c)
		}
		copy(a.Data[(p-m.offset)*w:], values.Data[(start+i)*w:(start+i+1)*w])
	}
	return nil
}
