package array

import (
	"fmt"
	"slices"
)

// decodeRLE decodes fastlanes.rle: metadata {values_len, indices_len, indices_ptype,
// values_idx_offsets_len, values_idx_offsets_ptype, offset}, children the values, the indices
// (u8 or u16, with the validity) and the offsets of the values of each block of 1024 indices.
// The values of block b are values[offsets[b]-offsets[0]:] up to the next block's. The array
// is rows indices from index offset.
func decodeRLE(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Prim {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	meta, err := varints(n.Metadata(), 6)
	if err != nil {
		return nil, err
	}
	valuesLen, err := count("values_len", meta[0])
	if err != nil {
		return nil, err
	}
	indicesLen, err := count("indices_len", meta[1])
	if err != nil {
		return nil, err
	}
	ip, err := intType("indices_ptype", meta[2])
	if err != nil {
		return nil, err
	}
	if ip != U8 && ip != U16 {
		return nil, fmt.Errorf("indices are %s, not u8 or u16", ip)
	}
	offsLen, err := count("values_idx_offsets_len", meta[3])
	if err != nil {
		return nil, err
	}
	op, err := intType("values_idx_offsets_ptype", meta[4])
	if err != nil {
		return nil, err
	}
	offset, err := count("offset", meta[5])
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 || n.NumChildren() != 3 {
		return nil, fmt.Errorf("expected no buffer and 3 children, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	if indicesLen%blockSize != 0 || offset > indicesLen || rows > indicesLen-offset {
		return nil, fmt.Errorf("%d indices cannot hold %d rows from offset %d, in blocks of %d", indicesLen, rows, offset, blockSize)
	}
	if offsLen != indicesLen/blockSize || indicesLen < valuesLen {
		return nil, fmt.Errorf("%d offsets and %d values for %d indices", offsLen, valuesLen, indicesLen)
	}
	values, err := n.Child(0, t.NonNullable(), valuesLen)
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}
	indices, err := n.primChild(1, ip, t, indicesLen)
	if err != nil {
		return nil, fmt.Errorf("indices: %w", err)
	}
	offs, err := n.ints(2, "values index offsets", op, offsLen)
	if err != nil {
		return nil, err
	}
	if !slices.IsSorted(offs) {
		return nil, fmt.Errorf("values index offsets decrease")
	}
	if len(offs) > 0 && offs[len(offs)-1]-offs[0] > valuesLen {
		return nil, fmt.Errorf("values index offsets span %d values, there are %d", offs[len(offs)-1]-offs[0], valuesLen)
	}
	idx, err := toInts(indices, "indices", ip, indices.Validity)
	if err != nil {
		return nil, err
	}
	w := t.PType.Width()
	a := &Array{Type: t, Len: rows, Data: make([]byte, rows*w)}
	if indices.Validity != nil {
		v := indices.Validity.Slice(offset)
		a.Validity = &v
	}
	for c := offset / blockSize; c*blockSize < offset+rows; c++ {
		start := offs[c] - offs[0]
		end := valuesLen
		if c+1 < len(offs) {
			end = offs[c+1] - offs[0]
		}
		if start == end {
			return nil, fmt.Errorf("block %d references no values", c)
		}
		lo, hi := max(c*blockSize, offset), min((c+1)*blockSize, offset+rows)
		for p := lo; p < hi; p++ {
			if idx[p] >= end-start {
				return nil, fmt.Errorf("index %d at %d is past the %d values of block %d", idx[p], p, end-start, c)
			}
			copy(a.Data[(p-offset)*w:], values.Data[(start+idx[p])*w:(start+idx[p]+1)*w])
		}
	}
	return a, nil
}
