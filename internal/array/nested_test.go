package array

import (
	"encoding/binary"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

var (
	utf8t  = DType{Kind: Utf8}
	utf8n  = DType{Kind: Utf8, Nullable: true}
	bint   = DType{Kind: Binary}
	i32l   = DType{Kind: List, Elem: &i32}
	i32ln  = DType{Kind: List, Elem: &i32, Nullable: true}
	strl   = DType{Kind: List, Elem: &utf8t}
	i32fs2 = DType{Kind: FixedSizeList, Elem: &i32, Size: 2}
	dec    = DType{Kind: Decimal}
	decn   = DType{Kind: Decimal, Nullable: true}
)

// pb is a protobuf message of varints, the fields numbered from 1.
func pb(vals ...uint64) []byte {
	var b []byte
	for i, v := range vals {
		b = append(b, byte((i+1)<<3))
		b = binary.AppendUvarint(b, v)
	}
	return b
}

// pnode is a primitive node over buffer i.
func pnode(i uint16) fbtest.Tab { return node(ePrim, nil, []uint16{i}) }

// bnode is a bool node (a validity) over buffer i.
func bnode(i uint16) fbtest.Tab { return node(eBool, nil, []uint16{i}) }

// ints encodes the values as little-endian integers of w bytes.
func ints(w int, vals ...int64) []byte {
	var b []byte
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint64(b, uint64(v))[:len(b)+w]
	}
	return b
}

func str(a *Array, i int) string { return string(a.Data[a.Starts[i]:a.Ends[i]]) }

func TestVarBin(t *testing.T) {
	data := buf{data: []byte("abcde")}
	for _, p := range []PType{U8, U16, U32, U64, I8, I16, I32, I64} {
		seg := segment(node(eVarBin, pb(uint64(p)), []uint16{0}, pnode(1)), data, buf{data: ints(p.Width(), 0, 2, 2, 5)})
		a := mustDecode(t, seg, utf8t, 3)
		if str(a, 0) != "ab" || str(a, 1) != "" || str(a, 2) != "cde" || a.Validity != nil {
			t.Fatalf("%s: %q %q %q", p, str(a, 0), str(a, 1), str(a, 2))
		}
	}
	seg := segment(node(eVarBin, pb(6), []uint16{0}, pnode(1), bnode(2)), data, buf{data: le32(0, 2, 2, 5)}, buf{data: []byte{0b101}})
	a := mustDecode(t, seg, DType{Kind: Binary, Nullable: true}, 3)
	if !a.Null(1) || a.Null(2) {
		t.Fatal("validity is wrong")
	}
	// The offsets may start after 0.
	seg = segment(node(eVarBin, pb(6), []uint16{0}, pnode(1)), data, buf{data: le32(1, 3)})
	if a = mustDecode(t, seg, binary2, 1); str(a, 0) != "bc" {
		t.Fatalf("got %q", str(a, 0))
	}
	offs := buf{data: le32(0, 2, 2, 5)}
	node1 := func(kids ...fbtest.Tab) []byte {
		return segment(node(eVarBin, pb(6), []uint16{0}, kids...), data, offs, buf{data: []byte{1}}, buf{data: le32(0, 2, 2, 5, 7)})
	}
	runErrCases(t, []errCase{
		{"decreasing", segment(node(eVarBin, pb(6), []uint16{0}, pnode(1)), data, buf{data: le32(0, 3, 2, 5)}), utf8t, 3, "offset 2 is 2, after 3"},
		{"past the bytes", segment(node(eVarBin, pb(6), []uint16{0}, pnode(1)), data, buf{data: le32(0, 2, 2, 6)}), utf8t, 3, "past the 5"},
		{"negative", segment(node(eVarBin, pb(6), []uint16{0}, pnode(1)), data, buf{data: le32(-1, 2, 2, 5)}), utf8t, 3, "negative value -1"},
		{"too large", segment(node(eVarBin, pb(3), []uint16{0}, pnode(1)), data, buf{data: le64(0, 1<<63)}), utf8t, 1, "too large"},
		{"float offsets", segment(node(eVarBin, pb(8), []uint16{0}, pnode(1)), data, offs), utf8t, 3, "not an integer type"},
		{"bad metadata", segment(node(eVarBin, []byte{0x08}, []uint16{0}, pnode(1)), data, offs), utf8t, 3, "truncated"},
		{"wrong type", node1(pnode(1)), i32, 3, "cannot be a i32"},
		{"three children", node1(pnode(1), bnode(2), bnode(2)), utf8t, 3, "1 or 2 children"},
		{"no buffer", segment(node(eVarBin, pb(6), nil, pnode(0)), offs), utf8t, 3, "expected 1 buffer"},
		{"missing buffer", segment(node(eVarBin, pb(6), []uint16{7}, pnode(1)), data, offs), utf8t, 3, "buffer 0 is buffer 7"},
		{"offsets of the wrong length", segment(node(eVarBin, pb(6), []uint16{0}, pnode(1)), data, offs), utf8t, 4, "offsets"},
		{"offsets with nulls", segment(node(eVarBin, pb(6), []uint16{0}, node(ePrim, nil, []uint16{1}, bnode(2))), data, offs, buf{data: []byte{1}}), utf8t, 3, "has nulls"},
		{"validity of the wrong length", segment(node(eVarBin, pb(6), []uint16{0}, pnode(1), bnode(2)), data, offs, buf{data: []byte{}}), utf8t, 3, "validity"},
	})
}

