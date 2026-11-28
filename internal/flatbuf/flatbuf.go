package flatbuf

import (
	"encoding/binary"
	"fmt"
)

// Error is a bounds violation in a buffer.
type Error struct {
	Msg string // what was wrong with it
	Off int    // the offset that was read, or that a computed offset led to
}

func (e *Error) Error() string {
	return fmt.Sprintf("flatbuf: %s at offset %d", e.Msg, e.Off)
}

// slice returns buf[off:off+n], or an error when it does not fit in the buffer.
func slice(buf []byte, off, n int) ([]byte, error) {
	if off < 0 || n < 0 || off > len(buf)-n {
		return nil, &Error{Off: off, Msg: fmt.Sprintf("%d bytes do not fit in a buffer of %d", n, len(buf))}
	}
	return buf[off : off+n], nil
}

func u32(buf []byte, off int) (int, error) {
	b, err := slice(buf, off, 4)
	if err != nil {
		return 0, err
	}
	return int(binary.LittleEndian.Uint32(b)), nil
}

// Table is a view of one flatbuffers table.
type Table struct {
	buf      []byte
	pos      int // the table
	vt       int // its vtable
	vtSize   int
	depth    int
	maxDepth int
}

// Root returns the root table of buf. maxDepth bounds how many tables may be nested below
// (and including) the root; the root is at depth 1.
func Root(buf []byte, maxDepth int) (Table, error) {
	pos, err := u32(buf, 0)
	if err != nil {
		return Table{}, err
	}
	return newTable(buf, pos, 1, maxDepth)
}

func newTable(buf []byte, pos, depth, maxDepth int) (Table, error) {
	if depth > maxDepth {
		return Table{}, &Error{Off: pos, Msg: fmt.Sprintf("tables nested deeper than %d", maxDepth)}
	}
	b, err := slice(buf, pos, 4)
	if err != nil {
		return Table{}, err
	}
	vt := pos - int(int32(binary.LittleEndian.Uint32(b)))
	hdr, err := slice(buf, vt, 4)
	if err != nil {
		return Table{}, err
	}
	vtSize := int(binary.LittleEndian.Uint16(hdr))
	if vtSize < 4 {
		return Table{}, &Error{Off: vt, Msg: fmt.Sprintf("vtable of %d bytes, 4 at least", vtSize)}
	}
	if _, err := slice(buf, vt, vtSize); err != nil {
		return Table{}, err
	}
	return Table{buf, pos, vt, vtSize, depth, maxDepth}, nil
}

// field returns the position of the field, and whether the table has it.
func (t Table) field(id int) (int, bool) {
	o := 4 + 2*id
	if id < 0 || o+2 > t.vtSize {
		return 0, false
	}
	fo := int(binary.LittleEndian.Uint16(t.buf[t.vt+o:]))
	return t.pos + fo, fo != 0
}

// Has reports whether the table has the field. A field with a "= null" default is optional:
// Has tells it apart from a field set to zero.
func (t Table) Has(id int) bool {
	_, ok := t.field(id)
	return ok
}

// scalar returns the n bytes of the field, nil when it is absent.
func (t Table) scalar(id, n int) ([]byte, error) {
	pos, ok := t.field(id)
	if !ok {
		return nil, nil
	}
	return slice(t.buf, pos, n)
}

// Uint8 returns the field, or def when it is absent. Bools and enums of a byte use it too.
func (t Table) Uint8(id int, def uint8) (uint8, error) {
	b, err := t.scalar(id, 1)
	if err != nil || b == nil {
		return def, err
	}
	return b[0], nil
}

// Int8 returns the field, or def when it is absent.
func (t Table) Int8(id int, def int8) (int8, error) {
	v, err := t.Uint8(id, uint8(def))
	return int8(v), err
}

// Bool returns the field, or def when it is absent.
func (t Table) Bool(id int, def bool) (bool, error) {
	v, err := t.Uint8(id, 0)
	if !t.Has(id) {
		return def, err
	}
	return v != 0, err
}

// Uint16 returns the field, or def when it is absent.
func (t Table) Uint16(id int, def uint16) (uint16, error) {
	b, err := t.scalar(id, 2)
	if err != nil || b == nil {
		return def, err
	}
	return binary.LittleEndian.Uint16(b), nil
}

