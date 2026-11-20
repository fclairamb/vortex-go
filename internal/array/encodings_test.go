package array

import (
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

type errCase struct {
	name string
	seg  []byte
	dt   DType
	rows int
	want string
}

func runErrCases(t *testing.T, cases []errCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.seg, c.dt, c.rows, c.want) })
	}
}

func TestNull(t *testing.T) {
	a := mustDecode(t, segment(node(eNull, nil, nil)), nul, 5)
	if a.Len != 5 || !a.Null(4) {
		t.Fatalf("a = %+v", a)
	}
	runErrCases(t, []errCase{
		{"metadata", segment(node(eNull, []byte{1}, nil)), nul, 1, "no metadata"},
		{"wrong type", segment(node(eNull, nil, nil)), i32, 1, "cannot be a i32"},
	})
}

func TestBool(t *testing.T) {
	// Bits 1,0,1,1,0 from bit offset 3 of 0b01101_000 | 0b..., plus a validity.
	bits := []byte{0b10110_000 | 0b101, 0}
	seg := segment(node(eBool, []byte{0x08, 0x03}, []uint16{0}), buf{data: bits})
	a := mustDecode(t, seg, bl, 5)
	if a.Bits.Off != 3 || a.Len != 5 {
		t.Fatalf("a = %+v", a)
	}
	// With a validity child, itself a bool array.
	seg = segment(node(eBool, nil, []uint16{0}, node(eBool, nil, []uint16{1})), buf{data: []byte{0b11}}, buf{data: []byte{0b01}})
	a = mustDecode(t, seg, DType{Kind: Bool, Nullable: true}, 2)
	if a.Null(0) || !a.Null(1) {
		t.Fatalf("validity = %v %v", a.Null(0), a.Null(1))
	}
	// Metadata with an unknown field and a known one of another wire type are ignored.
	seg = segment(node(eBool, []byte{0x10, 0x07, 0x0a, 0x00}, []uint16{0}), buf{data: []byte{1}})
	mustDecode(t, seg, bl, 8)
	one := buf{data: []byte{1}}
	runErrCases(t, []errCase{
		{"offset 8", segment(node(eBool, []byte{0x08, 0x08}, []uint16{0}), one), bl, 1, "bit offset 8"},
		{"bad metadata", segment(node(eBool, []byte{0x08}, []uint16{0}), one), bl, 1, "truncated"},
		{"buffer too short", segment(node(eBool, nil, []uint16{0}), one), bl, 9, "cannot hold 9 bits"},
		{"offset eats the buffer", segment(node(eBool, []byte{0x08, 0x01}, []uint16{0}), one), bl, 8, "cannot hold 8 bits"},
		{"no buffer", segment(node(eBool, nil, nil)), bl, 1, "buffer 0 is missing"},
		{"wrong type", segment(node(eBool, nil, []uint16{0}), one), i32, 1, "cannot be a i32"},
		{"two children", segment(node(eBool, nil, []uint16{0}, node(eBool, nil, []uint16{0}), node(eBool, nil, []uint16{0})), one), bl, 1, "0 or 1 child"},
		{"validity too short", segment(node(eBool, nil, []uint16{0}, node(eBool, nil, []uint16{1})), buf{data: []byte{1, 1}}, one), bl, 9, "validity"},
	})
}

func TestPrimitive(t *testing.T) {
	a := mustDecode(t, primSeg(1, -2, 3), i32, 3)
	if got := i32s(t, a); got[0] != 1 || got[1] != -2 || got[2] != 3 || a.Validity != nil {
		t.Fatalf("values = %v", got)
	}
	seg := segment(node(ePrim, nil, []uint16{0}, node(eBool, nil, []uint16{1})), buf{data: le32(1, 2)}, buf{data: []byte{0b10}})
	a = mustDecode(t, seg, i32n, 2)
	if !a.Null(0) || a.Null(1) {
		t.Fatal("validity is wrong")
	}
	runErrCases(t, []errCase{
		{"metadata", segment(node(ePrim, []byte{1}, []uint16{0}), buf{data: le32(1)}), i32, 1, "no metadata"},
		{"buffer too short", primSeg(1, 2), i32, 3, "want 12"},
		{"buffer too long", primSeg(1, 2), i32, 1, "want 4"},
		{"wrong type", primSeg(1), bl, 1, "cannot be a bool"},
		{"validity of the wrong length", segment(node(ePrim, nil, []uint16{0}, node(eBool, nil, []uint16{1})), buf{data: le32(1, 2, 3, 4, 5, 6, 7, 8, 9)}, buf{data: []byte{1}}), i32n, 9, "validity"},
	})
}

