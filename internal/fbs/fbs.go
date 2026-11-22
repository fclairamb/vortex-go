package fbs

import (
	"encoding/binary"

	"github.com/fclairamb/vortex-go/internal/flatbuf"
)

// shallow bounds the tables nested in the flat schemas (postscript, footer, statistics).
const shallow = 4

// List is a vector of tables, structs or strings, decoded element by element. It holds the
// vector and a function with no state for its elements, so that it is a value: reading the
// children of a node allocates nothing.
type List[T any] struct {
	table func(flatbuf.Table) T // a vector of tables
	strct func([]byte) T        // a vector of structs
	str   func(flatbuf.Vector, int) (T, error)
	v     flatbuf.Vector
}

// Len is the number of elements.
func (l List[T]) Len() int { return l.v.Len() }

// At returns the i-th element, or an error when i is out of range or the element is bad.
func (l List[T]) At(i int) (T, error) {
	switch {
	case l.table != nil:
		sub, err := l.v.Table(i)
		return l.table(sub), err
	case l.strct != nil:
		b, err := l.v.Struct(i)
		if err != nil {
			var zero T
			return zero, err
		}
		return l.strct(b), nil
	}
	return l.str(l.v, i)
}

func tables[T any](t flatbuf.Table, id int, wrap func(flatbuf.Table) T) (List[T], error) {
	v, err := t.Tables(id)
	return List[T]{table: wrap, v: v}, err
}

func structs[T any](t flatbuf.Table, id, size int, decode func([]byte) T) (List[T], error) {
	v, err := t.Structs(id, size)
	return List[T]{strct: decode, v: v}, err
}

func optBool(t flatbuf.Table, id int) (bool, bool, error) {
	v, err := t.Bool(id, false)
	return v, t.Has(id), err
}

func optUint64(t flatbuf.Table, id int) (uint64, bool, error) {
	v, err := t.Uint64(id, 0)
	return v, t.Has(id), err
}

// ----- footer.fbs -----

// Postscript is the table at the end of the file, before the 8-byte trailer.
type Postscript struct{ t flatbuf.Table }

// ReadPostscript reads the root Postscript of buf.
func ReadPostscript(buf []byte) (Postscript, error) {
	t, err := flatbuf.Root(buf, shallow)
	return Postscript{t}, err
}

func segment(t flatbuf.Table, id int) (PostscriptSegment, bool, error) {
	s, ok, err := t.Table(id)
	return PostscriptSegment{s}, ok, err
}

// DType is the segment of the root DType flatbuffer (field 0).
func (p Postscript) DType() (PostscriptSegment, bool, error) { return segment(p.t, 0) }

// Layout is the segment of the root Layout flatbuffer (field 1, required).
func (p Postscript) Layout() (PostscriptSegment, bool, error) { return segment(p.t, 1) }

// Statistics is the segment of the FileStatistics flatbuffer (field 2).
func (p Postscript) Statistics() (PostscriptSegment, bool, error) { return segment(p.t, 2) }

// Footer is the segment of the Footer flatbuffer (field 3, required).
func (p Postscript) Footer() (PostscriptSegment, bool, error) { return segment(p.t, 3) }

// Metadata lists the user-defined metadata segments (field 4).
func (p Postscript) Metadata() (List[PostscriptMetadata], error) {
	return tables(p.t, 4, func(t flatbuf.Table) PostscriptMetadata { return PostscriptMetadata{t} })
}

// PostscriptMetadata is a keyed user-defined metadata segment.
type PostscriptMetadata struct{ t flatbuf.Table }

// Key is field 0 (required).
func (m PostscriptMetadata) Key() (string, error) {
	s, _, err := m.t.String(0)
	return s, err
}

// Segment is field 1 (required).
func (m PostscriptMetadata) Segment() (PostscriptSegment, bool, error) { return segment(m.t, 1) }

// PostscriptSegment locates a segment, with its compression and encryption inline.
type PostscriptSegment struct{ t flatbuf.Table }

// Offset is field 0, from the start of the file.
func (s PostscriptSegment) Offset() (uint64, error) { return s.t.Uint64(0, 0) }

// Length is field 1, in bytes.
func (s PostscriptSegment) Length() (uint32, error) { return s.t.Uint32(1, 0) }

// AlignmentExponent is field 2, the base-2 exponent of the alignment.
func (s PostscriptSegment) AlignmentExponent() (uint8, error) { return s.t.Uint8(2, 0) }

// Compression is the scheme of the CompressionSpec in field 3: 0 None, 1 LZ4, 2 ZLib, 3 ZStd.
// It is 0 when the field is absent.
func (s PostscriptSegment) Compression() (uint8, error) {
	c, ok, err := s.t.Table(3)
	if err != nil || !ok {
		return 0, err
	}
	return c.Uint8(0, 0)
}

