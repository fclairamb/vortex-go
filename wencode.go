package vortex

import (
	"encoding/binary"

	"github.com/fclairamb/vortex-go/internal/flatbuf"
	"github.com/fclairamb/vortex-go/internal/protowire"
)

// The encodings of the arrays the writer makes. Each function adds the data buffers of its
// array, then those of its children (the order the reader's indices follow), and returns the
// node of the tree. The Data has been checked by checkData.

// maxInline is the longest string a view of a varbinview holds itself.
const maxInline = 12

// pU32 is the PType of the u32 offsets and sizes of a list, in the metadata.
const pU32 = 2

// addBuf adds a data buffer of the array being built.
func (w *Writer) addBuf(b []byte, align int) {
	w.bufs = append(w.bufs, buffer{b, align})
}

func (w *Writer) push(o flatbuf.Off) { w.stack = append(w.stack, o) }

// node writes the node of an array: its encoding, its metadata, the children pushed since
// kidsBase (which it pops) and the nBuf buffers from firstBuf.
func (w *Writer) node(id string, meta []byte, firstBuf, nBuf, kidsBase int) flatbuf.Off {
	b := w.fb
	var kids, bufs, md flatbuf.Off
	hasKids := len(w.stack) > kidsBase
	if hasKids {
		kids = b.Offsets(w.stack[kidsBase:])
		w.stack = w.stack[:kidsBase]
	}
	if nBuf > 0 {
		w.idx = w.idx[:0]
		for i := range nBuf {
			w.idx = append(w.idx, uint16(firstBuf+i))
		}
		bufs = b.Uint16s(w.idx)
	}
	if len(meta) > 0 {
		md = b.Bytes(meta)
	}
	var stats flatbuf.Off
	if w.depth == 1 && w.stats != 0 && id == w.statsID {
		stats, w.stats = w.stats, 0
	}
	b.StartTable(5)
	b.Uint16(0, specIndex(&w.arrayIDs, id), 0)
	if len(meta) > 0 {
		b.Ref(1, md)
	}
	if hasKids {
		b.Ref(2, kids)
	}
	if nBuf > 0 {
		b.Ref(3, bufs)
	}
	if stats != 0 {
		b.Ref(4, stats)
	}
	return b.EndTable()
}

// leaf is a node with the buffer it adds, and no child.
func (w *Writer) leaf(id string, data []byte, align int) flatbuf.Off {
	first := len(w.bufs)
	w.addBuf(data, align)
	return w.node(id, nil, first, 1, len(w.stack))
}

// encode writes the array of a column of type t.
func (w *Writer) encode(t Type, d Data, n int) flatbuf.Off {
	w.depth++
	off, ok := w.encodeCompressed(t, d)
	if !ok {
		off = w.encodeNode(t, d, n)
	}
	w.depth--
	return off
}

func (w *Writer) encodeNode(t Type, d Data, n int) flatbuf.Off {
	switch t.kind {
	case Null:
		return w.node("vortex.null", nil, len(w.bufs), 0, len(w.stack))
	case Bool:
		return w.encodeBool(d, n)
	case Utf8, Binary:
		return w.encodeStrings(t, d)
	case KindDecimal:
		return w.encodeDecimals(t, d, n)
	case Struct:
		return w.encodeStruct(t, d, n)
	case List, Map:
		return w.encodeList(t, d, n)
	case FixedSizeList:
		base := len(w.stack)
		w.push(w.encode(*t.elem, d.kids[0], n*t.Size()))
		w.validity(d, n)
		return w.node("vortex.fixed_size_list", nil, len(w.bufs), 0, base)
	case UUID:
		return w.arrayExtension(w.encodeUUID(d, n))
	case Date, Time, Timestamp:
		if t.kind == Timestamp {
			if off, ok := w.encodeForcedTimestamps(t, d); ok {
				return off
			}
		}
		return w.arrayExtension(w.encodePrimitive(d, n))
	default:
		return w.encodePrimitive(d, n)
	}
}

