package array

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// protobuf builders for the DTypes of the metadata.

func vField(num int, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(num<<3)), v)
}

// tPrim is a primitive DType message.
func tPrim(p PType, nullable bool) []byte {
	return lenField(pbPrimitive, cat(vField(1, uint64(p)), vField(2, b2u(nullable))))
}

func b2u(b bool) uint64 { return uint64(b2i(b)) }

func tSimple(arm int, nullable bool) []byte { return lenField(arm, vField(1, b2u(nullable))) }

func tStruct(names []string, types [][]byte, nullable bool) []byte {
	var body []byte
	for _, n := range names {
		body = append(body, lenField(1, []byte(n))...)
	}
	for _, t := range types {
		body = append(body, lenField(2, t)...)
	}
	return lenField(pbStruct, append(body, vField(3, b2u(nullable))...))
}

func tExt(id string, storage, meta []byte) []byte {
	body := cat(lenField(1, []byte(id)), lenField(2, storage))
	if meta != nil {
		body = append(body, lenField(3, meta)...)
	}
	return lenField(pbExtension, body)
}

func mustType(t *testing.T, b []byte) DType {
	t.Helper()
	d, err := ParseDType(b)
	if err != nil {
		t.Fatalf("ParseDType: %v", err)
	}
	return d
}

func TestParseDType(t *testing.T) {
	i32proto := tPrim(I32, false)
	u8proto := tPrim(U8, false)
	cases := []struct {
		name string
		in   []byte
		want DType
	}{
		{"null", lenField(pbNull, nil), DType{Kind: Null, Nullable: true}},
		{"bool", tSimple(pbBool, true), DType{Kind: Bool, Nullable: true}},
		{"primitive", tPrim(F64, true), DType{Kind: Prim, PType: F64, Nullable: true}},
		{"decimal", lenField(pbDecimal, cat(vField(1, 10), vField(2, uint64(math.MaxUint32)), vField(3, 1))), DType{Kind: Decimal, Scale: -1, Nullable: true}},
		{"utf8", tSimple(pbUtf8, false), DType{Kind: Utf8}},
		{"binary", tSimple(pbBinary, true), DType{Kind: Binary, Nullable: true}},
		{"variant", tSimple(pbVariant, true), DType{Kind: Variant, Nullable: true}},
		{"struct", tStruct([]string{"a", "b"}, [][]byte{i32proto, tSimple(pbBool, true)}, true), DType{Kind: Struct, Nullable: true, Fields: []Field{
			{Name: "a", Type: i32}, {Name: "b", Type: DType{Kind: Bool, Nullable: true}}}}},
		{"list", lenField(pbList, cat(lenField(1, i32proto), vField(2, 1))), DType{Kind: List, Nullable: true, Elem: &i32}},
		{"fixed size list", lenField(pbFixedSizeList, cat(lenField(1, i32proto), vField(2, 3))), DType{Kind: FixedSizeList, Size: 3, Elem: &i32}},
		{"map", lenField(pbMap, cat(lenField(1, u8proto), lenField(2, i32proto), vField(3, 1), vField(4, 1))), DType{Kind: Map, Nullable: true, Elem: &DType{Kind: Struct, Fields: []Field{
			{Name: "key", Type: DType{Kind: Prim, PType: U8}}, {Name: "value", Type: i32}}}}},
		{"extension", tExt("vortex.uuid", i32proto, nil), DType{Kind: Ext, Label: "vortex.uuid", Storage: &i32}},
		{"date", tExt("vortex.date", tPrim(I32, true), []byte{4}), DType{Kind: Ext, Label: "vortex.date", Nullable: true, Storage: &i32n}},
		{"timestamp", tExt("vortex.timestamp", tPrim(I64, false), []byte{1, 0, 0}), DType{Kind: Ext, Label: "vortex.timestamp", Ticks: 1e6, Storage: &DType{Kind: Prim, PType: I64}}},
		{"time", tExt("vortex.time", i32proto, []byte{3}), DType{Kind: Ext, Label: "vortex.time", Ticks: 1, Storage: &i32}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseDType(c.in)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ParseDType = %+v, %v\nwant %+v", got, err, c.want)
			}
		})
	}
}

