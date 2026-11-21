package array

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
)

// The rows of an array can be decoded without the others, when its encodings let a value be
// read at its place: a filter that keeps few rows, or a range of rows, then pays for the rows
// it returns, not for the segment.

// errNoRows says that an encoding of the tree has no way to decode some rows only.
var errNoRows = errors.New("no decoding of some rows")

// Rows is a serialized array whose rows are decoded some at a time.
type Rows struct {
	ctx  *context
	root nodeRef
}

// OpenRows reads the tree of the serialized array of seg, like Decode does, and decodes
// nothing: Decode does, for the rows it is given.
func OpenRows(seg []byte, specs []string, opt Options) (*Rows, error) {
	ctx, root, err := open(seg, specs, opt)
	if err != nil {
		return nil, err
	}
	return &Rows{ctx: ctx, root: root}, nil
}

// Decode decodes the rows idx, in increasing order, of the array, of type t and of that many
// rows, into buffers of the arena ar: row k of the result is row idx[k] of the array. It is
// not ok when an encoding of the array has no way to decode some rows only: the caller then
// decodes all of them.
func (r *Rows) Decode(t DType, rows int, idx []int, ar *Arena) (*Array, bool, error) {
	r.ctx.opt.Arena = ar
	a, err := r.ctx.rows(r.root, t, rows, idx)
	if errors.Is(err, errNoRows) {
		return nil, false, nil
	}
	return a, err == nil, err
}

