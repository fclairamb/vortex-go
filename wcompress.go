package vortex

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/fclairamb/vortex-go/internal/compress"
	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/flatbuf"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The writer hands each chunk of a column to the compressor of the column (internal/compress),
// unless the Compression is None or a test forces an encoding. When the file has more than one
// column, a full chunk waits in the spare buffer of its column: at the end of a Write, at
// Close, or when the column fills another one, the waiting chunks are compressed in parallel, a
// goroutine per column up to GOMAXPROCS, then written one after the other in the order they
// were filled, so that the file does not depend on the scheduling. A column keeps at most two
// chunks, the one being filled and the one that waits. The chunks of a file of one column, and
// those of the dictionary layout (its codes and its values), are compressed as they are
// written.

// compressing says whether the chunks are compressed.
func (w *Writer) compressing() bool { return w.opt.Compression != None && w.force == "" }

// compressor is the compressor of the column, made on the first use.
func (w *Writer) compressor(c *column) *compress.Compressor {
	if c.comp == nil {
		mode := compress.Default
		if w.opt.Compression == Compact {
			mode = compress.Compact
		}
		c.comp = compress.NewLimited(mode, w.limit)
	}
	return c.comp
}

// ptypeOf is the type of the compressor of the numbers of a column of kind k. A Float16 is not
// one: its bits are not integers to the reader.
func ptypeOf(k Kind) (compress.PType, bool) {
	switch k {
	case Int8, Int16, Int32, Int64:
		return compress.I8 + compress.PType(k-Int8), true
	case Uint8, Uint16, Uint32, Uint64:
		return compress.U8 + compress.PType(k-Uint8), true
	case Float32:
		return compress.F32, true
	case Float64:
		return compress.F64, true
	}
	return 0, false
}

// ticksOf is the ticks of a second of a unit of a timestamp.
func ticksOf(u Unit) int64 {
	switch u {
	case Second:
		return encode.TicksSecond
	case Millisecond:
		return encode.TicksMilli
	case Microsecond:
		return encode.TicksMicro
	}
	return encode.TicksNano
}

// compressData is the compressed array of the rows of d, of type t, or nil for the plain one.
// scratch holds the bytes of decimals.
func compressData(cp *compress.Compressor, t Type, d Data, scratch *[]byte) *encode.Node {
	switch t.kind {
	case Bool:
		return cp.Bools(d.bools, d.nulls)
	case Utf8, Binary:
		return cp.Strings(d.bins, d.nulls, t.kind == Utf8)
	case KindDecimal:
		width := decimalWidth(t.precision)
		raw := (*scratch)[:0]
		for _, v := range d.decs {
			le := v.le()
			raw = append(raw, le[:width]...)
		}
		*scratch = raw
		return cp.Decimals(raw, width, d.nulls)
	case Timestamp:
		return cp.Timestamps(unsafex.Slice[int64](d.raw), d.nulls, ticksOf(t.unit))
	}
	if p, ok := ptypeOf(storageKind(t)); ok && t.kind != Float16 {
		return cp.Numbers(p, d.raw, d.nulls)
	}
	return nil
}

// encodeCompressed writes the compressed array of the rows d of a column of type t, and is
// false when it is the plain one, or when the array is not compressed. The array is the one
// compressed before (w.pre) when there is one, else the compressor w.comp makes it.
func (w *Writer) encodeCompressed(t Type, d Data) (flatbuf.Off, bool) {
	if (w.depth != 1 || !w.hasPre) && w.comp == nil {
		return 0, false
	}
	node := w.pre
	if !w.hasPre {
		node = compressData(w.comp, t, d, &w.decRaw)
	}
	w.pre, w.hasPre = nil, false
	if node == nil {
		return 0, false
	}
	ext := (t.kind == Date || t.kind == Time || t.kind == Timestamp) && node.ID != encode.IDDateTimeParts
	if !ext && w.depth == 1 {
		w.statsID = node.ID // the statistics go on the root, whatever its encoding
	}
	off := w.emit(node)
	if ext {
		off = w.arrayExtension(off)
	}
	return off, true
}

