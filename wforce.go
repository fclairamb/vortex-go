package vortex

import (
	"fmt"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/flatbuf"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The writer does not choose among the encoders of internal/encode yet: that is the compressor.
// A test can force one on a Writer (the force field, the name of the encoding), which then
// writes with it every array of numbers it can, and every dictionary of strings, to check the
// files with the reference reader. An array that the encoder cannot take is an error.

// encodeForced is the array of the numbers d in the forced encoding.
func (w *Writer) encodeForced(d Data, n int) flatbuf.Off {
	if w.enc == nil {
		w.enc = encode.New()
	}
	var node *encode.Node
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
		node = forceNumber(w.enc, w.force, unsafex.Slice[float32](d.raw), nulls)
	default:
		node = forceNumber(w.enc, w.force, unsafex.Slice[float64](d.raw), nulls)
	}
	if node == nil {
		w.setErr(fmt.Errorf("vortex: Write: the %s encoding cannot take %s", w.force, d.describe()))
		return w.encodePlain(d, n)
	}
	return w.emit(node)
}

// encodeForcedStrings is the array of strings d (the buffer of a column holds them as byte
// slices) as a dictionary, when that is the encoding
// forced, and false when it is not.
func (w *Writer) encodeForcedStrings(d Data) (flatbuf.Off, bool) {
	if w.force != "dict" || w.depth != 1 {
		return 0, false
	}
	if w.enc == nil {
		w.enc = encode.New()
	}
	return w.emit(encode.DictBytes(w.enc, d.bins, d.nulls)), true
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

func forceNumber[T encode.Number](e *encode.Encoder, name string, v []T, nulls []bool) *encode.Node {
	switch name {
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
	}
	return nil
}
