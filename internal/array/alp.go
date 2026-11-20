package array

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// The powers of ten of the alp crate (0.0.6): F10[i] is 10^i and IF10[i] is 10^-i, each one
// rounded to the nearest float, which is what the literals of the crate are.
var (
	f10f64 = [...]float64{
		1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7,
		1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15,
		1e16, 1e17, 1e18,
	}
	if10f64 = [...]float64{
		1e-0, 1e-1, 1e-2, 1e-3, 1e-4, 1e-5, 1e-6, 1e-7,
		1e-8, 1e-9, 1e-10, 1e-11, 1e-12, 1e-13, 1e-14, 1e-15,
		1e-16, 1e-17, 1e-18,
	}
	f10f32 = [...]float32{
		1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7,
		1e8, 1e9, 1e10,
	}
	if10f32 = [...]float32{
		1e-0, 1e-1, 1e-2, 1e-3, 1e-4, 1e-5, 1e-6, 1e-7,
		1e-8, 1e-9, 1e-10,
	}
)

// decodeALP decodes vortex.alp: metadata {exp_e, exp_f, patches?}, child 0 the encoded
// integers (i32 for an f32, i64 for an f64), then the children of the patches, which hold the
// floats that did not round-trip. A value is encoded * 10^exp_f * 10^-exp_e, the two products
// in that order, each one rounded to the float type.
func decodeALP(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Prim || (t.PType != F32 && t.PType != F64) {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	meta, err := readMessage(n.Metadata())
	if err != nil {
		return nil, err
	}
	e, f := meta.varint[1], meta.varint[2]
	limit, enc := uint64(18), I64
	if t.PType == F32 {
		limit, enc = 10, I32
	}
	if e > limit || f > limit {
		return nil, fmt.Errorf("exponents e=%d, f=%d, want at most %d for %s", e, f, limit, t.PType)
	}
	if n.NumBuffers() != 0 {
		return nil, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	if n.NumChildren() < 1 {
		return nil, errors.New("has no child")
	}
	ints, err := n.primChild(0, enc, t, rows)
	if err != nil {
		return nil, err
	}
	out := &Array{Type: t, Len: rows, Validity: ints.Validity, Data: alpFloats(ints.Data, t.PType, e, f)}
	if !meta.hasBlob[3] {
		if n.NumChildren() != 1 {
			return nil, fmt.Errorf("expected 1 child without patches, got %d", n.NumChildren())
		}
		return out, nil
	}
	pm, err := parsePatchesMeta(meta.blob[3])
	if err != nil {
		return nil, err
	}
	pt, err := n.patchesChild(pm, 1, t, rows)
	if err != nil {
		return nil, err
	}
	return out, pt.apply(out.Data, t.PType.Width())
}

// alpFloats turns the encoded integers into the bits of floats of type p.
func alpFloats(ints []byte, p PType, e, f uint64) []byte {
	out := make([]byte, len(ints))
	if p == F32 {
		m, inv := f10f32[f], if10f32[e]
		for i := 0; i < len(ints); i += 4 {
			v := float32(int32(binary.LittleEndian.Uint32(ints[i:]))) * m * inv
			binary.LittleEndian.PutUint32(out[i:], math.Float32bits(v))
		}
		return out
	}
	m, inv := f10f64[f], if10f64[e]
	for i := 0; i < len(ints); i += 8 {
		v := float64(int64(binary.LittleEndian.Uint64(ints[i:]))) * m * inv
		binary.LittleEndian.PutUint64(out[i:], math.Float64bits(v))
	}
	return out
}

// alpRDMeta is the metadata of vortex.alprd.
type alpRDMeta struct {
	patches    []byte
	dict       []uint16
	rightBits  int
	left       PType
	hasPatches bool
}

// parseALPRDMeta reads {right_bit_width, dict_len, dict: [uint32], left_parts_ptype, patches}.
func parseALPRDMeta(b []byte) (m alpRDMeta, err error) {
	var dict []uint64
	var f [6]uint64
	it := protowire.NewIter(b)
	for it.Next() {
		num, typ := it.Num(), it.Type()
		switch {
		case num == 3 && typ == protowire.Bytes:
			vals, err := protowire.PackedVarints(it.Bytes())
			if err != nil {
				return m, fmt.Errorf("dictionary: %w", err)
			}
			dict = append(dict, vals...)
		case num == 3 && typ == protowire.Varint:
			dict = append(dict, it.Uint())
		case num == 5 && typ == protowire.Bytes:
			m.patches, m.hasPatches = it.Bytes(), true
		case num >= 1 && num <= 4 && typ == protowire.Varint:
			f[num] = it.Uint()
		}
	}
	if err := it.Err(); err != nil {
		return m, err
	}
	return m, m.finish(dict, f)
}

// finish checks the fields read by parseALPRDMeta and stores them: f is the varint fields 1 to 4.
func (m *alpRDMeta) finish(dict []uint64, f [6]uint64) (err error) {
	if m.dict, err = dictCodes(dict, f[2]); err != nil {
		return err
	}
	if f[1] > 255 {
		return fmt.Errorf("right_bit_width %d is out of range", f[1])
	}
	m.rightBits = int(f[1])
	if m.left, err = intType("left_parts_ptype", f[4]); err != nil {
		return err
	}
	if m.left.Signed() {
		return fmt.Errorf("left parts are %s, not unsigned", m.left)
	}
	return nil
}

// dictCodes checks the first n entries of the dictionary and narrows them to 16 bits.
func dictCodes(dict []uint64, n uint64) ([]uint16, error) {
	if n > uint64(len(dict)) {
		return nil, fmt.Errorf("dictionary length %d exceeds the %d stored entries", n, len(dict))
	}
	out := make([]uint16, n)
	for i := range out {
		if dict[i] > math.MaxUint16 {
			return nil, fmt.Errorf("dictionary code %d does not fit in 16 bits", dict[i])
		}
		out[i] = uint16(dict[i])
	}
	return out, nil
}

// decodeALPRD decodes vortex.alprd: child 0 the left parts (dictionary codes), child 1 the right
// parts (u32 or u64, never null), then the patches of the left parts. The bits of a float are
// dict[left] << right_bit_width | right. A patched row has its left bits from the patch.
func decodeALPRD(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Prim || (t.PType != F32 && t.PType != F64) {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	m, err := parseALPRDMeta(n.Metadata())
	if err != nil {
		return nil, err
	}
	w := t.PType.Width()
	if m.rightBits > 8*w {
		return nil, fmt.Errorf("right_bit_width %d is above the %d bits of a %s", m.rightBits, 8*w, t.PType)
	}
	left, right, err := n.rdParts(m.left, t, rows)
	if err != nil {
		return nil, err
	}
	bits, err := m.leftBits(left, rows)
	if err != nil {
		return nil, err
	}
	if m.hasPatches {
		err = n.patchLeftBits(&m, bits, rows)
	} else if n.NumChildren() != 2 {
		err = fmt.Errorf("expected 2 children without patches, got %d", n.NumChildren())
	}
	if err != nil {
		return nil, err
	}
	out := &Array{Type: t, Len: rows, Validity: left.Validity, Data: make([]byte, rows*w)}
	// A shift of the full width gives 0 in Go, which is what a right part of every bit needs.
	for i := range rows {
		if w == 4 {
			v := uint32(bits[i])<<m.rightBits | binary.LittleEndian.Uint32(right.Data[4*i:])
			binary.LittleEndian.PutUint32(out.Data[4*i:], v)
		} else {
			v := bits[i]<<m.rightBits | binary.LittleEndian.Uint64(right.Data[8*i:])
			binary.LittleEndian.PutUint64(out.Data[8*i:], v)
		}
	}
	return out, nil
}

// rdParts decodes the left parts (type p) and the right parts of an array of floats of type t.
func (n *Node) rdParts(p PType, t DType, rows int) (left, right *Array, err error) {
	if n.NumChildren() < 2 {
		return nil, nil, fmt.Errorf("expected at least 2 children, got %d", n.NumChildren())
	}
	if left, err = n.primChild(0, p, t, rows); err != nil {
		return nil, nil, fmt.Errorf("left parts: %w", err)
	}
	rightType := DType{Kind: Prim, PType: U32}
	if t.PType == F64 {
		rightType.PType = U64
	}
	if right, err = n.Child(1, rightType, rows); err != nil {
		return nil, nil, fmt.Errorf("right parts: %w", err)
	}
	return left, right, nil
}

// leftBits maps the codes of the left parts through the dictionary. Every valid code must be in
// it, patched or not. The rows that are null are 0.
func (m *alpRDMeta) leftBits(left *Array, rows int) ([]uint64, error) {
	codes, err := toInts(left, "left parts", m.left, left.Validity)
	if err != nil {
		return nil, err
	}
	bits := make([]uint64, rows)
	for i, c := range codes {
		if left.Null(i) {
			continue
		}
		if c >= len(m.dict) {
			return nil, fmt.Errorf("left code %d at row %d is past the dictionary of %d entries", c, i, len(m.dict))
		}
		bits[i] = uint64(m.dict[c])
	}
	return bits, nil
}

// patchLeftBits replaces the left bits of the patched rows: the patches are the children from
// child 2, and hold the left bits themselves, not codes.
func (n *Node) patchLeftBits(m *alpRDMeta, bits []uint64, rows int) error {
	pm, err := parsePatchesMeta(m.patches)
	if err != nil {
		return err
	}
	pt, err := n.patchesChild(pm, 2, DType{Kind: Prim, PType: m.left}, rows)
	if err != nil {
		return err
	}
	vals, err := toInts(pt.values, "left patch values", m.left, nil)
	if err != nil {
		return err
	}
	for k, v := range vals {
		bits[pt.row(k)] = uint64(v)
	}
	return nil
}
