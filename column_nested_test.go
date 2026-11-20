package vortex

import (
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
)

var (
	tUtf8   = Type{kind: Utf8}
	tInt32  = Type{kind: Int32}
	aInt32  = array.DType{Kind: array.Prim, PType: array.I32}
	aUtf8   = array.DType{Kind: array.Utf8}
	aInt32s = &array.Array{Type: aInt32, Len: 4, Data: []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 4, 0, 0, 0}}
)

// strArray is the strings s, the third of them null when null is set.
func strArray(null bool, s ...string) *array.Array {
	a := &array.Array{Type: aUtf8, Len: len(s)}
	for _, v := range s {
		a.Starts = append(a.Starts, len(a.Data))
		a.Data = append(a.Data, v...)
		a.Ends = append(a.Ends, len(a.Data))
	}
	if null {
		a.Validity = &array.Bitmap{Bytes: []byte{0b011}}
	}
	return a
}

func wantPanic(t *testing.T, name, want string, f func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("%s: no panic", name)
		} else if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Errorf("%s: panic %v, want %q", name, r, want)
		}
	}()
	f()
}

func TestColumnBytes(t *testing.T) {
	c := &Column{arr: strArray(true, "ab", "", "xyz"), typ: tUtf8}
	if string(c.Bytes(0)) != "ab" || len(c.Bytes(1)) != 0 || string(c.Bytes(2)) != "xyz" || !c.Null(2) {
		t.Fatal("wrong bytes")
	}
	if b := c.Bytes(0); cap(b) != len(b) {
		t.Error("the view must not let append write into the next value")
	}
	wantPanic(t, "row", "row 3 out of range", func() { c.Bytes(3) })
	wantPanic(t, "type", "Bytes on a column of type i32", func() { (&Column{arr: aInt32s, typ: tInt32}).Bytes(0) })
	// A UUID is a fixed-size list of 16 bytes.
	u := make([]byte, 32)
	for i := range u {
		u[i] = byte(i)
	}
	uuid := &array.Array{Type: array.DType{Kind: array.FixedSizeList, Size: 16}, Len: 2,
		Elems: &array.Array{Type: array.DType{Kind: array.Prim, PType: array.U8}, Len: 32, Data: u}}
	uc := &Column{arr: uuid, typ: Type{kind: UUID}}
	if got := uc.Bytes(1); len(got) != 16 || got[0] != 16 || got[15] != 31 {
		t.Fatalf("uuid = %v", got)
	}
	allocs := testing.AllocsPerRun(100, func() { _ = c.Bytes(2); _ = uc.Bytes(1); _ = c.Null(0) })
	if allocs != 0 {
		t.Errorf("%v allocations", allocs)
	}
}

func decimalColumn(width, precision int, vals ...int64) *Column {
	a := &array.Array{Type: array.DType{Kind: array.Decimal}, Len: len(vals), Width: width}
	for _, v := range vals {
		d := DecimalFromInt64(v).le()
		a.Data = append(a.Data, d[:width]...)
	}
	return &Column{arr: a, typ: Type{kind: KindDecimal, precision: uint8(precision), scale: 2}}
}

func TestColumnDecimal(t *testing.T) {
	c := decimalColumn(4, 9, 5, -7)
	if v, ok := c.Decimal(1).Int64(); !ok || v != -7 {
		t.Fatalf("Decimal(1) = %d, %v", v, ok)
	}
	if c.Decimal(0).Text(2) != "0.05" {
		t.Fatalf("Text = %s", c.Decimal(0).Text(2))
	}
	wantPanic(t, "row", "out of range", func() { c.Decimal(2) })
	wantPanic(t, "type", "Decimal on a column of type i32", func() { (&Column{arr: aInt32s, typ: tInt32}).Decimal(0) })
	if allocs := testing.AllocsPerRun(100, func() { _, _ = c.Decimal(1).Int64() }); allocs != 0 {
		t.Errorf("%v allocations", allocs)
	}
}

func TestValuesDecimal(t *testing.T) {
	// Whatever the width, a precision of 18 or less is read as int64.
	for _, w := range []int{1, 2, 4, 8, 16, 32} {
		c := decimalColumn(w, 2, 5, -7, 0)
		got, err := Values[int64](c)
		if err != nil || len(got) != 3 || got[0] != 5 || got[1] != -7 || got[2] != 0 {
			t.Fatalf("width %d: %v, %v", w, got, err)
		}
	}
	if _, err := Values[int64](decimalColumn(8, 19, 1)); err == nil || !strings.Contains(err.Error(), "decimal(19,2)") {
		t.Fatalf("a precision of 19 is not an int64: %v", err)
	}
	if _, err := Values[int32](decimalColumn(8, 5, 1)); err == nil {
		t.Fatal("a decimal is not an int32")
	}
	// A value too wide for an int64 in a column that says it is narrow is corrupt.
	big := decimalColumn(16, 5, 1)
	big.arr.Data[15] = 1
	if _, err := Values[int64](big); err == nil || !strings.Contains(err.Error(), "does not fit") {
		t.Fatalf("a wide value: %v", err)
	}
}

