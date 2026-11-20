package array

import (
	"encoding/binary"
	"fmt"
)

func isBytes(t DType) bool { return t.Kind == Utf8 || t.Kind == Binary }

// decodeVarBin decodes vortex.varbin: metadata {offsets_ptype}, child 0 the offsets (rows + 1),
// child 1 the validity, buffer 0 the bytes.
func decodeVarBin(n *Node, t DType, rows int) (*Array, error) {
	if !isBytes(t) {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	meta, err := varints(n.Metadata(), 1)
	if err != nil {
		return nil, err
	}
	p, err := intType("offsets_ptype", meta[0])
	if err != nil {
		return nil, err
	}
	if c := n.NumChildren(); c != 1 && c != 2 {
		return nil, fmt.Errorf("expected 1 or 2 children, got %d", c)
	}
	if n.NumBuffers() != 1 {
		return nil, fmt.Errorf("expected 1 buffer, got %d", n.NumBuffers())
	}
	data, err := n.Buffer(0)
	if err != nil {
		return nil, err
	}
	offs, err := n.ints(0, "offsets", p, rows+1)
	if err != nil {
		return nil, err
	}
	if err := checkOffsets(offs, len(data)); err != nil {
		return nil, err
	}
	a := &Array{Type: t, Len: rows, Data: data, Starts: offs[:rows], Ends: offs[1:]}
	if n.NumChildren() == 2 {
		if a.Validity, err = n.Validity(1, rows); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// checkOffsets checks that offsets never decrease and stay within limit.
func checkOffsets(offs []int, limit int) error {
	prev := 0
	for i, o := range offs {
		if o < prev {
			return fmt.Errorf("offset %d is %d, after %d", i, o, prev)
		}
		prev = o
	}
	if prev > limit {
		return fmt.Errorf("offset %d is past the %d elements", prev, limit)
	}
	return nil
}

// viewSize is the size of a view of vortex.varbinview.
const viewSize = 16

// maxInlined is the longest value a view holds itself.
const maxInlined = 12

// decodeVarBinView decodes vortex.varbinview: no metadata, child 0 the validity, the data
// buffers then, last, the views. A view is {size u32}, then for a value of up to 12 bytes the
// bytes, and otherwise {prefix [4]byte, buffer index u32, offset u32}.
//
// The result has one Data: the views, then the data buffers, so that no view needs a copy of
// its value.
func decodeVarBinView(n *Node, t DType, rows int) (*Array, error) {
	if !isBytes(t) {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if err := n.noMetadata(); err != nil {
		return nil, err
	}
	if n.NumBuffers() == 0 {
		return nil, fmt.Errorf("expected at least 1 buffer, got 0")
	}
	valid, err := n.optionalValidity(rows)
	if err != nil {
		return nil, err
	}
	nb := n.NumBuffers() - 1
	views, err := n.Buffer(nb)
	if err != nil {
		return nil, err
	}
	if len(views)%viewSize != 0 || len(views)/viewSize != rows {
		return nil, fmt.Errorf("views buffer of %d bytes, want %d bytes per row for %d rows", len(views), viewSize, rows)
	}
	data := views[:len(views):len(views)]
	bases, lens := make([]int, nb), make([]int, nb)
	for i := range nb {
		b, err := n.Buffer(i)
		if err != nil {
			return nil, err
		}
		bases[i], lens[i] = len(data), len(b)
		data = append(data, b...)
	}
	a := &Array{Type: t, Len: rows, Data: data, Validity: valid, Starts: make([]int, rows), Ends: make([]int, rows)}
	for i := range rows {
		v := data[i*viewSize : (i+1)*viewSize]
		size := int(binary.LittleEndian.Uint32(v))
		if size <= maxInlined {
			a.Starts[i], a.Ends[i] = i*viewSize+4, i*viewSize+4+size
			continue
		}
		idx := int(binary.LittleEndian.Uint32(v[8:]))
		off := int(binary.LittleEndian.Uint32(v[12:]))
		if idx >= nb {
			return nil, fmt.Errorf("view %d is in buffer %d, there are %d", i, idx, nb)
		}
		if off+size > lens[idx] {
			return nil, fmt.Errorf("view %d ends at %d of buffer %d, which has %d bytes", i, off+size, idx, lens[idx])
		}
		a.Starts[i], a.Ends[i] = bases[idx]+off, bases[idx]+off+size
	}
	return a, nil
}
