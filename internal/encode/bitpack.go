package encode

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// blockSize is the values of a FastLanes block.
const blockSize = 1024

// flOrder is the order of the groups of 8 rows of a block.
var flOrder = [8]int{0, 4, 2, 6, 1, 5, 3, 7}

type unsigned interface {
	~uint8 | ~uint16 | ~uint32 | ~uint64
}

// ErrNegative reports a negative value given to the bit-packing, which packs unsigned values.
var ErrNegative = errors.New("encode: cannot bit-pack a negative value")

func bitsOf[U unsigned]() int { return 8 * unsafex.Size[U]() }

// packBlock packs the 1024 values of blk, w bits each, into the 128*w bytes of dst: the
// inverse of the unpacking of a block. The lanes of a row of values are one word wide, and a
// lane packs its values one after the other from the low bit of its first word.
func packBlock[U unsigned](dst []byte, blk *[blockSize]U, w int) {
	nb := bitsOf[U]()
	lanes := blockSize / nb
	var words [blockSize]U
	mask := U(1)<<w - 1
	for row := range nb {
		word, shift := row*w/nb, row*w%nb
		src := blk[flOrder[row/8]*16+row%8*128:][:lanes]
		lo := words[word*lanes:][:lanes]
		if shift+w <= nb {
			for l, v := range src {
				lo[l] |= v & mask << shift
			}
			continue
		}
		hi := words[(word+1)*lanes:][:lanes]
		for l, v := range src {
			v &= mask
			lo[l] |= v << shift
			hi[l] |= v >> (nb - shift)
		}
	}
	storeWords(dst, words[:lanes*w], nb)
}

func storeWords[U unsigned](dst []byte, src []U, nb int) {
	switch nb {
	case 8:
		for i, v := range src {
			dst[i] = byte(v)
		}
	case 16:
		for i, v := range src {
			binary.LittleEndian.PutUint16(dst[2*i:], uint16(v))
		}
	case 32:
		for i, v := range src {
			binary.LittleEndian.PutUint32(dst[4*i:], uint32(v))
		}
	default:
		for i, v := range src {
			binary.LittleEndian.PutUint64(dst[8*i:], uint64(v))
		}
	}
}

// packAll packs vals, w bits each, in blocks of 1024 values, the last one padded with zeros.
func packAll[U unsigned](dst []byte, vals []U, w int) {
	var blk [blockSize]U
	for b := 0; b*blockSize < len(vals); b++ {
		part := vals[b*blockSize:]
		n := copy(blk[:], part[:min(len(part), blockSize)])
		clear(blk[n:])
		packBlock(dst[b*128*w:][:128*w], &blk, w)
	}
}

// pack is packAll for the integers of raw, each of width bytes.
func pack(dst, raw []byte, width, w int) {
	switch width {
	case 1:
		packAll(dst, raw, w)
	case 2:
		packAll(dst, unsafex.Slice[uint16](raw), w)
	case 4:
		packAll(dst, unsafex.Slice[uint32](raw), w)
	default:
		packAll(dst, unsafex.Slice[uint64](raw), w)
	}
}

// Histogram counts the valid values of v by their number of bits: h[b] values have b bits. A
// null counts as a value of no bit. h has the bits of T plus one entries, and is overwritten.
func Histogram[T Int](v []T, nulls []bool, h []int) {
	clear(h)
	for i, x := range v {
		if isNull(nulls, i) {
			h[0]++
		} else {
			h[bits.Len64(ubits(x))]++
		}
	}
}

// BestWidth is the bit width that makes the smallest array, from a histogram of the bit
// lengths of the values and the bytes of a value: an exception costs a value and an index.
// It is the choice of the reference.
func BestWidth(h []int, valueBytes int) int {
	total := 0
	for _, c := range h {
		total += c
	}
	perException := valueBytes + 4
	best, bestCost := 0, total*perException
	packed := 0
	for w, c := range h {
		packed += c
		cost := (w*total+7)/8 + (total-packed)*perException
		if cost < bestCost {
			best, bestCost = w, cost
		}
	}
	return best
}

