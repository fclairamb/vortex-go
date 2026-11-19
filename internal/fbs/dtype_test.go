package fbs

import (
	"encoding/binary"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// dt is a DType table: the union type then its table.
func dt(kind Kind, body fbtest.Tab) fbtest.Tab {
	return fbtest.Tab{0: fbtest.U8(uint8(kind)), 1: fbtest.T(body)}
}

func read(t *testing.T, tab fbtest.Tab) DType {
	t.Helper()
	d, err := ReadDType(fbtest.Build(tab), 8)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDTypeNullable(t *testing.T) {
	yes := fbtest.U8(1)
	tests := map[Kind]fbtest.Tab{
		KindBool: {0: yes}, KindPrimitive: {1: yes}, KindDecimal: {2: yes}, KindUtf8: {0: yes},
		KindBinary: {0: yes}, KindStruct: {2: yes}, KindList: {1: yes}, KindFixedSizeList: {2: yes},
		KindVariant: {0: yes}, KindUnion: {3: yes}, KindMap: {3: yes},
	}
	for kind, body := range tests {
		d := read(t, dt(kind, body))
		if k, err := d.Kind(); k != kind || err != nil {
			t.Fatal(kind, k, err)
		}
		if n, err := d.Nullable(); !n || err != nil {
			t.Fatal(kind, n, err)
		}
		if n, _ := read(t, dt(kind, fbtest.Tab{})).Nullable(); n {
			t.Fatal(kind, "nullable by default")
		}
	}
	if n, err := read(t, dt(KindNull, fbtest.Tab{})).Nullable(); n || err != nil {
		t.Fatal(n, err)
	}
	// An extension takes the nullability of its storage.
	ext := dt(KindExtension, fbtest.Tab{1: fbtest.T(dt(KindBool, fbtest.Tab{0: yes}))})
	if n, err := read(t, ext).Nullable(); !n || err != nil {
		t.Fatal(n, err)
	}
	// An extension without storage, a missing type and an unknown kind are errors.
	if _, err := read(t, dt(KindExtension, fbtest.Tab{})).Nullable(); err == nil {
		t.Fatal("extension without storage")
	}
	if _, err := read(t, fbtest.Tab{}).Nullable(); err == nil {
		t.Fatal("no type")
	}
	if _, err := read(t, fbtest.Tab{}).Kind(); err == nil {
		t.Fatal("no kind")
	}
	if _, err := read(t, dt(99, fbtest.Tab{})).Nullable(); err == nil {
		t.Fatal("unknown kind")
	}
	// A union whose value is missing.
	d := read(t, fbtest.Tab{0: fbtest.U8(uint8(KindBool))})
	if _, err := d.Nullable(); err == nil {
		t.Fatal("no value")
	}
	if _, err := d.Kind(); err == nil {
		t.Fatal("no value")
	}
}

func TestDTypeAccessors(t *testing.T) {
	prim := dt(KindPrimitive, fbtest.Tab{0: fbtest.U8(F64)})
	if p, err := read(t, prim).Primitive(); p != F64 || err != nil {
		t.Fatal(p, err)
	}
	if p, _ := read(t, dt(KindPrimitive, fbtest.Tab{})).Primitive(); p != U8 {
		t.Fatal(p)
	}
	prec, scale, err := read(t, dt(KindDecimal, fbtest.Tab{0: fbtest.U8(38), 1: fbtest.U8(0xfd)})).Decimal()
	if prec != 38 || scale != -3 || err != nil {
		t.Fatal(prec, scale, err)
	}

	st := dt(KindStruct, fbtest.Tab{
		0: fbtest.Strs("a", "b"),
		1: fbtest.Tables(prim, dt(KindUtf8, fbtest.Tab{})),
	})
	names, types, err := read(t, st).Struct()
	if err != nil || names.Len() != 2 || types.Len() != 2 {
		t.Fatal(names.Len(), types.Len(), err)
	}
	if n, _ := names.At(1); n != "b" {
		t.Fatal(n)
	}
	ty, _ := types.At(1)
	if k, _ := ty.Kind(); k != KindUtf8 {
		t.Fatal(k)
	}

	un := dt(KindUnion, fbtest.Tab{0: fbtest.Strs("x"), 1: fbtest.Tables(prim), 2: fbtest.Bytes([]byte{200})})
	n, ds, ids, err := read(t, un).Union()
	if err != nil || n.Len() != 1 || ds.Len() != 1 || len(ids) != 1 || ids[0] != 200 {
		t.Fatal(n.Len(), ds.Len(), ids, err)
	}

	l := dt(KindList, fbtest.Tab{0: fbtest.T(prim)})
	if e, err := read(t, l).List(); err != nil {
		t.Fatal(err)
	} else if p, _ := e.Primitive(); p != F64 {
		t.Fatal(p)
	}
	e, size, err := read(t, dt(KindFixedSizeList, fbtest.Tab{0: fbtest.T(prim), 1: fbtest.U32(4)})).FixedSizeList()
	if p, _ := e.Primitive(); p != F64 || size != 4 || err != nil {
		t.Fatal(p, size, err)
	}
	id, storage, md, err := read(t, dt(KindExtension, fbtest.Tab{
		0: fbtest.Str("vortex.date"), 1: fbtest.T(prim), 2: fbtest.Bytes([]byte{7}),
	})).Extension()
	if p, _ := storage.Primitive(); id != "vortex.date" || p != F64 || len(md) != 1 || err != nil {
		t.Fatal(id, p, md, err)
	}
	key, value, sorted, err := read(t, dt(KindMap, fbtest.Tab{
		0: fbtest.T(prim), 1: fbtest.T(dt(KindBool, fbtest.Tab{})), 2: fbtest.U8(1),
	})).Map()
	if k, _ := key.Kind(); k != KindPrimitive || !sorted || err != nil {
		t.Fatal(k, sorted, err)
	}
	if k, _ := value.Kind(); k != KindBool {
		t.Fatal(k)
	}
}

func TestDTypeWrongKind(t *testing.T) {
	d := read(t, dt(KindNull, fbtest.Tab{}))
	if _, err := d.Primitive(); err == nil {
		t.Error("Primitive")
	}
	if _, _, err := d.Decimal(); err == nil {
		t.Error("Decimal")
	}
	if _, _, err := d.Struct(); err == nil {
		t.Error("Struct")
	}
	if _, _, _, err := d.Union(); err == nil {
		t.Error("Union")
	}
	if _, err := d.List(); err == nil {
		t.Error("List")
	}
	if _, _, err := d.FixedSizeList(); err == nil {
		t.Error("FixedSizeList")
	}
	if _, _, _, err := d.Extension(); err == nil {
		t.Error("Extension")
	}
	if _, _, _, err := d.Map(); err == nil {
		t.Error("Map")
	}
}

// sweep cuts the buffer at every length and requires that fn fails for at least one of them
// (and never panics): each failure is a bounds check of one accessor.
func sweep(t *testing.T, name string, tab fbtest.Tab, fn func(DType) error) {
	t.Helper()
	b := fbtest.Build(tab)
	failed := false
	for n := len(b); n >= 4; n-- {
		if d, err := ReadDType(b[:n], 8); err == nil && fn(d) != nil {
			failed = true
		}
	}
	if !failed {
		t.Errorf("%s: no truncation made the accessor fail", name)
	}
}

func TestDTypeTruncated(t *testing.T) {
	leaf := fbtest.T(dt(KindPrimitive, fbtest.Tab{0: fbtest.U8(F64)}))
	sweep(t, "struct names", dt(KindStruct, fbtest.Tab{0: fbtest.Strs("abc")}),
		func(d DType) error { _, _, err := d.Struct(); return err })
	sweep(t, "struct dtypes", dt(KindStruct, fbtest.Tab{0: fbtest.Strs("a"), 1: fbtest.Tables(dt(KindBool, fbtest.Tab{}))}),
		func(d DType) error { _, _, err := d.Struct(); return err })
	sweep(t, "union names", dt(KindUnion, fbtest.Tab{0: fbtest.Strs("abc"), 1: fbtest.Tables(), 2: fbtest.Bytes([]byte{1})}),
		func(d DType) error { _, _, _, err := d.Union(); return err })
	sweep(t, "union ids", dt(KindUnion, fbtest.Tab{2: fbtest.Bytes([]byte{1, 2, 3})}),
		func(d DType) error { _, _, _, err := d.Union(); return err })
	sweep(t, "list element", dt(KindList, fbtest.Tab{0: leaf}),
		func(d DType) error { _, err := d.List(); return err })
	sweep(t, "fixed size list", dt(KindFixedSizeList, fbtest.Tab{0: leaf, 1: fbtest.U32(4)}),
		func(d DType) error { _, _, err := d.FixedSizeList(); return err })
	sweep(t, "extension id", dt(KindExtension, fbtest.Tab{0: fbtest.Str("abc"), 1: leaf}),
		func(d DType) error { _, _, _, err := d.Extension(); return err })
	sweep(t, "extension metadata", dt(KindExtension, fbtest.Tab{1: leaf, 2: fbtest.Bytes([]byte{1, 2})}),
		func(d DType) error { _, _, _, err := d.Extension(); return err })
	sweep(t, "map key", dt(KindMap, fbtest.Tab{0: leaf, 1: leaf, 2: fbtest.U8(1)}),
		func(d DType) error { _, _, _, err := d.Map(); return err })
	sweep(t, "map value", dt(KindMap, fbtest.Tab{1: leaf, 2: fbtest.U8(1)}),
		func(d DType) error { _, _, _, err := d.Map(); return err })
	sweep(t, "map sorted", dt(KindMap, fbtest.Tab{0: leaf, 1: leaf, 2: fbtest.U8(1)}),
		func(d DType) error { _, _, _, err := d.Map(); return err })
	sweep(t, "decimal precision", dt(KindDecimal, fbtest.Tab{0: fbtest.U8(1), 1: fbtest.U8(1)}),
		func(d DType) error { _, _, err := d.Decimal(); return err })
	sweep(t, "decimal scale", dt(KindDecimal, fbtest.Tab{1: fbtest.U8(1)}),
		func(d DType) error { _, _, err := d.Decimal(); return err })
	sweep(t, "primitive", dt(KindPrimitive, fbtest.Tab{0: fbtest.U8(1)}),
		func(d DType) error { _, err := d.Primitive(); return err })
	sweep(t, "nullable", dt(KindBool, fbtest.Tab{0: fbtest.U8(1)}),
		func(d DType) error { _, err := d.Nullable(); return err })
	sweep(t, "extension storage nullable", dt(KindExtension, fbtest.Tab{1: leaf}),
		func(d DType) error { _, err := d.Nullable(); return err })
}

// The body of the DType union starts at the root table's field 1; this points the offset of
// field id of that body at 0x7fffffff.
func TestDTypeCorruptOffset(t *testing.T) {
	le := binary.LittleEndian
	b := fbtest.Build(dt(KindExtension, fbtest.Tab{
		0: fbtest.Str("abc"), 1: fbtest.T(dt(KindBool, fbtest.Tab{})),
	}))
	field := func(tab int, id int) int { // position of the slot of a field
		vt := tab - int(int32(le.Uint32(b[tab:])))
		return tab + int(le.Uint16(b[vt+4+2*id:]))
	}
	root := int(le.Uint32(b))
	slot := field(root, 1)
	body := slot + int(le.Uint32(b[slot:]))
	idSlot := field(body, 0)
	le.PutUint32(b[idSlot:], 0x7fffffff)
	d, err := ReadDType(b, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.Extension(); err == nil {
		t.Fatal("no error")
	}
}