// arrayExtension wraps the storage array in a vortex.ext node.
func (w *Writer) arrayExtension(storage flatbuf.Off) flatbuf.Off {
	base := len(w.stack)
	w.push(storage)
	return w.node("vortex.ext", nil, len(w.bufs), 0, base)
}

// validity pushes the validity child of d when a row is null: a bool array, a bit per row.
func (w *Writer) validity(d Data, n int) {
	for _, null := range d.nulls {
		if null {
			w.push(w.encodeValidity(d, n))
			return
		}
	}
}

// encodeValidity is the bool array that says which rows of d are valid.
func (w *Writer) encodeValidity(d Data, n int) flatbuf.Off {
	bits := w.alloc((n + 7) / 8)
	for i := range n {
		if !isNull(d.nulls, i) {
			bits[i>>3] |= 1 << (i & 7)
		}
	}
	w.depth++ // a child, whatever its encoding
	off := w.leaf("vortex.bool", bits, 1)
	w.depth--
	return off
}

func (w *Writer) encodePrimitive(d Data, n int) flatbuf.Off {
	if w.force != "" && w.depth == 1 { // the array of a column, not the one of a table of zones
		return w.encodeForced(d, n)
	}
	return w.encodePlain(d, n)
}

func (w *Writer) encodePlain(d Data, n int) flatbuf.Off {
	first := len(w.bufs)
	w.addBuf(d.raw, numberWidth(d.pk))
	base := len(w.stack)
	w.validity(d, n)
	return w.node("vortex.primitive", nil, first, 1, base)
}

// numberWidth is the bytes of a number of this kind.
func numberWidth(k Kind) int {
	switch k {
	case Int8, Uint8:
		return 1
	case Int16, Uint16, Float16:
		return 2
	case Int32, Uint32, Float32:
		return 4
	default:
		return 8
	}
}

func (w *Writer) encodeBool(d Data, n int) flatbuf.Off {
	if off, ok := w.encodeForcedBools(d); ok {
		return off
	}
	bits := w.alloc((n + 7) / 8)
	for i, v := range d.bools {
		if v {
			bits[i>>3] |= 1 << (i & 7)
		}
	}
	first := len(w.bufs)
	w.addBuf(bits, 1)
	base := len(w.stack)
	w.validity(d, n)
	return w.node("vortex.bool", nil, first, 1, base)
}

// encodeStrings writes a varbinview: the data buffers, then the views, 16 bytes per row. A
// string of up to 12 bytes is in its view, a longer one is in a data buffer that its view
// points to.
func (w *Writer) encodeStrings(t Type, d Data) flatbuf.Off {
	if off, ok := w.encodeForcedStrings(t, d); ok {
		return off
	}
	first := len(w.bufs)
	writeViews(w, d.bins, d.nulls)
	own := len(w.bufs) - first
	base := len(w.stack)
	w.validity(d, d.n)
	return w.node("vortex.varbinview", nil, first, own, base)
}

func writeViews(w *Writer, vals [][]byte, nulls []bool) {
	views := w.alloc(16 * len(vals))
	long := 0
	for i, v := range vals {
		if len(v) > maxInline && !isNull(nulls, i) {
			long += len(v)
		}
	}
	var cur []byte
	index := uint32(0)
	for i, v := range vals {
		if isNull(nulls, i) {
			continue
		}
		view := views[16*i : 16*i+16]
		binary.LittleEndian.PutUint32(view, uint32(len(v)))
		if len(v) <= maxInline {
			copy(view[4:], v)
			continue
		}
		if len(cur)+len(v) > cap(cur) {
			if cur != nil {
				w.addBuf(cur, 1)
				index++
			}
			cur = w.alloc(max(len(v), min(long, maxViewBuffer)))[:0]
		}
		copy(view[4:8], v)
		binary.LittleEndian.PutUint32(view[8:], index)
		binary.LittleEndian.PutUint32(view[12:], uint32(len(cur)))
		cur = append(cur, v...)
		long -= len(v)
	}
	if cur != nil {
		w.addBuf(cur, 1)
	}
	w.addBuf(views, 16)
}

