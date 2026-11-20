package array

import (
	"errors"
	"fmt"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/scalar"
)

// decodeSparse decodes vortex.sparse: metadata {patches}, buffer 0 the fill value (a
// ScalarValue of the type of the array, null if the type is nullable), children the indices
// and the values of the patches. A row is the fill value unless a patch is at its index.
func decodeSparse(n *Node, t DType, rows int) (*Array, error) {
	var patchMsg []byte
	hasPatches := false
	it := protowire.NewIter(n.Metadata())
	for it.Next() {
		if it.Num() == 1 && it.Type() == protowire.Bytes {
			patchMsg, hasPatches = it.Bytes(), true
		}
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	if !hasPatches {
		return nil, errors.New("has no patches in its metadata")
	}
	m, err := parsePatchesMeta(patchMsg)
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 1 || n.NumChildren() != 2 {
		return nil, fmt.Errorf("expected 1 buffer and 2 children, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	buf, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	fv, err := scalar.Decode(buf)
	if err != nil {
		return nil, fmt.Errorf("fill value: %w", err)
	}
	fill, err := constantArray(&fv, t, 1)
	if err != nil {
		return nil, fmt.Errorf("fill value: %w", err)
	}
	m.chunkOffsets = false
	pt, err := n.patchesChild(m, 0, t, rows)
	if err != nil {
		return nil, err
	}
	idx := make([]int, rows) // 0 is the fill value, k + 1 patch k
	for k := range pt.index {
		idx[pt.row(k)] = k + 1
	}
	return withType(take(Concat(fill.Type, []*Array{fill, pt.values}), idx), t), nil
}