func TestStruct(t *testing.T) {
	two := structOf(false, i32, bl)
	seg := segment(node(eStruct, nil, nil, node(ePrim, nil, []uint16{0}), node(eBool, nil, []uint16{1})),
		buf{data: le32(4, 5)}, buf{data: []byte{0b10}})
	a := mustDecode(t, seg, two, 2)
	if len(a.Fields) != 2 || i32s(t, a.Fields[0])[1] != 5 || a.Validity != nil {
		t.Fatalf("a = %+v", a)
	}
	// Nullable: the validity is child 0.
	seg = segment(node(eStruct, nil, nil, node(eBool, nil, []uint16{2}), node(ePrim, nil, []uint16{0}), node(eBool, nil, []uint16{1})),
		buf{data: le32(4, 5)}, buf{data: []byte{0b10}}, buf{data: []byte{0b01}})
	a = mustDecode(t, seg, structOf(true, i32, bl), 2)
	if a.Null(0) || !a.Null(1) {
		t.Fatal("validity is wrong")
	}
	one := buf{data: le32(1)}
	prim := node(ePrim, nil, []uint16{0})
	runErrCases(t, []errCase{
		{"metadata", segment(node(eStruct, []byte{1}, nil, prim), one), structOf(false, i32), 1, "no metadata"},
		{"wrong type", segment(node(eStruct, nil, nil), one), i32, 1, "cannot be a i32"},
		{"too many children", segment(node(eStruct, nil, nil, prim, prim, prim), one), structOf(false, i32), 1, "3 children, want 1 or 2"},
		{"too few children", segment(node(eStruct, nil, nil, prim), one), structOf(false, i32, i32), 1, "1 children"},
		{"field error", segment(node(eStruct, nil, nil, node(ePrim, nil, nil)), one), structOf(false, i32), 1, `field "a"`},
		{"validity error", segment(node(eStruct, nil, nil, node(ePrim, nil, nil), prim), one), structOf(true, i32), 1, "validity"},
	})
}

// chunkSeg is a chunked array of i32 chunks: the offsets, then one primitive per chunk.
func chunkSeg(offsets []uint64, chunks ...[]int32) []byte {
	kids := []fbtest.Tab{node(ePrim, nil, []uint16{0})}
	bufs := []buf{{data: le64(offsets...)}}
	for i, c := range chunks {
		kids = append(kids, node(ePrim, nil, []uint16{uint16(i + 1)}))
		bufs = append(bufs, buf{data: le32(c...)})
	}
	return segment(node(eChunked, nil, nil, kids...), bufs...)
}

func TestChunked(t *testing.T) {
	a := mustDecode(t, chunkSeg([]uint64{0, 2, 2, 5}, []int32{1, 2}, nil, []int32{3, 4, 5}), i32, 5)
	if got := i32s(t, a); len(got) != 5 || got[0] != 1 || got[4] != 5 {
		t.Fatalf("values = %v", got)
	}
	// One chunk is returned as is, none gives an empty array, of any type.
	if a = mustDecode(t, chunkSeg([]uint64{0, 1}, []int32{9}), i32, 1); i32s(t, a)[0] != 9 {
		t.Fatal("one chunk")
	}
	for _, dt := range []DType{i32, bl, nul, structOf(false, i32, bl)} {
		if a = mustDecode(t, chunkSeg([]uint64{0}), dt, 0); a.Len != 0 {
			t.Fatalf("%s: no chunk gives %+v", dt, a)
		}
	}
	runErrCases(t, []errCase{
		{"metadata", segment(node(eChunked, []byte{1}, nil)), i32, 0, "no metadata"},
		{"no offsets", segment(node(eChunked, nil, nil)), i32, 0, "chunk offsets as its first child"},
		{"bad offsets", segment(node(eChunked, nil, nil, node(ePrim, nil, nil))), i32, 0, "chunk offsets"},
		{"offsets decrease", chunkSeg([]uint64{0, 3, 2}, []int32{1, 2, 3}, nil), i32, 2, "out of order"},
		{"offsets past the rows", chunkSeg([]uint64{0, 4}, []int32{1, 2, 3, 4}), i32, 3, "out of order, or past"},
		{"rows do not add up", chunkSeg([]uint64{0, 1}, []int32{1}), i32, 2, "chunks hold 1 rows, want 2"},
		{"chunk error", chunkSeg([]uint64{0, 2}, []int32{1}), i32, 2, "chunk 0"},
	})
}