// Encrypted reports whether the EncryptionSpec of field 4 is present.
func (s PostscriptSegment) Encrypted() bool { return s.t.Has(4) }

// Footer is the dictionary of the specs the layout and the arrays refer to by index.
type Footer struct{ t flatbuf.Table }

// ReadFooter reads the root Footer of buf.
func ReadFooter(buf []byte) (Footer, error) {
	t, err := flatbuf.Root(buf, shallow)
	return Footer{t}, err
}

// ArraySpec names an array encoding.
type ArraySpec struct{ t flatbuf.Table }

// ID is field 0 (required).
func (a ArraySpec) ID() (string, error) { s, _, err := a.t.String(0); return s, err }

// LayoutSpec names a layout encoding.
type LayoutSpec struct{ t flatbuf.Table }

// ID is field 0 (required).
func (l LayoutSpec) ID() (string, error) { s, _, err := l.t.String(0); return s, err }

// ArraySpecs is field 0.
func (f Footer) ArraySpecs() (List[ArraySpec], error) {
	return tables(f.t, 0, func(t flatbuf.Table) ArraySpec { return ArraySpec{t} })
}

// LayoutSpecs is field 1.
func (f Footer) LayoutSpecs() (List[LayoutSpec], error) {
	return tables(f.t, 1, func(t flatbuf.Table) LayoutSpec { return LayoutSpec{t} })
}

// SegmentSpec locates a buffer within the file. It is a 16-byte struct.
type SegmentSpec struct {
	Offset            uint64 // from the start of the file
	Length            uint32
	AlignmentExponent uint8
	Compression       uint8 // reserved
	Encryption        uint16
}

const segmentSpecSize = 16

// SegmentSpecs is field 2, a vector of structs.
func (f Footer) SegmentSpecs() (List[SegmentSpec], error) {
	return structs(f.t, 2, segmentSpecSize, func(b []byte) SegmentSpec {
		return SegmentSpec{
			Offset:            binary.LittleEndian.Uint64(b),
			Length:            binary.LittleEndian.Uint32(b[8:]),
			AlignmentExponent: b[12],
			Compression:       b[13],
			Encryption:        binary.LittleEndian.Uint16(b[14:]),
		}
	})
}

// FileStatistics holds the file-level statistics.
type FileStatistics struct{ t flatbuf.Table }

// ReadFileStatistics reads the root FileStatistics of buf.
func ReadFileStatistics(buf []byte) (FileStatistics, error) {
	t, err := flatbuf.Root(buf, shallow)
	return FileStatistics{t}, err
}

// FieldStats is field 0, one ArrayStats per top-level field (the legacy shape).
func (s FileStatistics) FieldStats() (List[ArrayStats], error) {
	return tables(s.t, 0, func(t flatbuf.Table) ArrayStats { return ArrayStats{t} })
}

// AggregateSpec is an aggregate function and its options.
type AggregateSpec struct{ t flatbuf.Table }

// ID is field 0 (required).
func (a AggregateSpec) ID() (string, error) { s, _, err := a.t.String(0); return s, err }

// Options is field 1, the serialized options (absent when empty).
func (a AggregateSpec) Options() ([]byte, error) { b, _, err := a.t.Bytes(1); return b, err }

// AggregateSpecs is field 1.
func (s FileStatistics) AggregateSpecs() (List[AggregateSpec], error) {
	return tables(s.t, 1, func(t flatbuf.Table) AggregateSpec { return AggregateSpec{t} })
}

// AggregateSet is an ordered list of aggregates recorded together.
type AggregateSet struct{ t flatbuf.Table }

// Specs is field 0 (required): indices into AggregateSpecs.
func (a AggregateSet) Specs() ([]uint16, error) { return a.t.Uint16s(0) }

// AggregateSets is field 2.
func (s FileStatistics) AggregateSets() (List[AggregateSet], error) {
	return tables(s.t, 2, func(t flatbuf.Table) AggregateSet { return AggregateSet{t} })
}

// EntrySets is field 3, an index into AggregateSets per entry.
func (s FileStatistics) EntrySets() ([]uint16, error) { return s.t.Uint16s(3) }

// Partials is field 4: per entry and spec, a LEB128 length then a ScalarValue.
func (s FileStatistics) Partials() ([]byte, error) { b, _, err := s.t.Bytes(4); return b, err }

// ----- layout.fbs -----

// Layout is a node of the layout tree.
type Layout struct{ t flatbuf.Table }

// ReadLayout reads the root Layout of buf. maxDepth bounds the nesting of the tree.
func ReadLayout(buf []byte, maxDepth int) (Layout, error) {
	t, err := flatbuf.Root(buf, maxDepth)
	return Layout{t}, err
}

// Encoding is field 0, an index into the footer's layout specs.
func (l Layout) Encoding() (uint16, error) { return l.t.Uint16(0, 0) }