func TestParseDTypeErrors(t *testing.T) {
	deep := tSimple(pbBool, false)
	for range maxDepth + 1 {
		deep = lenField(pbList, lenField(1, deep))
	}
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"empty", nil, "has no type"},
		{"wire type", vField(pbBool, 1), "wire type 0"},
		{"truncated", []byte{0x12, 5}, "truncated"},
		{"truncated body", lenField(pbBool, []byte{0x08}), "truncated"},
		{"too deep", deep, "nested deeper"},
		{"unknown primitive", tPrim(11, false), "primitive type 11"},
		{"union", lenField(pbUnion, nil), "union"},
		{"list without element", lenField(pbList, vField(2, 1)), "field 1 is missing"},
		{"map without value", lenField(pbMap, lenField(1, tSimple(pbBool, false))), "field 2 is missing"},
		{"bad element", lenField(pbList, lenField(1, []byte{1})), "invalid field"},
		{"extension without storage", lenField(pbExtension, lenField(1, []byte("x"))), "field 2 is missing"},
		{"timestamp without a unit", tExt("vortex.timestamp", tPrim(I64, false), nil), "no valid unit"},
		{"timestamp with a bad unit", tExt("vortex.timestamp", tPrim(I64, false), []byte{9}), "no valid unit"},
		{"struct names and types", lenField(pbStruct, lenField(1, []byte("a"))), "1 names and 0 types"},
		{"struct name", lenField(pbStruct, cat(lenField(1, []byte{0xff}), lenField(2, tSimple(pbBool, false)))), "not valid UTF-8"},
		{"struct field", lenField(pbStruct, cat(lenField(1, []byte("a")), lenField(2, vField(pbBool, 1)))), `field "a"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseDType(c.in); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ParseDType error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestParseDTypeSkipsUnknownFields(t *testing.T) {
	got, err := ParseDType(cat(lenField(20, []byte{1, 2}), tSimple(pbBool, false), vField(21, 1)))
	if err != nil || got.Kind != Bool {
		t.Fatalf("ParseDType = %+v, %v", got, err)
	}
}

// Arrays built by hand.

func binArr(vals ...[]byte) *Array {
	a := &Array{Type: DType{Kind: Binary, Nullable: true}, Len: len(vals)}
	valid := make([]byte, (len(vals)+7)/8)
	anyNull := false
	for i, v := range vals {
		a.Starts = append(a.Starts, len(a.Data))
		a.Data = append(a.Data, v...)
		a.Ends = append(a.Ends, len(a.Data))
		if v == nil {
			anyNull = true
		} else {
			valid[i/8] |= 1 << (i % 8)
		}
	}
	if anyNull {
		a.Validity = &Bitmap{Bytes: valid}
	}
	return a
}

// primArr is an array of the type, from little-endian values; nil entries of null are null.
func primArr(t DType, null []bool, vals ...int64) *Array {
	a := &Array{Type: t, Len: len(vals), Data: ints(t.PType.Width(), vals...)}
	if null != nil {
		a.Validity = &Bitmap{Bytes: make([]byte, 1)}
		for i, n := range null {
			if !n {
				a.Validity.Bytes[0] |= 1 << i
			}
		}
	}
	return a
}

func structArr(t DType, null []bool, fields ...*Array) *Array {
	a := &Array{Type: t, Fields: fields, Len: fields[0].Len}
	if null != nil {
		a.Validity = &Bitmap{Bytes: make([]byte, 1)}
		for i, n := range null {
			if !n {
				a.Validity.Bytes[0] |= 1 << i
			}
		}
	}
	return a
}

func utf8Arr(vals ...string) *Array {
	a := &Array{Type: utf8t, Len: len(vals)}
	for _, v := range vals {
		a.Starts = append(a.Starts, len(a.Data))
		a.Data = append(a.Data, v...)
		a.Ends = append(a.Ends, len(a.Data))
	}
	return a
}

// testMeta is a metadata of the keys, with one-byte offsets.
func testMeta(keys ...string) []byte {
	out := []byte{1, byte(len(keys))}
	pos := 0
	var data []byte
	for _, k := range keys {
		out = append(out, byte(pos))
		pos += len(k)
		data = append(data, k...)
	}
	return append(append(out, byte(pos)), data...)
}

// int8v is an int8 Variant value.
func int8v(v int8) []byte { return []byte{3 << 2, byte(v)} }

// objectv is an object of one-byte ids and offsets.
func objectv(ids []byte, vals ...[]byte) []byte {
	out := append([]byte{2, byte(len(ids))}, ids...)
	var body []byte
	for _, v := range vals {
		out = append(out, byte(len(body)))
		body = append(body, v...)
	}
	return append(append(out, byte(len(body))), body...)
}

func decText(unscaled []byte, scale int) any { return fmt.Sprintf("%x/%d", unscaled, scale) }

func variantAt(t *testing.T, a *Array, i int) any {
	t.Helper()
	v, err := a.VariantValue(i, decText)
	if err != nil {
		t.Fatalf("row %d: %v", i, err)
	}
	return v
}

func variantErr(t *testing.T, a *Array, i int, want string) {
	t.Helper()
	if _, err := a.VariantValue(i, decText); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("row %d error = %v, want %q", i, err, want)
	}
}

var variantT = DType{Kind: Variant, Nullable: true}

func TestVariantUnshredded(t *testing.T) {
	m := testMeta("a", "b")
	a := &Array{Type: variantT, Len: 4, Validity: &Bitmap{Bytes: []byte{0b0111}}, Var: &VariantData{
		Metadata: binArr(m, m, m, m),
		Value:    binArr(objectv([]byte{1}, int8v(5)), nil, int8v(-1), int8v(2)),
	}}
	if got := variantAt(t, a, 0); !reflect.DeepEqual(got, map[string]any{"b": int64(5)}) {
		t.Fatalf("row 0 = %#v", got)
	}
	// A null value is a missing one, a null row is nil.
	if variantAt(t, a, 1) != nil || variantAt(t, a, 3) != nil || !a.Null(3) {
		t.Fatal("rows 1 and 3 must be nil")
	}
	if got := variantAt(t, a, 2); got != int64(-1) {
		t.Fatalf("row 2 = %#v", got)
	}
	// A slice reads the same rows.
	s := a.Slice(2, 2)
	if got := variantAt(t, s, 0); got != int64(-1) || !s.Null(1) {
		t.Fatalf("slice row 0 = %#v", got)
	}
	// Damaged metadata and value.
	bad := &Array{Type: variantT, Len: 1, Var: &VariantData{Metadata: binArr([]byte{9}), Value: binArr(int8v(1))}}
	variantErr(t, bad, 0, "version 9")
	bad = &Array{Type: variantT, Len: 1, Var: &VariantData{Metadata: binArr(m), Value: binArr([]byte{3 << 2})}}
	variantErr(t, bad, 0, "cut short")
}

func TestVariantTypedValue(t *testing.T) {
	typed := mustType(t, tStruct([]string{"a", "b", "l"}, [][]byte{
		tStruct([]string{"value", "typed_value"}, [][]byte{tSimple(pbBinary, true), tPrim(I32, true)}, false),
		tStruct([]string{"typed_value"}, [][]byte{tSimple(pbUtf8, true)}, true),
		lenField(pbList, cat(lenField(1, tStruct([]string{"typed_value"}, [][]byte{tPrim(I8, true)}, false)), vField(2, 1))),
	}, true))
	m := testMeta("a", "b", "c")
	// Rows: 0 fully shredded; 1 partly, with the field c in the value; 2 missing;
	// 3 not shredded, a value; 4 the field a is in its value; 5 a null typed struct.
	aw := structArr(typed.Fields[0].Type, nil,
		binArr(nil, nil, nil, nil, int8v(99), nil),
		primArr(i32n, []bool{false, false, true, true, true, true}, 10, 20, 0, 0, 0, 0))
	bw := structArr(typed.Fields[1].Type, []bool{false, true, true, true, true, true}, utf8Arr("x", "", "", "", "", ""))
	elems := structArr(*typed.Fields[2].Type.Elem, []bool{false, true, false}, primArr(DType{Kind: Prim, PType: I8, Nullable: true}, []bool{false, false, false}, 1, 0, 3))
	list := &Array{Type: typed.Fields[2].Type, Len: 6, Elems: elems, Starts: []int{0, 0, 0, 0, 0, 0}, Ends: []int{3, 0, 0, 0, 0, 0}}
	ts := structArr(typed, []bool{false, false, false, true, false, true}, aw, bw, list)
	ts.Validity = &Bitmap{Bytes: []byte{0b010011}}
	ts.Len = 6
	a := &Array{Type: variantT, Len: 6, Var: &VariantData{
		Metadata:  binArr(m, m, m, m, m, m),
		Value:     binArr(nil, objectv([]byte{2}, int8v(7)), nil, int8v(5), objectv(nil), nil),
		Typed:     ts,
		TypedType: typed,
	}}
	want0 := map[string]any{"a": int64(10), "b": "x", "l": []any{int64(1), nil, int64(3)}}
	if got := variantAt(t, a, 0); !reflect.DeepEqual(got, want0) {
		t.Fatalf("row 0 = %#v", got)
	}
	if got := variantAt(t, a, 1); !reflect.DeepEqual(got, map[string]any{"a": int64(20), "c": int64(7), "l": []any{}}) {
		t.Fatalf("row 1 = %#v", got)
	}
	if got := variantAt(t, a, 2); got != nil {
		t.Fatalf("row 2 = %#v", got)
	}
	if got := variantAt(t, a, 3); got != int64(5) {
		t.Fatalf("row 3 = %#v", got)
	}
	// Row 4: the typed struct has a valid, empty field a, whose value holds 99.
	if got := variantAt(t, a, 4); !reflect.DeepEqual(got, map[string]any{"a": int64(99), "l": []any{}}) {
		t.Fatalf("row 4 = %#v", got)
	}
	if got := variantAt(t, a, 5); got != nil {
		t.Fatalf("row 5 = %#v", got)
	}
}

func TestVariantTypedValueNotWrapped(t *testing.T) {
	// A field that is a struct but not a wrapper is an object of its own.
	typed := mustType(t, tStruct([]string{"a"}, [][]byte{tStruct([]string{"c"}, [][]byte{tPrim(I32, true)}, false)}, false))
	inner := structArr(typed.Fields[0].Type, nil, primArr(i32N, nil, 4))
	a := &Array{Type: variantT, Len: 1, Var: &VariantData{Metadata: binArr(testMeta()), Typed: structArr(typed, nil, inner), TypedType: typed}}
	if got := variantAt(t, a, 0); !reflect.DeepEqual(got, map[string]any{"a": map[string]any{"c": int64(4)}}) {
		t.Fatalf("got %#v", got)
	}
}

func TestVariantTypedValueErrors(t *testing.T) {
	m := testMeta("a")
	wrapper := tStruct([]string{"typed_value"}, [][]byte{tPrim(U64, true)}, false)
	typed := mustType(t, tStruct([]string{"a"}, [][]byte{wrapper}, false))
	big := primArr(DType{Kind: Prim, PType: U64, Nullable: true}, nil, -1)
	mk := func(value *Array, typedArr *Array) *Array {
		return &Array{Type: variantT, Len: 1, Var: &VariantData{Metadata: binArr(m), Value: value, Typed: typedArr, TypedType: typed}}
	}
	ts := structArr(typed, nil, structArr(typed.Fields[0].Type, nil, big))
	variantErr(t, mk(nil, ts), 0, `field "a": the u64 value`)
	// A value and a typed_value, the value not being an object.
	ok := structArr(typed, nil, structArr(typed.Fields[0].Type, nil, primArr(DType{Kind: Prim, PType: U64, Nullable: true}, nil, 1)))
	variantErr(t, mk(binArr(int8v(1)), ok), 0, "not both objects")
	variantErr(t, mk(binArr(objectv([]byte{0}, int8v(1))), ok), 0, `field "a" is in both`)
	variantErr(t, mk(binArr([]byte{3 << 2}), ok), 0, "cut short")
	// A damaged element of a typed list.
	lt := mustType(t, lenField(pbList, lenField(1, tPrim(U64, true))))
	list := &Array{Type: lt, Len: 1, Elems: big, Starts: []int{0}, Ends: []int{1}}
	a := &Array{Type: variantT, Len: 1, Var: &VariantData{Metadata: binArr(m), Typed: list, TypedType: lt}}
	variantErr(t, a, 0, "element 0")
	// A typed fixed-size list.
	fl := mustType(t, lenField(pbFixedSizeList, cat(lenField(1, tPrim(I32, false)), vField(2, 2))))
	fa := &Array{Type: fl, Len: 1, Elems: primArr(i32, nil, 4, 5)}
	a = &Array{Type: variantT, Len: 1, Var: &VariantData{Metadata: binArr(m), Typed: fa, TypedType: fl}}
	if got := variantAt(t, a, 0); !reflect.DeepEqual(got, []any{int64(4), int64(5)}) {
		t.Fatalf("fixed size list = %#v", got)
	}
}

var i32N = DType{Kind: Prim, PType: I32, Nullable: true}

func logical(rows int, core, shredded *Array, t DType) *Array {
	return &Array{Type: variantT, Len: rows, Validity: core.Validity,
		Var: &VariantData{Core: core, Shredded: shredded, TypedType: t}}
}

func coreOf(values ...[]byte) *Array {
	m := testMeta("x", "y", "k", "nul", "q", "r")
	metas := make([][]byte, len(values))
	for i := range metas {
		metas[i] = m
	}
	return &Array{Type: variantT, Len: len(values), Var: &VariantData{Metadata: binArr(metas...), Value: binArr(values...)}}
}

func TestVariantLogicalPrimitive(t *testing.T) {
	core := coreOf(nil, int8v(8), nil, int8v(9))
	core.Validity = &Bitmap{Bytes: []byte{0b0111}}
	a := logical(4, core, primArr(i32N, []bool{false, true, true, false}, 1, 0, 0, 4), i32N)
	for i, want := range []any{int64(1), int64(8), nil, nil} {
		if got := variantAt(t, a, i); got != want {
			t.Errorf("row %d = %#v, want %#v", i, got, want)
		}
	}
	// A shredded value of a core that fails to decode.
	core = coreOf([]byte{3 << 2})
	a = logical(1, core, primArr(i32N, []bool{true}, 0), i32N)
	variantErr(t, a, 0, "cut short")
}

func TestVariantLogicalList(t *testing.T) {
	vt := DType{Kind: Variant, Nullable: true}
	lt := mustType(t, lenField(pbList, cat(lenField(1, tSimple(pbVariant, true)), vField(2, 1))))
	m := testMeta("x")
	elems := &Array{Type: vt, Len: 3, Validity: &Bitmap{Bytes: []byte{0b101}},
		Var: &VariantData{Metadata: binArr(m, m, m), Value: binArr(int8v(1), nil, []byte{3 << 2})}}
	list := &Array{Type: lt, Len: 2, Elems: elems, Starts: []int{0, 0}, Ends: []int{2, 3}}
	core := coreOf(nil, nil)
	a := logical(2, core, list, lt)
	if got := variantAt(t, a, 0); !reflect.DeepEqual(got, []any{int64(1), nil}) {
		t.Fatalf("row 0 = %#v", got)
	}
	variantErr(t, a, 1, "element 2")
	// A fixed-size list of plain values.
	ft := mustType(t, lenField(pbFixedSizeList, cat(lenField(1, tPrim(I16, false)), vField(2, 2))))
	fl := &Array{Type: ft, Len: 1, Elems: primArr(DType{Kind: Prim, PType: I16}, nil, -3, 4)}
	a = logical(1, coreOf(nil), fl, ft)
	if got := variantAt(t, a, 0); !reflect.DeepEqual(got, []any{int64(-3), int64(4)}) {
		t.Fatalf("fixed size list = %#v", got)
	}
}

func TestVariantLogicalObject(t *testing.T) {
	yT := tStruct([]string{"p", "q"}, [][]byte{tPrim(I32, true), tPrim(I32, true)}, true)
	rootT := mustType(t, tStruct([]string{"x", "y", "w", "z", "k"}, [][]byte{
		tPrim(I32, true), yT, tSimple(pbVariant, true), tSimple(pbUtf8, true), tPrim(U64, true)}, true))
	m := testMeta("x")
	yt := rootT.Fields[1].Type
	// Rows: 0 a merge; 1 the whole object null; 2 the nested variant and a string; 3 y is
	// not an object in the core; 4 a field that does not convert.
	rawY := objectv([]byte{4, 5}, int8v(7), int8v(8))
	core := coreOf(
		objectv([]byte{0, 1, 2, 3}, int8v(99), rawY, int8v(3), []byte{0}),
		objectv([]byte{2}, int8v(1)), nil, objectv([]byte{1}, int8v(5)), nil)
	x := primArr(i32N, []bool{false, true, true, false, true}, 1, 0, 0, 2, 0)
	y := structArr(yt, []bool{false, true, true, false, true},
		primArr(i32N, []bool{false, true, true, false, true}, 5, 0, 0, 6, 0),
		primArr(i32N, []bool{true, true, true, true, true}, 0, 0, 0, 0, 0))
	w := &Array{Type: variantT, Len: 5, Validity: &Bitmap{Bytes: []byte{0b00100}},
		Var: &VariantData{Metadata: binArr(m, m, m, m, m), Value: binArr(nil, nil, int8v(42), nil, nil)}}
	z := utf8Arr("", "", "s", "", "")
	z.Validity = &Bitmap{Bytes: []byte{0b00100}}
	k := primArr(DType{Kind: Prim, PType: U64, Nullable: true}, []bool{true, true, true, true, false}, 0, 0, 0, 0, -1)
	root := structArr(rootT, []bool{false, true, false, false, false}, x, y, w, z, k)
	root.Validity = &Bitmap{Bytes: []byte{0b11101}}
	a := logical(5, core, root, rootT)

	want0 := map[string]any{
		"x": int64(1), "y": map[string]any{"p": int64(5), "q": int64(7), "r": int64(8)}, "k": int64(3),
	}
	if got := variantAt(t, a, 0); !reflect.DeepEqual(got, want0) {
		t.Fatalf("row 0 = %#v", got)
	}
	if got := variantAt(t, a, 1); !reflect.DeepEqual(got, map[string]any{"k": int64(1)}) {
		t.Fatalf("row 1 = %#v", got)
	}
	if got := variantAt(t, a, 2); !reflect.DeepEqual(got, map[string]any{"w": int64(42), "z": "s"}) {
		t.Fatalf("row 2 = %#v", got)
	}
	if got := variantAt(t, a, 3); !reflect.DeepEqual(got, map[string]any{"x": int64(2), "y": map[string]any{"p": int64(6)}}) {
		t.Fatalf("row 3 = %#v", got)
	}
	variantErr(t, a, 4, `field "k": the u64 value`)
	// The core of the row does not decode: the object cannot be completed.
	a = logical(1, coreOf([]byte{3 << 2}), structArr(rootT, nil, x.Slice(0, 1), y.Slice(0, 1), w.Slice(0, 1), z.Slice(0, 1), k.Slice(0, 1)), rootT)
	variantErr(t, a, 0, "cut short")
}

// typedArr is a Variant array whose typed_value is a, of the protobuf DType.
func typedArr(t *testing.T, proto []byte, a *Array) *Array {
	t.Helper()
	m := testMeta("x")
	metas := make([][]byte, a.Len)
	for i := range metas {
		metas[i] = m
	}
	return &Array{Type: variantT, Len: a.Len, Var: &VariantData{Metadata: binArr(metas...), Typed: a, TypedType: mustType(t, proto)}}
}

func TestVariantLeaves(t *testing.T) {
	day := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	bitsOf := &Array{Type: bl, Len: 2, Bits: Bitmap{Bytes: []byte{0b10}}}
	dec := &Array{Type: DType{Kind: Decimal}, Len: 1, Width: 4, Data: le32(-2)}
	uuid := &Array{Type: DType{Kind: FixedSizeList, Size: 16}, Len: 1, Elems: &Array{Type: primT(U8), Len: 16, Data: []byte("0123456789abcdef")}}
	cases := []struct {
		name  string
		proto []byte
		arr   *Array
		want  any
	}{
		{"bool", tSimple(pbBool, false), bitsOf, true},
		{"utf8", tSimple(pbUtf8, false), utf8Arr("é"), "é"},
		{"binary", tSimple(pbBinary, false), binArr([]byte{1}), []byte{1}},
		{"i8", tPrim(I8, false), primArr(primT(I8), nil, -3), int64(-3)},
		{"i64", tPrim(I64, false), primArr(primT(I64), nil, math.MinInt64), int64(math.MinInt64)},
		{"u8", tPrim(U8, false), primArr(primT(U8), nil, 200), int64(200)},
		{"u64", tPrim(U64, false), primArr(primT(U64), nil, 5), int64(5)},
		{"f32", tPrim(F32, false), &Array{Type: primT(F32), Len: 1, Data: binary.LittleEndian.AppendUint32(nil, math.Float32bits(1.5))}, 1.5},
		{"f64", tPrim(F64, false), &Array{Type: primT(F64), Len: 1, Data: binary.LittleEndian.AppendUint64(nil, math.Float64bits(-2.5))}, -2.5},
		{"decimal", lenField(pbDecimal, cat(vField(1, 9), vField(2, 2))), dec, "feffffff/2"},
		{"date days", tExt("vortex.date", tPrim(I32, false), []byte{4}), primArr(i32, nil, int64(day.Unix()/86400)), day},
		{"date ms", tExt("vortex.date", tPrim(I64, false), []byte{3}), primArr(primT(I64), nil, day.UnixMilli()), day},
		{"timestamp us", tExt("vortex.timestamp", tPrim(I64, false), []byte{1, 0, 0}), primArr(primT(I64), nil, day.UnixMicro()+5), day.Add(5 * time.Microsecond)},
		{"timestamp ns", tExt("vortex.timestamp", tPrim(I64, false), []byte{0, 0, 0}), primArr(primT(I64), nil, day.UnixNano()+5), day.Add(5)},
		{"time s", tExt("vortex.time", tPrim(I32, false), []byte{3}), primArr(i32, nil, 61), time.Unix(61, 0).UTC()},
		{"uuid", tExt("vortex.uuid", lenField(pbFixedSizeList, cat(lenField(1, tPrim(U8, false)), vField(2, 16), vField(3, 0))), nil), uuid, []byte("0123456789abcdef")},
		{"other extension", tExt("acme.thing", tPrim(I32, false), nil), primArr(i32, nil, 4), int64(4)},
		{"extension of a string", tExt("vortex.date", tSimple(pbUtf8, false), []byte{4}), utf8Arr("s"), "s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := typedArr(t, c.proto, c.arr)
			i := c.arr.Len - 1
			if c.name == "bool" {
				i = 1
			}
			if got := variantAt(t, a, i); !reflect.DeepEqual(got, want(c.want)) {
				t.Fatalf("got %#v, want %#v", got, c.want)
			}
		})
	}
}

func want(v any) any { return v }

func TestVariantLeafErrors(t *testing.T) {
	cases := []struct {
		name  string
		proto []byte
		arr   *Array
		want  string
	}{
		{"f16", tPrim(F16, false), &Array{Type: primT(F16), Len: 1, Data: []byte{0, 0}}, "f16 value"},
		{"u64", tPrim(U64, false), primArr(primT(U64), nil, -1), "does not fit"},
		{"map", lenField(pbMap, cat(lenField(1, tPrim(U8, false)), lenField(2, tPrim(I32, false)))), &Array{Type: DType{Kind: Map}, Len: 1}, "a value of type"},
		{"uuid of another size", tExt("vortex.uuid", tPrim(I32, false), nil), primArr(i32, nil, 1), "a uuid of type"},
		{"date without builder", lenField(pbDecimal, vField(1, 9)), &Array{Type: DType{Kind: Decimal}, Len: 1, Width: 4, Data: le32(1)}, ""},
		{"time of an unknown unit", tExt("vortex.timestamp", tPrim(I64, false), []byte{1, 0, 0}), primArr(primT(I64), nil, 1), ""},
	}
	for _, c := range cases[:4] {
		t.Run(c.name, func(t *testing.T) {
			a := typedArr(t, c.proto, c.arr)
			if _, err := a.VariantValue(0, decText); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
	// A decimal needs a way to build it.
	dec := typedArr(t, cases[4].proto, cases[4].arr)
	if _, err := dec.VariantValue(0, nil); err == nil || !strings.Contains(err.Error(), "no way to build") {
		t.Fatalf("error = %v", err)
	}
	// A date, time or timestamp whose storage has no unit.
	tv := typedArr(t, tExt("vortex.time", tPrim(I64, false), []byte{1}), primArr(primT(I64), nil, 1))
	tv.Var.TypedType.Ticks = 0
	if _, err := tv.VariantValue(0, decText); err == nil || !strings.Contains(err.Error(), "with no unit") {
		t.Fatalf("error = %v", err)
	}
	if !errors.Is(mustErr(typedArr(t, tPrim(F16, false), cases[0].arr).VariantValue(0, decText)), errors.ErrUnsupported) {
		t.Fatal("f16 must be unsupported")
	}
}

func mustErr(_ any, err error) error { return err }

func TestVariantPartsSliceAndNulls(t *testing.T) {
	m := testMeta("x")
	part := func(vals ...int8) *Array {
		var metas, values [][]byte
		for _, v := range vals {
			metas, values = append(metas, m), append(values, int8v(v))
		}
		return &Array{Type: variantT, Len: len(vals), Var: &VariantData{Metadata: binArr(metas...), Value: binArr(values...)}}
	}
	parts := []*Array{part(1, 2), part(), part(3, 4, 5)}
	a := Concat(variantT, parts)
	for i, want := range []int64{1, 2, 3, 4, 5} {
		if got := variantAt(t, a, i); got != want {
			t.Errorf("row %d = %v", i, got)
		}
	}
	s := a.Slice(1, 3)
	if got := variantAt(t, s, 2); got != int64(4) {
		t.Fatalf("slice row 2 = %v", got)
	}
	// A null constant of the type has no content to read.
	z, err := zeroArray(variantT, 2)
	if err != nil {
		t.Fatal(err)
	}
	z.Validity = &Bitmap{Bytes: []byte{0}}
	if variantAt(t, z, 1) != nil {
		t.Fatal("a null row must be nil")
	}
}

// Decoding from segments.

// viewsOf is the views buffer of inline binaries of up to 12 bytes.
func viewsOf(vals ...[]byte) buf {
	var b []byte
	for _, v := range vals {
		b = append(b, inlineView(string(v))...)
	}
	return buf{data: b}
}

func binNode(i uint16) fbtest.Tab { return node(eVarBinView, nil, []uint16{i}) }

func TestDecodeParquetVariant(t *testing.T) {
	m := testMeta("x")
	meta := cat(vField(1, 1), lenField(2, tPrim(I32, true)), vField(3, 1))
	pn := node(eParquetVariant, meta, nil, bnode(0), binNode(1), node(eVarBinView, nil, []uint16{2}, bnode(5)), node(ePrim, nil, []uint16{3}, bnode(4)))
	seg := segment(pn, buf{data: []byte{0b101}}, viewsOf(m, m, m), viewsOf(objectv([]byte{0}, int8v(5)), nil, nil),
		buf{data: le32(1, 2, 3)}, buf{data: []byte{0b100}}, buf{data: []byte{0b001}})
	a := mustDecode(t, seg, variantT, 3)
	if a.Null(0) || !a.Null(1) || variantAt(t, a, 1) != nil {
		t.Fatal("validity is wrong")
	}
	if got := variantAt(t, a, 0); !reflect.DeepEqual(got, map[string]any{"x": int64(5)}) {
		t.Fatalf("row 0 = %#v", got)
	}
	if got := variantAt(t, a, 2); got != int64(3) {
		t.Fatalf("row 2 = %#v", got)
	}
}

func TestDecodeVariant(t *testing.T) {
	m := testMeta("x")
	core := func(buf0 uint16) fbtest.Tab {
		return node(eParquetVariant, vField(1, 1), nil, binNode(buf0), node(eVarBinView, nil, []uint16{buf0 + 1}))
	}
	// Without a shredded type, the array is its core storage.
	seg := segment(node(eVariant, nil, nil, core(0)), viewsOf(m, m), viewsOf(int8v(1), int8v(2)))
	a := mustDecode(t, seg, variantT, 2)
	if got := variantAt(t, a, 1); got != int64(2) {
		t.Fatalf("row 1 = %#v", got)
	}
	// With one, the shredded value wins and the core completes it.
	meta := lenField(1, tPrim(I32, true))
	shredded := node(ePrim, nil, []uint16{2}, bnode(3))
	seg = segment(node(eVariant, meta, nil, core(0), shredded), viewsOf(m, m), viewsOf(int8v(1), int8v(2)),
		buf{data: le32(10, 0)}, buf{data: []byte{0b01}})
	a = mustDecode(t, seg, variantT, 2)
	if got := variantAt(t, a, 0); got != int64(10) {
		t.Fatalf("row 0 = %#v", got)
	}
	if got := variantAt(t, a, 1); got != int64(2) {
		t.Fatalf("row 1 = %#v", got)
	}
	// Nested in a struct, as the field of a shredded object.
	nested := structOf(false, variantT)
	seg = segment(node(eStruct, nil, nil, node(eVariant, nil, nil, core(0))), viewsOf(m), viewsOf(int8v(7)))
	a = mustDecode(t, seg, nested, 1)
	if got := variantAt(t, a.Fields[0], 0); got != int64(7) {
		t.Fatalf("nested = %#v", got)
	}
}

func TestDecodeVariantErrors(t *testing.T) {
	m := testMeta("x")
	one := viewsOf(m)
	meta1 := vField(1, 1)
	pq := func(meta []byte, kids ...fbtest.Tab) fbtest.Tab { return node(eParquetVariant, meta, nil, kids...) }
	bin := binNode(0)
	typedMeta := cat(meta1, lenField(2, tPrim(I32, false)))
	runErrCases(t, []errCase{
		{"parquet: wrong type", segment(pq(meta1, bin), one), i32, 1, "cannot be a i32"},
		{"parquet: a buffer", segment(node(eParquetVariant, meta1, []uint16{0}, bin, bin), one), variantT, 1, "no buffer"},
		{"parquet: bad dtype", segment(pq(cat(meta1, lenField(2, []byte{1})), bin, bin), one), variantT, 1, "typed_value_dtype"},
		{"parquet: bad metadata", segment(pq([]byte{0x0a, 9}, bin), one), variantT, 1, "truncated"},
		{"parquet: no value and no typed value", segment(pq(nil, bin), one), variantT, 1, "neither a value"},
		{"parquet: children", segment(pq(meta1, bin), one), variantT, 1, "expected 2 or 3 children, got 1"},
		{"parquet: validity", segment(pq(meta1, node(ePrim, nil, nil), bin, bin), one), variantT, 1, "validity"},
		{"parquet: metadata", segment(pq(meta1, node(ePrim, nil, nil), bin), one), variantT, 1, "metadata:"},
		{"parquet: value", segment(pq(meta1, bin, node(ePrim, nil, nil)), one), variantT, 1, "value:"},
		{"parquet: typed value", segment(pq(typedMeta, bin, bin, node(ePrim, nil, nil)), one), variantT, 1, "typed_value:"},
		{"variant: wrong type", segment(node(eVariant, nil, nil, bin), one), i32, 1, "cannot be a i32"},
		{"variant: a buffer", segment(node(eVariant, nil, []uint16{0}, bin), one), variantT, 1, "no buffer"},
		{"variant: bad dtype", segment(node(eVariant, lenField(1, []byte{1}), nil, bin), one), variantT, 1, "shredded_dtype"},
		{"variant: bad metadata", segment(node(eVariant, []byte{0x0a, 9}, nil, bin), one), variantT, 1, "truncated"},
		{"variant: children", segment(node(eVariant, nil, nil, bin, bin), one), variantT, 1, "expected 1 children, got 2"},
		{"variant: core", segment(node(eVariant, nil, nil, node(ePrim, nil, nil)), one), variantT, 1, "core storage:"},
		{"variant: shredded", segment(node(eVariant, lenField(1, tPrim(I32, false)), nil, pq(meta1, bin, bin), node(ePrim, nil, nil)), one, one), variantT, 1, "shredded:"},
	})
}