func TestChunkedConcatsEveryShape(t *testing.T) {
	// Two chunks of bool with validity, of null, of struct, and of ext.
	boolChunk := func(bits, valid byte, b0 uint16) fbtest.Tab {
		return node(eBool, nil, []uint16{b0}, node(eBool, nil, []uint16{b0 + 1}))
	}
	seg := segment(node(eChunked, nil, nil, node(ePrim, nil, []uint16{0}), boolChunk(0, 0, 1), boolChunk(0, 0, 3)),
		buf{data: le64(0, 2, 4)}, buf{data: []byte{0b01}}, buf{data: []byte{0b10}}, buf{data: []byte{0b10}}, buf{data: []byte{0b11}})
	a := mustDecode(t, seg, DType{Kind: Bool, Nullable: true}, 4)
	if !a.Bits.Get(0) || a.Bits.Get(1) || a.Bits.Get(2) || !a.Bits.Get(3) || !a.Null(0) || a.Null(1) || a.Null(2) || a.Null(3) {
		t.Fatalf("bool chunks: %+v", a)
	}
	// null chunks
	seg = segment(node(eChunked, nil, nil, node(ePrim, nil, []uint16{0}), node(eNull, nil, nil), node(eNull, nil, nil)), buf{data: le64(0, 1, 3)})
	if a = mustDecode(t, seg, nul, 3); a.Len != 3 || !a.Null(2) {
		t.Fatalf("null chunks: %+v", a)
	}
	// struct chunks, and ext chunks (the storage is concatenated).
	st := node(eStruct, nil, nil, node(ePrim, nil, []uint16{1}))
	st2 := node(eStruct, nil, nil, node(ePrim, nil, []uint16{2}))
	seg = segment(node(eChunked, nil, nil, node(ePrim, nil, []uint16{0}), st, st2), buf{data: le64(0, 1, 2)}, buf{data: le32(1)}, buf{data: le32(2)})
	if a = mustDecode(t, seg, structOf(false, i32), 2); i32s(t, a.Fields[0])[1] != 2 {
		t.Fatalf("struct chunks: %+v", a)
	}
	ext := DType{Kind: Ext, Storage: &i32}
	e1 := node(eExt, nil, nil, node(ePrim, nil, []uint16{1}))
	e2 := node(eExt, nil, nil, node(ePrim, nil, []uint16{2}))
	seg = segment(node(eChunked, nil, nil, node(ePrim, nil, []uint16{0}), e1, e2), buf{data: le64(0, 1, 2)}, buf{data: le32(1)}, buf{data: le32(2)})
	if a = mustDecode(t, seg, ext, 2); i32s(t, a)[1] != 2 {
		t.Fatalf("ext chunks: %+v", a)
	}
}

func TestChunkedKeepsSkippedFields(t *testing.T) {
	inner := func(b uint16) fbtest.Tab {
		return node(eStruct, nil, nil, node(eAlp, nil, nil), node(ePrim, nil, []uint16{b}))
	}
	seg := segment(node(eStruct, nil, nil, node(eChunked, nil, nil, node(ePrim, nil, []uint16{0}), inner(1), inner(2))),
		buf{data: le64(0, 1, 2)}, buf{data: le32(1)}, buf{data: le32(2)})
	opt := Options{Tolerate: func(string, error) bool { return true }}
	a, err := Decode(seg, specs, structOf(false, structOf(false, i32, i32)), 2, opt)
	if err != nil {
		t.Fatal(err)
	}
	chunked := a.Fields[0]
	if chunked.Fields[0].Skipped == nil || chunked.Fields[0].Len != 2 || i32s(t, chunked.Fields[1])[1] != 2 {
		t.Fatalf("a = %+v", chunked)
	}
}

func TestExt(t *testing.T) {
	ext := DType{Kind: Ext, Storage: &i32}
	seg := segment(node(eExt, nil, nil, node(ePrim, nil, []uint16{0})), buf{data: le32(8)})
	if a := mustDecode(t, seg, ext, 1); i32s(t, a)[0] != 8 {
		t.Fatal("storage")
	}
	runErrCases(t, []errCase{
		{"metadata", segment(node(eExt, []byte{1}, nil)), ext, 1, "no metadata"},
		{"wrong type", seg, i32, 1, "cannot be a i32"},
		{"no storage", segment(node(eExt, nil, nil)), ext, 1, "expected 1 child"},
	})
}