// encodeU32s writes the offsets or the sizes of a list, compressed when the chunk is, as the
// reference does.
func (w *Writer) encodeU32s(raw []byte) flatbuf.Off { return w.encodeOffsets(compress.U32, raw) }

// encodeOffsets writes the unsigned numbers raw of type p, compressed when the chunk is.
func (w *Writer) encodeOffsets(p compress.PType, raw []byte) flatbuf.Off {
	w.depth++ // a child, whatever its encoding
	var off flatbuf.Off
	if node := w.compressNumbers(p, raw); node != nil {
		off = w.emit(node)
	} else {
		off = w.leaf("vortex.primitive", raw, p.Width())
	}
	w.depth--
	return off
}

// compressNumbers is the compressed array of the numbers raw of type p, nil for the plain one.
func (w *Writer) compressNumbers(p compress.PType, raw []byte) *encode.Node {
	if w.comp == nil {
		return nil
	}
	return w.comp.Numbers(p, raw, nil)
}

// queueChunk makes the rows of the buffer of the column a chunk that waits to be compressed.
func (w *Writer) queueChunk(c *column) {
	if c.pending {
		w.drain()
	}
	if c.spare == nil {
		c.spare = newBody(c.t)
	}
	c.buf, c.spare = c.spare, c.buf
	c.arena, c.spareArena = c.spareArena, c.arena
	c.pending = true
	w.queue = append(w.queue, c)
}

// drain compresses the chunks that wait, in parallel, then writes them in order.
func (w *Writer) drain() {
	q := w.queue
	if len(q) == 0 {
		return
	}
	workers := min(len(q), runtime.GOMAXPROCS(0))
	if workers == 1 {
		for _, c := range q {
			compressPending(w, c)
		}
	} else {
		var next atomic.Int32
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for i := int(next.Add(1)) - 1; i < len(q); i = int(next.Add(1)) - 1 {
					compressPending(w, q[i])
				}
			})
		}
		wg.Wait()
	}
	for _, c := range q {
		w.pre, w.hasPre = c.node, true
		if c.node == nil { // the children of a nested column are compressed as they are written
			w.comp = c.comp
		}
		w.writeChunk(c, c.spare)
		w.pre, w.hasPre, w.comp, c.node = nil, false, nil, nil
		resetBody(c.spare)
		c.spareArena = c.spareArena[:0]
		c.pending = false
	}
	clear(q)
	w.queue = q[:0]
}

// compressPending compresses the chunk that waits in the spare buffer of the column. It only
// touches the column.
func compressPending(w *Writer, c *column) {
	cp := w.compressor(c)
	cp.Reset()
	c.node = compressData(cp, c.t, Data{c.spare}, &c.decRaw)
}

// withCompressor runs f with the compressor of the column as the one of the arrays written at
// the top of the segment, when the chunks are compressed.
func (w *Writer) withCompressor(c *column, f func()) {
	if w.compressing() {
		w.comp = w.compressor(c)
		w.comp.Reset()
	}
	f()
	w.comp = nil
}

// probeDict says whether the compressor would write the rows lo to hi of the buffer, the
// first block of the column, as a dictionary.
func (w *Writer) probeDict(c *column, lo, hi int) bool {
	cp := w.compressor(c)
	cp.Reset()
	defer cp.Reset()
	var nulls []bool
	if c.buf.nulls != nil {
		nulls = c.buf.nulls[lo:hi]
	}
	if c.buf.kind == dBytes {
		return cp.PicksDictStrings(c.buf.bins[lo:hi], nulls, c.t.kind == Utf8)
	}
	p, _ := ptypeOf(storageKind(c.t)) // a column of numbers that can be a dictionary, not a Float16
	width := p.Width()
	return cp.PicksDict(p, c.buf.raw[lo*width:hi*width], nulls)
}
