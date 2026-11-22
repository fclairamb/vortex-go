package vortex

import (
	"runtime"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/fclairamb/vortex-go/internal/compress"
	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/flatbuf"
	"github.com/fclairamb/vortex-go/internal/protowire"
)

// The sizes of the blocks and of the chunks, variables so that the tests can lower them.
var (
	rowBlock   = 8192    // the rows of a block: the unit of the chunks and of the zones
	chunkBytes = 1 << 20 // the uncompressed bytes at which a chunk is cut, at a block
)

// wideChunks is the size, in chunks, from which a chunk is compressed and written alone.
const wideChunks = 4

type colMode uint8

const (
	modeUndecided colMode = iota // the probe (the first block) has not been seen
	modePlain                    // chunks of plain arrays
	modeDict                     // dictionaries
)

// piece is a part of the data of a column: a flat chunk, or a dictionary (a run of rows
// coded against one set of values).
type piece struct {
	codes []chunk
	rows  uint64
	nvals int
	seg   uint32
}

type chunk struct {
	rows uint64
	seg  uint32
}

// column is a column that has no children: the rows that are not in a segment yet, and what
// has been written of it.
type column struct {
	zones    *zoneAcc
	dict     *dictEnc
	last     lastRun // what finishColumns made of the end of the column
	buf      *body
	comp     *compress.Compressor // of the dictionaries, made on the first use
	free     []*waiting           // the chunks written, whose buffers the next ones take
	pieces   []piece
	arena    []byte
	zoneMeta []byte // the metadata of the zone map layout, nil when there is none
	t        Type
	ranger
	block     int
	size      int
	rows      uint64
	rest      int // the row of the Data of the Write from which settle adds the rows
	coded     int // the row of the buffer up to which prepare coded the rows of a dictionary
	waiting   int // the chunks that wait to be compressed, see wcompress.go
	slots     int // the waiting chunks made for the column
	zoneSeg   uint32
	mode      colMode
	eligible  bool
	wide      bool // a chunk was many times the size of a chunk: the compressors are not kept
	full      bool // fill filled a block, which settle commits
	codesFull bool // prepare stopped at a chunk of codes that is full
}

func newColumn(t Type, zoned bool) *column {
	c := &column{ranger: newRanger(t), t: t, buf: newBody(t), eligible: dictEligible(t)}
	if zoned {
		c.zones = newZoneAcc(t)
	}
	return c
}

// colWork is a column and its Data of the Write being added.
type colWork struct {
	c *column
	d Data
}

// validWork is the validity of a struct and the nulls of its Data of the Write being added.
type validWork struct {
	valid *validBuild
	nulls []bool
}

// parallelRows is the rows of a step from which the columns work on every core: under it,
// starting the goroutines costs more than they save.
const parallelRows = 1024

// advance adds the rows lo to hi of the Write to every column. What a column does alone (the
// copy of its rows, the statistics of a block, the choice and the codes of a dictionary) runs
// on every core; what goes to the file is then done a column after the other, in order.
func (w *Writer) advance(lo, hi int) {
	work := w.work
	if workers := min(len(work), runtime.GOMAXPROCS(0)); workers > 1 && hi-lo >= parallelRows {
		each(workers, len(work), func(i int) { work[i].c.fill(w, work[i].d, lo, hi) })
	} else {
		for _, k := range work {
			k.c.fill(w, k.d, lo, hi)
		}
	}
	for _, k := range work {
		w.settle(k.c, k.d, hi)
	}
}

// each runs f(0) to f(n-1) on that many goroutines, and waits for them. It is a function of
// its own so that a caller that does without it allocates nothing for it.
func each(workers, n int, f func(i int)) {
	var next atomic.Int32
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := int(next.Add(1)) - 1; i < n; i = int(next.Add(1)) - 1 {
				f(i)
			}
		})
	}
	wg.Wait()
}