func TestMasked(t *testing.T) {
	prim := node(ePrim, nil, []uint16{0})
	vals := buf{data: le32(1, 2)}
	seg := segment(node(eMasked, nil, nil, prim, node(eBool, nil, []uint16{1})), vals, buf{data: []byte{0b01}})
	a := mustDecode(t, seg, i32n, 2)
	if a.Null(0) || !a.Null(1) || !a.Type.Nullable {
		t.Fatalf("a = %+v", a)
	}
	// Without a validity child every row is valid.
	if a = mustDecode(t, segment(node(eMasked, nil, nil, prim), vals), i32n, 2); a.Validity != nil {
		t.Fatal("all valid")
	}
	runErrCases(t, []errCase{
		{"metadata", segment(node(eMasked, []byte{1}, nil, prim), vals), i32n, 2, "no metadata"},
		{"buffer", segment(node(eMasked, nil, []uint16{0}, prim), vals), i32n, 2, "no buffer"},
		{"no child", segment(node(eMasked, nil, nil)), i32n, 2, "1 or 2 children"},
		{"three children", segment(node(eMasked, nil, nil, prim, prim, prim), vals), i32n, 2, "1 or 2 children"},
		{"values error", segment(node(eMasked, nil, nil, node(ePrim, nil, nil))), i32n, 2, "buffer 0 is missing"},
		{"validity error", segment(node(eMasked, nil, nil, prim, node(eBool, nil, nil)), vals), i32n, 2, "validity"},
	})
}

func TestByteBool(t *testing.T) {
	seg := segment(node(eByteBool, nil, []uint16{0}), buf{data: []byte{1, 0, 7}})
	a := mustDecode(t, seg, bl, 3)
	if !a.Bits.Get(0) || a.Bits.Get(1) || !a.Bits.Get(2) {
		t.Fatalf("bits = %+v", a.Bits)
	}
	seg = segment(node(eByteBool, nil, []uint16{0}, node(eBool, nil, []uint16{1})), buf{data: []byte{1, 1}}, buf{data: []byte{0b01}})
	if a = mustDecode(t, seg, DType{Kind: Bool, Nullable: true}, 2); !a.Null(1) {
		t.Fatal("validity")
	}
	one := buf{data: []byte{1}}
	runErrCases(t, []errCase{
		{"metadata", segment(node(eByteBool, []byte{1}, []uint16{0}), one), bl, 1, "no metadata"},
		{"wrong type", segment(node(eByteBool, nil, []uint16{0}), one), i32, 1, "cannot be a i32"},
		{"too short", segment(node(eByteBool, nil, []uint16{0}), one), bl, 2, "buffer of 1 bytes"},
		{"no buffer", segment(node(eByteBool, nil, nil)), bl, 1, "buffer 0 is missing"},
		{"validity error", segment(node(eByteBool, nil, []uint16{0}, node(eBool, nil, nil)), one), bl, 1, "validity"},
	})
}

func TestSliceAndNull(t *testing.T) {
	seg := segment(node(eStruct, nil, nil, node(eBool, nil, []uint16{2}), node(ePrim, nil, []uint16{0}, node(eBool, nil, []uint16{1})), node(eBool, nil, []uint16{3})),
		buf{data: le32(0, 1, 2, 3)}, buf{data: []byte{0b1011}}, buf{data: []byte{0b0111}}, buf{data: []byte{0b0101}})
	dt := structOf(true, DType{Kind: Prim, PType: I32, Nullable: true}, bl)
	a := mustDecode(t, seg, dt, 4)
	s := a.Slice(1, 2).Slice(1, 1) // row 2 of the original
	if s.Len != 1 || i32s(t, s.Fields[0])[0] != 2 || !s.Fields[0].Null(0) || s.Null(0) || !s.Fields[1].Bits.Get(0) {
		t.Fatalf("slice = %+v", s)
	}
	if a.Slice(1, 1).Fields[0].Null(0) || !a.Null(3) {
		t.Fatal("nulls are wrong after slicing")
	}
	skipped := &Array{Skipped: errors2, Len: 4}
	if sk := skipped.Slice(1, 2); sk.Len != 2 || sk.Skipped == nil {
		t.Fatalf("skipped slice = %+v", sk)
	}
}
