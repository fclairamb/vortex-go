package array

import "fmt"

// decimalWidths are the bytes of a value, by the values_type of vortex.decimal: i8, i16, i32,
// i64, i128 and i256.
var decimalWidths = [...]int{1, 2, 4, 8, 16, 32}

// decodeDecimal decodes vortex.decimal: metadata {values_type}, child 0 the validity, buffer 0
// the values, little-endian two's complement. The width is independent of the precision.
func decodeDecimal(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Decimal {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	meta, err := varints(n.Metadata(), 1)
	if err != nil {
		return nil, err
	}
	if meta[0] >= uint64(len(decimalWidths)) {
		return nil, fmt.Errorf("values_type is %d, want 0 to %d", meta[0], len(decimalWidths)-1)
	}
	w := decimalWidths[meta[0]]
	if n.NumBuffers() != 1 {
		return nil, fmt.Errorf("expected 1 buffer, got %d", n.NumBuffers())
	}
	buf, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	if len(buf)%w != 0 || len(buf)/w != rows {
		return nil, fmt.Errorf("buffer of %d bytes, want %d bytes per value for %d values", len(buf), w, rows)
	}
	valid, err := n.optionalValidity(rows)
	if err != nil {
		return nil, err
	}
	return &Array{Type: t, Len: rows, Data: buf, Width: w, Validity: valid}, nil
}
