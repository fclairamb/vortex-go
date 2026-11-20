package vortex

import (
	"context"
	"fmt"
	"iter"
	"math"
	"os"
	"slices"

	"github.com/fclairamb/vortex-go/internal/array"
)

// maxBatchRows is the most rows of a batch.
const maxBatchRows = 8192

// Range is a half-open range of rows of a file: Start is the first row, End the one after
// the last. The zero value is the whole file, so an End of 0 means the end of the file. A
// range outside the file is clamped to it.
type Range struct{ Start, End int64 }

// Query selects the rows of a scan: the rows of Rows for which Filter is true. The zero
// value selects every row.
type Query struct {
	// Filter is exact: a row is returned when it is true, and the caller never has to filter
	// again. The statistics of the file are used to skip what it rules out.
	Filter Expr
	// Rows limits the scan to a range of rows.
	Rows Range
}

// scanOptions are the knobs of a scan that tests change.
type scanOptions struct {
	array     array.Options
	columns   []string
	q         Query
	batchRows int
}

// Scan reads the columns of the file that meet the query, batch by batch, in order, on the
// calling goroutine. A batch has at most 8,192 rows and is dense: its Len is the number of
// rows kept, and a batch without any is not returned. The batch has the columns that are
// named, in that order, and every column in the order of the schema when none is. A name is
// a top-level column; the fields of a struct come with Column.Field. A column that only the
// filter needs is read and not returned.
//
// An unknown column, an invalid range, or a filter that does not fit the schema is the error
// of the first iteration, before any read.
func (f *File) Scan(ctx context.Context, q Query, columns ...string) iter.Seq2[*Batch, error] {
	return f.scan(ctx, scanOptions{batchRows: maxBatchRows, q: q, columns: columns})
}

// source reads the top-level columns of the file, the ones that are wanted, in lockstep.
type source struct {
	valid  reader // the validity of a nullable root struct, which is not returned
	whole  reader // the root layout when it is not a struct layout: all its columns are read
	cur    *array.Array
	fields []reader
	n      int
}

func (s *source) avail() int {
	n := math.MaxInt
	if s.whole != nil {
		n = s.whole.avail()
	}
	if s.valid != nil {
		n = min(n, s.valid.avail())
	}
	for _, r := range s.fields {
		if r != nil {
			n = min(n, r.avail())
		}
	}
	return n
}

// begin starts a batch of n rows.
func (s *source) begin(n int) error {
	s.n, s.cur = n, nil
	if s.valid != nil {
		if _, err := s.valid.take(n); err != nil {
			return err
		}
	}
	if s.whole != nil {
		var err error
		s.cur, err = s.whole.take(n)
		return err
	}
	return nil
}

// read gets the columns idx of the batch into arrs.
func (s *source) read(idx []int, arrs []*array.Array) error {
	for _, i := range idx {
		if s.cur != nil {
			arrs[i] = s.cur.Fields[i]
			continue
		}
		a, err := s.fields[i].take(s.n)
		if err != nil {
			return err
		}
		arrs[i] = a
	}
	return nil
}

// skipFields drops the columns idx of the batch.
func (s *source) skipFields(idx []int) {
	if s.cur != nil {
		return
	}
	for _, i := range idx {
		s.fields[i].skip(s.n)
	}
}

// units lists the flats of the readers, the first row being base.
func (s *source) units(base int, out *[]*unit) {
	if s.whole != nil {
		s.whole.units(base, out)
	}
	if s.valid != nil {
		s.valid.units(base, out)
	}
	for _, r := range s.fields {
		if r != nil {
			r.units(base, out)
		}
	}
}

// skip drops n rows of every column, whatever they are.
func (s *source) skip(n int) {
	if s.whole != nil {
		s.whole.skip(n)
	}
	if s.valid != nil {
		s.valid.skip(n)
	}
	for _, r := range s.fields {
		if r != nil {
			r.skip(n)
		}
	}
}

// scanPlan is a checked query: what to read, and what to return.
type scanPlan struct {
	out        []int // the top-level field of each returned column
	early      []int // the fields the filter reads
	late       []int // the fields only returned
	filter     *node
	src        source
	lo, hi     int64
	wantFilter bool
}

