package vortex

import (
	"errors"
	"fmt"
	"math"

	"github.com/fclairamb/vortex-go/internal/array"
)

// reader yields the rows of a layout, in order.
type reader interface {
	// avail is the number of rows take can return now, without crossing the boundary of a
	// chunk or of a segment. It is 0 once every row has been read.
	avail() int
	// take returns the next n rows, n being at most avail().
	take(n int) (*array.Array, error)
}

// newReader makes the reader of a layout whose rows have type t.
func (f *File) newReader(l *layoutNode, t array.DType, opt array.Options) (reader, error) {
	switch id := f.layoutEncodings[l.encoding]; id {
	case "vortex.flat":
		return f.newFlat(l, t, opt)
	case "vortex.struct":
		return f.newStruct(l, t, opt)
	case "vortex.chunked":
		return f.newChunked(l, t, opt)
	default:
		return nil, fmt.Errorf("vortex: unsupported layout %q: %w", id, errors.ErrUnsupported)
	}
}

// flat is vortex.flat: one segment, no child. Its metadata is ignored.
type flat struct {
	f    *File
	arr  *array.Array
	opt  array.Options
	t    array.DType
	seg  uint32
	rows int
	pos  int
}

func (f *File) newFlat(l *layoutNode, t array.DType, opt array.Options) (reader, error) {
	if len(l.segments) != 1 || len(l.children) != 0 {
		return nil, badLayout("a flat layout has %d segments and %d children, want 1 and 0", len(l.segments), len(l.children))
	}
	return &flat{f: f, t: t, opt: opt, seg: l.segments[0], rows: int(l.rowCount)}, nil
}

func (r *flat) avail() int { return r.rows - r.pos }

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

func (f *File) newStruct(l *layoutNode, t array.DType, opt array.Options) (reader, error) {
	if t.Kind != array.Struct {
		return nil, badLayout("a struct layout for the type %s", t)
	}
	first := 0
	if t.Nullable {
		first = 1
	}
	if len(l.children) != first+len(t.Fields) {
		return nil, badLayout("a struct layout of %d children for %d fields", len(l.children), len(t.Fields))
	}
	for i, c := range l.children {
		if c.rowCount != l.rowCount {
			return nil, badLayout("struct child %d has %d rows, its parent %d", i, c.rowCount, l.rowCount)
		}
	}
	s := &structLayout{t: t, left: int(l.rowCount)}
	var err error
	if first == 1 {
		if s.valid, err = f.newReader(l.children[0], array.DType{Kind: array.Bool}, opt); err != nil {
			return nil, err
		}
	}
	for i, ft := range t.Fields {
		r, err := f.newReader(l.children[first+i], ft, opt)
		if err != nil {
			return nil, err
		}
		s.fields = append(s.fields, r)
	}
	return s, nil
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

// chunked is vortex.chunked: row chunks of the same type.
type chunked struct {
	chunks []reader
	i      int
}

func (f *File) newChunked(l *layoutNode, t array.DType, opt array.Options) (reader, error) {
	var total uint64
	c := &chunked{}
	for _, k := range l.children {
		if total += k.rowCount; total < k.rowCount || total > math.MaxInt64 {
			return nil, badLayout("chunked row counts overflow")
		}
		r, err := f.newReader(k, t, opt)
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

func (c *chunked) take(n int) (*array.Array, error) { return c.chunks[c.i].take(n) }

// badLayout reports a layout tree that does not fit the schema or itself.
func badLayout(format string, args ...any) error {
	return fmt.Errorf("%w: layout: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}