// RowCount is field 1.
func (l Layout) RowCount() (uint64, error) { return l.t.Uint64(1, 0) }

// Metadata is field 2, opaque at this layer.
func (l Layout) Metadata() ([]byte, error) { b, _, err := l.t.Bytes(2); return b, err }

// Children is field 3.
func (l Layout) Children() (List[Layout], error) {
	return tables(l.t, 3, func(t flatbuf.Table) Layout { return Layout{t} })
}

// Segments is field 4, indices into the footer's segment specs.
func (l Layout) Segments() ([]uint32, error) { return l.t.Uint32s(4) }

// ----- array.fbs -----

// Array is the flatbuffer of an array message.
type Array struct{ t flatbuf.Table }

// ReadArray reads the root Array of buf. maxDepth bounds the nesting of the node tree.
func ReadArray(buf []byte, maxDepth int) (Array, error) {
	t, err := flatbuf.Root(buf, maxDepth)
	return Array{t}, err
}

// Root is field 0, the node tree.
func (a Array) Root() (ArrayNode, bool, error) {
	n, ok, err := a.t.Table(0)
	return ArrayNode{n}, ok, err
}

// Buffer locates a data buffer after the message. It is an 8-byte struct.
type Buffer struct {
	Padding           uint16 // bytes written before the buffer
	AlignmentExponent uint8
	Compression       uint8 // 0 None, 1 LZ4
	Length            uint32
}

const bufferSize = 8

// Buffers is field 1, a vector of structs.
func (a Array) Buffers() (List[Buffer], error) {
	return structs(a.t, 1, bufferSize, func(b []byte) Buffer {
		return Buffer{
			Padding:           binary.LittleEndian.Uint16(b),
			AlignmentExponent: b[2],
			Compression:       b[3],
			Length:            binary.LittleEndian.Uint32(b[4:]),
		}
	})
}

// ArrayNode is a node of an array's tree.
type ArrayNode struct{ t flatbuf.Table }

// Encoding is field 0, an index into the array specs.
func (n ArrayNode) Encoding() (uint16, error) { return n.t.Uint16(0, 0) }

// Metadata is field 1, the protobuf of the encoding.
func (n ArrayNode) Metadata() ([]byte, error) { b, _, err := n.t.Bytes(1); return b, err }

// Children is field 2.
func (n ArrayNode) Children() (List[ArrayNode], error) {
	return tables(n.t, 2, func(t flatbuf.Table) ArrayNode { return ArrayNode{t} })
}

// Buffers is field 3, indices into the array's Buffers.
func (n ArrayNode) Buffers() ([]uint16, error) { return n.t.Uint16s(3) }

// AppendBuffers appends field 3 to dst.
func (n ArrayNode) AppendBuffers(dst []uint16) ([]uint16, error) { return n.t.AppendUint16s(dst, 3) }

// Stats is field 4.
func (n ArrayNode) Stats() (ArrayStats, bool, error) {
	s, ok, err := n.t.Table(4)
	return ArrayStats{s}, ok, err
}

// Precision of a min or max: 0 Inexact, 1 Exact.
const (
	Inexact = 0
	Exact   = 1
)

// ArrayStats holds the statistics of an array. The scalar values are protobuf ScalarValue
// bytes (see internal/scalar). The optional fields report whether they are set.
type ArrayStats struct{ t flatbuf.Table }

// Min is field 0.
func (s ArrayStats) Min() ([]byte, bool, error) { return s.t.Bytes(0) }

// MinPrecision is field 1.
func (s ArrayStats) MinPrecision() (uint8, error) { return s.t.Uint8(1, Inexact) }

// Max is field 2.
func (s ArrayStats) Max() ([]byte, bool, error) { return s.t.Bytes(2) }

// MaxPrecision is field 3.
func (s ArrayStats) MaxPrecision() (uint8, error) { return s.t.Uint8(3, Inexact) }

// Sum is field 4.
func (s ArrayStats) Sum() ([]byte, bool, error) { return s.t.Bytes(4) }

// IsSorted is field 5.
func (s ArrayStats) IsSorted() (v, ok bool, err error) { return optBool(s.t, 5) }

// IsStrictSorted is field 6.
func (s ArrayStats) IsStrictSorted() (v, ok bool, err error) { return optBool(s.t, 6) }

// IsConstant is field 7.
func (s ArrayStats) IsConstant() (v, ok bool, err error) { return optBool(s.t, 7) }

// NullCount is field 8.
func (s ArrayStats) NullCount() (v uint64, ok bool, err error) { return optUint64(s.t, 8) }

// UncompressedSizeInBytes is field 9.
func (s ArrayStats) UncompressedSizeInBytes() (v uint64, ok bool, err error) {
	return optUint64(s.t, 9)
}

// NanCount is field 10.
func (s ArrayStats) NanCount() (v uint64, ok bool, err error) { return optUint64(s.t, 10) }