// projection lists the top-level fields a scan returns, in order.
func (f *File) projection(columns []string) ([]int, error) {
	fields := f.schema.root.fields
	var out []int
	for _, name := range columns {
		i := slices.IndexFunc(fields, func(fl Field) bool { return fl.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("vortex: Scan: unknown column %q", name)
		}
		out = append(out, i)
	}
	if len(columns) == 0 {
		for i := range fields {
			out = append(out, i)
		}
	}
	return out, nil
}

// clamp is the rows of the file that a range selects, from lo to hi (excluded).
func clamp(r Range, rows int64) (lo, hi int64, err error) {
	if r.Start < 0 || r.End < 0 || r.End != 0 && r.Start > r.End {
		return 0, 0, fmt.Errorf("vortex: Scan: invalid row range [%d, %d): want 0 <= Start <= End", r.Start, r.End)
	}
	hi = rows
	if r.End != 0 {
		hi = min(r.End, rows)
	}
	return min(r.Start, hi), hi, nil
}

// plan checks the query against the schema and opens the readers it needs.
func (f *File) plan(o scanOptions) (*scanPlan, error) {
	var err error
	p := &scanPlan{wantFilter: o.q.Filter.n != nil}
	if p.out, err = f.projection(o.columns); err != nil {
		return nil, err
	}
	if p.lo, p.hi, err = clamp(o.q.Rows, int64(f.layout.rowCount)); err != nil {
		return nil, err
	}
	need := make([]bool, len(f.schema.root.fields))
	if p.filter, err = o.q.Filter.compile(f.schema.root, need); err != nil {
		return nil, err
	}
	// The fields the filter reads are read first, the others once the rows are chosen.
	filterNeeds := slices.Clone(need)
	for _, i := range p.out {
		need[i] = true
	}
	for i, n := range need {
		switch {
		case filterNeeds[i]:
			p.early = append(p.early, i)
		case n:
			p.late = append(p.late, i)
		}
	}
	o.array.MaxRows = f.maxNodeRows()
	if err = f.openSource(p, o.array, need); err != nil {
		return nil, err
	}
	if p.wantFilter {
		p.filter.loadZones(f, f.layout, [2]int64{p.lo, p.hi})
	}
	return p, nil
}

// maxNodeRows is the most rows of a node of an array of the file: a multiple of its size and
// of its rows. A list of constants may expand, not without bound.
func (f *File) maxNodeRows() int {
	const factor = 64
	if n := uint64(f.size) + uint64(f.numRows); n <= uint64(math.MaxInt-nodeRowsBase)/factor {
		return nodeRowsBase + factor*int(n)
	}
	return math.MaxInt
}

// nodeRowsBase is the rows any node may have, whatever the file. A variable for the tests.
var nodeRowsBase = 1 << 20

// openSource makes the readers of the fields of need.
func (f *File) openSource(p *scanPlan, opt array.Options, need []bool) (err error) {
	t, lo, hi := arrayType(f.schema.root), int(p.lo), int(p.hi)
	if f.layoutEncodings[f.layout.encoding] == layoutStruct {
		p.src.valid, p.src.fields, err = f.structParts(f.layout, t, opt, "", lo, hi, need)
	} else {
		p.src.whole, err = f.newReader(f.layout, t, opt, "", lo, hi)
	}
	return err
}

func (f *File) scan(ctx context.Context, o scanOptions) iter.Seq2[*Batch, error] {
	return func(yield func(*Batch, error) bool) {
		p, err := f.plan(o)
		if err != nil {
			yield(nil, err)
			return
		}
		ld := newLoader(ctx, f, p, workers())
		defer ld.close()
		s := newScanner(f, p, o.batchRows)
		for pos := p.lo; pos < p.hi; {
			ld.recycle() // the consumer is done with the previous batch
			if err = s.check(ctx); err == nil {
				var b *Batch
				var n int
				b, n, err = s.step(pos)
				pos += int64(n)
				if err == nil && b != nil && !yield(b, nil) {
					return
				}
			}
			if err != nil {
				yield(nil, err)
				return
			}
		}
	}
}

// scanner reads the batches of a plan. It owns the batch it returns, which it reuses.
type scanner struct {
	f         *File
	p         *scanPlan
	arrs      []*array.Array
	sel       []int
	store     []Column
	picked    []picked
	batch     Batch
	batchRows int
}

func newScanner(f *File, p *scanPlan, batchRows int) *scanner {
	s := &scanner{f: f, p: p, batchRows: batchRows, arrs: make([]*array.Array, len(f.schema.root.fields)),
		sel: make([]int, 0, batchRows), store: make([]Column, len(p.out)), picked: make([]picked, len(p.out))}
	s.batch.cols = make([]*Column, len(p.out))
	for i := range s.store {
		s.batch.cols[i] = &s.store[i]
	}
	return s
}

// check is the error that stops a scan before its next batch: the context's, or a closed file.
func (s *scanner) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.f.closed.Load() {
		return fmt.Errorf("vortex: Scan: %w", os.ErrClosed)
	}
	return nil
}

