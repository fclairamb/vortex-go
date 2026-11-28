package flatbuf

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// raw hand-builds a buffer: the root offset, a vtable with the given field offsets (relative
// to the table), then the table, which is its soffset followed by body.
func raw(fields []uint16, body ...byte) []byte {
	vtSize := 4 + 2*len(fields)
	b := make([]byte, 4+vtSize+4)
	binary.LittleEndian.PutUint32(b, uint32(4+vtSize))
	binary.LittleEndian.PutUint16(b[4:], uint16(vtSize))
	binary.LittleEndian.PutUint16(b[6:], uint16(4+len(body)))
	for i, f := range fields {
		binary.LittleEndian.PutUint16(b[8+2*i:], f)
	}
	binary.LittleEndian.PutUint32(b[4+vtSize:], uint32(vtSize))
	return append(b, body...)
}

func u32le(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	var fe *Error
	if !errors.As(err, &fe) || !strings.Contains(err.Error(), substr) {
		t.Fatalf("want a flatbuf error with %q, got %v", substr, err)
	}
}

func TestScalars(t *testing.T) {
	buf := fbtest.Build(fbtest.Tab{
		0: fbtest.U8(7), 1: fbtest.U16(0x1234), 2: fbtest.U32(0xdeadbeef), 3: fbtest.U64(1 << 40),
		4: fbtest.U8(0xfe), 5: fbtest.U8(1), 6: fbtest.U8(0),
	})
	tab, err := Root(buf, 1)
	if err != nil {
		t.Fatal(err)
	}
	u8, _ := tab.Uint8(0, 9)
	u16, _ := tab.Uint16(1, 9)
	u32, _ := tab.Uint32(2, 9)
	u64, _ := tab.Uint64(3, 9)
	i8, _ := tab.Int8(4, 9)
	b1, _ := tab.Bool(5, false)
	b0, _ := tab.Bool(6, true)
	if u8 != 7 || u16 != 0x1234 || u32 != 0xdeadbeef || u64 != 1<<40 || i8 != -2 || !b1 || b0 {
		t.Fatal(u8, u16, u32, u64, i8, b1, b0)
	}
	if !tab.Has(0) || tab.Has(7) || tab.Has(-1) {
		t.Fatal("Has")
	}
}

func TestDefaults(t *testing.T) {
	tab, err := Root(fbtest.Build(fbtest.Tab{}), 1)
	if err != nil {
		t.Fatal(err)
	}
	u8, _ := tab.Uint8(0, 9)
	i8, _ := tab.Int8(0, -3)
	bo, _ := tab.Bool(0, true)
	u16, _ := tab.Uint16(0, 9)
	u32, _ := tab.Uint32(0, 9)
	u64, _ := tab.Uint64(0, 9)
	if u8 != 9 || i8 != -3 || !bo || u16 != 9 || u32 != 9 || u64 != 9 {
		t.Fatal("defaults")
	}
	if _, ok, err := tab.Bytes(0); ok || err != nil {
		t.Fatal("absent bytes")
	}
	if _, ok, err := tab.String(0); ok || err != nil {
		t.Fatal("absent string")
	}
	if _, ok, err := tab.Table(0); ok || err != nil {
		t.Fatal("absent table")
	}
	if typ, _, err := tab.Union(0); typ != 0 || err != nil {
		t.Fatal("absent union")
	}
	if v, err := tab.Tables(0); v.Len() != 0 || err != nil {
		t.Fatal("absent vector")
	}
	if v, err := tab.Uint16s(0); len(v) != 0 || err != nil {
		t.Fatal("absent vector")
	}
	var zero Table
	if zero.Has(0) {
		t.Fatal("zero table")
	}
}

