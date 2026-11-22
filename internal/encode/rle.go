package encode

import (
	"encoding/binary"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// RLE is the FastLanes run-length encoding of v, in blocks of 1024 values: the children are
// the values of the runs of all the blocks, one u16 index per row (the position of its run in
// its block), and, per block, the number of values before it. The last block is padded with
// its last value. A null row takes the value before it in its block, the first of a block
// zero, so that it does not start a run; floats are compared by their bits.
func RLE[T Number](e *Encoder, v []T, nulls []bool) *Node {
	w := unsafex.Size[T]()
	rows := len(v)
	blocks := (rows + blockSize - 1) / blockSize
	padded := blocks * blockSize
	filled := e.fill(unsafex.Bytes(v), w, nulls, padded)
	vals, idx, offs := e.allocDirty(padded*w), e.allocDirty(padded*2), e.allocDirty(blocks*8)
	count := 0
	for b := range blocks {
		binary.LittleEndian.PutUint64(offs[8*b:], uint64(count))
		prev := getWidth(filled[b*blockSize*w:], w)
		copy(vals[count*w:], filled[b*blockSize*w:][:w])
		count++
		pos := uint16(0)
		binary.LittleEndian.PutUint16(idx[2*b*blockSize:], pos)
		for i := 1; i < blockSize; i++ {
			at := b*blockSize + i
			if cur := getWidth(filled[at*w:], w); cur != prev {
				copy(vals[count*w:], filled[at*w:][:w])
				count++
				pos++
				prev = cur
			}
			binary.LittleEndian.PutUint16(idx[2*at:], pos)
		}
	}
	n := e.node(IDRLE)
	e.tmp = putUint(e.tmp[:0], 1, uint64(count))
	e.tmp = putUint(e.tmp, 2, uint64(padded))
	e.tmp = protowire.AppendUint(e.tmp, 3, pU16)
	e.tmp = putUint(e.tmp, 4, uint64(blocks))
	e.tmp = protowire.AppendUint(e.tmp, 5, pU64)
	n.Meta = e.keep(e.tmp)
	n.kid(e.leaf(vals[:count*w], w, nil, count))
	n.kid(e.leafPadded(idx, 2, nulls, rows, padded))
	n.kid(e.leaf(offs, 8, nil, blocks))
	return n
}

// fill is the raw values padded to padded rows, a null row filled forward within its block,
// and the padding repeating the last row.
func (e *Encoder) fill(raw []byte, w int, nulls []bool, padded int) []byte {
	out := e.allocDirty(padded * w) // every row is written
	copy(out, raw)
	if hasNull(nulls) {
		last := uint64(0)
		for i := range len(raw) / w {
			switch {
			case !nulls[i]:
				last = getWidth(raw[i*w:], w)
			case i%blockSize == 0:
				last = 0
				fallthrough
			default:
				putWidth(out[i*w:], w, last)
			}
		}
	}
	for i := len(raw) / w; i < padded; i++ {
		copy(out[i*w:], out[(i-1)*w:i*w])
	}
	return out
}
