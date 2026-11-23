package flatbuf

import (
	"bytes"
	"encoding/binary"
)

// Off is the position of a table, a vector or a string that a Builder has written: its
// distance from the end of the buffer. It is only meaningful for the Builder that returned it.
type Off uint32

// Builder writes a flatbuffer back to front, like the reference implementation does. It
// finishes a buffer "minimal": no size prefix and no file identifier. A table is written
// between StartTable and EndTable, and everything it points to (child tables, strings,
// vectors) before StartTable. A Builder is reused with Reset, and then allocates nothing once
// its buffer has grown to the size of the largest message.
type Builder struct {
	buf      []byte // the message is buf[head:]
	fields   []int  // the offset of each field of the open table, 0 when absent
	vtables  []int  // the position (its distance from the end) of each vtable written
	head     int
	minAlign int
	start    int // the size of the buffer when the open table started
}

// NewBuilder returns a Builder with room for size bytes.
func NewBuilder(size int) *Builder {
	b := &Builder{}
	b.buf = make([]byte, max(size, 16))
	b.Reset()
	return b
}

// Reset empties the builder, keeping its memory.
func (b *Builder) Reset() {
	b.head = len(b.buf)
	b.minAlign = 1
	b.fields = b.fields[:0]
	b.vtables = b.vtables[:0]
}

// size is the number of bytes written.
func (b *Builder) size() int { return len(b.buf) - b.head }

// grow makes room for n more bytes in front of the message.
func (b *Builder) grow(n int) {
	if b.head >= n {
		return
	}
	used := b.size()
	buf := make([]byte, max(2*len(b.buf), used+n))
	copy(buf[len(buf)-used:], b.buf[b.head:])
	b.buf = buf
	b.head = len(buf) - used
}

// prep aligns the next write: after it, extra more bytes put the size at a multiple of align.
// The bytes skipped are zero.
func (b *Builder) prep(align, extra int) {
	b.minAlign = max(b.minAlign, align)
	pad := (align - (b.size()+extra)%align) % align
	b.grow(pad + extra + align)
	b.head -= pad
	clear(b.buf[b.head : b.head+pad])
}

// take reserves n bytes in front of the message and returns them, with the alignment of
// the caller already done.
func (b *Builder) take(n int) []byte {
	b.head -= n
	return b.buf[b.head : b.head+n]
}

func (b *Builder) putUint16(v uint16) {
	b.prep(2, 0)
	binary.LittleEndian.PutUint16(b.take(2), v)
}

func (b *Builder) putUint32(v uint32) {
	b.prep(4, 0)
	binary.LittleEndian.PutUint32(b.take(4), v)
}

// ref writes the 4 bytes that point back to the object written at off.
func (b *Builder) ref(off Off) {
	b.prep(4, 0)
	b.putUint32Raw(uint32(b.size() + 4 - int(off)))
}

func (b *Builder) putUint32Raw(v uint32) { binary.LittleEndian.PutUint32(b.take(4), v) }

// ----- strings and vectors -----

// String writes a string, with its terminating zero.
func (b *Builder) String(s string) Off {
	b.prep(4, len(s)+1)
	b.take(1)[0] = 0
	copy(b.take(len(s)), s)
	b.putUint32Raw(uint32(len(s)))
	return Off(b.size())
}

// Bytes writes a vector of bytes.
func (b *Builder) Bytes(v []byte) Off {
	b.prep(4, len(v))
	copy(b.take(len(v)), v)
	b.putUint32Raw(uint32(len(v)))
	return Off(b.size())
}

// Uint16s writes a vector of uint16.
func (b *Builder) Uint16s(v []uint16) Off {
	b.prep(4, 2*len(v))
	b.prep(2, 2*len(v))
	for i := len(v) - 1; i >= 0; i-- {
		binary.LittleEndian.PutUint16(b.take(2), v[i])
	}
	b.putUint32Raw(uint32(len(v)))
	return Off(b.size())
}

// Uint32s writes a vector of uint32.
func (b *Builder) Uint32s(v []uint32) Off {
	b.prep(4, 4*len(v))
	for i := len(v) - 1; i >= 0; i-- {
		b.putUint32Raw(v[i])
	}
	b.putUint32Raw(uint32(len(v)))
	return Off(b.size())
}

