package vortex

import (
	"fmt"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/flatbuf"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The writer does not choose among the encoders of internal/encode yet: that is the compressor.
// A test can force one on a Writer (the force field, the name of the encoding), which then
// writes with it every array of numbers, strings, decimals or timestamps it can, to check the
// files with the reference reader. An array that the encoder cannot take is an error.

// encodeForced is the array of the numbers d in the forced encoding.
func (w *Writer) encodeForced(d Data, n int) flatbuf.Off {
	if w.enc == nil {
		w.enc = encode.New()
	}
	var node *encode.Node
	if w.force == forceChunked {
		node = w.chunkedNumbers(d, n)
		return w.emit(node)
	}
	switch nulls := d.nulls; d.pk {
	case Int8:
		node = forceSigned(w.enc, w.force, unsafex.Slice[int8](d.raw), nulls)
	case Int16:
		node = forceSigned(w.enc, w.force, unsafex.Slice[int16](d.raw), nulls)
	case Int32:
		node = forceSigned(w.enc, w.force, unsafex.Slice[int32](d.raw), nulls)
	case Int64:
		node = forceSigned(w.enc, w.force, unsafex.Slice[int64](d.raw), nulls)
	case Uint8:
		node = forceInt(w.enc, w.force, unsafex.Slice[uint8](d.raw), nulls)
	case Uint16:
		node = forceInt(w.enc, w.force, unsafex.Slice[uint16](d.raw), nulls)
	case Uint32:
		node = forceInt(w.enc, w.force, unsafex.Slice[uint32](d.raw), nulls)
	case Uint64:
		node = forceInt(w.enc, w.force, unsafex.Slice[uint64](d.raw), nulls)
	case Float32:
		node = forceFloat(w.enc, w.force, unsafex.Slice[float32](d.raw), nulls)
	default:
		node = forceFloat(w.enc, w.force, unsafex.Slice[float64](d.raw), nulls)
	}
	if node == nil {
		w.setErr(fmt.Errorf("vortex: Write: the %s encoding cannot take %s", w.force, d.describe()))
		return w.encodePlain(d, n)
	}
	return w.emit(node)
}

// encodeForcedStrings is the array of strings d (the buffer of a column holds them as byte
// slices) as a dictionary, FSST, OnPair, zstd or (binary only) varbin, when that is the encoding
// forced, and false when it is not.
func (w *Writer) encodeForcedStrings(t Type, d Data) (flatbuf.Off, bool) {
	if w.depth != 1 {
		return 0, false
	}
	if w.enc == nil {
		w.enc = encode.New()
	}
	switch w.force {
	case "dict":
		return w.emit(encode.DictBytes(w.enc, d.bins, d.nulls)), true
	case "fsst":
		return w.emit(encode.FSSTBytes(w.enc, d.bins, d.nulls)), true
	case "zstd":
		return w.emit(encode.ZstdBytes(w.enc, d.bins, d.nulls)), true
	case "onpair":
		return w.emit(encode.OnPairBytes(w.enc, d.bins, d.nulls)), true
	case "varbin":
		if t.kind == Binary {
			return w.emit(encode.VarBinBytes(w.enc, d.bins, d.nulls)), true
		}
	case forceChunked:
		return w.emit(w.chunked(d.n, func(lo, hi int) *encode.Node {
			return encode.VarBinBytes(w.enc, d.bins[lo:hi], sliceNulls(d.nulls, lo, hi))
		})), true
	case forceMasked:
		if n, ok := encode.ConstantBytesMasked(w.enc, d.bins, d.nulls, t.kind == Utf8); ok {
			return w.emit(n), true
		}
	}
	return 0, false
}

// encodeForcedBools is the array of the booleans d as a constant under a validity, when that is
// the encoding forced and the values fit.
func (w *Writer) encodeForcedBools(d Data) (flatbuf.Off, bool) {
	if w.depth != 1 || (w.force != forceMasked && w.force != "bytebool" && w.force != forceChunked) {
		return 0, false
	}
	if w.enc == nil {
		w.enc = encode.New()
	}
	switch w.force {
	case "bytebool":
		return w.emit(encode.ByteBool(w.enc, d.bools, d.nulls)), true
	case forceChunked:
		return w.emit(w.chunked(len(d.bools), func(lo, hi int) *encode.Node {
			return encode.Bools(w.enc, d.bools[lo:hi], sliceNulls(d.nulls, lo, hi))
		})), true
	}
	if n, ok := encode.ConstantBoolsMasked(w.enc, d.bools, d.nulls); ok {
		return w.emit(n), true
	}
	return 0, false
}

// encodeForcedDecimals is the array of the decimals of d as decimal byte parts, when that is
// the encoding forced and the values fit: else false, with the error of the write.
func (w *Writer) encodeForcedDecimals(t Type, d Data, n int) (flatbuf.Off, bool) {
	if w.force != "decimal_byte_parts" || w.depth != 1 {
		return 0, false
	}
	if w.enc == nil {
		w.enc = encode.New()
	}
	width := decimalWidth(t.precision)
	raw := w.alloc(n * width)
	for i, v := range d.decs {
		le := v.le()
		copy(raw[i*width:], le[:width])
	}
	node, ok := encode.DecimalBytes(w.enc, raw, width, d.nulls)
	if !ok {
		w.setErr(fmt.Errorf("vortex: Write: the %s encoding cannot take %s", w.force, d.describe()))
		return 0, false
	}
	return w.emit(node), true
}

// encodeForcedTimestamps is the array of the timestamps of d as datetime parts, when that is
// the encoding forced: it replaces the extension array of the timestamps, which has the type.
func (w *Writer) encodeForcedTimestamps(t Type, d Data) (flatbuf.Off, bool) {
	if w.force != "datetimeparts" || w.depth != 1 {
		return 0, false
	}
	if w.enc == nil {
		w.enc = encode.New()
	}
	ticks := map[Unit]int64{Second: encode.TicksSecond, Millisecond: encode.TicksMilli,
		Microsecond: encode.TicksMicro, Nanosecond: encode.TicksNano}[t.unit]
	node, _ := encode.DateTimeParts(w.enc, unsafex.Slice[int64](d.raw), d.nulls, ticks) // the units are the 4 ticks
	return w.emit(node), true
}

// chunked is the vortex.chunked array of n rows in 3 parts, each made by part(lo, hi) as a
// plain array. The first cut is at a third, the second at the same row, so that a part is empty
// when there are rows enough.
func (w *Writer) chunked(n int, part func(lo, hi int) *encode.Node) *encode.Node {
	cuts := [4]int{0, n / 3, n / 3, n}
	parts, rows := make([]*encode.Node, 0, 3), make([]int, 0, 3)
	for i := range 3 {
		parts = append(parts, part(cuts[i], cuts[i+1]))
		rows = append(rows, cuts[i+1]-cuts[i])
	}
	return encode.Chunked(w.enc, parts, rows)
}

// chunkedNumbers is the vortex.chunked array of the numbers d, each part a plain primitive.
func (w *Writer) chunkedNumbers(d Data, n int) *encode.Node {
	width := numberWidth(d.pk)
	return w.chunked(n, func(lo, hi int) *encode.Node {
		return encode.Primitive(w.enc, d.raw[lo*width:hi*width], width, sliceNulls(d.nulls, lo, hi), hi-lo)
	})
}

// sliceNulls is the nulls of rows lo to hi, nil when there are none.
func sliceNulls(nulls []bool, lo, hi int) []bool {
	if nulls == nil {
		return nil
	}
	return nulls[lo:hi]
}

// emit writes the tree of an encoded array: the buffers of a node, then those of its
// children, as the reader numbers them.
func (w *Writer) emit(n *encode.Node) flatbuf.Off {
	first := len(w.bufs)
	for _, b := range n.Bufs {
		w.addBuf(b.Data, b.Align)
	}
	base := len(w.stack)
	for _, k := range n.Kids {
		w.depth++ // a child, whatever its encoding
		w.push(w.emit(k))
		w.depth--
	}
	return w.node(n.ID, n.Meta, first, len(n.Bufs), base)
}

func forceSigned[T encode.Signed](e *encode.Encoder, name string, v []T, nulls []bool) *encode.Node {
	if name == "zigzag" {
		return encode.ZigZag(e, v, nulls)
	}
	return forceInt(e, name, v, nulls)
}

func forceInt[T encode.Int](e *encode.Encoder, name string, v []T, nulls []bool) *encode.Node {
	switch name {
	case "bitpacked":
		n, err := encode.BitPackBest(e, v, nulls)
		if err != nil { // nothing to gain, or a value that is too wide for the best width
			n, _ = encode.BitPack(e, v, nulls, 8*unsafex.Size[T]()-1)
		}
		return n
	case "for":
		return encode.FoR(e, v, nulls)
	case "sequence":
		n, _ := encode.Sequence(e, v, nulls)
		return n
	}
	return forceNumber(e, name, v, nulls)
}

func forceFloat[T encode.Float](e *encode.Encoder, name string, v []T, nulls []bool) *encode.Node {
	switch name {
	case "alp":
		return encode.ALP(e, v, nulls)
	case "alprd":
		n, _ := encode.ALPRD(e, v, nulls)
		return n
	}
	return forceNumber(e, name, v, nulls)
}

func forceNumber[T encode.Number](e *encode.Encoder, name string, v []T, nulls []bool) *encode.Node {
	switch name {
	case "zstd":
		return encode.Zstd(e, v, nulls)
	case "rle":
		return encode.RLE(e, v, nulls)
	case "runend":
		return encode.RunEnd(e, v, nulls)
	case "dict":
		return encode.Dict(e, v, nulls)
	case "sparse":
		n, _ := encode.Sparse(e, v, nulls)
		return n
	case "constant":
		n, _ := encode.Constant(e, v, nulls)
		return n
	case forceMasked:
		n, _ := encode.ConstantMasked(e, v, nulls)
		return n
	}
	return nil
}

// forceMasked forces a constant under a validity.
const forceMasked = "masked"

// forceChunked forces a vortex.chunked array.
const forceChunked = "chunked"