// step reads the rows from pos, up to the next batch: it returns the number of rows it went
// through, and the batch they make, which is nil when none of them is kept.
func (s *scanner) step(pos int64) (*Batch, int, error) {
	p := s.p
	n := int(min(int64(s.batchRows), p.hi-pos, int64(p.src.avail())))
	if p.wantFilter {
		b, limit := p.filter.bounds(pos)
		n = int(min(int64(n), limit-pos))
		if !b.canTrue { // no row of these zones can match
			p.src.skip(n)
			return nil, n, nil
		}
	}
	if err := p.src.begin(n); err != nil {
		return nil, n, err
	}
	if err := p.src.read(p.early, s.arrs); err != nil {
		return nil, n, err
	}
	s.sel = s.sel[:0]
	if p.wantFilter {
		p.filter.bind(s.arrs)
		for i := range n {
			if p.filter.eval(i) == triTrue {
				s.sel = append(s.sel, i)
			}
		}
		if len(s.sel) == 0 {
			p.src.skipFields(p.late)
			return nil, n, nil
		}
	}
	if err := p.src.read(p.late, s.arrs); err != nil {
		return nil, n, err
	}
	return s.fill(n, p.wantFilter && len(s.sel) < n), n, nil
}

// fill makes the batch of the returned columns of the arrays read, keeping the rows sel of
// them when pick is set.
func (s *scanner) fill(n int, pick bool) *Batch {
	b := &s.batch
	b.n = n
	if pick {
		b.n = len(s.sel)
	}
	for i, k := range s.p.out {
		a := s.arrs[k]
		if pick {
			a = s.picked[i].gather(a, s.sel)
		}
		s.store[i] = Column{arr: a, typ: s.f.schema.root.fields[k].Type}
	}
	return b
}

// picked is the rows a filter keeps of an array, in buffers that the next batch reuses.
type picked struct {
	kids           []picked // the fields of a struct, the elements of a fixed-size list
	fields         []*array.Array
	vbytes, bbytes []byte
	data           []byte
	starts, ends   []int
	elems          []int // the elements of the rows kept of a fixed-size list
	valid          array.Bitmap
	arr            array.Array
	// runs, parts and vdata are a Variant: a part per run of consecutive rows kept.
	runs  []array.Array
	parts []*array.Array
	vdata array.VariantData
}

// gather returns the rows sel (ascending) of a. The bytes and the elements that rows point to
// are shared. It allocates nothing once its buffers are large enough.
func (p *picked) gather(a *array.Array, sel []int) *array.Array {
	out := &p.arr
	*out = *a
	out.Len = len(sel)
	if a.Skipped != nil {
		return out
	}
	if a.Validity != nil {
		p.vbytes = pickBits(p.vbytes, *a.Validity, sel)
		p.valid = array.Bitmap{Bytes: p.vbytes}
		out.Validity = &p.valid
	}
	switch a.Type.Kind {
	case array.Bool:
		p.bbytes = pickBits(p.bbytes, a.Bits, sel)
		out.Bits = array.Bitmap{Bytes: p.bbytes}
	case array.Prim:
		p.data = pickBytes(p.data, a.Data, a.Type.PType.Width(), sel)
		out.Data = p.data
	case array.Decimal:
		p.data = pickBytes(p.data, a.Data, a.Width, sel)
		out.Data = p.data
	case array.Utf8, array.Binary, array.List, array.Map:
		p.starts, p.ends = p.starts[:0], p.ends[:0]
		for _, r := range sel {
			p.starts, p.ends = append(p.starts, a.Starts[r]), append(p.ends, a.Ends[r])
		}
		out.Starts, out.Ends = p.starts, p.ends
	case array.FixedSizeList, array.Struct:
		p.gatherNested(a, sel)
	case array.Variant:
		p.gatherVariant(a, sel)
	}
	return out
}

// gatherVariant makes p.arr the rows sel of a Variant array: a chunked one whose parts are
// the runs of consecutive rows of sel, sliced from a into buffers p keeps.
func (p *picked) gatherVariant(a *array.Array, sel []int) {
	n := 0
	for i := range sel {
		if i == 0 || sel[i] != sel[i-1]+1 {
			n++
		}
	}
	if cap(p.runs) < n {
		p.runs = make([]array.Array, n)
	}
	p.runs, p.parts = p.runs[:n], p.parts[:0]
	for i, k := 0, 0; i < len(sel); k++ {
		j := i + 1
		for j < len(sel) && sel[j] == sel[j-1]+1 {
			j++
		}
		p.parts = append(p.parts, a.SliceInto(&p.runs[k], sel[i], j-i))
		i = j
	}
	p.vdata = array.VariantData{Parts: p.parts}
	p.arr.Var, p.arr.VOff = &p.vdata, 0
}