func (w *Writer) encodeDecimals(t Type, d Data, n int) flatbuf.Off {
	if off, ok := w.encodeForcedDecimals(t, d, n); ok {
		return off
	}
	width := decimalWidth(t.precision)
	buf := w.alloc(n * width)
	for i, v := range d.decs {
		le := v.le()
		copy(buf[i*width:], le[:width])
	}
	var meta []byte
	if kind := uint64(widthIndex(width)); kind != 0 {
		meta = protowire.AppendUint(w.meta[:0], 1, kind)
	}
	first := len(w.bufs)
	w.addBuf(buf, min(width, 16))
	base := len(w.stack)
	w.validity(d, n)
	node := w.node("vortex.decimal", meta, first, 1, base)
	w.meta = meta[:0]
	return node
}

// widthIndex is the DecimalType of a width in bytes: i8 is 0, i16 1 ... i256 5.
func widthIndex(width int) int {
	i := 0
	for width > 1 {
		width >>= 1
		i++
	}
	return i
}

func (w *Writer) encodeStruct(t Type, d Data, n int) flatbuf.Off {
	base := len(w.stack)
	w.validity(d, n)
	for i, f := range t.fields {
		w.push(w.encode(f.Type, d.kids[i], n))
	}
	return w.node("vortex.struct", nil, len(w.bufs), 0, base)
}

// encodeList writes a listview: the elements, the offset and the size of every row, and the
// validity. A map is a vortex.map around a listview of its entries.
func (w *Writer) encodeList(t Type, d Data, n int) flatbuf.Off {
	elems := d.kids[0]
	base := len(w.stack)
	if t.kind == Map {
		w.push(w.encodeEntries(t, elems))
	} else {
		w.push(w.encode(*t.elem, elems, elems.n))
	}
	offsets, sizes := w.alloc(4*n), w.alloc(4*n)
	for i := range n {
		binary.LittleEndian.PutUint32(offsets[4*i:], uint32(d.offs[i]))
		binary.LittleEndian.PutUint32(sizes[4*i:], uint32(d.offs[i+1]-d.offs[i]))
	}
	w.push(w.encodeU32s(offsets))
	w.push(w.encodeU32s(sizes))
	w.validity(d, n)
	meta := protowire.AppendUint(w.meta[:0], 1, uint64(elems.n))
	meta = protowire.AppendUint(meta, 2, pU32)
	meta = protowire.AppendUint(meta, 3, pU32)
	view := w.node("vortex.listview", meta, len(w.bufs), 0, base)
	w.meta = meta[:0]
	if t.kind != Map {
		return view
	}
	w.push(view)
	return w.node("vortex.map", nil, len(w.bufs), 0, base)
}

// encodeEntries writes the entries of a map: a struct of keys and values.
func (w *Writer) encodeEntries(t Type, elems Data) flatbuf.Off {
	base := len(w.stack)
	w.push(w.encode(*t.elem, elems.kids[0], elems.n))
	w.push(w.encode(*t.value, elems.kids[1], elems.n))
	return w.node("vortex.struct", nil, len(w.bufs), 0, base)
}

// encodeUUID writes the storage of a uuid column: a fixed-size list of 16 bytes.
func (w *Writer) encodeUUID(d Data, n int) flatbuf.Off {
	bytes := w.alloc(16 * n)
	for i, v := range d.bins {
		if !isNull(d.nulls, i) {
			copy(bytes[16*i:], v)
		}
	}
	base := len(w.stack)
	w.push(w.leaf("vortex.primitive", bytes, 1))
	w.validity(d, n)
	return w.node("vortex.fixed_size_list", nil, len(w.bufs), 0, base)
}
