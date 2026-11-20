// Package fbtest builds flatbuffers for tests. It is a tiny forward builder: a table is
// described by its fields, laid out as vtable, table, then its children, with the offsets
// patched. It does not align anything, which the reader does not need. Test-only: nothing
// outside _test.go files imports it.
package fbtest

import (
	"encoding/binary"
	"sort"
)

// Tab is a table under construction: field id to value.
type Tab map[int]Val

// Val is the value of one field.
type Val struct {
	child func(*enc) int // writes the pointed-to object, returns its position
	bits  uint64         // the value of a scalar
	size  int            // bytes the field takes inside the table
}

// U8 is a uint8, a bool or an enum.
func U8(v uint8) Val { return Val{size: 1, bits: uint64(v)} }

// U16 is a uint16.
func U16(v uint16) Val { return Val{size: 2, bits: uint64(v)} }

// U32 is a uint32.
func U32(v uint32) Val { return Val{size: 4, bits: uint64(v)} }

// U64 is a uint64.
func U64(v uint64) Val { return Val{size: 8, bits: v} }

// Bytes is a vector of bytes.
func Bytes(b []byte) Val {
	return vec(1, len(b), func(e *enc, at int) { copy(e.buf[at:], b) })
}

// Str is a string.
func Str(s string) Val { return Bytes([]byte(s)) }

// U16s is a vector of uint16.
func U16s(v ...uint16) Val {
	return vec(2, len(v), func(e *enc, at int) {
		for i, x := range v {
			binary.LittleEndian.PutUint16(e.buf[at+2*i:], x)
		}
	})
}

// U32s is a vector of uint32.
func U32s(v ...uint32) Val {
	return vec(4, len(v), func(e *enc, at int) {
		for i, x := range v {
			binary.LittleEndian.PutUint32(e.buf[at+4*i:], x)
		}
	})
}

// Structs is a vector of structs, each already encoded.
func Structs(size int, v ...[]byte) Val {
	return vec(size, len(v), func(e *enc, at int) {
		for i, x := range v {
			copy(e.buf[at+size*i:], x)
		}
	})
}

// T is a table.
func T(t Tab) Val {
	return Val{size: 4, child: func(e *enc) int { return e.table(t) }}
}

// Tables is a vector of tables.
func Tables(v ...Tab) Val {
	return refs(len(v), func(e *enc, i int) int { return e.table(v[i]) })
}

// Strs is a vector of strings.
func Strs(v ...string) Val {
	return refs(len(v), func(e *enc, i int) int { return Str(v[i]).child(e) })
}

func refs(n int, child func(*enc, int) int) Val {
	return Val{size: 4, child: func(e *enc) int {
		pos := e.grow(4 + 4*n)
		binary.LittleEndian.PutUint32(e.buf[pos:], uint32(n))
		for i := range n {
			slot := pos + 4 + 4*i
			off := child(e, i) - slot // the child grows e.buf: evaluate it first
			binary.LittleEndian.PutUint32(e.buf[slot:], uint32(off))
		}
		return pos
	}}
}

func vec(elem, n int, fill func(e *enc, at int)) Val {
	return Val{size: 4, child: func(e *enc) int {
		pos := e.grow(4 + elem*n)
		binary.LittleEndian.PutUint32(e.buf[pos:], uint32(n))
		fill(e, pos+4)
		return pos
	}}
}

type enc struct{ buf []byte }

func (e *enc) grow(n int) int {
	pos := len(e.buf)
	e.buf = append(e.buf, make([]byte, n)...)
	return pos
}

// table writes the vtable and the table, then the children, and returns the table position.
func (e *enc) table(t Tab) int {
	ids := make([]int, 0, len(t))
	for id := range t {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	nfields := 0
	if len(ids) > 0 {
		nfields = ids[len(ids)-1] + 1
	}
	vtSize := 4 + 2*nfields
	vt := e.grow(vtSize)
	pos := e.grow(4) // soffset
	binary.LittleEndian.PutUint32(e.buf[pos:], uint32(pos-vt))
	slots := map[int]int{}
	for _, id := range ids {
		v := t[id]
		at := e.grow(v.size)
		binary.LittleEndian.PutUint16(e.buf[vt+4+2*id:], uint16(at-pos))
		slots[id] = at
		for i := range v.size {
			if v.child == nil {
				e.buf[at+i] = byte(v.bits >> (8 * i))
			}
		}
	}
	binary.LittleEndian.PutUint16(e.buf[vt:], uint16(vtSize))
	binary.LittleEndian.PutUint16(e.buf[vt+2:], uint16(len(e.buf)-pos))
	for _, id := range ids {
		if v := t[id]; v.child != nil {
			at := slots[id]
			off := v.child(e) - at // the child grows e.buf: evaluate it first
			binary.LittleEndian.PutUint32(e.buf[at:], uint32(off))
		}
	}
	return pos
}

// Build returns the finished flatbuffer: the root offset, then the root table.
func Build(root Tab) []byte {
	e := &enc{buf: make([]byte, 4)}
	pos := e.table(root)
	binary.LittleEndian.PutUint32(e.buf, uint32(pos))
	return e.buf
}
