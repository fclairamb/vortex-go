package array

import (
	"encoding/binary"
	"errors"
	"fmt"

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
	meta, err := readMessage(n.Metadata())
	if err != nil {
		return nil, err
	}
	if !meta.hasBlob[1] || !meta.hasBlob[2] {
		return nil, errors.New("needs a base and a multiplier")
	}
	base, step, err := sequenceParams(meta.blob[1], meta.blob[2], t.PType)
	if err != nil {
		return nil, err
	}
	a := &Array{Type: t, Len: rows, Data: make([]byte, rows*width)}
	var b [8]byte
	for i := range rows {
		binary.LittleEndian.PutUint64(b[:], base+uint64(i)*step)
		copy(a.Data[i*width:], b[:width])
	}
	return a, nil
}

// sequenceParams is the base, which must fit the type p, and the multiplier, as the bits of
// 64-bit integers: the values wrap at 64 bits, and the type keeps their low bits.
func sequenceParams(baseMsg, stepMsg []byte, p PType) (base, step uint64, err error) {
	bv, err := scalar.Decode(baseMsg)
	if err != nil {
		return 0, 0, fmt.Errorf("base: %w", err)
	}
	baseBytes, err := primBytes(&bv, p)
	if err != nil {
		return 0, 0, fmt.Errorf("base: %w", err)
	}
	base = binary.LittleEndian.Uint64(append(baseBytes, 0, 0, 0, 0, 0, 0, 0, 0))
	sv, err := scalar.Decode(stepMsg)
	if err != nil {
		return 0, 0, fmt.Errorf("multiplier: %w", err)
	}
	switch sv.Kind {
	case scalar.Int:
		step = uint64(sv.Int)
	case scalar.Uint:
		step = sv.Uint
	default:
		return 0, 0, fmt.Errorf("multiplier of kind %d is not an integer", sv.Kind)
	}
	return base, step, nil
}
