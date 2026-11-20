package encode

import (
	"encoding/binary"

	"github.com/klauspost/compress/zstd"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The frames of vortex.zstd are cut as the reference's string compressor cuts them: 8192 valid
// values each, level 3, no dictionary. The valid values of an array, and only those, are in
// the frames: the bytes of the numbers, or each string as a u32 length and its bytes.
const zstdValuesPerFrame = 8192

// zstdEncoder is the encoder of an Encoder, made on the first use and kept: its pool of
// compression states makes a running Encoder allocate nothing.
func (e *Encoder) zstdEncoder() *zstd.Encoder {
	if e.zenc == nil {
		// the options are constants: they cannot fail
		e.zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(false))
	}
	return e.zenc
}

// Zstd is the zstd encoding of the numbers of v.
func Zstd[T Number](e *Encoder, v []T, nulls []bool) *Node {
	w := unsafex.Size[T]()
	valid := 0
	for i := range v {
		if !isNull(nulls, i) {
			valid++
		}
	}
	raw := e.alloc(valid * w)
	src := unsafex.Bytes(v)
	k := 0
	for i := range v {
		if !isNull(nulls, i) {
			copy(raw[k*w:], src[i*w:(i+1)*w])
			k++
		}
	}
	// a frame is zstdValuesPerFrame values: the sizes are in bytes, the values counted by value
	return e.zstdNode(raw, valid, nulls, len(v), func(frame int) int {
		return min(zstdValuesPerFrame, valid-frame*zstdValuesPerFrame) * w
	})
}

// ZstdBytes is the zstd encoding of the byte slices of vals.
func ZstdBytes(e *Encoder, vals [][]byte, nulls []bool) *Node {
	return e.zstdStrings(lineSet{bins: vals}, nulls, len(vals))
}

// ZstdStrings is the zstd encoding of the strings of vals.
func ZstdStrings(e *Encoder, vals []string, nulls []bool) *Node {
	return e.zstdStrings(lineSet{strs: vals}, nulls, len(vals))
}

func (e *Encoder) zstdStrings(ls lineSet, nulls []bool, rows int) *Node {
	valid, total := 0, 0
	for i := range rows {
		if !isNull(nulls, i) {
			valid++
			total += len(ls.get(i))
		}
	}
	raw := e.alloc(total + 4*valid)
	o := 0
	sizes := e.alloc(8 * ((valid + zstdValuesPerFrame - 1) / zstdValuesPerFrame))
	k := 0
	for i := range rows {
		if isNull(nulls, i) {
			continue
		}
		if k%zstdValuesPerFrame == 0 {
			binary.LittleEndian.PutUint64(sizes[8*(k/zstdValuesPerFrame):], uint64(o))
		}
		line := ls.get(i)
		binary.LittleEndian.PutUint32(raw[o:], uint32(len(line)))
		o += 4 + copy(raw[o+4:], line)
		k++
	}
	// the size of a frame is where the next one starts
	return e.zstdNode(raw, valid, nulls, rows, func(frame int) int {
		start := int(binary.LittleEndian.Uint64(sizes[8*frame:]))
		if 8*(frame+1) < len(sizes) {
			return int(binary.LittleEndian.Uint64(sizes[8*(frame+1):])) - start
		}
		return len(raw) - start
	})
}

// zstdNode compresses the frames of raw: the valid values, frameBytes giving the bytes of each.
func (e *Encoder) zstdNode(raw []byte, valid int, nulls []bool, rows int, frameBytes func(frame int) int) *Node {
	node := e.node(IDZstd)
	frames := (valid + zstdValuesPerFrame - 1) / zstdValuesPerFrame
	enc := e.zstdEncoder()
	meta := e.tmp[:0]
	start := 0
	for f := range frames {
		size := frameBytes(f)
		src := raw[start : start+size]
		start += size
		dst := e.alloc(len(src) + len(src)>>10 + 64)
		out := enc.EncodeAll(src, dst[:0])
		node.add(out, 1)
		values := min(zstdValuesPerFrame, valid-f*zstdValuesPerFrame)
		e.tmp2 = putUint(protowire.AppendUint(e.tmp2[:0], 1, uint64(size)), 2, uint64(values))
		meta = protowire.AppendBytes(meta, 2, e.tmp2)
	}
	node.kid(e.validity(nulls, rows, rows))
	e.tmp = meta
	node.Meta = e.keep(meta)
	return node
}

// EstimateZstd is the size of Zstd: the sample is compressed and what comes out scaled.
func EstimateZstd[T Number](e *Encoder, sample []T, nulls []bool, rows int) int {
	return e.estimateZstd(Zstd(e, sample, nulls), len(sample), nulls, rows)
}

// EstimateZstdBytes is the size of ZstdBytes.
func EstimateZstdBytes(e *Encoder, sample [][]byte, nulls []bool, rows int) int {
	return e.estimateZstd(ZstdBytes(e, sample, nulls), len(sample), nulls, rows)
}

// EstimateZstdStrings is the size of ZstdStrings.
func EstimateZstdStrings(e *Encoder, sample []string, nulls []bool, rows int) int {
	return e.estimateZstd(ZstdStrings(e, sample, nulls), len(sample), nulls, rows)
}

// zstdFrameCost is what a frame costs whatever it holds: its header and its block headers.
const zstdFrameCost = 20

func (e *Encoder) estimateZstd(n *Node, sampled int, nulls []bool, rows int) int {
	bytes := 0
	for _, b := range n.Bufs {
		bytes += len(b.Data)
	}
	if sampled == rows {
		return bytes + validBytes(nulls, rows)
	}
	nullRows := 0
	for _, null := range nulls {
		if null {
			nullRows++
		}
	}
	valid := rows - scale(nullRows, sampled, rows)
	frames := (valid + zstdValuesPerFrame - 1) / zstdValuesPerFrame
	body := max(bytes-len(n.Bufs)*zstdFrameCost, 0)
	return scale(body, sampled, rows) + frames*zstdFrameCost + validBytes(nulls, rows)
}