// Uint32 returns the field, or def when it is absent.
func (t Table) Uint32(id int, def uint32) (uint32, error) {
	b, err := t.scalar(id, 4)
	if err != nil || b == nil {
		return def, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// Uint64 returns the field, or def when it is absent.
func (t Table) Uint64(id int, def uint64) (uint64, error) {
	b, err := t.scalar(id, 8)
	if err != nil || b == nil {
		return def, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// indirect follows the offset stored in the field.
func (t Table) indirect(id int) (int, bool, error) {
	pos, ok := t.field(id)
	if !ok {
		return 0, false, nil
	}
	off, err := u32(t.buf, pos)
	return pos + off, true, err
}

// vector returns where the elements of the vector in the field start and how many there are.
// The count is checked against the bytes left, so n*elem never overflows or leaves the buffer.
func (t Table) vector(id, elem int) (start, n int, ok bool, err error) {
	pos, ok, err := t.indirect(id)
	if err != nil || !ok {
		return 0, 0, false, err
	}
	return lengthPrefixed(t.buf, pos, elem)
}

func lengthPrefixed(buf []byte, pos, elem int) (start, n int, ok bool, err error) {
	n, err = u32(buf, pos)
	if err != nil {
		return 0, 0, false, err
	}
	if elem < 1 {
		return 0, 0, false, &Error{Off: pos, Msg: fmt.Sprintf("element size %d", elem)}
	}
	start = pos + 4
	if n > (len(buf)-start)/elem {
		return 0, 0, false, &Error{Off: pos, Msg: fmt.Sprintf("vector of %d elements of %d bytes does not fit", n, elem)}
	}
	return start, n, true, nil
}

// Bytes returns a vector of bytes (or a string's bytes). It aliases the buffer.
// The bool is false when the field is absent.
func (t Table) Bytes(id int) ([]byte, bool, error) {
	start, n, ok, err := t.vector(id, 1)
	if err != nil || !ok {
		return nil, false, err
	}
	return t.buf[start : start+n], true, nil
}

// String returns a string field. The bool is false when the field is absent.
func (t Table) String(id int) (string, bool, error) {
	b, ok, err := t.Bytes(id)
	return string(b), ok, err
}

// Uint16s returns a vector of uint16 as a new slice, its size bounded by the buffer.
func (t Table) Uint16s(id int) ([]uint16, error) {
	start, n, _, err := t.vector(id, 2)
	out := make([]uint16, n)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(t.buf[start+2*i:])
	}
	return out, err
}

// AppendUint16s appends a vector of uint16 to dst: in its memory when it has room.
func (t Table) AppendUint16s(dst []uint16, id int) ([]uint16, error) {
	start, n, _, err := t.vector(id, 2)
	for i := range n {
		dst = append(dst, binary.LittleEndian.Uint16(t.buf[start+2*i:]))
	}
	return dst, err
}

// Uint32Vec is a vector of uint32 that is not decoded yet: its reader chooses where the
// numbers go, so that the vectors of a whole tree can share one slice.
type Uint32Vec struct{ raw []byte }

// Uint32Vec returns a vector of uint32, its size bounded by the buffer. It is empty when
// the field is absent.
func (t Table) Uint32Vec(id int) (Uint32Vec, error) {
	start, n, _, err := t.vector(id, 4)
	return Uint32Vec{t.buf[start : start+4*n]}, err
}

// Len is the number of elements.
func (v Uint32Vec) Len() int { return len(v.raw) / 4 }

// Read decodes the vector to dst, which has Len elements.
func (v Uint32Vec) Read(dst []uint32) {
	for i := range dst {
		dst[i] = binary.LittleEndian.Uint32(v.raw[4*i:])
	}
}

// Table returns a table field. The bool is false when the field is absent.
func (t Table) Table(id int) (Table, bool, error) {
	pos, ok, err := t.indirect(id)
	if err != nil || !ok {
		return Table{}, false, err
	}
	sub, err := newTable(t.buf, pos, t.depth+1, t.maxDepth)
	return sub, err == nil, err
}

// Union returns the type and the table of a union field: the type is at id, the value at
// id+1. The type is 0 (NONE) when the field is absent.
func (t Table) Union(id int) (uint8, Table, error) {
	typ, err := t.Uint8(id, 0)
	if err != nil || typ == 0 {
		return 0, Table{}, err
	}
	v, ok, err := t.Table(id + 1)
	if err == nil && !ok {
		err = &Error{Off: t.pos, Msg: fmt.Sprintf("union type %d without a value", typ)}
	}
	return typ, v, err
}

// Vector is a view of a vector of tables, strings or structs, built by Tables, Strings or Structs.
type Vector struct {
	buf      []byte
	depth    int
	maxDepth int
	start    int
	n        int
	elem     int
}

// Len is the number of elements, 0 for an absent vector.
func (v Vector) Len() int { return v.n }

func (v Vector) at(i int) (int, error) {
	if i < 0 || i >= v.n {
		return 0, &Error{Off: v.start, Msg: fmt.Sprintf("index %d out of %d elements", i, v.n)}
	}
	return v.start + i*v.elem, nil
}

func (t Table) vec(id, elem int) (Vector, error) {
	start, n, _, err := t.vector(id, elem)
	return Vector{t.buf, t.depth, t.maxDepth, start, n, elem}, err
}

// Tables returns a vector of tables.
func (t Table) Tables(id int) (Vector, error) { return t.vec(id, 4) }

// Strings returns a vector of strings.
func (t Table) Strings(id int) (Vector, error) { return t.vec(id, 4) }

// Structs returns a vector of structs of size bytes each.
func (t Table) Structs(id, size int) (Vector, error) { return t.vec(id, size) }

// Table returns the i-th table of a vector built by Tables.
func (v Vector) Table(i int) (Table, error) {
	pos, err := v.at(i)
	if err != nil {
		return Table{}, err
	}
	return newTable(v.buf, pos+v.u32(pos), v.depth+1, v.maxDepth)
}

// String returns the i-th string of a vector built by Strings.
func (v Vector) String(i int) (string, error) {
	pos, err := v.at(i)
	if err != nil {
		return "", err
	}
	start, n, _, err := lengthPrefixed(v.buf, pos+v.u32(pos), 1)
	if err != nil {
		return "", err
	}
	return string(v.buf[start : start+n]), nil
}

// Struct returns the bytes of the i-th struct of a vector built by Structs. It aliases the buffer.
func (v Vector) Struct(i int) ([]byte, error) {
	pos, err := v.at(i)
	if err != nil {
		return nil, err
	}
	return v.buf[pos : pos+v.elem], nil
}

// u32 reads an element of a vector of offsets; at() has checked that it is in the buffer.
func (v Vector) u32(pos int) int {
	return int(binary.LittleEndian.Uint32(v.buf[pos:]))
}
