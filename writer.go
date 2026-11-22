package vortex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	mbits "math/bits"

	"github.com/fclairamb/vortex-go/internal/compress"
	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/fbs"
	"github.com/fclairamb/vortex-go/internal/flatbuf"
)

// Compression is how much effort the writer spends to make the file smaller.
type Compression uint8

// The compression levels.
const (
	// Default is the level to use when there is no reason to pick another: the compressor
	// picks the lightweight encodings of each chunk (bit-packing, frame of reference,
	// dictionaries, run-end, ALP, FSST...), as the reference writer does.
	Default Compression = iota
	// Compact favors a small file over the time to write it: zstd is a candidate too, for
	// the numbers and the strings.
	Compact
	// None writes the values as they are, in plain arrays.
	None
)

// WriteOptions are the options of a Writer.
type WriteOptions struct {
	// Edition is the edition the file must be readable with, as the upstream names it
	// ("core 2025.05.0"). Empty is the newest the library knows, "core 2026.08.3". The writer
	// only makes what the edition has: a file for an edition before "core 2026.08.0" has the
	// older zone maps (vortex.stats) and only the legacy file statistics, one before
	// "core 2025.10.0" has no listview and no run-length or masked arrays, and so on. An
	// unknown edition, or a type it cannot hold (a uuid before "core 2026.08.3", a map before
	// "core 2026.08.2", a fixed-size list before "core 2025.10.0"), is an error of the first
	// Write or Close.
	Edition string
	// Buffer is the bytes of rows, as they are before compression, that may wait to be
	// compressed together: the writer compresses the chunks that wait on every core, and each
	// one holds its rows and the memory of its compressor until it is written. 0 is the
	// default, a queue of chunks whose length depends on the schema (two chunks of about 1 MiB
	// a column, 16 at least). A smaller Buffer lowers the memory of the writer and uses fewer
	// cores at once; under the size of a chunk, the chunks are compressed one at a time, on one
	// core. A larger one only matters for a file of few columns. The rows of the file do not
	// depend on it, the order of its segments may.
	Buffer      int
	Compression Compression
}

// ErrWriterClosed is returned by Write after Close.
var ErrWriterClosed = errors.New("vortex: the writer is closed")

// The limits of the format, and the sizes of the buffers: variables so that the tests can
// lower them.
var (
	maxSegment    uint64 = math.MaxUint32 // the bytes of a segment
	maxBuffers           = math.MaxUint16 // the buffers of an array
	maxListElems         = math.MaxUint32 // the elements of a list column: its offsets are u32
	maxViewBuffer        = math.MaxInt32  // the bytes of a data buffer of a string column
	maxPostscript        = math.MaxUint16 // the bytes of the postscript
	smallWrites          = 32 << 10       // the bytes gathered before they are written
	arenaBlock           = 64 << 10       // the smallest block of the arena
)

var (
	zeros      = make([]byte, 64) // the padding
	magicBytes = []byte(magic)    // the magic, at the start of the file
)

// Writer writes a Vortex file. It needs an io.Writer and never seeks.
//
// A column takes the Data its type calls for: Of for the numbers (the Go type is the one of
// the column, int32 for Int32; the storage of a date, a time or a timestamp, and uint16 for
// the bits of a Float16), OfBools, OfStrings or OfBytes for Utf8 and Binary, OfBytes for a
// UUID, OfDecimals, OfVariants for a Variant, OfNulls, OfStruct, and OfList for a list, a fixed-size list and a map.
// Each Write adds rows. The rows of a column are cut into chunks that do not depend on how
// the caller splits them: at multiples of 8,192 rows, about 1 MiB of data a chunk. Each
// column carries the statistics a reader uses to skip a zone, and a column of few distinct
// values is written as a dictionary. Write copies the rows into a buffer of a few MiB a
// column, so they reach the io.Writer when a chunk is full, and at Close.
//
// Write and Close are not safe for concurrent use.
type Writer struct {
	err       error
	w         io.Writer
	schema    *Schema
	root      *layoutBuild
	fb        *flatbuf.Builder
	enc       *encode.Encoder      // the encoders of a forced encoding
	force     string               // the encoding a test forces, see wforce.go
	comp      *compress.Compressor // the compressor of the arrays written now, see wcompress.go
	pre       *encode.Node         // the array of the chunk being written, compressed before
	queue     []*waiting           // the chunks that wait to be compressed
	work      []colWork            // the columns of the Write being added, with their Data
	valids    []validWork          // the validities of its structs
	errs      []error              // the errors of its columns
	cols      []Data               // its Data, while they are checked
	statsID   string
	bufs      []buffer
	blocks    [][]byte
	layoutIDs []string
	arrayIDs  []string
	segs      []byte
	bspec     []byte
	out       []byte
	stack     []flatbuf.Off
	idx       []uint16
	meta      []byte
	opt       WriteOptions
	depth     int
	queued    int // the bytes, before compression, of the chunks that wait
	pos       int64
	written   int64
	rows      uint64
	block     int
	used      int
	stats     flatbuf.Off
	lenBuf    [4]byte
	limit     compress.Limits // the encodings of the edition that the compressor leaves out
	started   bool
	done      bool
	hasPre    bool // pre is the array of the chunk, nil for the plain one
	// What the edition of the file has not: see WriteOptions.Edition.
	legacyZones bool // zone maps are vortex.stats layouts, not vortex.zoned
	legacyFile  bool // the file statistics have no aggregates
	plainLists  bool // a list is a vortex.list, never a vortex.listview
	leaves      int  // the columns that have no children
	ahead       int  // the rows of the Write that the columns have not taken yet
}