func TestListOfListsOfStrings(t *testing.T) {
	inner := Type{kind: List, elem: &tUtf8}
	outer := Type{kind: List, elem: &inner}
	// [["a", "bc"], []], [["d"]], null, [] over the strings a, bc, d.
	strs := strArray(false, "a", "bc", "d")
	lists := &array.Array{Type: array.DType{Kind: array.List, Elem: &aUtf8}, Len: 3, Elems: strs,
		Starts: []int{0, 2, 2}, Ends: []int{2, 2, 3}}
	oa := &array.Array{Type: array.DType{Kind: array.List}, Len: 4, Elems: lists,
		Starts: []int{0, 2, 0, 0}, Ends: []int{2, 3, 0, 0}, Validity: &array.Bitmap{Bytes: []byte{0b1011}}}
	c := &Column{arr: oa, typ: outer}
	if !c.Null(2) || c.Null(3) {
		t.Fatal("validity is wrong")
	}
	if s, e := c.List(1); s != 2 || e != 3 {
		t.Fatalf("List(1) = %d, %d", s, e)
	}
	in := c.Elems()
	s, e := in.List(0)
	words := in.Elems()
	if e-s != 2 || string(words.Bytes(s+1)) != "bc" {
		t.Fatalf("inner list 0 = [%d, %d)", s, e)
	}
	if s, e = in.List(1); s != e {
		t.Fatalf("inner list 1 must be empty, is [%d, %d)", s, e)
	}
	if !c.Type().Elem().Equal(inner) || c.Elems().Type().Kind() != List {
		t.Fatal("types are wrong")
	}
	wantPanic(t, "elems", "Elems on a column of type utf8", func() { (&Column{arr: strs, typ: tUtf8}).Elems() })
	wantPanic(t, "list", "List on a column of type utf8", func() { (&Column{arr: strs, typ: tUtf8}).List(0) })
	wantPanic(t, "row", "out of range", func() { c.List(4) })
}

func TestNullableStructInNullableList(t *testing.T) {
	st := Type{kind: Struct, nullable: true, fields: []Field{{Name: "n", Type: tInt32}, {Name: "s", Type: tUtf8}}}
	lt := Type{kind: List, nullable: true, elem: &st}
	sa := &array.Array{
		Type: array.DType{Kind: array.Struct, Nullable: true}, Len: 3,
		Validity: &array.Bitmap{Bytes: []byte{0b101}}, // the middle one is null
		Fields:   []*array.Array{aInt32s.Slice(0, 3), strArray(false, "x", "y", "z")},
	}
	la := &array.Array{Type: array.DType{Kind: array.List}, Len: 2, Elems: sa, Starts: []int{0, 3}, Ends: []int{3, 3},
		Validity: &array.Bitmap{Bytes: []byte{0b01}}}
	c := &Column{arr: la, typ: lt}
	if c.Null(0) || !c.Null(1) {
		t.Fatal("list validity is wrong")
	}
	s, e := c.List(0)
	el := c.Elems()
	if s != 0 || e != 3 || el.Null(0) || !el.Null(1) || el.Null(2) {
		t.Fatalf("struct validity: [%d, %d)", s, e)
	}
	n, _ := Values[int32](el.Field(0))
	if n[2] != 3 || string(el.Field(1).Bytes(2)) != "z" {
		t.Fatal("fields are wrong")
	}
}

func TestMapColumn(t *testing.T) {
	mt := Type{kind: Map, elem: &tUtf8, value: &tInt32}
	entries := &array.Array{Type: array.DType{Kind: array.Struct}, Len: 3,
		Fields: []*array.Array{strArray(false, "a", "b", "c"), aInt32s.Slice(0, 3)}}
	ma := &array.Array{Type: array.DType{Kind: array.Map}, Len: 2, Elems: entries, Starts: []int{0, 2}, Ends: []int{2, 3}}
	c := &Column{arr: ma, typ: mt}
	s, e := c.List(1)
	el := c.Elems()
	vals, _ := Values[int32](el.Field(1))
	if s != 2 || e != 3 || string(el.Field(0).Bytes(2)) != "c" || vals[2] != 3 || el.Type().Kind() != Struct {
		t.Fatal("map entries are wrong")
	}
	if f := el.Type().Fields(); f[0].Name != "key" || !f[1].Type.Equal(tInt32) {
		t.Fatalf("entry type = %v", el.Type())
	}
}

func TestFixedSizeListColumn(t *testing.T) {
	ft := Type{kind: FixedSizeList, size: 2, elem: &tInt32}
	fa := &array.Array{Type: array.DType{Kind: array.FixedSizeList, Size: 2}, Len: 2, Elems: aInt32s}
	c := &Column{arr: fa, typ: ft}
	if s, e := c.List(1); s != 2 || e != 4 {
		t.Fatalf("List(1) = %d, %d", s, e)
	}
	wantPanic(t, "row", "out of range", func() { c.List(2) })
	if v, _ := Values[int32](c.Elems()); v[3] != 4 {
		t.Fatal("elements are wrong")
	}
}

func TestArrayType(t *testing.T) {
	lt := Type{kind: Map, elem: &tUtf8, value: &Type{kind: List, elem: &tInt32}}
	d := arrayType(Type{kind: Struct, fields: []Field{
		{Name: "u", Type: Type{kind: UUID, nullable: true}},
		{Name: "m", Type: lt},
		{Name: "d", Type: Type{kind: KindDecimal}},
		{Name: "b", Type: Type{kind: Binary}},
		{Name: "f", Type: Type{kind: FixedSizeList, size: 3, elem: &tInt32}},
	}})
	got := d.String()
	want := "struct{u: ext(fixed_size_list<u8, 16>?)?, m: map<struct{key: utf8, value: list<i32>}>, d: decimal, b: binary, f: fixed_size_list<i32, 3>}"
	if got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
}
