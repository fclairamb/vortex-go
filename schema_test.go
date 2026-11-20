package vortex

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

func ptr(t Type) *Type { return &t }

func TestTypeString(t *testing.T) {
	i32 := Type{kind: Int32}
	str := Type{kind: Utf8, nullable: true}
	tests := []struct {
		t    Type
		want string
	}{
		{Type{kind: Null, nullable: true}, "null"},
		{Type{kind: Bool}, "bool"},
		{Type{kind: Int8}, "i8"},
		{Type{kind: Int16}, "i16"},
		{i32, "i32"},
		{Type{kind: Int64}, "i64"},
		{Type{kind: Uint8}, "u8"},
		{Type{kind: Uint16}, "u16"},
		{Type{kind: Uint32}, "u32"},
		{Type{kind: Uint64}, "u64"},
		{Type{kind: Float16}, "f16"},
		{Type{kind: Float32}, "f32"},
		{Type{kind: Float64, nullable: true}, "f64?"},
		{Type{kind: Decimal, precision: 10, scale: -2}, "decimal(10,-2)"},
		{str, "utf8?"},
		{Type{kind: Binary}, "binary"},
		{Type{kind: List, elem: &i32}, "list<i32>"},
		{Type{kind: FixedSizeList, elem: &i32, size: 3}, "fixed_size_list<i32>[3]"},
		{Type{kind: Map, elem: &str, value: &i32}, "map<utf8?, i32>"},
		{Type{kind: Struct, fields: []Field{{"a", i32}, {"b", str}}}, "struct{a: i32, b: utf8?}"},
		{Type{kind: Struct}, "struct{}"},
		{Type{kind: Date, unit: Day}, "date[days]"},
		{Type{kind: Time, unit: Microsecond}, "time[us]"},
		{Type{kind: Timestamp, unit: Millisecond, zone: "UTC"}, "timestamp[ms, tz=UTC]"},
		{Type{kind: Timestamp, unit: Second, nullable: true}, "timestamp[s]?"},
		{Type{kind: Timestamp, unit: Nanosecond}, "timestamp[ns]"},
		{Type{kind: UUID}, "uuid"},
		{Type{kind: Variant}, "variant"},
		{Type{kind: Kind(99)}, "kind(99)"},
		{Type{kind: Date, unit: Unit(9)}, "date[unit(9)]"},
	}
	for _, tt := range tests {
		if got := tt.t.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestTypeEqual(t *testing.T) {
	i32 := Type{kind: Int32}
	i64 := Type{kind: Int64}
	base := Type{kind: Struct, fields: []Field{{"a", i32}}}
	equal := []struct{ a, b Type }{
		{i32, Type{kind: Int32}},
		{base, Type{kind: Struct, fields: []Field{{"a", i32}}}},
		{Type{kind: List, elem: &i32}, Type{kind: List, elem: ptr(i32)}},
		{Type{kind: Map, elem: &i32, value: &i64}, Type{kind: Map, elem: ptr(i32), value: ptr(i64)}},
		{Type{kind: Timestamp, unit: Second, zone: "UTC"}, Type{kind: Timestamp, unit: Second, zone: "UTC"}},
	}
	for _, e := range equal {
		if !e.a.Equal(e.b) || !e.b.Equal(e.a) {
			t.Errorf("%s != %s", e.a, e.b)
		}
	}
	different := []struct{ a, b Type }{
		{i32, i64},
		{i32, Type{kind: Int32, nullable: true}},
		{Type{kind: Decimal, precision: 5}, Type{kind: Decimal, precision: 6}},
		{Type{kind: Decimal, scale: 1}, Type{kind: Decimal, scale: 2}},
		{Type{kind: FixedSizeList, elem: &i32, size: 1}, Type{kind: FixedSizeList, elem: &i32, size: 2}},
		{Type{kind: Time, unit: Second}, Type{kind: Time, unit: Millisecond}},
		{Type{kind: Timestamp, zone: "UTC"}, Type{kind: Timestamp, zone: "CET"}},
		{Type{kind: List, elem: &i32}, Type{kind: List, elem: &i64}},
		{Type{kind: List, elem: &i32}, Type{kind: List}},
		{Type{kind: List}, Type{kind: List, elem: &i32}},
		{Type{kind: Map, elem: &i32, value: &i32}, Type{kind: Map, elem: &i32, value: &i64}},
		{base, Type{kind: Struct, fields: []Field{{"b", i32}}}},
		{base, Type{kind: Struct, fields: []Field{{"a", i64}}}},
		{base, Type{kind: Struct}},
	}
	for _, d := range different {
		if d.a.Equal(d.b) || d.b.Equal(d.a) {
			t.Errorf("%s == %s", d.a, d.b)
		}
	}
}

func TestTypeAccessors(t *testing.T) {
	i32 := Type{kind: Int32}
	utf8 := Type{kind: Utf8}
	m := Type{kind: Map, elem: &utf8, value: &i32, nullable: true}
	if m.Kind() != Map || !m.Nullable() || !m.Key().Equal(utf8) || !m.Value().Equal(i32) {
		t.Fatal(m)
	}
	l := Type{kind: FixedSizeList, elem: &i32, size: 4}
	if !l.Elem().Equal(i32) || l.Size() != 4 {
		t.Fatal(l)
	}
	d := Type{kind: Decimal, precision: 10, scale: -3}
	if d.Precision() != 10 || d.Scale() != -3 {
		t.Fatal(d)
	}
	ts := Type{kind: Timestamp, unit: Microsecond, zone: "UTC"}
	if ts.Unit() != Microsecond || ts.Zone() != "UTC" {
		t.Fatal(ts)
	}
	// The wrong kind gives the zero value.
	if !i32.Elem().Equal(Type{}) || !i32.Key().Equal(Type{}) || !i32.Value().Equal(Type{}) || i32.Fields() != nil {
		t.Fatal("accessors of another kind")
	}
	if !(Type{kind: List}).Elem().Equal(Type{}) || !(Type{kind: Map}).Key().Equal(Type{}) ||
		!(Type{kind: Map}).Value().Equal(Type{}) {
		t.Fatal("missing children")
	}
	fields := []Field{{"a", i32}, {"b", utf8}}
	s := &Schema{Type{kind: Struct, fields: fields}}
	got := s.Fields()
	got[0].Name = "changed"
	if s.Fields()[0].Name != "a" {
		t.Fatal("Fields must return a copy")
	}
	if f, ok := s.Field("b"); !ok || !f.Type.Equal(utf8) {
		t.Fatal(f, ok)
	}
	if _, ok := s.Field("c"); ok {
		t.Fatal("c")
	}
	if s.String() != "struct{a: i32, b: utf8}" {
		t.Fatal(s)
	}
}

// ext builds a file whose only column is an extension type.
func openExt(id string, meta []byte) error {
	p := defaults()
	p.dtype = fbtest.Build(structTab([]string{"x"}, extTab(id, meta, prim(7, true))))
	f, err := open(p.build())
	if err == nil {
		want := f.Schema().Fields()[0].Type
		if !want.Nullable() {
			return errors.New("the extension must take the nullability of its storage")
		}
	}
	return err
}

func TestExtensionTypes(t *testing.T) {
	ok := []struct {
		id   string
		meta []byte
		want string
	}{
		{"vortex.date", []byte{4}, "date[days]?"},
		{"vortex.date", []byte{2}, "date[ms]?"},
		{"vortex.time", []byte{3}, "time[s]?"},
		{"vortex.time", []byte{0}, "time[ns]?"},
		{"vortex.timestamp", []byte{1, 0, 0}, "timestamp[us]?"},
		{"vortex.timestamp", []byte{2, 3, 0, 'U', 'T', 'C'}, "timestamp[ms, tz=UTC]?"},
		{"vortex.uuid", nil, "uuid?"},
		{"vortex.uuid", []byte{1}, "uuid?"},
	}
	for _, tt := range ok {
		p := defaults()
		p.dtype = fbtest.Build(structTab([]string{"x"}, extTab(tt.id, tt.meta, prim(7, true))))
		f, err := open(p.build())
		if err != nil {
			t.Fatalf("%s %v: %v", tt.id, tt.meta, err)
		}
		if got := f.Schema().Fields()[0].Type.String(); got != tt.want {
			t.Errorf("%s %v: %s, want %s", tt.id, tt.meta, got, tt.want)
		}
		if err := openExt(tt.id, tt.meta); err != nil {
			t.Fatal(err)
		}
	}
	bad := []struct {
		id   string
		meta []byte
	}{
		{"vortex.date", nil},
		{"vortex.date", []byte{4, 4}},
		{"vortex.date", []byte{1}}, // microseconds
		{"vortex.date", []byte{9}}, // no such unit
		{"vortex.time", []byte{4}}, // days
		{"vortex.timestamp", nil},
		{"vortex.timestamp", []byte{4, 0, 0}},
		{"vortex.timestamp", []byte{2, 3, 0, 'U'}},
		{"vortex.timestamp", []byte{2, 0, 0, 'U'}},
		{"vortex.uuid", []byte{1, 2}},
	}
	for _, tt := range bad {
		if err := openExt(tt.id, tt.meta); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s %v: %v", tt.id, tt.meta, err)
		}
	}
}

func TestNestedTypes(t *testing.T) {
	p := defaults()
	list := dtypeTab(8, fbtest.Tab{0: fbtest.T(prim(1, false)), 1: fbtest.U8(1)})
	fsl := dtypeTab(10, fbtest.Tab{0: fbtest.T(prim(0, false)), 1: fbtest.U32(16)})
	mp := dtypeTab(13, fbtest.Tab{0: fbtest.T(dtypeTab(5, fbtest.Tab{})), 1: fbtest.T(prim(10, false))})
	dec := dtypeTab(4, fbtest.Tab{0: fbtest.U8(10), 1: fbtest.U8(0xfe)})
	simple := []fbtest.Tab{
		dtypeTab(1, fbtest.Tab{}), dtypeTab(2, fbtest.Tab{}), dtypeTab(6, fbtest.Tab{}), dtypeTab(11, fbtest.Tab{}),
	}
	types := append([]fbtest.Tab{list, fsl, mp, dec, structTab([]string{"i"}, prim(8, false))}, simple...)
	names := []string{"l", "f", "m", "d", "s", "n", "b", "bin", "v"}
	p.dtype = fbtest.Build(structTab(names, types...))
	f, err := open(p.build())
	if err != nil {
		t.Fatal(err)
	}
	want := "struct{l: list<u16>?, f: fixed_size_list<u8>[16], m: map<utf8, f64>, d: decimal(10,-2), " +
		"s: struct{i: f16}, n: null, b: bool, bin: binary, v: variant}"
	if got := f.Schema().String(); got != want {
		t.Fatalf("%s\nwant %s", got, want)
	}
}

func TestBadNestedTypes(t *testing.T) {
	wrong := fbtest.Tab{} // a type with a missing union value
	cases := map[string]fbtest.Tab{
		"unknown primitive":  structTab([]string{"x"}, dtypeTab(3, fbtest.Tab{0: fbtest.U8(77)})),
		"names and types":    structTab([]string{"x", "y"}, prim(0, false)),
		"field error":        structTab([]string{"x"}, dtypeTab(8, fbtest.Tab{0: fbtest.U32(1 << 20)})),
		"list element":       structTab([]string{"x"}, dtypeTab(8, fbtest.Tab{0: fbtest.T(extTab("acme.x", nil, prim(0, false)))})),
		"fixed size element": structTab([]string{"x"}, dtypeTab(10, fbtest.Tab{0: fbtest.T(extTab("acme.x", nil, prim(0, false)))})),
		"map key":            structTab([]string{"x"}, dtypeTab(13, fbtest.Tab{0: fbtest.T(extTab("acme.x", nil, prim(0, false))), 1: fbtest.T(prim(0, false))})),
		"map value":          structTab([]string{"x"}, dtypeTab(13, fbtest.Tab{0: fbtest.T(prim(0, false)), 1: fbtest.T(extTab("acme.x", nil, prim(0, false)))})),
		"no kind":            structTab([]string{"x"}, wrong),
		"no list":            structTab([]string{"x"}, dtypeTab(8, fbtest.Tab{})),
		"no fixed size list": structTab([]string{"x"}, dtypeTab(10, fbtest.Tab{})),
		"no map":             structTab([]string{"x"}, dtypeTab(13, fbtest.Tab{})),
		"no extension":       structTab([]string{"x"}, dtypeTab(9, fbtest.Tab{})),
		"nested struct":      structTab([]string{"x"}, dtypeTab(7, fbtest.Tab{0: fbtest.U32(1 << 20)})),
		"struct types":       dtypeTab(7, fbtest.Tab{0: fbtest.Strs("a")}),
		"struct names":       dtypeTab(7, fbtest.Tab{0: fbtest.U32(1 << 20), 1: fbtest.Tables(prim(0, false))}),
	}
	for name, dt := range cases {
		p := defaults()
		p.dtype = fbtest.Build(dt)
		if _, err := open(p.build()); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOpenFileStatError(t *testing.T) {
	old := statFile
	defer func() { statFile = old }()
	statFile = func(*os.File) (os.FileInfo, error) { return nil, errors.New("no stat") }
	if _, err := OpenFile(fixtures(t)[0]); err == nil || !strings.Contains(err.Error(), "no stat") {
		t.Fatal(err)
	}
}

func TestBadExtensionStorage(t *testing.T) {
	p := defaults()
	p.dtype = fbtest.Build(structTab([]string{"x"},
		dtypeTab(9, fbtest.Tab{0: fbtest.Str("vortex.uuid"), 1: fbtest.T(fbtest.Tab{})})))
	if _, err := open(p.build()); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}