func TestVectors(t *testing.T) {
	buf := fbtest.Build(fbtest.Tab{
		0: fbtest.Str("héllo"),
		1: fbtest.Bytes([]byte{1, 2, 3}),
		2: fbtest.U16s(1, 2, 65535),
		3: fbtest.U32s(10, 4000000000),
		4: fbtest.Strs("a", "", "bc"),
		5: fbtest.Tables(fbtest.Tab{0: fbtest.U8(1)}, fbtest.Tab{0: fbtest.U8(2)}, fbtest.Tab{}),
		6: fbtest.Structs(3, []byte{1, 2, 3}, []byte{4, 5, 6}),
		7: fbtest.T(fbtest.Tab{0: fbtest.U8(42)}),
		8: fbtest.Bytes(nil),
	})
	tab, err := Root(buf, 2)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok, err := tab.String(0); s != "héllo" || !ok || err != nil {
		t.Fatal(s, ok, err)
	}
	if b, ok, _ := tab.Bytes(1); string(b) != "\x01\x02\x03" || !ok {
		t.Fatal(b)
	}
	if b, ok, _ := tab.Bytes(8); len(b) != 0 || !ok {
		t.Fatal("empty bytes is present")
	}
	if v, _ := tab.Uint16s(2); len(v) != 3 || v[2] != 65535 {
		t.Fatal(v)
	}
	if v, _ := uint32s(tab, 3); len(v) != 2 || v[1] != 4000000000 {
		t.Fatal(v)
	}
	strs, err := tab.Strings(4)
	if err != nil || strs.Len() != 3 {
		t.Fatal(err)
	}
	for i, want := range []string{"a", "", "bc"} {
		if s, err := strs.String(i); s != want || err != nil {
			t.Fatal(i, s, err)
		}
	}
	tabs, err := tab.Tables(5)
	if err != nil || tabs.Len() != 3 {
		t.Fatal(err)
	}
	for i, want := range []uint8{1, 2, 7} {
		sub, err := tabs.Table(i)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := sub.Uint8(0, 7); got != want {
			t.Fatal(i, got)
		}
	}
	structs, err := tab.Structs(6, 3)
	if err != nil || structs.Len() != 2 {
		t.Fatal(err)
	}
	if b, err := structs.Struct(1); string(b) != "\x04\x05\x06" || err != nil {
		t.Fatal(b, err)
	}
	sub, ok, err := tab.Table(7)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if v, _ := sub.Uint8(0, 0); v != 42 {
		t.Fatal(v)
	}
	// Out of range indexes are errors.
	for _, i := range []int{-1, 3} {
		_, err := tabs.Table(i)
		wantErr(t, err, "out of 3 elements")
		_, err = strs.String(i)
		wantErr(t, err, "out of 3 elements")
		_, err = structs.Struct(i)
		wantErr(t, err, "out of 2 elements")
	}
}

func TestUnion(t *testing.T) {
	buf := fbtest.Build(fbtest.Tab{0: fbtest.U8(3), 1: fbtest.T(fbtest.Tab{0: fbtest.U8(5)})})
	tab, _ := Root(buf, 2)
	typ, v, err := tab.Union(0)
	if typ != 3 || err != nil {
		t.Fatal(typ, err)
	}
	if got, _ := v.Uint8(0, 0); got != 5 {
		t.Fatal(got)
	}
	// A type without a value.
	tab, _ = Root(fbtest.Build(fbtest.Tab{0: fbtest.U8(3)}), 2)
	_, _, err = tab.Union(0)
	wantErr(t, err, "union type 3 without a value")
	// A type that is out of the buffer.
	tab, _ = Root(raw([]uint16{100}), 2)
	_, _, err = tab.Union(0)
	wantErr(t, err, "do not fit")
}

func TestDepth(t *testing.T) {
	buf := fbtest.Build(fbtest.Tab{0: fbtest.T(fbtest.Tab{0: fbtest.T(fbtest.Tab{})}), 1: fbtest.Tables(fbtest.Tab{})})
	_, err := Root(buf, 0)
	wantErr(t, err, "deeper than 0")
	root, err := Root(buf, 2)
	if err != nil {
		t.Fatal(err)
	}
	sub, ok, err := root.Table(0)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	_, _, err = sub.Table(0)
	wantErr(t, err, "deeper than 2")
	vec, _ := root.Tables(1)
	if _, err := vec.Table(0); err != nil {
		t.Fatal(err)
	}
	root, _ = Root(buf, 1)
	vec, _ = root.Tables(1)
	_, err = vec.Table(0)
	wantErr(t, err, "deeper than 1")
}