// checkColumns checks the Data of a Write against the fields, of n rows each: on every core
// when there is enough of it. The error is the one of the first field that has one.
func (w *Writer) checkColumns(cols []Data, n int) error {
	fields := w.schema.root.fields
	workers := min(len(fields), runtime.GOMAXPROCS(0))
	if workers < 2 || n < parallelRows {
		for i, f := range fields {
			if err := checkData(f.Name, f.Type, cols[i], n); err != nil {
				return err
			}
		}
		return nil
	}
	// The goroutines read a copy of cols: the slice of the caller stays on its stack.
	w.cols = append(w.cols[:0], cols...)
	w.errs = append(w.errs[:0], make([]error, len(fields))...)
	each(workers, len(fields), func(i int) { w.errs[i] = checkData(fields[i].Name, fields[i].Type, w.cols[i], n) })
	clear(w.cols)
	for _, err := range w.errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// fill copies the rows lo to hi of d into the buffer of the column, up to the end of its
// block, and prepares the block when it is full. It only touches the column.
func (c *column) fill(w *Writer, d Data, lo, hi int) {
	c.rows += uint64(hi - lo)
	c.reserve(w.ahead)
	take := min(hi-lo, rowBlock-(c.buf.n-c.block))
	appendRows(c.buf, d.body, c.t, lo, lo+take, &c.arena)
	c.rest = lo + take
	if c.full = c.buf.n-c.block == rowBlock; c.full {
		c.prepare(w, c.buf.n)
	}
}

// settle does what fill left for the column: the block that is full goes to its dictionary or
// to its chunk, and the rows after it are added.
func (w *Writer) settle(c *column, d Data, hi int) {
	if c.full {
		c.full = false
		w.commit(c, c.buf.n)
	}
	// A step is a block of rows at most: what is left after a full block starts the next one.
	appendRows(c.buf, d.body, c.t, c.rest, hi, &c.arena)
}

// reserve makes room in the buffer for the rows that the Write still has for the column, up
// to a chunk of them: a buffer that grows as the rows come is copied many times over.
func (c *column) reserve(ahead int) {
	b := c.buf
	// A dictionary takes the rows a block at a time, and so does a column that has not chosen:
	// only its codes grow with the rows.
	rows := rowBlock
	switch c.mode {
	case modeDict:
		c.dict.codes = slices.Grow(c.dict.codes, min(ahead*c.dict.width, chunkBytes+2-len(c.dict.codes)))
	case modePlain:
		rows = min(ahead, 8*rowBlock)
		if b.kind == dPrim {
			rows = min(ahead, chunkBytes/numberWidth(b.pk)+rowBlock)
		}
	}
	if rows -= b.n - c.block; rows <= 0 {
		return
	}
	if c.t.nullable {
		b.nulls = slices.Grow(b.nulls, rows)
	}
	switch b.kind {
	case dPrim:
		b.raw = slices.Grow(b.raw, rows*numberWidth(b.pk))
	case dBool:
		b.bools = slices.Grow(b.bools, rows)
	case dBytes:
		b.bins = slices.Grow(b.bins, rows)
	case dDec:
		b.decs = slices.Grow(b.decs, rows)
	}
}

// The rows of the buffer from the last block to hi, a whole block or the last rows of the
// column, are taken in two steps: prepare records their statistics and codes them in the
// dictionary, commit writes what goes to the file, or lets them wait for the chunk to be full.

// prepare is the part of taking rows that only touches the column: the statistics of the rows,
// the choice of a dictionary, and their codes up to the first one that needs a write.
func (c *column) prepare(w *Writer, hi int) {
	lo := c.block
	if c.zones != nil {
		c.zones.add(c.buf, lo, hi)
	}
	if c.mode == modeUndecided {
		if w.force != "" {
			c.mode = modePlain // a test that forces an encoding gets no dictionary layout
		} else {
			c.decide(w, lo, hi)
		}
	}
	if c.mode == modeDict {
		c.coded, c.codesFull = c.dict.codeRows(c, lo, hi)
		return
	}
	c.size += sizeOf(c.buf, c.t, lo, hi)
}

// commit is the part of taking rows that writes: the end of the codes of a dictionary, with its
// runs and its chunks of codes, or the chunk that is full.
func (w *Writer) commit(c *column, hi int) {
	if c.mode == modeDict {
		w.encodeDict(c, hi)
		c.empty()
		return
	}
	c.block = hi
	if c.size >= chunkBytes {
		w.flushPlain(c)
	}
}

// empty drops the rows of the buffer.
func (c *column) empty() {
	resetBody(c.buf)
	c.arena = c.arena[:0]
	c.block, c.size = 0, 0
}

// flushPlain makes the buffer one flat chunk: written now, or compressed with the other
// chunks that are full.
func (w *Writer) flushPlain(c *column) {
	switch {
	case !w.compressing():
		w.writeChunk(c, c.buf)
	case c.size > wideChunks*chunkBytes:
		// A chunk of wide rows is a block, many times the size of a chunk: it does not wait,
		// so that the writer holds one of them, not a queue of them.
		w.drain()
		c.wide = true
		w.withCompressor(c, func() { w.writeChunk(c, c.buf) })
	default:
		w.queueChunk(c)
	}
	c.empty()
}

// writeChunk writes the rows of b, a chunk of the column, as a flat layout.
func (w *Writer) writeChunk(c *column, b *body) {
	s := c.arrayStats(b)
	w.writeChunkWith(c, b, &s)
}

// writeChunkWith is writeChunk with the statistics of the rows, found before.
func (w *Writer) writeChunkWith(c *column, b *body, s *astat) {
	w.start()
	w.beginFlat()
	w.stats, w.statsID = s.table(w.fb), rootID(c.t)
	root := w.encode(c.t, Data{b}, b.n)
	w.stats = 0
	if seg, ok := w.endFlat(root); ok {
		c.pieces = append(c.pieces, piece{rows: uint64(b.n), seg: seg})
	}
}

// lastRun is what finishColumns made of the end of a column, on every core, for finish to
// write.
type lastRun struct {
	codes    *encode.Node         // the compressed codes of the run, nil for the plain ones
	vals     *encode.Node         // the compressed values of the run, nil for the plain ones
	comp     *compress.Compressor // the compressor of vals: the one of the column made codes
	scratch  []byte
	cost     int  // about what finishAlone costs, see tailCost
	prepared bool // prepare took the last rows
	run      bool // codes and vals are the arrays of the run of the dictionary
}

// finishColumns does, on every core, the part of finish that only touches a column: the
// statistics, the choice and the codes of its last rows, and the compressed arrays of the run
// of its dictionary. finish then writes them, a column after the other, in order: the file
// is the same whatever the cores. A file of few rows does it all in finish.
func (w *Writer) finishColumns() {
	workers := min(w.leaves, runtime.GOMAXPROCS(0))
	if workers < 2 {
		return
	}
	work, rows := w.work[:0], 0
	work = appendLeaves(work, w.root)
	for _, k := range work {
		c := k.c
		if rows += c.buf.n - c.block; c.mode == modeDict {
			rows += c.dict.rows
		}
		c.last.cost = c.tailCost()
	}
	w.work = work
	if rows >= parallelRows {
		// The costliest columns start first, so that one of them does not run alone at the end.
		slices.SortStableFunc(work, func(a, b colWork) int { return b.c.last.cost - a.c.last.cost })
		each(workers, len(work), func(i int) { work[i].c.finishAlone(w) })
	}
	clear(work)
}

// tailCost is about what finishAlone costs: the bytes it chooses for, codes, or compresses.
// The last rows of a column of plain chunks only have their statistics found.
func (c *column) tailCost() int {
	switch c.mode {
	case modePlain:
		return 0
	case modeDict:
		return sizeOf(c.buf, c.t, c.block, c.buf.n) + c.dict.bytes + len(c.dict.codes)
	}
	return sizeOf(c.buf, c.t, c.block, c.buf.n)
}

// appendLeaves appends the columns under l that have no children, in order.
func appendLeaves(work []colWork, l *layoutBuild) []colWork {
	if !l.isStruct {
		return append(work, colWork{c: l.col})
	}
	for _, k := range l.kids {
		work = appendLeaves(work, k)
	}
	return work
}

// finishAlone is the part of finish that only touches the column: it prepares the last rows
// and, when all that is left of a dictionary is its run, compresses its codes and its values.
func (c *column) finishAlone(w *Writer) {
	if c.buf.n > c.block {
		c.prepare(w, c.buf.n)
		c.last.prepared = true
	}
	coded := c.buf.n == c.block || c.coded == c.buf.n && !c.codesFull
	if c.mode != modeDict || !w.compressing() || !coded {
		return
	}
	d := c.dict
	if n := len(d.codes) / d.width; n > 0 {
		cp := w.compressor(c)
		cp.Reset()
		t, codes := d.codesArray(n)
		c.last.codes = compressData(cp, t, codes, &c.last.scratch)
	}
	c.last.comp = w.newCompressor(c.buf.kind)
	c.last.vals = compressData(c.last.comp, c.t, c.valuesArray(), &c.last.scratch)
	c.last.run = true
}

// finish writes what is left of the column: the last rows, the dictionary being built, and
// the zones table.
func (w *Writer) finish(c *column) {
	if c.buf.n > c.block {
		if !c.last.prepared {
			c.prepare(w, c.buf.n)
		}
		w.commit(c, c.buf.n)
	}
	if c.mode == modeDict {
		w.endRun(c)
	} else if c.buf.n > 0 || len(c.pieces) == 0 && c.waiting == 0 {
		w.flushPlain(c)
	}
	// Compact: a column of one zone has no table of zones, the statistics of the file say what
	// it would; a small file favors the size of its structure over what a reader may skip
	if c.zones != nil && c.rows > 0 && (c.zones.n > 1 || w.opt.Compression != Compact) && w.err == nil {
		w.writeZones(c)
	}
}

// writeZones writes the zones table, a struct of one field per statistic, as a flat layout.
func (w *Writer) writeZones(c *column) {
	var fields []Field
	var cols []Data
	var meta []byte
	if w.legacyZones {
		var bits []byte
		fields, cols, bits = c.zones.legacyTable()
		meta = legacyMetadata(rowBlock, bits)
	} else {
		var specs []aggSpec
		fields, cols, specs = c.zones.table()
		meta = zonedMetadata(rowBlock, specs)
	}
	w.beginFlat()
	root := w.encode(StructOf(fields...), OfStruct(cols...), c.zones.n)
	if seg, ok := w.endFlat(root); ok {
		c.zoneSeg, c.zoneMeta = seg, meta
	}
}

// dataLayout writes the layout of the data of a column: its piece, or the chunked pieces.
func (w *Writer) dataLayout(c *column) flatbuf.Off {
	if len(c.pieces) == 1 {
		return w.pieceLayout(c, c.pieces[0])
	}
	kids := make([]flatbuf.Off, len(c.pieces))
	for i, p := range c.pieces {
		kids[i] = w.pieceLayout(c, p)
	}
	return w.layoutNode("vortex.chunked", c.rows, nil, kids, nil)
}

func (w *Writer) pieceLayout(c *column, p piece) flatbuf.Off {
	if p.codes == nil {
		return w.layoutNode("vortex.flat", p.rows, nil, nil, []uint32{p.seg})
	}
	values := w.layoutNode("vortex.flat", uint64(p.nvals), nil, nil, []uint32{p.seg})
	var codes flatbuf.Off
	if len(p.codes) == 1 {
		codes = w.layoutNode("vortex.flat", p.codes[0].rows, nil, nil, []uint32{p.codes[0].seg})
	} else {
		kids := make([]flatbuf.Off, len(p.codes))
		for i, k := range p.codes {
			kids[i] = w.layoutNode("vortex.flat", k.rows, nil, nil, []uint32{k.seg})
		}
		codes = w.layoutNode("vortex.chunked", p.rows, nil, kids, nil)
	}
	return w.layoutNode("vortex.dict", p.rows, dictMetadata(uint64(c.codeWidth()-1)), []flatbuf.Off{values, codes}, nil)
}

// columnLayout is the layout of a column: its data, and the zones table when it has one.
func (w *Writer) columnLayout(c *column) flatbuf.Off {
	data := w.dataLayout(c)
	if c.zoneMeta == nil {
		return data
	}
	zones := w.layoutNode("vortex.flat", uint64(c.zones.n), nil, nil, []uint32{c.zoneSeg})
	id := "vortex.zoned"
	if w.legacyZones {
		id = "vortex.stats"
	}
	return w.layoutNode(id, c.rows, c.zoneMeta, []flatbuf.Off{data, zones}, nil)
}

// dictMetadata is the metadata of a dictionary layout: the type of the codes (0 is u8, 1
// u16), and two flags: the codes are not nullable, and not every value is referenced.
func dictMetadata(ptype uint64) []byte {
	var meta []byte
	if ptype != 0 {
		meta = protowire.AppendUint(meta, 1, ptype)
	}
	meta = protowire.AppendUint(meta, 2, 0)
	return protowire.AppendUint(meta, 3, 0)
}
