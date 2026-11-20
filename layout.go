package vortex

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/edition"
)

// reader yields the rows of a layout, in order.
type reader interface {
	// avail is the number of rows take can return now, without crossing the boundary of a
	// chunk or of a segment. It is 0 once every row has been read.
	avail() int
	// take returns the next n rows, n being at most avail().
	take(n int) (*array.Array, error)
	// skip drops the next n rows, n being at most avail(), without reading what only they need.
	skip(n int)
}

// newReader makes the reader of the rows lo to hi (excluded) of a layout whose rows have
// type t. Only what these rows need is read: a chunk outside the range is never touched.
func (f *File) newReader(l *layoutNode, t array.DType, opt array.Options, path string, lo, hi int) (reader, error) {
	if lo < 0 || hi < lo || uint64(hi) > l.rowCount {
		return nil, badLayout("rows %d to %d of a layout of %d rows", lo, hi, l.rowCount)
	}
	id := f.layoutEncodings[l.encoding]
	edition.Met(edition.KindLayout, id)
	mk, ok := f.readers[id]
	if !ok {
		return nil, fmt.Errorf("vortex: unsupported layout %q at column %q: %w", id, path, errors.ErrUnsupported)
	}
	return mk(f, l, t, opt, path, lo, hi)
}

// layoutMaker builds the reader of one kind of layout.
type layoutMaker func(f *File, l *layoutNode, t array.DType, opt array.Options, path string, lo, hi int) (reader, error)

// layoutReaders is the table of the layout readers, by identifier. A function, so that the
// readers can call newReader without an initialization cycle.
var layoutReaders = sync.OnceValue(func() map[string]layoutMaker {
	zoned := func(legacy bool) layoutMaker {
		return func(f *File, l *layoutNode, t array.DType, opt array.Options, path string, lo, hi int) (reader, error) {
			return f.newZoned(l, legacy, t, opt, path, lo, hi)
		}
	}
	return map[string]layoutMaker{
		layoutFlat: func(f *File, l *layoutNode, t array.DType, opt array.Options, _ string, lo, hi int) (reader, error) {
			return f.newFlat(l, t, opt, lo, hi)
		},
		layoutStruct:  (*File).newStruct,
		layoutChunked: (*File).newChunked,
		layoutZoned:   zoned(false),
		layoutStats:   zoned(true),
		layoutDict:    (*File).newDict,
	}
})

// The identifiers of the layouts the scan looks at.
const (
	layoutFlat    = "vortex.flat"
	layoutStruct  = "vortex.struct"
	layoutChunked = "vortex.chunked"
	layoutZoned   = "vortex.zoned"
	layoutStats   = "vortex.stats"
	layoutDict    = "vortex.dict"
)

// readerLayoutIDs lists the layouts that have a reader.
func readerLayoutIDs() []string {
	return slices.Sorted(maps.Keys(layoutReaders()))
}

var _ = edition.Register(edition.KindLayout, readerLayoutIDs)

// flat is vortex.flat: one segment, no child. Its metadata is ignored.
type flat struct {
	f    *File
	arr  *array.Array
	opt  array.Options
	t    array.DType
	seg  uint32
	rows int // the rows of the segment
	pos  int
	end  int
}

func (f *File) newFlat(l *layoutNode, t array.DType, opt array.Options, lo, hi int) (reader, error) {
	if len(l.segments) != 1 || len(l.children) != 0 {
		return nil, badLayout("a flat layout has %d segments and %d children, want 1 and 0", len(l.segments), len(l.children))
	}
	return &flat{f: f, t: t, opt: opt, seg: l.segments[0], rows: int(l.rowCount), pos: lo, end: hi}, nil
}

func (r *flat) avail() int { return r.end - r.pos }

func (r *flat) skip(n int) { r.pos += n }

func (r *flat) take(n int) (*array.Array, error) {
	if r.arr == nil {
		spec := r.f.segments[r.seg]
		buf := make([]byte, spec.Length)
		if err := readFull(r.f.r, buf, int64(spec.Offset)); err != nil {
			return nil, err
		}
		a, err := array.Decode(buf, r.f.arrayEncodings, r.t, r.rows, r.opt)
		if err != nil {
			if errors.Is(err, errors.ErrUnsupported) {
				return nil, fmt.Errorf("vortex: segment at offset %d: %w", spec.Offset, err)
			}
			return nil, wrapCorrupt("array", spec.Offset, err)
		}
		r.arr = a
	}
	a := r.arr.Slice(r.pos, n)
	r.pos += n
	return a, nil
}

// structLayout is vortex.struct: the validity when the struct is nullable, then one child
// per field.
type structLayout struct {
	valid  reader
	fields []reader
	t      array.DType
	left   int
}

func (f *File) newStruct(l *layoutNode, t array.DType, opt array.Options, path string, lo, hi int) (reader, error) {
	valid, fields, err := f.structParts(l, t, opt, path, lo, hi, nil)
	if err != nil {
		return nil, err
	}
	return &structLayout{t: t, left: hi - lo, valid: valid, fields: fields}, nil
}