// buffer is a data buffer of the array being built, and the alignment its first byte needs.
type buffer struct {
	data  []byte
	align int
}

// layoutBuild is a node of the layout tree being built: a struct (fields, and the validity
// when it is nullable) or a column.
type layoutBuild struct {
	valid    *validBuild
	col      *column
	kids     []*layoutBuild
	isStruct bool
}

// validBuild gathers the validity of a nullable struct, a bit a row, written as one chunk.
type validBuild struct {
	bits  []byte
	rows  int
	nulls uint64
	seg   uint32
}

func (v *validBuild) add(nulls []bool, lo, hi int) {
	for i := lo; i < hi; i++ {
		if v.rows&7 == 0 {
			v.bits = append(v.bits, 0)
		}
		if isNull(nulls, i) {
			v.nulls++
		} else {
			v.bits[len(v.bits)-1] |= 1 << (v.rows & 7)
		}
		v.rows++
	}
}

// newLayoutBuild builds the tree of a column of type t. A field under a nullable struct has
// no zone statistics (the reference writer has none either).
func newLayoutBuild(t Type, underNullable bool) *layoutBuild {
	if t.kind != Struct {
		return &layoutBuild{col: newColumn(t, !underNullable)}
	}
	n := &layoutBuild{isStruct: true}
	if t.nullable {
		n.valid = &validBuild{}
	}
	for _, f := range t.fields {
		n.kids = append(n.kids, newLayoutBuild(f.Type, underNullable || t.nullable))
	}
	return n
}

// countLeaves is the columns of the tree that have no children.
func countLeaves(l *layoutBuild) int {
	if !l.isStruct {
		return 1
	}
	n := 0
	for _, k := range l.kids {
		n += countLeaves(k)
	}
	return n
}

// NewWriter returns a Writer of a file with this schema. Nothing is written until the first
// Write or Close. A schema that cannot be written (a decimal of precision 0, say) is
// reported by the first Write or Close.
func NewWriter(w io.Writer, s *Schema, o WriteOptions) *Writer {
	wr := &Writer{w: w, schema: s, opt: o, fb: flatbuf.NewBuilder(1 << 10)}
	wr.err = validType(s.root, "")
	if wr.err == nil {
		wr.err = wr.setEdition(o.Edition)
	}
	if wr.err == nil {
		wr.err = validEdition(s.root, "", wr.opt.Edition)
	}
	wr.root = newLayoutBuild(s.root, false)
	wr.leaves = countLeaves(wr.root)
	wr.out = make([]byte, 0, smallWrites)
	return wr
}

// setErr records the first error.
func (w *Writer) setErr(err error) {
	if w.err == nil {
		w.err = err
	}
}

// raw hands bytes to the io.Writer.
func (w *Writer) raw(b []byte) {
	if w.err != nil {
		return
	}
	n, err := w.w.Write(b)
	w.written += int64(n)
	if err == nil && n < len(b) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.setErr(fmt.Errorf("vortex: writing at offset %d: %w", w.written, err))
	}
}

// put writes b at the current position. Small pieces are gathered, big ones go straight to
// the io.Writer.
func (w *Writer) put(b []byte) {
	w.pos += int64(len(b))
	if len(b) > cap(w.out)/2 {
		w.flush()
		w.raw(b)
		return
	}
	if len(w.out)+len(b) > cap(w.out) {
		w.flush()
	}
	w.out = append(w.out, b...)
}