// Offsets writes a vector of tables or strings, which are already written.
func (b *Builder) Offsets(v []Off) Off {
	b.prep(4, 4*len(v))
	for i := len(v) - 1; i >= 0; i-- {
		b.ref(v[i])
	}
	b.putUint32Raw(uint32(len(v)))
	return Off(b.size())
}

// Structs writes a vector of structs of size bytes each, with the given alignment. raw holds
// the structs in order, already in little-endian: its length is a multiple of size.
func (b *Builder) Structs(size, align int, raw []byte) Off {
	b.prep(4, len(raw))
	b.prep(align, len(raw))
	copy(b.take(len(raw)), raw)
	b.putUint32Raw(uint32(len(raw) / size))
	return Off(b.size())
}

// ----- tables -----

// StartTable begins a table of n fields (the ids 0 to n-1). A union takes two ids.
func (b *Builder) StartTable(n int) {
	b.fields = append(b.fields[:0], make([]int, n)...)
	b.start = b.size()
}

func (b *Builder) mark(id int) {
	b.fields[id] = b.size()
}

// Uint8 sets a field of one byte (also a bool or an enum). A field equal to its default is
// left out.
func (b *Builder) Uint8(id int, v, def uint8) {
	if v == def {
		return
	}
	b.prep(1, 0)
	b.take(1)[0] = v
	b.mark(id)
}

// Uint16 sets a field of two bytes.
func (b *Builder) Uint16(id int, v, def uint16) {
	if v == def {
		return
	}
	b.putUint16(v)
	b.mark(id)
}

// Uint32 sets a field of four bytes.
func (b *Builder) Uint32(id int, v, def uint32) {
	if v == def {
		return
	}
	b.putUint32(v)
	b.mark(id)
}

// Uint64 sets a field of eight bytes.
func (b *Builder) Uint64(id int, v, def uint64) {
	if v == def {
		return
	}
	b.prep(8, 0)
	binary.LittleEndian.PutUint64(b.take(8), v)
	b.mark(id)
}

// Ref sets a field that points to a table, a string or a vector.
func (b *Builder) Ref(id int, off Off) {
	b.ref(off)
	b.mark(id)
}

// Union sets the two fields of a union: its type at id, its value at id+1.
func (b *Builder) Union(id int, typ uint8, value Off) {
	b.Ref(id+1, value)
	b.Uint8(id, typ, 0)
}

// EndTable writes the vtable and returns the table. A vtable equal to one already written is
// not written again: the table points to the first one, as the reference builder does, so a
// message of many tables of the same shape (the fields of a schema, say) carries one vtable.
func (b *Builder) EndTable() Off {
	b.prep(4, 0)
	b.putUint32Raw(0) // the offset to the vtable, set below
	table := b.size()
	n := len(b.fields)
	for n > 0 && b.fields[n-1] == 0 {
		n--
	}
	for i := n - 1; i >= 0; i-- {
		var rel uint16
		if b.fields[i] != 0 {
			rel = uint16(table - b.fields[i])
		}
		b.putUint16(rel)
	}
	b.putUint16(uint16(table - b.start))
	b.putUint16(uint16(4 + 2*n))
	at := b.size()
	vt := b.buf[b.head : b.head+at-table] // b.head, not a position kept before: a grow moves it
	for _, old := range b.vtables {
		if o := b.buf[len(b.buf)-old:]; len(o) >= len(vt) && bytes.Equal(o[:len(vt)], vt) {
			b.head, at = len(b.buf)-table, old // the vtable is dropped
			break
		}
	}
	if at == b.size() {
		b.vtables = append(b.vtables, at)
	}
	binary.LittleEndian.PutUint32(b.buf[len(b.buf)-table:], uint32(int32(at-table)))
	return Off(table)
}

// Finish writes the offset of the root table, and returns the message. It stays valid until
// the next Reset.
func (b *Builder) Finish(root Off) []byte {
	b.prep(b.minAlign, 4)
	b.ref(root)
	return b.buf[b.head:]
}