// patchWidth is the PType number and the width in bytes of the indices of the patches of an
// array of rows values.
func patchWidth(rows int) (ptype, width int) {
	switch {
	case rows < 1<<8-1:
		return pU8, 1
	case rows < 1<<16-1:
		return pU16, 2
	case rows < 1<<32-1:
		return pU32, 4
	}
	return pU64, 8
}

// BitPackBest is BitPack at the width BestWidth picks from the histogram of the values. It
// fails when that width is the bits of T: the values are not worth packing.
func BitPackBest[T Int](e *Encoder, v []T, nulls []bool) (*Node, error) {
	h := e.hist[:8*unsafex.Size[T]()+1]
	Histogram(v, nulls, h)
	return BitPack(e, v, nulls, BestWidth(h, unsafex.Size[T]()))
}

// BitPack packs the integers of v in width bits each, in FastLanes blocks of 1024. A value
// that does not fit goes to the patches: its index and its value, with the number of patches
// before each block. The slots of null rows are packed as they are. The width must be under
// the bits of T and the valid values not negative.
func BitPack[T Int](e *Encoder, v []T, nulls []bool, width int) (*Node, error) {
	w := unsafex.Size[T]()
	if width < 0 || width >= 8*w {
		return nil, fmt.Errorf("encode: cannot pack %d-bit integers in %d bits", 8*w, width)
	}
	count := 0
	for i, x := range v {
		if isNull(nulls, i) {
			continue
		}
		if x < 0 {
			return nil, ErrNegative
		}
		if bits.Len64(ubits(x)) > width {
			count++
		}
	}
	raw := unsafex.Bytes(v)
	blocks := (len(v) + blockSize - 1) / blockSize
	packed := e.alloc(blocks * 128 * width)
	if width > 0 {
		pack(packed, raw, w, width)
	}
	n := e.node(IDBitPacked)
	n.add(packed, w)
	if count > 0 {
		patches(e, n, v, nulls, width, count)
	}
	n.kid(e.validity(nulls, len(v), len(v)))
	e.tmp = putUint(e.tmp[:0], 1, uint64(width))
	if count > 0 {
		e.tmp = protowire.AppendBytes(e.tmp, 3, e.patchMeta(count, len(v), blocks))
	}
	n.Meta = e.keep(e.tmp)
	return n, nil
}

// patchMeta is the PatchesMetadata message of patches patches of an array of rows rows, with
// the offsets of the blocks.
func (e *Encoder) patchMeta(patches, rows, blocks int) []byte {
	ptype, _ := patchWidth(rows)
	m := protowire.AppendUint(e.tmp2[:0], 1, uint64(patches))
	m = putUint(m, 3, uint64(ptype))
	m = protowire.AppendUint(m, 4, uint64(blocks))
	m = protowire.AppendUint(m, 5, pU64)
	m = protowire.AppendUint(m, 6, 0)
	e.tmp2 = m
	return m
}

// patches adds the three children of the patches of the values that do not fit in width bits:
// the indices, the values, and the offsets, the number of patches before each block.
func patches[T Int](e *Encoder, n *Node, v []T, nulls []bool, width, count int) {
	_, iw := patchWidth(len(v))
	w := unsafex.Size[T]()
	blocks := (len(v) + blockSize - 1) / blockSize
	idx, vals, offs := e.alloc(count*iw), e.alloc(count*w), e.alloc(blocks*8)
	k := 0
	for i, x := range v {
		if i%blockSize == 0 {
			binary.LittleEndian.PutUint64(offs[8*(i/blockSize):], uint64(k))
		}
		if !isNull(nulls, i) && bits.Len64(ubits(x)) > width {
			putWidth(idx[k*iw:], iw, uint64(i))
			putWidth(vals[k*w:], w, ubits(x))
			k++
		}
	}
	n.kid(e.leaf(idx, iw, nil, count))
	n.kid(e.leaf(vals, w, nil, count))
	n.kid(e.leaf(offs, 8, nil, blocks))
}
