package array

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/scalar"
)

// decodeSequence decodes vortex.sequence: metadata {base, multiplier}, two ScalarValues, no
// buffer and no child. Value i is base + i * multiplier, wrapping at the width of the type. A
// nullable sequence has no null.
func decodeSequence(n *Node, t DType, rows int) (*Array, error) {
	width, err := intPrim(t)
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 || n.NumChildren() != 0 {
		return nil, fmt.Errorf("expected no buffer and no child, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	var baseMsg, stepMsg []byte
	it := protowire.NewIter(n.Metadata())
	for it.Next() {
		if it.Type() != protowire.Bytes {
			continue
		}
		switch it.Num() {
		case 1:
			baseMsg = it.Bytes()
		case 2:
			stepMsg = it.Bytes()
		}
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	if baseMsg == nil || stepMsg == nil {
		return nil, errors.New("needs a base and a multiplier")
	}
	bv, err := scalar.Decode(baseMsg)
	if err != nil {
		return nil, fmt.Errorf("base: %w", err)
	}
	baseBytes, err := primBytes(&bv, t.PType)
	if err != nil {
		return nil, fmt.Errorf("base: %w", err)
	}
	base := binary.LittleEndian.Uint64(append(baseBytes, 0, 0, 0, 0, 0, 0, 0, 0))
	sv, err := scalar.Decode(stepMsg)
	if err != nil {
		return nil, fmt.Errorf("multiplier: %w", err)
	}
	var step uint64
	switch sv.Kind {
	case scalar.Int:
		step = uint64(sv.Int)
	case scalar.Uint:
		step = sv.Uint
	default:
		return nil, fmt.Errorf("multiplier of kind %d is not an integer", sv.Kind)
	}
	a := &Array{Type: t, Len: rows, Data: make([]byte, rows*width)}
	var b [8]byte
	for i := range rows {
		binary.LittleEndian.PutUint64(b[:], base+uint64(i)*step)
		copy(a.Data[i*width:], b[:width])
	}
	return a, nil
}