// rows decodes the rows idx of the node fb, an array of the type t and of that many rows.
func (c *context) rows(fb nodeRef, t DType, rows int, idx []int) (*Array, error) {
	id, n, err := c.node(fb, "", rows)
	if err != nil {
		return nil, err
	}
	a, err := n.rowsOf(id, t, rows, idx)
	if err != nil && !errors.Is(err, errNoRows) {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	return a, err
}

// rowsOf decodes the rows idx of the node, whose encoding is id.
func (n *Node) rowsOf(id string, t DType, rows int, idx []int) (*Array, error) {
	switch id {
	case "vortex.primitive":
		return n.primitiveRows(t, rows, idx)
	case "fastlanes.bitpacked":
		return n.bitPackedRows(t, rows, idx)
	case "fastlanes.for":
		return n.forRows(t, rows, idx)
	case "vortex.zigzag":
		return n.zigZagRows(t, rows, idx)
	case "vortex.alp":
		return n.alpRows(t, rows, idx)
	case "vortex.sequence":
		return n.sequenceRows(t, rows, idx)
	case "vortex.constant":
		// Every row is the same: the rows asked for are as many rows of the array.
		if err := checkRows(idx, rows); err != nil {
			return nil, err
		}
		return decodeConstant(n, t, len(idx))
	}
	return nil, errNoRows
}

// childRows decodes the rows idx of child i.
func (n *Node) childRows(i int, t DType, rows int, idx []int) (*Array, error) {
	return n.ctx.rows(n.childRef(i), t, rows, idx)
}

// validityRows is the validity of the rows idx, from the validity of all the rows: nil when
// there is none.
func (n *Node) validityRows(valid *Bitmap, idx []int) *Bitmap {
	if valid == nil {
		return nil
	}
	out := &Bitmap{Bytes: n.ctx.opt.Arena.Bytes((len(idx)+7)/8, true)}
	for k, i := range idx {
		if valid.Get(i) {
			out.Bytes[k>>3] |= 1 << (k & 7)
		}
	}
	return out
}

// checkRows checks that the rows idx are rows of an array of that many: they are in
// increasing order, so the last one tells.
func checkRows(idx []int, rows int) error {
	if len(idx) > 0 && (idx[0] < 0 || idx[len(idx)-1] >= rows) {
		return errors.New("a row past the array")
	}
	return nil
}

// primitiveRows is decodePrimitive for the rows idx.
func (n *Node) primitiveRows(t DType, rows int, idx []int) (*Array, error) {
	all, err := decodePrimitive(n, t, rows) // the buffer where it is, and the validity
	if err != nil {
		return nil, err
	}
	if err := checkRows(idx, rows); err != nil {
		return nil, err
	}
	w := t.PType.Width()
	out := &Array{Type: t, Len: len(idx), Data: n.ctx.opt.Arena.Bytes(len(idx)*w, false), fresh: true}
	for k, i := range idx {
		copy(out.Data[k*w:(k+1)*w], all.Data[i*w:])
	}
	out.Validity = n.validityRows(all.Validity, idx)
	return out, nil
}

// laneOf is, for a block of words of that many bits, the lane and the row of the lane of the
// value at position pos of the block: see unpackBlock.
func laneOf(pos, bits int) (lane, row int) {
	lanes := blockSize / bits
	x := pos % 128
	return x % lanes, 8*flRank[x/lanes*lanes/16] + pos/128
}

// flRank is the place of a group of 8 rows in flOrder.
var flRank = func() (r [8]int) {
	for k, v := range flOrder {
		r[v] = k
	}
	return r
}()

// unpackAt reads the values of w bits at the positions pos of the blocks of packed, the first
// position of the first block being offset, into out.
func unpackAt[T word](out, packed []T, w, offset int, idx []int) {
	bits := bitsOf[T]()
	lanes := blockSize / bits
	mask := T(1)<<w - 1 // w == bits shifts everything out: the mask is all ones
	for k, i := range idx {
		p := i + offset
		block := packed[p/blockSize*lanes*w:][:lanes*w]
		lane, row := laneOf(p%blockSize, bits)
		word, shift := row*w/bits, row*w%bits
		v := block[word*lanes+lane] >> shift
		if shift+w > bits {
			v |= block[(word+1)*lanes+lane] << (bits - shift)
		}
		out[k] = v & mask
	}
}

// bitPackedRows is decodeBitPacked for the rows idx.
func (n *Node) bitPackedRows(t DType, rows int, idx []int) (*Array, error) {
	m, err := n.bitPackedParts(t, rows)
	if err != nil {
		return nil, err
	}
	if err := checkRows(idx, rows); err != nil {
		return nil, err
	}
	width := t.PType.Width()
	a := &Array{Type: t, Len: len(idx), Data: n.ctx.opt.Arena.Bytes(len(idx)*width, false), fresh: true}
	if m.w > 0 {
		switch width {
		case 1:
			unpackAt(a.Data, m.packed, m.w, m.off, idx)
		case 2:
			unpackAt(words[uint16](a.Data), words[uint16](m.packed), m.w, m.off, idx)
		case 4:
			unpackAt(words[uint32](a.Data), words[uint32](m.packed), m.w, m.off, idx)
		default:
			unpackAt(words[uint64](a.Data), words[uint64](m.packed), m.w, m.off, idx)
		}
	} else {
		clear(a.Data)
	}
	if m.patches != nil {
		if err := m.patches.applyRows(a.Data, width, idx); err != nil {
			return nil, err
		}
	}
	a.Validity = n.validityRows(m.valid, idx)
	return a, nil
}

// applyRows is apply for an array of the rows idx only: a patch of another row is left out.
func (p *patches) applyRows(data []byte, w int, idx []int) error {
	for k, at := range p.index {
		if p.values.Null(k) {
			return patchNull(k)
		}
		if j, found := slices.BinarySearch(idx, at-p.offset); found {
			copy(data[j*w:(j+1)*w], p.values.Data[k*w:])
		}
	}
	return nil
}

// forRows is decodeFoR for the rows idx.
func (n *Node) forRows(t DType, rows int, idx []int) (*Array, error) {
	width, ref, err := n.forParts(t)
	if err != nil {
		return nil, err
	}
	enc, err := n.childRows(0, t, rows, idx)
	if err != nil {
		return nil, err
	}
	out := *enc
	out.Data = addConst(n.target(enc), enc.Data, width, ref)
	out.fresh = true
	return &out, nil
}

// zigZagRows is decodeZigZag for the rows idx.
func (n *Node) zigZagRows(t DType, rows int, idx []int) (*Array, error) {
	width, err := n.zigZagParts(t)
	if err != nil {
		return nil, err
	}
	enc, err := n.childRows(0, DType{Kind: Prim, PType: unsignedOf(t.PType), Nullable: t.Nullable}, rows, idx)
	if err != nil {
		return nil, err
	}
	out := *enc
	out.Type = t
	out.Data = unzigzag(n.target(enc), enc.Data, width)
	out.fresh = true
	return &out, nil
}

// alpRows is decodeALP for the rows idx.
func (n *Node) alpRows(t DType, rows int, idx []int) (*Array, error) {
	m, err := n.alpParts(t)
	if err != nil {
		return nil, err
	}
	ints, err := n.childRows(0, DType{Kind: Prim, PType: m.enc, Nullable: t.Nullable}, rows, idx)
	if err != nil {
		return nil, err
	}
	out := &Array{Type: t, Len: len(idx), Validity: ints.Validity}
	out.Data = alpFloats(n.target(ints), ints.Data, t.PType, m.e, m.f)
	if !m.hasPatches {
		return out, nil
	}
	pt, err := n.patchesChild(m.patches, 1, t, rows)
	if err != nil {
		return nil, err
	}
	return out, pt.applyRows(out.Data, t.PType.Width(), idx)
}

// sequenceRows is decodeSequence for the rows idx.
func (n *Node) sequenceRows(t DType, rows int, idx []int) (*Array, error) {
	width, base, step, err := n.sequenceParts(t)
	if err != nil {
		return nil, err
	}
	if err := checkRows(idx, rows); err != nil {
		return nil, err
	}
	a := &Array{Type: t, Len: len(idx), Data: n.ctx.opt.Arena.Bytes(len(idx)*width, false)}
	var b [8]byte
	for k, i := range idx {
		binary.LittleEndian.PutUint64(b[:], base+uint64(i)*step)
		copy(a.Data[k*width:(k+1)*width], b[:width])
	}
	return a, nil
}