var binary2 = DType{Kind: Binary}

// inlineView is the 16 bytes of a view of a value of 12 bytes or fewer.
func inlineView(s string) []byte {
	return append(binary.LittleEndian.AppendUint32(nil, uint32(len(s))), append([]byte(s), make([]byte, 12-len(s))...)...)
}

// refView is the view of the value of buffer idx at off, of the given size.
func refView(prefix string, size, idx, off int) []byte {
	v := binary.LittleEndian.AppendUint32(nil, uint32(size))
	v = append(v, prefix[:4]...)
	v = binary.LittleEndian.AppendUint32(v, uint32(idx))
	return binary.LittleEndian.AppendUint32(v, uint32(off))
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestVarBinView(t *testing.T) {
	long0, long1 := "0123456789abcdefg", "zzzz-and-a-long-tail"
	views := cat(inlineView("hi"), refView(long1, len(long1), 1, 2), inlineView(""), refView(long0, len(long0)-1, 0, 1), inlineView("twelve bytes"))
	b0 := buf{data: []byte("x" + long0)}
	b1 := buf{data: []byte("yy" + long1)}
	seg := segment(node(eVarBinView, nil, []uint16{0, 1, 2}), b0, b1, buf{data: views})
	a := mustDecode(t, seg, utf8t, 5)
	want := []string{"hi", long1, "", long0[:len(long0)-1], "twelve bytes"}
	for i, w := range want {
		if str(a, i) != w {
			t.Fatalf("row %d = %q, want %q", i, str(a, i), w)
		}
	}
	// A validity child, and no data buffer at all.
	seg = segment(node(eVarBinView, nil, []uint16{0}, bnode(1)), buf{data: inlineView("a")}, buf{data: []byte{0}})
	if a = mustDecode(t, seg, utf8n, 1); !a.Null(0) {
		t.Fatal("must be null")
	}
	one := buf{data: inlineView("a")}
	bad := func(view []byte) []byte {
		return segment(node(eVarBinView, nil, []uint16{0, 1}), b0, buf{data: view})
	}
	runErrCases(t, []errCase{
		{"metadata", segment(node(eVarBinView, []byte{1}, []uint16{0}), one), utf8t, 1, "no metadata"},
		{"wrong type", segment(node(eVarBinView, nil, []uint16{0}), one), i32, 1, "cannot be a i32"},
		{"no buffer", segment(node(eVarBinView, nil, nil)), utf8t, 1, "at least 1 buffer"},
		{"two children", segment(node(eVarBinView, nil, []uint16{0}, bnode(0), bnode(0)), one), utf8t, 1, "0 or 1 child"},
		{"views of the wrong length", segment(node(eVarBinView, nil, []uint16{0}), one), utf8t, 2, "views buffer of 16 bytes"},
		{"views of a partial length", segment(node(eVarBinView, nil, []uint16{0}), buf{data: make([]byte, 17)}), utf8t, 1, "views buffer of 17 bytes"},
		{"missing views buffer", segment(node(eVarBinView, nil, []uint16{9}), one), utf8t, 1, "buffer 0 is buffer 9"},
		{"missing data buffer", segment(node(eVarBinView, nil, []uint16{9, 0}), one), utf8t, 1, "buffer 0 is buffer 9"},
		{"bad buffer index", bad(refView(long0, len(long0), 1, 0)), utf8t, 1, "buffer 1, there are 1"},
		{"view past its buffer", bad(refView(long0, len(long0), 0, 2)), utf8t, 1, "ends at 19 of buffer 0"},
		{"validity of the wrong length", segment(node(eVarBinView, nil, []uint16{0}, bnode(1)), one, buf{data: []byte{}}), utf8t, 1, "validity"},
	})
}

func TestList(t *testing.T) {
	elems := buf{data: le32(10, 20, 30, 40)}
	meta := pb(4, 6)
	seg := segment(node(eList, meta, nil, pnode(0), pnode(1)), elems, buf{data: le32(0, 1, 1, 4)})
	a := mustDecode(t, seg, i32l, 3)
	if a.Starts[2] != 1 || a.Ends[2] != 4 || a.Elems.Len != 4 || i32s(t, a.Elems)[3] != 40 {
		t.Fatalf("a = %+v", a)
	}
	seg = segment(node(eList, meta, nil, pnode(0), pnode(1), bnode(2)), elems, buf{data: le32(0, 1, 1, 4)}, buf{data: []byte{0b101}})
	if a = mustDecode(t, seg, i32ln, 3); !a.Null(1) {
		t.Fatal("must be null")
	}
	offs := buf{data: le32(0, 1, 1, 4)}
	runErrCases(t, []errCase{
		{"wrong type", segment(node(eList, meta, nil, pnode(0), pnode(1)), elems, offs), i32, 3, "cannot be a i32"},
		{"bad metadata", segment(node(eList, []byte{0x08}, nil, pnode(0), pnode(1)), elems, offs), i32l, 3, "truncated"},
		{"huge elements", segment(node(eList, pb(1<<63), nil, pnode(0), pnode(1)), elems, offs), i32l, 3, "too large"},
		{"float offsets", segment(node(eList, pb(4, 9), nil, pnode(0), pnode(1)), elems, offs), i32l, 3, "not an integer type"},
		{"one child", segment(node(eList, meta, nil, pnode(0)), elems), i32l, 3, "2 or 3 children"},
		{"elements of the wrong length", segment(node(eList, pb(5, 6), nil, pnode(0), pnode(1)), elems, offs), i32l, 3, "elements"},
		{"offsets of the wrong length", segment(node(eList, meta, nil, pnode(0), pnode(1)), elems, offs), i32l, 4, "offsets"},
		{"decreasing", segment(node(eList, meta, nil, pnode(0), pnode(1)), elems, buf{data: le32(0, 2, 1, 4)}), i32l, 3, "after 2"},
		{"past the elements", segment(node(eList, meta, nil, pnode(0), pnode(1)), elems, buf{data: le32(0, 1, 1, 5)}), i32l, 3, "past the 4"},
		{"validity of the wrong length", segment(node(eList, meta, nil, pnode(0), pnode(1), bnode(2)), elems, offs, buf{data: []byte{}}), i32l, 3, "validity"},
	})
}

func TestListView(t *testing.T) {
	elems := buf{data: le32(10, 20, 30, 40)}
	meta := pb(4, 6, 5) // offsets i32, sizes i16
	// Views overlap and are out of order: [2,4), [0,3), [] and [1,2).
	seg := segment(node(eListView, meta, nil, pnode(0), pnode(1), pnode(2)), elems, buf{data: le32(2, 0, 4, 1)}, buf{data: ints(2, 2, 3, 0, 1)})
	a := mustDecode(t, seg, i32l, 4)
	if a.Starts[0] != 2 || a.Ends[0] != 4 || a.Starts[1] != 0 || a.Ends[1] != 3 || a.Starts[2] != 4 || a.Ends[2] != 4 {
		t.Fatalf("ranges = %v %v", a.Starts, a.Ends)
	}
	seg = segment(node(eListView, meta, nil, pnode(0), pnode(1), pnode(2), bnode(3)), elems, buf{data: le32(2)}, buf{data: ints(2, 2)}, buf{data: []byte{0}})
	if a = mustDecode(t, seg, i32ln, 1); !a.Null(0) {
		t.Fatal("must be null")
	}
	mk := func(offs, sizes []byte, kids ...fbtest.Tab) []byte {
		return segment(node(eListView, meta, nil, append([]fbtest.Tab{pnode(0), pnode(1), pnode(2)}, kids...)...), elems, buf{data: offs}, buf{data: sizes})
	}
	runErrCases(t, []errCase{
		{"wrong type", mk(le32(0), ints(2, 1)), i32, 1, "cannot be a i32"},
		{"bad metadata", segment(node(eListView, []byte{0x08}, nil, pnode(0), pnode(1), pnode(2)), elems), i32l, 1, "truncated"},
		{"huge elements", segment(node(eListView, pb(1<<63, 6, 5), nil, pnode(0), pnode(1), pnode(2)), elems), i32l, 1, "too large"},
		{"float offsets", segment(node(eListView, pb(4, 9, 5), nil, pnode(0), pnode(1), pnode(2)), elems), i32l, 1, "offset_ptype"},
		{"float sizes", segment(node(eListView, pb(4, 6, 9), nil, pnode(0), pnode(1), pnode(2)), elems), i32l, 1, "size_ptype"},
		{"a buffer", segment(node(eListView, meta, []uint16{0}, pnode(0), pnode(1), pnode(2)), elems), i32l, 1, "no buffer"},
		{"two children", segment(node(eListView, meta, nil, pnode(0), pnode(1)), elems), i32l, 1, "3 or 4 children"},
		{"elements of the wrong length", segment(node(eListView, pb(3, 6, 5), nil, pnode(0), pnode(1), pnode(2)), elems, buf{data: le32(0)}, buf{data: ints(2, 0)}), i32l, 1, "elements"},
		{"offsets of the wrong length", mk(le32(0, 1), ints(2, 1)), i32l, 1, "offsets"},
		{"sizes of the wrong length", mk(le32(0), ints(2, 1, 2)), i32l, 1, "sizes"},
		{"offset past the elements", mk(le32(5), ints(2, 0)), i32l, 1, "from 5"},
		{"size past the elements", mk(le32(3), ints(2, 2)), i32l, 1, "2 elements from 3"},
		{"negative size", mk(le32(0), ints(2, -1)), i32l, 1, "negative"},
		{"validity of the wrong length", mk(le32(0), ints(2, 1), bnode(3)), i32l, 1, "validity"},
	})
}

func TestFixedSizeList(t *testing.T) {
	seg := segment(node(eFSL, nil, nil, pnode(0)), buf{data: le32(1, 2, 3, 4)})
	a := mustDecode(t, seg, i32fs2, 2)
	if a.Elems.Len != 4 || a.Len != 2 {
		t.Fatalf("a = %+v", a)
	}
	seg = segment(node(eFSL, nil, nil, pnode(0), bnode(1)), buf{data: le32(1, 2, 3, 4)}, buf{data: []byte{0b10}})
	if a = mustDecode(t, seg, DType{Kind: FixedSizeList, Elem: &i32, Size: 2, Nullable: true}, 2); !a.Null(0) || a.Null(1) {
		t.Fatal("validity is wrong")
	}
	// Size 0: no element at all.
	seg = segment(node(eFSL, nil, nil, pnode(0)), buf{})
	if a = mustDecode(t, seg, DType{Kind: FixedSizeList, Elem: &i32}, 3); a.Len != 3 || a.Elems.Len != 0 {
		t.Fatalf("a = %+v", a)
	}
	els := buf{data: le32(1, 2, 3, 4)}
	runErrCases(t, []errCase{
		{"wrong type", segment(node(eFSL, nil, nil, pnode(0)), els), i32l, 2, "cannot be a list"},
		{"metadata", segment(node(eFSL, []byte{1}, nil, pnode(0)), els), i32fs2, 2, "no metadata"},
		{"a buffer", segment(node(eFSL, nil, []uint16{0}, pnode(0)), els), i32fs2, 2, "no buffer"},
		{"no child", segment(node(eFSL, nil, nil)), i32fs2, 2, "1 or 2 children"},
		{"too many", segment(node(eFSL, nil, nil, pnode(0)), els), DType{Kind: FixedSizeList, Elem: &i32, Size: 1 << 40}, 1 << 40, "too many"},
		{"elements of the wrong length", segment(node(eFSL, nil, nil, pnode(0)), els), i32fs2, 3, "elements"},
		{"validity of the wrong length", segment(node(eFSL, nil, nil, pnode(0), bnode(1)), els, buf{data: []byte{}}), i32fs2, 2, "validity"},
	})
}

func TestMap(t *testing.T) {
	entry := structOf(false, i32, i32)
	mt := DType{Kind: Map, Elem: &entry}
	lv := node(eListView, pb(3, 6, 6), nil, node(eStruct, nil, nil, pnode(0), pnode(1)), pnode(2), pnode(3))
	bufs := []buf{{data: le32(1, 2, 3)}, {data: le32(10, 20, 30)}, {data: le32(0, 2)}, {data: le32(2, 1)}}
	a := mustDecode(t, segment(node(eMap, nil, nil, lv), bufs...), mt, 2)
	if a.Type.Kind != Map || a.Starts[1] != 2 || a.Ends[1] != 3 || a.Elems.Fields[1] == nil {
		t.Fatalf("a = %+v", a)
	}
	notView := node(eList, pb(3, 6), nil, node(eStruct, nil, nil, pnode(0), pnode(1)), pnode(4))
	runErrCases(t, []errCase{
		{"wrong type", segment(node(eMap, nil, nil, lv), bufs...), i32, 2, "cannot be a i32"},
		{"metadata", segment(node(eMap, []byte{1}, nil, lv), bufs...), mt, 2, "no metadata"},
		{"a buffer", segment(node(eMap, nil, []uint16{0}, lv), bufs...), mt, 2, "no buffer"},
		{"no child", segment(node(eMap, nil, nil)), mt, 2, "1 child"},
		{"not a list view", segment(node(eMap, nil, nil, notView), append(bufs[:4:4], buf{data: le32(0, 2, 3)})...), mt, 2, "want vortex.listview"},
		{"bad entries", segment(node(eMap, nil, nil, lv), bufs[:2]...), mt, 2, "entries"},
		{"unknown encoding", segment(node(99, nil, nil, lv), bufs...), mt, 2, "encoding 99"},
		{"unknown encoding of the child", segment(node(eMap, nil, nil, node(99, nil, nil)), bufs...), mt, 2, "encoding 99"},
	})
}

func TestDecimal(t *testing.T) {
	// i8 to i256 values, -2 each.
	for vt, w := range decimalWidths {
		vals := cat(minusTwo(w), minusTwo(w))
		a := mustDecode(t, segment(node(eDecimal, pb(uint64(vt)), []uint16{0}), buf{data: vals}), dec, 2)
		if a.Width != w || len(a.Data) != 2*w || a.Validity != nil {
			t.Fatalf("width %d: %+v", w, a)
		}
	}
	seg := segment(node(eDecimal, pb(3), []uint16{0}, bnode(1)), buf{data: le64(1, 2)}, buf{data: []byte{0b10}})
	if a := mustDecode(t, seg, decn, 2); !a.Null(0) || a.Null(1) {
		t.Fatal("validity is wrong")
	}
	vals := buf{data: le64(1, 2)}
	runErrCases(t, []errCase{
		{"wrong type", segment(node(eDecimal, pb(3), []uint16{0}), vals), i32, 2, "cannot be a i32"},
		{"bad metadata", segment(node(eDecimal, []byte{0x08}, []uint16{0}), vals), dec, 2, "truncated"},
		{"unknown values_type", segment(node(eDecimal, pb(6), []uint16{0}), vals), dec, 2, "values_type is 6"},
		{"no buffer", segment(node(eDecimal, pb(3), nil)), dec, 2, "expected 1 buffer"},
		{"missing buffer", segment(node(eDecimal, pb(3), []uint16{4}), vals), dec, 2, "buffer 0 is buffer 4"},
		{"buffer too short", segment(node(eDecimal, pb(3), []uint16{0}), buf{data: le64(1)}), dec, 2, "buffer of 8 bytes"},
		{"buffer too long", segment(node(eDecimal, pb(3), []uint16{0}), vals), dec, 1, "buffer of 16 bytes"},
		{"partial value", segment(node(eDecimal, pb(3), []uint16{0}), buf{data: make([]byte, 12)}), dec, 2, "buffer of 12 bytes"},
		{"two children", segment(node(eDecimal, pb(3), []uint16{0}, bnode(0), bnode(0)), vals), dec, 2, "0 or 1 child"},
	})
}

// minusTwo is -2 on w bytes.
func minusTwo(w int) []byte {
	b := make([]byte, w)
	for i := range b {
		b[i] = 0xff
	}
	b[0] = 0xfe
	return b
}

func TestConstantNested(t *testing.T) {
	a := mustDecode(t, constSeg(sStr("héllo")), utf8t, 3)
	if str(a, 0) != "héllo" || str(a, 2) != "héllo" || a.Len != 3 {
		t.Fatalf("a = %+v", a)
	}
	a = mustDecode(t, constSeg(sBytes([]byte{0, 1})), bint, 2)
	if str(a, 1) != "\x00\x01" {
		t.Fatalf("binary = %+v", a)
	}
	a = mustDecode(t, constSeg(sBytes([]byte("txt"))), utf8t, 1)
	if str(a, 0) != "txt" {
		t.Fatalf("utf8 from bytes = %+v", a)
	}
	a = mustDecode(t, constSeg(sBytes([]byte{0xfe, 0xff})), dec, 3)
	if a.Width != 2 || len(a.Data) != 6 {
		t.Fatalf("decimal = %+v", a)
	}
	// Lists, of ints and of strings, and a fixed-size one.
	a = mustDecode(t, constSeg(sList(sInt(7), sInt(8))), i32l, 3)
	if a.Elems.Len != 6 || a.Starts[2] != 4 || a.Ends[2] != 6 || i32s(t, a.Elems)[5] != 8 {
		t.Fatalf("list = %+v", a)
	}
	a = mustDecode(t, constSeg(sList(sStr("a"), sStr("bc"))), strl, 2)
	if str(a.Elems, 3) != "bc" {
		t.Fatalf("string list = %+v", a)
	}
	a = mustDecode(t, constSeg(sList(sInt(7), sInt(8))), i32fs2, 2)
	if a.Elems.Len != 4 {
		t.Fatalf("fsl = %+v", a)
	}
	a = mustDecode(t, constSeg(sList()), i32l, 2)
	if a.Elems.Len != 0 || a.Ends[1] != 0 {
		t.Fatalf("empty list = %+v", a)
	}
	// Nulls of every type.
	for _, dt := range []DType{
		{Kind: Utf8, Nullable: true}, {Kind: Binary, Nullable: true}, {Kind: Decimal, Nullable: true},
		{Kind: List, Elem: &i32, Nullable: true}, {Kind: List, Elem: &utf8t, Nullable: true},
		{Kind: FixedSizeList, Elem: &i32, Size: 2, Nullable: true}, {Kind: FixedSizeList, Elem: &i32, Nullable: true},
		{Kind: Ext, Storage: &DType{Kind: Utf8, Nullable: true}},
		{Kind: Map, Elem: &DType{Kind: Prim, PType: I8}, Nullable: true},
		{Kind: List, Elem: &DType{Kind: Ext, Storage: &i32}, Nullable: true},
	} {
		a = mustDecode(t, constSeg(sNull()), dt, 3)
		if !a.Null(0) || !a.Null(2) || a.Len != 3 {
			t.Fatalf("%s: %+v", dt, a)
		}
	}
	sl := DType{Kind: List, Elem: &DType{Kind: List, Elem: &i32}}
	a = mustDecode(t, constSeg(sList(sList(sInt(1)), sList())), sl, 2)
	if a.Elems.Len != 4 || a.Elems.Elems.Len != 2 || a.Elems.Ends[3] != 2 {
		t.Fatalf("list of lists = %+v", a)
	}
	runErrCases(t, []errCase{
		{"int for a string", constSeg(sInt(1)), utf8t, 1, "cannot be a utf8"},
		{"string for a binary", constSeg(sStr("a")), bint, 1, "cannot be a binary"},
		{"invalid utf8", constSeg(sBytes([]byte{0xff})), utf8t, 1, "cannot be a utf8"},
		{"string for a decimal", constSeg(sStr("a")), dec, 1, "cannot be a decimal"},
		{"decimal of 3 bytes", constSeg(sBytes([]byte{1, 2, 3})), dec, 1, "3 bytes"},
		{"int for a list", constSeg(sInt(1)), i32l, 1, "cannot be a list"},
		{"list of 3 for a fixed-size list of 2", constSeg(sList(sInt(1), sInt(2), sInt(3))), i32fs2, 1, "3 elements"},
		{"bad element", constSeg(sList(sStr("a"))), i32l, 1, "element 0"},
		{"too many", constSeg(sList(sInt(1), sInt(2))), DType{Kind: List, Elem: &i32}, 1 << 62, "too many"},
		{"null for a non-nullable string", constSeg(sNull()), utf8t, 1, "cannot be a utf8"},
		{"null fixed-size list too big", constSeg(sNull()), DType{Kind: FixedSizeList, Elem: &i32, Size: 1 << 40, Nullable: true}, 1 << 40, "too many"},
		{"null struct elements", constSeg(sNull()), DType{Kind: List, Elem: &DType{Kind: Struct}, Nullable: true}, 1, "unsupported"},
		{"null map", constSeg(sNull()), DType{Kind: Map, Elem: &DType{Kind: Struct}, Nullable: true}, 1, "unsupported"},
		{"map", constSeg(sList()), DType{Kind: Map, Elem: &i32}, 1, "unsupported"},
	})
}

func TestSliceAndConcat(t *testing.T) {
	two := func(dt DType, a, b []byte) []*Array {
		return []*Array{
			mustDecode(t, constSeg(a), dt, 2),
			mustDecode(t, constSeg(b), dt, 3),
		}
	}
	// Strings.
	s := Concat(utf8t, two(utf8t, sStr("ab"), sStr("c")))
	if s.Len != 5 || str(s, 1) != "ab" || str(s, 2) != "c" || str(s, 4) != "c" {
		t.Fatalf("strings = %+v", s)
	}
	if sl := s.Slice(1, 3); str(sl, 0) != "ab" || str(sl, 2) != "c" || sl.Len != 3 {
		t.Fatalf("slice = %+v", sl)
	}
	// Lists.
	l := Concat(i32l, two(i32l, sList(sInt(1)), sList(sInt(2), sInt(3))))
	if l.Elems.Len != 8 || l.Starts[2] != 2 || l.Ends[4] != 8 || i32s(t, l.Elems)[7] != 3 {
		t.Fatalf("lists = %+v", l)
	}
	if sl := l.Slice(2, 2); sl.Starts[0] != 2 || sl.Elems.Len != 8 {
		t.Fatalf("list slice = %+v", sl)
	}
	// Fixed-size lists: the slice slices the elements.
	f := Concat(i32fs2, two(i32fs2, sList(sInt(1), sInt(2)), sList(sInt(3), sInt(4))))
	if sl := f.Slice(1, 3); sl.Elems.Len != 6 || i32s(t, sl.Elems)[2] != 3 || sl.Len != 3 {
		t.Fatalf("fsl slice = %+v", sl)
	}
	// Nullable and valid parts.
	n := Concat(utf8n, []*Array{
		mustDecode(t, constSeg(sNull()), utf8n, 1),
		mustDecode(t, constSeg(sStr("x")), utf8n, 1),
	})
	if !n.Null(0) || n.Null(1) {
		t.Fatal("validity is wrong")
	}
	// Decimals of different widths are widened, with their sign.
	d := Concat(dec, []*Array{
		mustDecode(t, constSeg(sBytes([]byte{0xfe})), dec, 1),
		mustDecode(t, constSeg(sBytes([]byte{5, 0, 0, 0})), dec, 2),
		mustDecode(t, constSeg(sBytes([]byte{0xfe, 0xff})), dec, 1),
	})
	if d.Width != 4 || string(d.Data) != "\xfe\xff\xff\xff\x05\x00\x00\x00\x05\x00\x00\x00\xfe\xff\xff\xff" {
		t.Fatalf("decimals = %v", d.Data)
	}
	if sl := d.Slice(1, 2); sl.Len != 2 || sl.Data[0] != 5 {
		t.Fatalf("decimal slice = %+v", sl)
	}
	// Maps and a list of structs share the list code.
	entry := structOf(false, i32)
	mt := DType{Kind: Map, Elem: &entry}
	m := mustDecode(t, segment(node(eMap, nil, nil, node(eListView, pb(1, 6, 6), nil, node(eStruct, nil, nil, pnode(0)), pnode(1), pnode(2))),
		buf{data: le32(1)}, buf{data: le32(0)}, buf{data: le32(1)}), mt, 1)
	mm := Concat(mt, []*Array{m, m})
	if mm.Len != 2 || mm.Starts[1] != 1 || mm.Elems.Len != 2 {
		t.Fatalf("maps = %+v", mm)
	}
}