// The smallest bad buffer for every check.
func TestBounds(t *testing.T) {
	huge := u32le(0xffffffff)
	tests := []struct {
		name string
		buf  []byte
		fn   func(Table) error
		want string
	}{
		{"root offset truncated", []byte{1, 2}, nil, "4 bytes do not fit"},
		{"root offset past the end", []byte{0xff, 0xff, 0xff, 0x7f}, nil, "4 bytes do not fit"},
		{"root offset 8 in 8 bytes", []byte{8, 0, 0, 0, 0, 0, 0, 0}, nil, "4 bytes do not fit"},
		{"vtable before the buffer", []byte{4, 0, 0, 0, 100, 0, 0, 0}, nil, "do not fit"},
		{"vtable after the buffer", []byte{4, 0, 0, 0, 0x9c, 0xff, 0xff, 0xff}, nil, "do not fit"},
		{"vtable size under 4", []byte{8, 0, 0, 0, 2, 0, 0, 0, 4, 0, 0, 0}, nil, "vtable of 2 bytes"},
		{"vtable size past the end", []byte{8, 0, 0, 0, 100, 0, 0, 0, 4, 0, 0, 0}, nil, "100 bytes do not fit"},
		{"scalar past the end", raw([]uint16{100}), func(t Table) error { _, err := t.Uint32(0, 0); return err }, "4 bytes do not fit"},
		{"u8 past the end", raw([]uint16{100}), func(t Table) error { _, err := t.Uint8(0, 0); return err }, "1 bytes do not fit"},
		{"offset past the end", raw([]uint16{100}), func(t Table) error { _, _, err := t.Table(0); return err }, "do not fit"},
		{"table offset leads out", raw([]uint16{4}, huge...), func(t Table) error { _, _, err := t.Table(0); return err }, "do not fit"},
		{"string offset leads out", raw([]uint16{4}, huge...), func(t Table) error { _, _, err := t.String(0); return err }, "do not fit"},
		{"vector count past the end", raw([]uint16{4}, append(u32le(4), huge...)...), func(t Table) error { _, _, err := t.Bytes(0); return err }, "vector of 4294967295 elements of 1 bytes"},
		{"vector of 2-byte elements, one short", raw([]uint16{4}, append(u32le(4), append(u32le(2), 1, 0, 2)...)...), func(t Table) error { _, err := t.Uint16s(0); return err }, "elements of 2 bytes"},
		{"vector of u32, count past the end", raw([]uint16{4}, append(u32le(4), huge...)...), func(t Table) error { _, err := uint32s(t, 0); return err }, "elements of 4 bytes"},
		{"vector of struct of size 0", raw([]uint16{4}, append(u32le(4), u32le(1)...)...), func(t Table) error { _, err := t.Structs(0, 0); return err }, "element size 0"},
		{"vector of tables, element leads out", raw([]uint16{4}, append(u32le(4), append(u32le(1), huge...)...)...), func(t Table) error {
			v, err := t.Tables(0)
			if err != nil {
				return err
			}
			_, err = v.Table(0)
			return err
		}, "do not fit"},
		{"vector of strings, element leads out", raw([]uint16{4}, append(u32le(4), append(u32le(1), huge...)...)...), func(t Table) error {
			v, err := t.Strings(0)
			if err != nil {
				return err
			}
			_, err = v.String(0)
			return err
		}, "do not fit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tab, err := Root(tt.buf, 4)
			if tt.fn != nil {
				if err != nil {
					t.Fatal(err)
				}
				err = tt.fn(tab)
			}
			wantErr(t, err, tt.want)
		})
	}
}

func TestErrorOffset(t *testing.T) {
	_, err := Root([]byte{1}, 1)
	var fe *Error
	if !errors.As(err, &fe) || fe.Off != 0 {
		t.Fatal(err)
	}
}

func walk(t Table, depth int) {
	for id := -1; id < 8; id++ {
		_ = t.Has(id)
		_, _ = t.Uint8(id, 0)
		_, _ = t.Int8(id, 0)
		_, _ = t.Bool(id, false)
		_, _ = t.Uint16(id, 0)
		_, _ = t.Uint32(id, 0)
		_, _ = t.Uint64(id, 0)
		_, _, _ = t.Bytes(id)
		_, _, _ = t.String(id)
		_, _ = t.Uint16s(id)
		_, _ = uint32s(t, id)
		if v, err := t.Strings(id); err == nil {
			for i := -1; i <= v.Len(); i++ {
				_, _ = v.String(i)
			}
		}
		if v, err := t.Structs(id, 5); err == nil {
			for i := -1; i <= v.Len(); i++ {
				_, _ = v.Struct(i)
			}
		}
		if depth == 0 {
			continue
		}
		if sub, ok, _ := t.Table(id); ok {
			walk(sub, depth-1)
		}
		if _, sub, _ := t.Union(id); sub.vtSize != 0 {
			walk(sub, depth-1)
		}
		if v, err := t.Tables(id); err == nil {
			for i := -1; i <= v.Len(); i++ {
				if sub, err := v.Table(i); err == nil {
					walk(sub, depth-1)
				}
			}
		}
	}
}

func FuzzRead(f *testing.F) {
	f.Add(fbtest.Build(fbtest.Tab{
		0: fbtest.U8(3), 1: fbtest.T(fbtest.Tab{0: fbtest.Str("x")}), 2: fbtest.Tables(fbtest.Tab{0: fbtest.U16s(1)}),
		3: fbtest.Strs("a"), 4: fbtest.Structs(5, []byte{1, 2, 3, 4, 5}), 5: fbtest.U64(9),
	}))
	f.Add(raw([]uint16{100, 4}, 1, 2, 3, 4))
	f.Fuzz(func(t *testing.T, b []byte) {
		if root, err := Root(b, 6); err == nil {
			walk(root, 5)
		}
	})
}

// uint32s decodes a vector of uint32 to a new slice.
func uint32s(t Table, id int) ([]uint32, error) {
	v, err := t.Uint32Vec(id)
	out := make([]uint32, v.Len())
	v.Read(out)
	return out, err
}
