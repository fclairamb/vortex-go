package flatbuf

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildAll writes a table with every kind of field, and the tables it points to.
func buildAll(b *Builder) []byte {
	b.Reset()
	str := b.String("héllo")
	blob := b.Bytes([]byte{1, 2, 3})
	u16s := b.Uint16s([]uint16{7, 8, 9})
	u32s := b.Uint32s([]uint32{70, 80})
	structs := b.Structs(8, 4, []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 4, 0, 0, 0})
	names := []Off{b.String("a"), b.String("bc")}
	strs := b.Offsets(names)
	b.StartTable(1)
	b.Uint8(0, 5, 0)
	kid1 := b.EndTable()
	b.StartTable(2)
	b.Uint32(1, 6, 0)
	kid2 := b.EndTable()
	kids := b.Offsets([]Off{kid1, kid2})
	b.StartTable(1)
	b.Uint8(0, 9, 0)
	inner := b.EndTable()
	b.StartTable(16)
	b.Uint8(0, 0xfe, 0)
	b.Uint8(1, 1, 0)
	b.Uint8(2, 3, 3) // default: left out
	b.Uint16(3, 0x1234, 0)
	b.Uint32(4, 0xdeadbeef, 0)
	b.Uint64(5, 1<<40, 0)
	b.Ref(6, str)
	b.Ref(7, blob)
	b.Ref(8, u16s)
	b.Ref(9, u32s)
	b.Ref(10, structs)
	b.Ref(11, strs)
	b.Ref(12, kids)
	b.Ref(13, inner)
	b.Union(14, 4, inner)
	b.Uint16(3, 0, 0) // default, no-op
	root := b.EndTable()
	return b.Finish(root)
}

func TestBuilderRoundTrip(t *testing.T) {
	b := NewBuilder(0)
	buf := buildAll(b)
	if len(buf)%b.minAlign != 0 {
		t.Fatalf("length %d is not a multiple of the alignment %d", len(buf), b.minAlign)
	}
	root, err := Root(buf, 4)
	if err != nil {
		t.Fatal(err)
	}
	i8, _ := root.Int8(0, 0)
	flag, _ := root.Bool(1, false)
	def, _ := root.Uint8(2, 3)
	u16, _ := root.Uint16(3, 0)
	u32, _ := root.Uint32(4, 0)
	u64, _ := root.Uint64(5, 0)
	if i8 != -2 || !flag || def != 3 || root.Has(2) || u16 != 0x1234 || u32 != 0xdeadbeef || u64 != 1<<40 {
		t.Fatal(i8, flag, def, u16, u32, u64)
	}
	if s, ok, err := root.String(6); s != "héllo" || !ok || err != nil {
		t.Fatal(s, ok, err)
	}
	if v, _, _ := root.Bytes(7); !bytes.Equal(v, []byte{1, 2, 3}) {
		t.Fatal(v)
	}
	if v, _ := root.Uint16s(8); len(v) != 3 || v[0] != 7 || v[2] != 9 {
		t.Fatal(v)
	}
	if v, _ := uint32s(root, 9); len(v) != 2 || v[0] != 70 || v[1] != 80 {
		t.Fatal(v)
	}
	sv, err := root.Structs(10, 8)
	if err != nil || sv.Len() != 2 {
		t.Fatal(sv.Len(), err)
	}
	if st, _ := sv.Struct(1); binary.LittleEndian.Uint32(st) != 3 {
		t.Fatal(st)
	}
	if uintptrOf(buf, 10, root)%4 != 0 {
		t.Fatal("struct vector is not aligned")
	}
	strs, _ := root.Strings(11)
	if s0, _ := strs.String(0); strs.Len() != 2 || s0 != "a" {
		t.Fatal(s0)
	}
	if s1, _ := strs.String(1); s1 != "bc" {
		t.Fatal(s1)
	}
	kids, _ := root.Tables(12)
	k2, err := kids.Table(1)
	if err != nil || kids.Len() != 2 {
		t.Fatal(err)
	}
	if v, _ := k2.Uint32(1, 0); v != 6 {
		t.Fatal(v)
	}
	k1, _ := kids.Table(0)
	if v, _ := k1.Uint8(0, 0); v != 5 {
		t.Fatal(v)
	}
	inner, ok, err := root.Table(13)
	if v, _ := inner.Uint8(0, 0); !ok || err != nil || v != 9 {
		t.Fatal(v, ok, err)
	}
	typ, un, err := root.Union(14)
	if v, _ := un.Uint8(0, 0); typ != 4 || err != nil || v != 9 {
		t.Fatal(typ, v, err)
	}
}

// uintptrOf is the position, in buf, of the elements of the struct vector in field id.
func uintptrOf(buf []byte, id int, t Table) int {
	pos, _, _ := t.indirect(id)
	_ = buf
	return pos + 4
}

func TestBuilderReuseIsStable(t *testing.T) {
	b := NewBuilder(1 << 10)
	first := append([]byte(nil), buildAll(b)...)
	second := buildAll(b)
	if !bytes.Equal(first, second) {
		t.Fatal("the second message differs from the first")
	}
	if n := testing.AllocsPerRun(10, func() { buildAll(b) }); n > 3 {
		// the slices of offsets of buildAll itself
		t.Fatalf("%v allocations", n)
	}
}

func TestBuilderGrowsFromNothing(t *testing.T) {
	b := NewBuilder(0)
	big := make([]byte, 100_000)
	for i := range big {
		big[i] = byte(i)
	}
	v := b.Bytes(big)
	b.StartTable(1)
	b.Ref(0, v)
	buf := b.Finish(b.EndTable())
	root, err := Root(buf, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := root.Bytes(0)
	if !ok || err != nil || !bytes.Equal(got, big) {
		t.Fatal(ok, err, len(got))
	}
}

func TestBuilderEmptyTable(t *testing.T) {
	b := NewBuilder(0)
	b.StartTable(3)
	buf := b.Finish(b.EndTable())
	root, err := Root(buf, 1)
	if err != nil || root.Has(0) {
		t.Fatal(err)
	}
}

// TestBuilderSharesVTables: tables of the same shape point to a vtable written once, and each
// still reads its own fields. (A table that starts after a vtable of 6 bytes has 2 bytes of
// padding in it, so its vtable says another size: four tables of one field have two vtables.)
func TestBuilderSharesVTables(t *testing.T) {
	b := NewBuilder(0)
	var offs []Off
	for i := range 4 {
		b.StartTable(2)
		b.Uint32(0, uint32(10+i), 0)
		offs = append(offs, b.EndTable())
	}
	v := b.Offsets(offs)
	b.StartTable(1)
	b.Ref(0, v)
	buf := b.Finish(b.EndTable())
	root, err := Root(buf, 4)
	if err != nil {
		t.Fatal(err)
	}
	list, err := root.Tables(0)
	if err != nil || list.Len() != 4 {
		t.Fatal(err)
	}
	vtables := map[int]bool{}
	for i := range 4 {
		tab, err := list.Table(i)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := tab.Uint32(0, 0); v != uint32(10+i) {
			t.Errorf("table %d: %d", i, v)
		}
		vtables[tab.vt] = true
	}
	if len(vtables) != 2 {
		t.Errorf("4 tables of one field have %d vtables, want 2", len(vtables))
	}
}
