package vortex

import (
	"github.com/fclairamb/vortex-go/internal/flatbuf"
	"github.com/fclairamb/vortex-go/internal/protowire"
)

// The sizes of the blocks and of the chunks, variables so that the tests can lower them.
var (
	rowBlock   = 8192    // the rows of a block: the unit of the chunks and of the zones
	chunkBytes = 1 << 20 // the uncompressed bytes at which a chunk is cut, at a block
)

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
	zones  *zoneAcc
	dict   *dictEnc
	buf    *body
	pieces []piece
	arena  []byte
	specs  []aggSpec
	t      Type
	ranger
	block    int
	size     int
	rows     uint64
	zoneSeg  uint32
	mode     colMode
	eligible bool
}

func newColumn(t Type, zoned bool) *column {
	c := &column{ranger: newRanger(t), t: t, buf: newBody(t), eligible: dictEligible(t)}
	if zoned {
		c.zones = newZoneAcc(t)
	}
	return c
}

// addRows adds the first n rows of d to the column, a block at a time.
func (w *Writer) addRows(c *column, d Data, n int) {
	for lo := 0; lo < n && w.err == nil; {
		take := min(n-lo, rowBlock-(c.buf.n-c.block))
		appendRows(c.buf, d.body, c.t, lo, lo+take, &c.arena)
		lo += take
		if c.buf.n-c.block == rowBlock {
			w.consume(c, c.buf.n)
		}
	}
	c.rows += uint64(n)
}

// consume takes the rows of the buffer from the last block to hi: a whole block, or the
// last rows of the column. Their statistics are recorded, then they go to a dictionary, or
// they wait for the chunk to be full.
func (w *Writer) consume(c *column, hi int) {
	lo := c.block
	if c.zones != nil {
		c.zones.add(c.buf, lo, hi)
	}
	if c.mode == modeUndecided {
		if w.force != "" {
			c.mode = modePlain // a test that forces an encoding gets no dictionary layout
		} else {
			c.decide(lo, hi)
		}
	}
	if c.mode == modeDict {
		w.encodeDict(c, lo, hi)
		c.empty()
		return
	}
	c.size += sizeOf(c.buf, c.t, lo, hi)
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

// flushPlain writes the buffer as one flat chunk.
func (w *Writer) flushPlain(c *column) {
	w.start()
	w.beginFlat()
	w.stats, w.statsID = w.arrayStats(c), rootID(c.t)
	root := w.encode(c.t, Data{c.buf}, c.buf.n)
	w.stats = 0
	if seg, ok := w.endFlat(root); ok {
		c.pieces = append(c.pieces, piece{rows: uint64(c.buf.n), seg: seg})
	}
	c.empty()
}

// finish writes what is left of the column: the last rows, the dictionary being built, and
// the zones table.
func (w *Writer) finish(c *column) {
	if c.buf.n > c.block {
		w.consume(c, c.buf.n)
	}
	if c.mode == modeDict {
		w.endRun(c)
	} else if c.buf.n > 0 || len(c.pieces) == 0 {
		w.flushPlain(c)
	}
	if c.zones != nil && c.rows > 0 && w.err == nil {
		w.writeZones(c)
	}
}

// writeZones writes the zones table, a struct of one field per statistic, as a flat layout.
func (w *Writer) writeZones(c *column) {
	fields, cols, specs := c.zones.table()
	w.beginFlat()
	root := w.encode(StructOf(fields...), OfStruct(cols...), c.zones.n)
	if seg, ok := w.endFlat(root); ok {
		c.zoneSeg, c.specs = seg, specs
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
	if c.specs == nil {
		return data
	}
	zones := w.layoutNode("vortex.flat", uint64(c.zones.n), nil, nil, []uint32{c.zoneSeg})
	return w.layoutNode("vortex.zoned", c.rows, zonedMetadata(rowBlock, c.specs), []flatbuf.Off{data, zones}, nil)
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
