package array

import (
	"encoding/binary"
	"fmt"
)

// decodeChunked decodes vortex.chunked: child 0 is the chunk offsets (u64, chunks + 1), then
// the chunks.
func decodeChunked(n *Node, t DType, rows int) (*Array, error) {
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	if n.NumChildren() == 0 {
		return nil, fmt.Errorf("needs the chunk offsets as its first child")
	}
	chunks := n.NumChildren() - 1
	offs, err := n.Child(0, DType{Kind: Prim, PType: U64}, chunks+1)
	if err != nil {
		return nil, fmt.Errorf("chunk offsets: %w", err)
	}
	parts := make([]*Array, chunks)
	total := 0
	for i := range parts {
		lo := binary.LittleEndian.Uint64(offs.Data[i*8:])
		hi := binary.LittleEndian.Uint64(offs.Data[i*8+8:])
		if hi < lo || hi-lo > uint64(rows-total) {
			return nil, fmt.Errorf("chunk %d runs from offset %d to %d: out of order, or past the %d rows", i, lo, hi, rows)
		}
		size := int(hi - lo)
		if parts[i], err = n.Child(i+1, t, size); err != nil {
			return nil, fmt.Errorf("chunk %d: %w", i, err)
		}
		total += size
	}
	if total != rows {
		return nil, fmt.Errorf("chunks hold %d rows, want %d", total, rows)
	}
	return Concat(t, parts), nil
}
