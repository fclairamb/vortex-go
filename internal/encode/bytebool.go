package encode

import "encoding/binary"

// ByteBool is the vortex.bytebool array of the booleans v: a byte per row, 0 or 1 (0 for a
// null row), and the validity as its child. The compressor never picks it, as vortex.bool
// holds the same values in a bit each: a test forces it, to read it back with the reference.
func ByteBool(e *Encoder, v []bool, nulls []bool) *Node {
	buf := e.alloc(len(v))
	for i, x := range v {
		if x && !isNull(nulls, i) {
			buf[i] = 1
		}
	}
	n := e.node(IDByteBool)
	n.add(buf, 1)
	n.kid(e.validity(nulls, len(v), len(v)))
	return n
}

// Bools is the plain vortex.bool array of the booleans v: a bit per row, and the validity.
func Bools(e *Encoder, v []bool, nulls []bool) *Node {
	bits := e.alloc((len(v) + 7) / 8)
	for i, x := range v {
		if x {
			bits[i>>3] |= 1 << (i & 7)
		}
	}
	n := e.node(IDBool)
	n.add(bits, 1)
	n.kid(e.validity(nulls, len(v), len(v)))
	return n
}

// Primitive is the plain vortex.primitive array of the rows numbers of width bytes in raw, and
// the validity.
func Primitive(e *Encoder, raw []byte, width int, nulls []bool, rows int) *Node {
	return e.leaf(raw, width, nulls, rows)
}

// Chunked is the vortex.chunked array of parts, which hold rows[i] rows each: the offsets of
// the parts as u64 (parts + 1) are child 0, then the parts. It has no validity of its own: the
// parts have theirs.
func Chunked(e *Encoder, parts []*Node, rows []int) *Node {
	offs := e.alloc(8 * (len(parts) + 1))
	at := 0
	for i, r := range rows {
		at += r
		binary.LittleEndian.PutUint64(offs[8*(i+1):], uint64(at))
	}
	n := e.node(IDChunked)
	n.kid(e.leaf(offs, 8, nil, len(parts)+1))
	n.Kids = append(n.Kids, parts...)
	return n
}