// structParts makes the readers of the validity (nil when the struct is not nullable) and of
// the fields of a struct layout. A field that want rejects (nil want accepts all) has no
// reader: its entry is nil.
func (f *File) structParts(
	l *layoutNode, t array.DType, opt array.Options, path string, lo, hi int, want []bool,
) (reader, []reader, error) {
	if t.Kind != array.Struct {
		return nil, nil, badLayout("a struct layout for the type %s", t)
	}
	first := 0
	if t.Nullable {
		first = 1
	}
	if len(l.children) != first+len(t.Fields) {
		return nil, nil, badLayout("a struct layout of %d children for %d fields", len(l.children), len(t.Fields))
	}
	for i, c := range l.children {
		if c.rowCount != l.rowCount {
			return nil, nil, badLayout("struct child %d has %d rows, its parent %d", i, c.rowCount, l.rowCount)
		}
	}
	var valid reader
	var err error
	if first == 1 {
		if valid, err = f.newReader(l.children[0], array.DType{Kind: array.Bool}, opt, "", lo, hi); err != nil {
			return nil, nil, err
		}
	}
	fields := make([]reader, len(t.Fields))
	for i, ft := range t.Fields {
		if want != nil && !want[i] {
			continue
		}
		fp := ft.Name
		if path != "" {
			fp = path + "." + ft.Name
		}
		r, err := f.newReader(l.children[first+i], ft.Type, opt, fp, lo, hi)
		if err != nil {
			return nil, nil, err
		}
		if opt.Tolerate != nil {
			r = &tolerant{reader: r, path: fp, t: ft.Type, tolerate: opt.Tolerate, left: hi - lo}
		}
		fields[i] = r
	}
	return valid, fields, nil
}

func (s *structLayout) avail() int {
	n := s.left
	if s.valid != nil {
		n = min(n, s.valid.avail())
	}
	for _, r := range s.fields {
		n = min(n, r.avail())
	}
	return n
}

func (s *structLayout) skip(n int) {
	if s.valid != nil {
		s.valid.skip(n)
	}
	for _, r := range s.fields {
		r.skip(n)
	}
	s.left -= n
}

func (s *structLayout) take(n int) (*array.Array, error) {
	a := &array.Array{Type: s.t, Len: n, Fields: make([]*array.Array, len(s.fields))}
	if s.valid != nil {
		v, err := s.valid.take(n)
		if err != nil {
			return nil, err
		}
		a.Validity = &v.Bits
	}
	for i, r := range s.fields {
		f, err := r.take(n)
		if err != nil {
			return nil, err
		}
		a.Fields[i] = f
	}
	s.left -= n
	return a, nil
}

// chunked is vortex.chunked: row chunks of the same type. Only the chunks that overlap the
// range have a reader.
type chunked struct {
	chunks []reader
	i      int
}

func (f *File) newChunked(l *layoutNode, t array.DType, opt array.Options, path string, lo, hi int) (reader, error) {
	var total uint64
	c := &chunked{}
	for _, k := range l.children {
		start := total
		if total += k.rowCount; total < k.rowCount {
			return nil, badLayout("chunked row counts overflow")
		}
		from, to := max(uint64(lo), start), min(uint64(hi), total)
		if from >= to {
			continue
		}
		r, err := f.newReader(k, t, opt, path, int(from-start), int(to-start))
		if err != nil {
			return nil, err
		}
		c.chunks = append(c.chunks, r)
	}
	if total != l.rowCount {
		return nil, badLayout("chunked children have %d rows, their parent %d", total, l.rowCount)
	}
	return c, nil
}

func (c *chunked) avail() int {
	for ; c.i < len(c.chunks); c.i++ {
		if n := c.chunks[c.i].avail(); n > 0 {
			return n
		}
	}
	return 0
}

func (c *chunked) skip(n int) { c.chunks[c.i].skip(n) }

func (c *chunked) take(n int) (*array.Array, error) { return c.chunks[c.i].take(n) }

// tolerant is a field of a struct layout that Options.Tolerate may skip: when its rows fail
// with an unsupported encoding and the option accepts it, they are an array with Skipped set.
// Tests use it, a scan never does.
type tolerant struct {
	reader
	skipped  error
	tolerate func(path string, err error) bool
	path     string
	t        array.DType
	left     int
}

func (r *tolerant) avail() int {
	if r.skipped != nil {
		return r.left
	}
	return r.reader.avail()
}

func (r *tolerant) skip(n int) {
	if r.skipped == nil {
		r.reader.skip(n)
	}
	r.left -= n
}

func (r *tolerant) take(n int) (*array.Array, error) {
	if r.skipped == nil {
		a, err := r.reader.take(n)
		if err == nil || !errors.Is(err, errors.ErrUnsupported) || !r.tolerate(r.path, err) {
			r.left -= n
			return a, err
		}
		r.skipped = err
	}
	r.left -= n
	return &array.Array{Type: r.t, Len: n, Skipped: r.skipped}, nil
}

// badLayout reports a layout tree that does not fit the schema or itself.
func badLayout(format string, args ...any) error {
	return fmt.Errorf("%w: layout: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}
