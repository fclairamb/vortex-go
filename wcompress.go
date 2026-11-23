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
// unless the Compression is None or a test forces an encoding. A full chunk waits in a queue,
// with its own buffer and its own compressor: at the end of a Write, at Close, or when the
// queue is full, the waiting chunks are compressed in parallel, up to GOMAXPROCS at once, then
// written one after the other in the order they were filled, so that the file does not depend
// on the scheduling, nor on the number of cores: the length of the queue only depends on the
// schema, or on WriteOptions.Buffer, which trades the memory of the chunks that wait for the
// cores that work at once. Several chunks of one column can wait: a column of long strings fills its chunks
// faster than the others, and costs more to compress. The chunks of the dictionary layout (its
// codes and its values) are compressed as they are written, but for the last run of a column,
// which Close compresses on every core with the other columns (finishColumns).

// compressing says whether the chunks are compressed.
func (w *Writer) compressing() bool { return w.opt.Compression != None && w.force == "" }

// compressor is the compressor of the column, made on the first use.
func (w *Writer) compressor(c *column) *compress.Compressor {
	if c.comp == nil {
		c.comp = w.newCompressor(c.buf.kind)
	}
	return c.comp
}

// compressors is the compressors of the writers that are closed, by the kind of the data of
// their column: the next writers take them, with the memory they have, in place of growing
// their own from nothing. A compressor of strings holds much more than one of numbers, so they
// do not mix. What a compressor makes of a chunk does not depend on the chunks it has seen.
var compressors [dVars + 1]sync.Pool

// newCompressor returns a compressor of the mode and of the edition of the file, for a column
// of data of this kind.
func (w *Writer) newCompressor(kind dataKind) *compress.Compressor {
	mode := compress.Default
	if w.opt.Compression == Compact {
		mode = compress.Compact
	}
	if cp, ok := compressors[kind].Get().(*compress.Compressor); ok {
		cp.Configure(mode, w.limit)
		return cp
	}
	return compress.NewLimited(mode, w.limit)
}

// releaseCompressors gives the compressors of the columns under l back, once the file is
// written: nothing of what they made is read any more.
func releaseCompressors(l *layoutBuild) {
	for _, k := range l.kids {
		releaseCompressors(k)
	}
	c := l.col
	if c == nil || c.wide { // a compressor of wide rows holds too much to be kept
		return
	}
	pool := &compressors[c.buf.kind]
	if c.comp != nil {
		c.comp.Reset()
		pool.Put(c.comp)
	}
	for _, q := range c.free {
		if q.comp != c.comp { // one of the chunks has the compressor of the column
			q.comp.Reset()
			pool.Put(q.comp)
		}
	}
	if c.last.comp != nil {
		c.last.comp.Reset()
		pool.Put(c.last.comp)
	}
	c.comp, c.free, c.last.comp = nil, nil, nil
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
func compressData(cp *compress.Compressor, t Type, d Data) *encode.Node {
	switch t.kind {
	case Bool:
		return cp.Bools(d.bools, d.nulls)
	case Utf8, Binary:
		return cp.Strings(d.bins, d.nulls, t.kind == Utf8)
	case KindDecimal:
		return cp.Decimals(d.raw, decimalWidth(t.precision), d.nulls)
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
		node = compressData(w.comp, t, d)
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

// waiting is a full chunk of a column that waits to be compressed, then written.
type waiting struct {
	c     *column
	buf   *body
	comp  *compress.Compressor
	node  *encode.Node // the compressed array, nil for the plain one
	arena []byte
	stats astat // the statistics of the rows, found with the compressed array
	bare  bool  // the chunk has no statistics, see Writer.chunkStats
}

// queueLen is the chunks that wait at most, when WriteOptions.Buffer sets no size: enough for
// every column to have one, and for a column that fills faster to have several.
func (w *Writer) queueLen() int { return max(2*w.leaves, minQueue) }

// full says whether the chunks that wait are compressed now: they are as many as the queue
// holds, or their rows are the bytes of WriteOptions.Buffer.
func (w *Writer) full() bool {
	if w.opt.Buffer > 0 {
		return w.queued >= w.opt.Buffer
	}
	return len(w.queue) >= w.queueLen()
}

// minQueue is the least chunks the queue holds. A variable so that the tests can lower it.
var minQueue = 16

// queueChunk makes the rows of the buffer of the column a chunk that waits to be compressed.
func (w *Writer) queueChunk(c *column) {
	var q *waiting
	if n := len(c.free); n > 0 {
		q, c.free = c.free[n-1], c.free[:n-1]
	} else {
		q = &waiting{c: c, buf: newBody(c.t)}
		if c.slots++; c.slots == 1 {
			q.comp = w.compressor(c) // the one of the dictionaries: they never work at once
		} else {
			q.comp = w.newCompressor(c.buf.kind)
		}
	}
	c.buf, q.buf = q.buf, c.buf
	c.arena, q.arena = q.arena, c.arena
	q.bare = !w.chunkStats()
	c.waiting++
	w.queue = append(w.queue, q)
	if w.queued += c.size; w.full() {
		w.drain()
	}
}

// drain compresses the chunks that wait, in parallel, then writes them in order.
func (w *Writer) drain() {
	q := w.queue
	if len(q) == 0 {
		return
	}
	w.queued = 0
	workers := min(len(q), runtime.GOMAXPROCS(0))
	if workers == 1 {
		for _, c := range q {
			compressWaiting(c)
		}
	} else {
		var next atomic.Int32
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for i := int(next.Add(1)) - 1; i < len(q); i = int(next.Add(1)) - 1 {
					compressWaiting(q[i])
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
		w.writeChunkWith(c.c, c.buf, &c.stats)
		c.stats = astat{}
		w.pre, w.hasPre, w.comp, c.node = nil, false, nil, nil
		resetBody(c.buf)
		c.arena = c.arena[:0]
		c.c.waiting--
		c.c.free = append(c.c.free, c)
	}
	clear(q)
	w.queue = q[:0]
}

// compressWaiting compresses a chunk that waits. It only touches the chunk.
func compressWaiting(q *waiting) {
	q.comp.Reset()
	q.node = compressData(q.comp, q.c.t, Data{q.buf})
	if !q.bare {
		q.stats = q.c.arrayStats(q.buf)
	}
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

// withNode runs f with node, the array that cp compressed before (nil for the plain one), as the
// array written at the top of the segment: what withCompressor would have made with cp.
func (w *Writer) withNode(node *encode.Node, cp *compress.Compressor, f func()) {
	w.pre, w.hasPre = node, true
	if node == nil { // the children of the plain array are compressed as they are written
		w.comp = cp
	}
	f()
	w.pre, w.hasPre, w.comp = nil, false, nil
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