func (w *Writer) flush() {
	if len(w.out) > 0 {
		w.raw(w.out)
		w.out = w.out[:0]
	}
}

// pad writes n zero bytes.
func (w *Writer) pad(n int) {
	for n > 0 {
		k := min(n, len(zeros))
		w.put(zeros[:k])
		n -= k
	}
}

func alignUp(v int64, align int) int64 {
	a := int64(align)
	return (v + a - 1) / a * a
}

// Write adds rows to the file: one Data per field of the schema, all of the same length.
// It returns an error when a Data does not fit its field, and then, or when the io.Writer
// fails, the writer is closed for writing. Write keeps none of the slices of the Data: the
// caller may write them again when it returns. It copies the bytes of OfBytes, not the ones
// of the strings of OfStrings, which a Go string never changes: it points to them until their
// chunk is written. The rows are in the io.Writer when Close returns, some of them before.
func (w *Writer) Write(cols ...Data) error {
	if w.done {
		return ErrWriterClosed
	}
	if w.err != nil {
		return w.err
	}
	if err := w.writeRows(cols); err != nil {
		w.setErr(err)
		return w.err
	}
	w.flush()
	return w.err
}

// writeRows adds the rows of every column to its buffer.
func (w *Writer) writeRows(cols []Data) error {
	fields := w.schema.root.fields
	if len(cols) != len(fields) {
		return fmt.Errorf("vortex: Write: %d columns for a schema of %d fields", len(cols), len(fields))
	}
	n := 0
	if len(cols) > 0 && cols[0].body != nil {
		n = cols[0].n
	}
	if err := w.checkColumns(cols, n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	w.start()
	w.work, w.valids = w.work[:0], w.valids[:0]
	for i, f := range fields {
		w.gather(w.root.kids[i], f.Type, cols[i])
	}
	// The columns advance together, a block at a time: the chunks that fill up in a block are
	// then compressed together, on every core, whatever the rows of one Write.
	for lo := 0; lo < n && w.err == nil; lo += rowBlock {
		hi := min(n, lo+rowBlock)
		w.ahead = n - lo
		for _, v := range w.valids {
			v.valid.add(v.nulls, lo, hi)
		}
		w.advance(lo, hi)
	}
	clear(w.work) // the Data of the caller is not kept
	clear(w.valids)
	w.drain()
	w.rows += uint64(n)
	return w.err
}

// start writes the magic, once.
func (w *Writer) start() {
	if !w.started {
		w.started = true
		w.put(magicBytes)
	}
}

// gather lists the columns under l, with their Data of a Write, and the validities of the
// structs. A struct has no segment of its own: its validity, when it has one, and its fields
// do.
func (w *Writer) gather(l *layoutBuild, t Type, d Data) {
	if t.kind != Struct {
		w.work = append(w.work, colWork{l.col, d})
		return
	}
	if l.valid != nil {
		w.valids = append(w.valids, validWork{l.valid, d.nulls})
	}
	for i, f := range t.fields {
		w.gather(l.kids[i], f.Type, d.kids[i])
	}
}

// beginFlat starts a segment: the buffers and the tree of one array.
func (w *Writer) beginFlat() {
	w.bufs = w.bufs[:0]
	w.stack = w.stack[:0]
	w.fb.Reset()
	w.block, w.used = 0, 0
	if w.enc != nil {
		w.enc.Reset()
	}
}

// alloc returns n zeroed bytes that live until the next beginFlat. Blocks are kept between
// segments, so a running writer does not allocate.
func (w *Writer) alloc(n int) []byte {
	for ; w.block < len(w.blocks); w.block, w.used = w.block+1, 0 {
		if b := w.blocks[w.block]; len(b)-w.used >= n {
			s := b[w.used : w.used+n : w.used+n]
			w.used += n
			clear(s)
			return s
		}
	}
	w.blocks = append(w.blocks, make([]byte, max(n, arenaBlock)))
	w.used = n
	return w.blocks[w.block][:n:n]
}

// endFlat writes the segment of the array whose root node is root, and returns its index:
// the buffers, each aligned in the file, the flatbuffer of the tree, its length. It is false
// when the segment could not be written.
func (w *Writer) endFlat(root flatbuf.Off) (uint32, bool) {
	if w.err != nil {
		return 0, false
	}
	if len(w.bufs) > maxBuffers {
		w.setErr(fmt.Errorf("vortex: Write: an array of %d buffers, at most %d", len(w.bufs), maxBuffers))
		return 0, false
	}
	align := 8 // the flatbuffer
	for _, b := range w.bufs {
		align = max(align, b.align)
	}
	start := alignUp(w.pos, align)
	pos := start
	w.bspec = w.bspec[:0]
	for _, b := range w.bufs {
		pad := alignUp(pos, b.align) - pos
		w.bspec = binary.LittleEndian.AppendUint16(w.bspec, uint16(pad))
		w.bspec = append(w.bspec, byte(mbits.TrailingZeros(uint(b.align))), 0)
		w.bspec = binary.LittleEndian.AppendUint32(w.bspec, uint32(len(b.data)))
		pos += pad + int64(len(b.data))
	}
	structs := w.fb.Structs(8, 4, w.bspec)
	w.fb.StartTable(2)
	w.fb.Ref(0, root)
	w.fb.Ref(1, structs)
	msg := w.fb.Finish(w.fb.EndTable())
	fbPad := alignUp(pos, 8) - pos
	length := uint64(pos-start) + uint64(fbPad) + uint64(len(msg)) + 4
	if length > maxSegment {
		w.setErr(fmt.Errorf("vortex: Write: a segment of %d bytes, at most %d", length, maxSegment))
		return 0, false
	}
	w.pad(int(start - w.pos))
	for _, b := range w.bufs {
		w.pad(int(alignUp(w.pos, b.align) - w.pos))
		w.put(b.data)
	}
	w.pad(int(fbPad))
	w.put(msg)
	binary.LittleEndian.PutUint32(w.lenBuf[:], uint32(len(msg)))
	w.put(w.lenBuf[:])
	return w.addSegment(start, length, align), true
}

// addSegment records a segment of the footer, and returns its index.
func (w *Writer) addSegment(offset int64, length uint64, align int) uint32 {
	idx := uint32(len(w.segs) / 16)
	w.segs = binary.LittleEndian.AppendUint64(w.segs, uint64(offset))
	w.segs = binary.LittleEndian.AppendUint32(w.segs, uint32(length))
	w.segs = append(w.segs, byte(mbits.TrailingZeros(uint(align))), 0, 0, 0)
	return idx
}

// Close writes the footer. The writer is closed for writing, and w is left open. A file with
// no row has one empty chunk. A second Close returns nil; Close returns the error that
// closed the writer for writing, if any.
func (w *Writer) Close() error {
	if w.done {
		return nil
	}
	w.done = true
	if w.err != nil {
		return w.err
	}
	w.start()
	w.finishColumns()
	w.finishNode(w.root)
	w.drain()
	w.footer()
	w.flush()
	releaseCompressors(w.root)
	return w.err
}

// finishNode writes what is left of the columns: their last rows, their zones, the validity
// of the structs.
func (w *Writer) finishNode(l *layoutBuild) {
	if !l.isStruct {
		w.finish(l.col)
		return
	}
	if v := l.valid; v != nil {
		w.beginFlat()
		root := w.leaf("vortex.bool", v.bits, 1)
		if seg, ok := w.endFlat(root); ok {
			v.seg = seg
		}
	}
	for _, k := range l.kids {
		w.finishNode(k)
	}
}

// storageKind is the kind of the numbers a column of this type takes.
func storageKind(t Type) Kind {
	switch t.kind {
	case Float16:
		return Uint16
	case Date:
		if t.unit == Day {
			return Int32
		}
		return Int64
	case Time:
		if t.unit == Second || t.unit == Millisecond {
			return Int32
		}
		return Int64
	case Timestamp:
		return Int64
	default:
		return t.kind
	}
}

// footer writes the dtype, the layout, the footer, the postscript and the trailer.
func (w *Writer) footer() {
	dtype := w.segment(func() flatbuf.Off { return w.dtypeTable(w.schema.root) })
	layout := w.segment(func() flatbuf.Off { return w.layoutTable(w.root, w.rows) })
	stats := w.segment(w.fileStatsTable)
	foot := w.segment(w.footerTable)
	w.fb.Reset()
	seg := func(s [2]uint64) flatbuf.Off {
		w.fb.StartTable(3)
		w.fb.Uint64(0, s[0], 0)
		w.fb.Uint32(1, uint32(s[1]), 0)
		w.fb.Uint8(2, 3, 0) // alignment 8
		return w.fb.EndTable()
	}
	d, l, st, f := seg(dtype), seg(layout), seg(stats), seg(foot)
	w.fb.StartTable(5)
	w.fb.Ref(0, d)
	w.fb.Ref(1, l)
	w.fb.Ref(2, st)
	w.fb.Ref(3, f)
	ps := w.fb.Finish(w.fb.EndTable())
	if len(ps) > maxPostscript {
		w.setErr(fmt.Errorf("vortex: Close: a postscript of %d bytes, at most %d", len(ps), maxPostscript))
		return
	}
	w.put(ps)
	var trailer [8]byte
	binary.LittleEndian.PutUint16(trailer[0:], formatVersion)
	binary.LittleEndian.PutUint16(trailer[2:], uint16(len(ps)))
	copy(trailer[4:], magic)
	w.put(trailer[:])
}

// segment writes the flatbuffer that build makes, aligned to 8, and returns its offset and
// length.
func (w *Writer) segment(build func() flatbuf.Off) [2]uint64 {
	w.fb.Reset()
	msg := w.fb.Finish(build())
	w.pad(int(alignUp(w.pos, 8) - w.pos))
	at := w.pos
	w.put(msg)
	return [2]uint64{uint64(at), uint64(len(msg))}
}

// specIndex returns the index of id in ids, adding it when it is not there.
func specIndex(ids *[]string, id string) uint16 {
	for i, s := range *ids {
		if s == id {
			return uint16(i)
		}
	}
	*ids = append(*ids, id)
	return uint16(len(*ids) - 1)
}

func (w *Writer) footerTable() flatbuf.Off {
	b := w.fb
	spec := func(ids []string) flatbuf.Off {
		offs := make([]flatbuf.Off, len(ids))
		for i, id := range ids {
			s := b.String(id)
			b.StartTable(1)
			b.Ref(0, s)
			offs[i] = b.EndTable()
		}
		return b.Offsets(offs)
	}
	arrays, layouts := spec(w.arrayIDs), spec(w.layoutIDs)
	segs := b.Structs(16, 8, w.segs)
	b.StartTable(3)
	b.Ref(0, arrays)
	b.Ref(1, layouts)
	b.Ref(2, segs)
	return b.EndTable()
}

// layoutTable writes the layout of a column, or for a struct its validity and its fields.
func (w *Writer) layoutTable(l *layoutBuild, rows uint64) flatbuf.Off {
	if !l.isStruct {
		return w.columnLayout(l.col)
	}
	var kids []flatbuf.Off
	if l.valid != nil {
		kids = append(kids, w.layoutNode("vortex.flat", rows, nil, nil, []uint32{l.valid.seg}))
	}
	for _, k := range l.kids {
		kids = append(kids, w.layoutTable(k, rows))
	}
	return w.layoutNode("vortex.struct", rows, nil, kids, nil)
}

// layoutNode writes a node of the layout tree.
func (w *Writer) layoutNode(id string, rows uint64, meta []byte, kids []flatbuf.Off, segs []uint32) flatbuf.Off {
	b := w.fb
	var md, children, segments flatbuf.Off
	if len(meta) > 0 {
		md = b.Bytes(meta)
	}
	if len(kids) > 0 {
		children = b.Offsets(kids)
	}
	if len(segs) > 0 {
		segments = b.Uint32s(segs)
	}
	b.StartTable(5)
	b.Uint16(0, specIndex(&w.layoutIDs, id), 0)
	b.Uint64(1, rows, 0)
	if md != 0 {
		b.Ref(2, md)
	}
	if children != 0 {
		b.Ref(3, children)
	}
	if segments != 0 {
		b.Ref(4, segments)
	}
	return b.EndTable()
}

// filePTypes are the PType values of the kinds that are numbers.
var filePTypes = map[Kind]uint8{
	Int8: fbs.I8, Int16: fbs.I16, Int32: fbs.I32, Int64: fbs.I64,
	Uint8: fbs.U8, Uint16: fbs.U16, Uint32: fbs.U32, Uint64: fbs.U64,
	Float16: fbs.F16, Float32: fbs.F32, Float64: fbs.F64,
}

// simpleDTypes are the dtypes that have a nullability and nothing else.
var simpleDTypes = map[Kind]fbs.Kind{
	Bool: fbs.KindBool, Utf8: fbs.KindUtf8, Binary: fbs.KindBinary, Variant: fbs.KindVariant,
}

// dtypeTable writes the DType flatbuffer of t.
func (w *Writer) dtypeTable(t Type) flatbuf.Off {
	switch t.kind {
	case Date, Time, Timestamp, UUID:
		return w.dtypeExtension(t)
	case List, FixedSizeList, Map, Struct:
		return w.dtypeNested(t)
	}
	b := w.fb
	var kind fbs.Kind
	switch t.kind {
	case Null:
		kind = fbs.KindNull
		b.StartTable(0)
	case Bool, Utf8, Binary, Variant:
		kind = simpleDTypes[t.kind]
		b.StartTable(1)
		b.Uint8(0, boolByte(t.nullable), 0)
	case KindDecimal:
		kind = fbs.KindDecimal
		b.StartTable(3)
		b.Uint8(0, t.precision, 0)
		b.Uint8(1, uint8(t.scale), 0)
		b.Uint8(2, boolByte(t.nullable), 0)
	default:
		kind = fbs.KindPrimitive
		b.StartTable(2)
		b.Uint8(0, filePTypes[t.kind], 0)
		b.Uint8(1, boolByte(t.nullable), 0)
	}
	return w.wrapDType(kind, b.EndTable())
}

// dtypeNested writes the types made of other types.
func (w *Writer) dtypeNested(t Type) flatbuf.Off {
	b := w.fb
	var kind fbs.Kind
	var body flatbuf.Off
	switch t.kind {
	case List:
		kind = fbs.KindList
		elem := w.dtypeTable(*t.elem)
		b.StartTable(2)
		b.Ref(0, elem)
		b.Uint8(1, boolByte(t.nullable), 0)
		body = b.EndTable()
	case FixedSizeList:
		kind = fbs.KindFixedSizeList
		elem := w.dtypeTable(*t.elem)
		b.StartTable(3)
		b.Ref(0, elem)
		b.Uint32(1, t.size, 0)
		b.Uint8(2, boolByte(t.nullable), 0)
		body = b.EndTable()
	case Map:
		kind = fbs.KindMap
		key := w.dtypeTable(*t.elem)
		value := w.dtypeTable(*t.value)
		b.StartTable(4)
		b.Ref(0, key)
		b.Ref(1, value)
		b.Uint8(3, boolByte(t.nullable), 0)
		body = b.EndTable()
	default:
		return w.dtypeStruct(t)
	}
	return w.wrapDType(kind, body)
}

func (w *Writer) dtypeStruct(t Type) flatbuf.Off {
	b := w.fb
	names := make([]flatbuf.Off, len(t.fields))
	types := make([]flatbuf.Off, len(t.fields))
	for i, f := range t.fields {
		names[i] = b.String(f.Name)
		types[i] = w.dtypeTable(f.Type)
	}
	nv, tv := b.Offsets(names), b.Offsets(types)
	b.StartTable(3)
	b.Ref(0, nv)
	b.Ref(1, tv)
	b.Uint8(2, boolByte(t.nullable), 0)
	return w.wrapDType(fbs.KindStruct, b.EndTable())
}

func (w *Writer) wrapDType(kind fbs.Kind, body flatbuf.Off) flatbuf.Off {
	w.fb.StartTable(2)
	w.fb.Union(0, uint8(kind), body)
	return w.fb.EndTable()
}

func boolByte(v bool) uint8 {
	if v {
		return 1
	}
	return 0
}

// dtypeExtension writes a date, a time, a timestamp or a uuid: an extension of its storage.
func (w *Writer) dtypeExtension(t Type) flatbuf.Off {
	b := w.fb
	var id string
	var meta []byte
	storage := Type{kind: storageKind(t), nullable: t.nullable}
	switch t.kind {
	case Date:
		id, meta = extDate, []byte{byte(t.unit)}
	case Time:
		id, meta = extTime, []byte{byte(t.unit)}
	case Timestamp:
		id = extTimestamp
		meta = binary.LittleEndian.AppendUint16([]byte{byte(t.unit)}, uint16(len(t.zone)))
		meta = append(meta, t.zone...)
	default:
		id = extUUID
		u8 := Uint8Type()
		storage = Type{kind: FixedSizeList, elem: &u8, size: 16, nullable: t.nullable}
	}
	st := w.dtypeTable(storage)
	idOff := b.String(id)
	metaOff := b.Bytes(meta) // required, even when empty
	b.StartTable(3)
	b.Ref(0, idOff)
	b.Ref(1, st)
	b.Ref(2, metaOff)
	return w.wrapDType(fbs.KindExtension, b.EndTable())
}