// gatherNested gathers the children of a fixed-size list or of a struct into p.arr.
func (p *picked) gatherNested(a *array.Array, sel []int) {
	if a.Type.Kind == array.FixedSizeList {
		size := a.Type.Size
		p.elems = p.elems[:0]
		for _, r := range sel {
			for j := range size {
				p.elems = append(p.elems, r*size+j)
			}
		}
		p.grow(1)
		p.arr.Elems = p.kids[0].gather(a.Elems, p.elems)
		return
	}
	p.grow(len(a.Fields))
	p.fields = append(p.fields[:0], a.Fields...)
	for i, fld := range a.Fields {
		p.fields[i] = p.kids[i].gather(fld, sel)
	}
	p.arr.Fields = p.fields
}

// grow makes sure p has n children.
func (p *picked) grow(n int) {
	if len(p.kids) < n {
		p.kids = make([]picked, n)
	}
}

// pickBits returns the bits sel of b, in buf.
func pickBits(buf []byte, b array.Bitmap, sel []int) []byte {
	n := (len(sel) + 7) / 8
	buf = slices.Grow(buf[:0], n)[:n]
	clear(buf)
	for i, r := range sel {
		if b.Get(r) {
			buf[i/8] |= 1 << (i % 8)
		}
	}
	return buf
}

// pickBytes returns the values sel of an array of values of w bytes, in buf.
func pickBytes(buf, data []byte, w int, sel []int) []byte {
	buf = buf[:0]
	for _, r := range sel {
		buf = append(buf, data[r*w:(r+1)*w]...)
	}
	return buf
}

var primOfKind = map[Kind]array.PType{
	Int8: array.I8, Int16: array.I16, Int32: array.I32, Int64: array.I64,
	Uint8: array.U8, Uint16: array.U16, Uint32: array.U32, Uint64: array.U64,
	Float16: array.F16, Float32: array.F32, Float64: array.F64,
}

// arrayType is the type the decoders produce for t.
func arrayType(t Type) array.DType {
	d := array.DType{Nullable: t.nullable}
	switch t.kind {
	case Null:
		d.Kind = array.Null
	case Bool:
		d.Kind = array.Bool
	case Struct:
		d.Kind = array.Struct
		for _, f := range t.fields {
			d.Fields = append(d.Fields, array.Field{Name: f.Name, Type: arrayType(f.Type)})
		}
	case Utf8:
		d.Kind = array.Utf8
	case Binary:
		d.Kind = array.Binary
	case KindDecimal:
		d.Kind = array.Decimal
	case List:
		d.Kind, d.Elem = array.List, elemOf(t.elem)
	case FixedSizeList:
		d.Kind, d.Elem, d.Size = array.FixedSizeList, elemOf(t.elem), int(t.size)
	case Map:
		d.Kind = array.Map
		d.Elem = &array.DType{Kind: array.Struct, Fields: []array.Field{
			{Name: "key", Type: arrayType(*t.elem)}, {Name: "value", Type: arrayType(*t.value)},
		}}
	case Variant:
		d.Kind = array.Variant
	case UUID:
		d.Kind = array.Ext
		d.Storage = &array.DType{Kind: array.FixedSizeList, Size: uuidSize, Nullable: t.nullable,
			Elem: &array.DType{Kind: array.Prim, PType: array.U8}}
	case Date, Time, Timestamp:
		d.Kind = array.Ext
		d.Storage = &array.DType{Kind: array.Prim, PType: storage(t), Nullable: t.nullable}
		if t.kind == Timestamp {
			d.Storage.Ticks = ticksPerSecond[t.unit]
		}
	default:
		d.Kind, d.PType = array.Prim, primOfKind[t.kind]
	}
	return d
}

// ticksPerSecond is the ticks of a second, by time unit.
var ticksPerSecond = map[Unit]int32{Nanosecond: 1e9, Microsecond: 1e6, Millisecond: 1e3, Second: 1}

// storage is the primitive type that holds a date, a time or a timestamp.
func storage(t Type) array.PType {
	switch {
	case t.kind == Date && t.unit == Day, t.kind == Time && t.unit >= Millisecond:
		return array.I32
	}
	return array.I64
}

func elemOf(t *Type) *array.DType {
	d := arrayType(*t)
	return &d
}
