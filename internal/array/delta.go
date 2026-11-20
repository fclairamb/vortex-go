package array

import (
	"fmt"
)

// decodeDelta decodes fastlanes.delta, which releases before 0.93 wrote and the edition no
// longer lists. The metadata is {deltas_len, offset}, child 0 the bases (one per lane of each
// block of 1024 values), child 1 the deltas, with the validity, in the transposed order of the
// blocks. A lane has one word per row of the block: its first delta is relative to its base, the
// next ones to the word before, wrapping.
func decodeDelta(n *Node, t DType, rows int) (*Array, error) {
	width, err := intPrim(t)
	if err != nil {
		return nil, err
	}
	deltasLen, offset, err := n.deltaMeta(rows)
	if err != nil {
		return nil, err
	}
	lanes := blockSize / (8 * width)
	bases, err := n.Child(0, t.NonNullable(), deltasLen/blockSize*lanes)
	if err != nil {
		return nil, fmt.Errorf("bases: %w", err)
	}
	deltas, err := n.Child(1, t, deltasLen)
	if err != nil {
		return nil, fmt.Errorf("deltas: %w", err)
	}
	a := &Array{Type: t, Len: rows, Data: make([]byte, deltasLen*width)}
	switch width {
	case 1:
		undelta[uint8](a.Data, bases.Data, deltas.Data)
	case 2:
		undelta[uint16](a.Data, bases.Data, deltas.Data)
	case 4:
		undelta[uint32](a.Data, bases.Data, deltas.Data)
	default:
		undelta[uint64](a.Data, bases.Data, deltas.Data)
	}
	if deltas.Validity != nil {
		v := untransposeValidity(deltas.Validity, deltasLen).Slice(offset)
		a.Validity = &v
	}
	a.Data = a.Data[offset*width : (offset+rows)*width]
	return a, nil
}

// deltaMeta reads the metadata, {deltas_len, offset}, and checks it against the shape of the
// node and the number of rows.
func (n *Node) deltaMeta(rows int) (deltasLen, offset int, err error) {
	v, err := varints(n.Metadata(), 2)
	if err != nil {
		return 0, 0, err
	}
	if deltasLen, err = count("deltas_len", v[0]); err != nil {
		return 0, 0, err
	}
	if offset, err = count("offset", v[1]); err != nil {
		return 0, 0, err
	}
	if n.NumBuffers() != 0 || n.NumChildren() != 2 {
		return 0, 0, fmt.Errorf("expected no buffer and 2 children, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	if deltasLen%blockSize != 0 || offset >= blockSize || offset > deltasLen || rows > deltasLen-offset {
		return 0, 0, fmt.Errorf("%d deltas cannot hold %d rows from offset %d, in blocks of %d",
			deltasLen, rows, offset, blockSize)
	}
	return deltasLen, offset, nil
}

// untranspose is the position, in the block of values, of the value that is at position p of
// the transposed block. It is the same for every width: the transposition is the one of the 64
// bit words, and only the lanes of the deltas depend on the width.
func untranspose(p int) int {
	return (p%16)*64 + flOrder[(p/16)%8]*8 + p/128
}

// undelta rebuilds the values of every block of deltas, little-endian words of T, into out.
func undelta[T word](out, bases, deltas []byte) {
	bits := bitsOf[T]()
	lanes := blockSize / bits
	var d, b, v [blockSize]T
	for c := range len(deltas) / (bits / 8 * blockSize) {
		loadWords(d[:], deltas[c*blockSize*bits/8:], bits)
		loadWords(b[:lanes], bases[c*lanes*bits/8:], bits)
		for lane := range lanes {
			prev := b[lane]
			for row := range bits {
				p := flOrder[row/8]*16 + row%8*128 + lane
				prev += d[p]
				v[untranspose(p)] = prev
			}
		}
		storeWords(out[c*blockSize*bits/8:], v[:], bits)
	}
}

// untransposeValidity reorders the validity bits of the deltas into the order of the values:
// slot p of a block describes the value at untranspose(p).
func untransposeValidity(v *Bitmap, n int) *Bitmap {
	out := make([]byte, (n+7)/8)
	for c := 0; c < n; c += blockSize {
		for p := range blockSize {
			if i := c + untranspose(p); v.Get(c + p) {
				out[i>>3] |= 1 << (i & 7)
			}
		}
	}
	return &Bitmap{Bytes: out}
}
