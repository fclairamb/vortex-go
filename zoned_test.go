package vortex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

func extraFixture(t *testing.T, name string) []byte {
	t.Helper()
	paths := mustGlob(t, filepath.Join("testdata", "fixtures", "*", "extra", name))
	if len(paths) != 1 {
		t.Fatalf("fixture %s: %v", name, paths)
	}
	b, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func walkLayout(l *layoutNode, fn func(*layoutNode)) {
	fn(l)
	for _, c := range l.children {
		walkLayout(c, fn)
	}
}

func layoutIDs(f *File) map[string]int {
	ids := map[string]int{}
	walkLayout(f.layout, func(l *layoutNode) { ids[f.layoutEncodings[l.encoding]]++ })
	return ids
}

func TestDefaultStrategyLayouts(t *testing.T) {
	f, err := open(extraFixture(t, "default_ints.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	ids := layoutIDs(f)
	for _, want := range []string{"vortex.zoned", "vortex.dict", "vortex.chunked", "vortex.flat", "vortex.struct"} {
		if ids[want] == 0 {
			t.Errorf("default_ints has no %s layout: %v", want, ids)
		}
	}
}

func TestZonedMetadataOfFixtures(t *testing.T) {
	aggs := map[string]bool{}
	for _, name := range []string{"default_ints.vortex", "default_mixed.vortex", "default_compact.vortex"} {
		f, err := open(extraFixture(t, name))
		if err != nil {
			t.Fatal(err)
		}
		zones := 0
		walkLayout(f.layout, func(l *layoutNode) {
			if f.layoutEncodings[l.encoding] != "vortex.zoned" {
				return
			}
			z, err := parseZoned(l.metadata)
			if err != nil {
				t.Fatal(err)
			}
			if z.zoneLen != 8192 || len(z.aggregates) == 0 || z.legacy {
				t.Errorf("%s: zone map %+v", name, z)
			}
			for _, a := range z.aggregates {
				aggs[a.id] = true
			}
			zones++
		})
		if zones == 0 {
			t.Errorf("%s: no zoned layout", name)
		}
	}
	want := []string{"vortex.bounded_max", "vortex.bounded_min", "vortex.max", "vortex.min", "vortex.nan_count", "vortex.null_count"}
	var got []string
	for id := range aggs {
		got = append(got, id)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("aggregates %v, want %v", got, want)
	}
}

// A zone map is not kept under a nullable struct: the column is the struct layout itself.
func TestNoZoneMapUnderNullableStruct(t *testing.T) {
	f, err := open(extraFixture(t, "default_mixed.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	for i, fld := range f.schema.root.fields {
		got := f.layoutEncodings[f.layout.children[i].encoding]
		want := "vortex.zoned"
		if fld.Name == "rec" {
			want = "vortex.struct"
		}
		if got != want {
			t.Errorf("column %s is a %s layout, want %s", fld.Name, got, want)
		}
	}
}

// logReader records every read.
type logReader struct {
	r    io.ReaderAt
	offs []int64
}

func (l *logReader) ReadAt(p []byte, off int64) (int, error) {
	l.offs = append(l.offs, off)
	return l.r.ReadAt(p, off)
}

func readRange(t *testing.T, f *File, lo, hi, batch int) (*array.Array, []int64) {
	t.Helper()
	log := &logReader{r: f.r}
	f.r = log
	defer func() { f.r = log.r }()
	rd, err := f.newReader(f.layout, arrayType(f.schema.root), array.Options{}, "", lo, hi)
	if err != nil {
		t.Fatal(err)
	}
	var parts []*array.Array
	for n := min(batch, rd.avail()); n > 0; n = min(batch, rd.avail()) {
		a, err := rd.take(n)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, a)
	}
	return array.Concat(arrayType(f.schema.root), parts), log.offs
}

func TestRowRangeReadsLess(t *testing.T) {
	f, err := open(extraFixture(t, "default_ints.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	full, fullReads := readRange(t, f, 0, 30000, 8192)
	head, headReads := readRange(t, f, 0, 100, 8192)
	if head.Len != 100 || full.Len != 30000 {
		t.Fatalf("%d and %d rows", head.Len, full.Len)
	}
	if len(headReads) >= len(fullReads) {
		t.Errorf("100 rows read %d segments, the full scan %d", len(headReads), len(fullReads))
	}
	// The values of a dictionary, like every segment, are read once by a full scan.
	seen := map[int64]bool{}
	for _, off := range fullReads {
		if seen[off] {
			t.Errorf("the segment at offset %d is read twice", off)
		}
		seen[off] = true
	}
	// A range in the middle gives the rows of the full read, across the chunks.
	for _, r := range [][2]int{{8190, 8200}, {16380, 24580}, {29990, 30000}, {5, 5}} {
		got, _ := readRange(t, f, r[0], r[1], 1000)
		for i := 0; i < r[1]-r[0]; i++ {
			g := binary.LittleEndian.Uint64(got.Fields[0].Data[8*i:])
			if g != uint64(1000+r[0]+i) {
				t.Fatalf("range %v row %d: sorted = %d", r, i, g)
			}
			if !bytes.Equal(got.Fields[1].Data[8*i:8*i+8], full.Fields[1].Data[8*(r[0]+i):8*(r[0]+i)+8]) {
				t.Fatalf("range %v row %d: low_card differs", r, i)
			}
		}
	}
	for _, r := range [][2]int{{-1, 5}, {5, 4}, {0, 30001}} {
		if _, err := f.newReader(f.layout, arrayType(f.schema.root), array.Options{}, "", r[0], r[1]); !errors.Is(err, ErrCorrupt) {
			t.Errorf("range %v: err = %v", r, err)
		}
	}
}

// A vortex.stats layout made of a column of default_ints: the same children, the older
// metadata.
func TestLegacyStatsLayout(t *testing.T) {
	f, err := open(extraFixture(t, "default_ints.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	f.layoutEncodings = append(f.layoutEncodings, "vortex.stats")
	meta := binary.LittleEndian.AppendUint32(nil, 8192)
	meta = append(meta, 0x1f, 0x00)
	for i := range 4 {
		z := *f.layout.children[i]
		z.encoding = uint16(len(f.layoutEncodings) - 1)
		z.metadata = meta
		want, err := f.newReader(f.layout.children[i], arrayType(f.schema.root.fields[i].Type), array.Options{}, "", 0, 30000)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.newReader(&z, arrayType(f.schema.root.fields[i].Type), array.Options{}, "", 0, 30000)
		if err != nil {
			t.Fatal(err)
		}
		if zr, ok := got.(*zoned); !ok || !zr.zones.legacy || zr.zones.zoneLen != 8192 || !bytes.Equal(zr.zones.stats, meta[4:]) || zr.zones.zones == nil {
			t.Fatalf("column %d: reader %#v", i, got)
		}
		a, err := want.take(8192)
		if err != nil {
			t.Fatal(err)
		}
		b, err := got.take(8192)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.Data, b.Data) || a.Len != b.Len {
			t.Errorf("column %d differs", i)
		}
	}
}

// ----- hand-built layouts -----

func metaLayout(enc uint16, rows uint64, meta []byte, kids ...fbtest.Tab) fbtest.Tab {
	t := layoutTab(enc, rows, nil, kids...)
	t[2] = fbtest.Bytes(meta)
	return t
}

func zonedMeta(version byte, zoneLen uint64, ids ...string) []byte {
	b := []byte{version}
	b = append(b, 0x08)
	b = binary.AppendUvarint(b, zoneLen)
	for _, id := range ids {
		spec := append([]byte{0x0a, byte(len(id))}, id...)
		spec = append(spec, 0x12, 0x00)
		b = append(b, 0x12, byte(len(spec)))
		b = append(b, spec...)
	}
	return b
}

func primSeg(width int, v ...uint64) []byte {
	return aSeg(aNode(aPrim, []uint16{0}), abuf{data: bytesOf(width, v...)})
}

func TestZonedLayouts(t *testing.T) {
	dtype := structType(false, []string{"a"}, prim(7, false))
	data := layoutTab(lFlat, 3, []uint32{0})
	zones := layoutTab(lFlat, 1, []uint32{1})
	segs := [][]byte{primSeg(8, 1, 2, 3), primSeg(8, 9)}
	good := zonedMeta(1, 8192, "vortex.max", "an.unknown.aggregate")

	f, err := open(buildFile(dtype, layoutTab(lStruct, 3, nil, metaLayout(lZoned, 3, good, data, zones)), segs...))
	if err != nil {
		t.Fatal(err)
	}
	batches, err := scanAll(t, f, 8)
	if err != nil || len(batches) != 1 {
		t.Fatal(len(batches), err)
	}
	equal(t, mustValues[int64](t, batches[0].Col(0)), []int64{1, 2, 3})

	z, err := parseZoned(good)
	if err != nil || z.zoneLen != 8192 || len(z.aggregates) != 2 || z.aggregates[1].id != "an.unknown.aggregate" {
		t.Fatalf("%+v, %v", z, err)
	}

	cases := []struct {
		name   string
		layout fbtest.Tab
		want   string
	}{
		{"one child", metaLayout(lZoned, 3, good, data), "1 children, want 2"},
		{"three children", metaLayout(lZoned, 3, good, data, zones, zones), "3 children, want 2"},
		{"bad version", metaLayout(lZoned, 3, zonedMeta(2, 8192), data, zones), "version 2, want 1"},
		{"no metadata", metaLayout(lZoned, 3, nil, data, zones), "metadata is empty"},
		{"no protobuf", metaLayout(lZoned, 3, []byte{1}, data, zones), "no protobuf"},
		{"truncated protobuf", metaLayout(lZoned, 3, []byte{1, 0x12, 0x40}, data, zones), "zoned metadata"},
		{"zone length too large", metaLayout(lZoned, 3, zonedMeta(1, 1<<32), data, zones), "overflows 32 bits"},
		{"truncated aggregate", metaLayout(lZoned, 3, []byte{1, 0x12, 0x03, 0x0a, 0x09, 'x'}, data, zones), "aggregate spec"},
		{"rows do not add up", metaLayout(lZoned, 4, good, data, zones), "zoned data has 3 rows, its parent 4"},
		{"stats metadata too short", metaLayout(lStats, 3, []byte{1, 2, 3}, data, zones), "want 4 at least"},
		{"stats with one child", metaLayout(lStats, 3, []byte{0, 0, 0, 0}, data), "1 children, want 2"},
		{"unsupported data", metaLayout(lZoned, 3, good, layoutTab(lList, 3, nil), zones), "vortex.list"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := uint64(3)
			if c.name == "rows do not add up" {
				rows = 4 // the struct agrees with the zoned layout, not with its data
			}
			f, err := open(buildFile(dtype, layoutTab(lStruct, rows, nil, c.layout), segs...))
			if err != nil {
				t.Fatal(err)
			}
			_, err = scanAll(t, f, 8)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if !strings.Contains(c.want, "vortex.list") && !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestDictLayouts(t *testing.T) {
	dtype := structType(false, []string{"a"}, prim(7, false))
	values := layoutTab(lFlat, 3, []uint32{0})
	codes := layoutTab(lFlat, 4, []uint32{1})
	meta := []byte{0x08, 0x00} // codes_ptype u8
	segs := [][]byte{primSeg(8, 10, 20, 30), primSeg(1, 2, 0, 1, 1)}
	build := func(rows uint64, meta []byte, kids ...fbtest.Tab) *File {
		f, err := open(buildFile(dtype, layoutTab(lStruct, rows, nil, metaLayout(lDict, rows, meta, kids...)), segs...))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	f := build(4, meta, values, codes)
	for _, batch := range []int{8, 1, 3} {
		batches, err := scanAll(t, f, batch)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, b := range batches {
			got = append(got, mustValues[int64](t, b.Col(0))...)
		}
		equal(t, got, []int64{30, 10, 20, 20})
	}

	cases := []struct {
		name  string
		rows  uint64
		meta  []byte
		kids  []fbtest.Tab
		codes []byte
		want  string
	}{
		{"one child", 4, meta, []fbtest.Tab{values}, nil, "1 children, want 2"},
		{"codes of another size", 5, meta, []fbtest.Tab{values, codes}, nil, "dict codes have 4 rows, their parent 5"},
		{"code past the values", 4, meta, []fbtest.Tab{values, codes}, []byte{2, 0, 3, 1}, "code 3 at row 2 is past the 3 values"},
		{"codes of a float type", 4, []byte{0x08, 0x08}, []fbtest.Tab{values, codes}, nil, "not an integer type"},
		{"truncated metadata", 4, []byte{0x08}, []fbtest.Tab{values, codes}, nil, "dict metadata"},
		{"unsupported values", 4, meta, []fbtest.Tab{layoutTab(lList, 3, nil), codes}, nil, "vortex.list"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs := segs
			if c.codes != nil {
				segs = [][]byte{segs[0], primSeg(1, 2, 0, 3, 1)}
			}
			f, err := open(buildFile(dtype, layoutTab(lStruct, c.rows, nil, metaLayout(lDict, c.rows, c.meta, c.kids...)), segs...))
			if err != nil {
				t.Fatal(err)
			}
			_, err = scanAll(t, f, 8)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if !strings.Contains(c.want, "vortex.list") && !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestDictLayoutOddities(t *testing.T) {
	dtype := structType(false, []string{"a"}, prim(7, false))
	values := layoutTab(lFlat, 3, []uint32{0})
	codes := layoutTab(lFlat, 4, []uint32{1})
	meta := []byte{0x08, 0x00}
	bad := aSeg(aNode(aAlp, nil))
	for name, c := range map[string]struct {
		kids []fbtest.Tab
		seg  []byte
	}{
		"unsupported codes layout": {[]fbtest.Tab{values, layoutTab(lList, 4, nil)}, primSeg(1, 2, 0, 1, 1)},
		"unsupported codes array":  {[]fbtest.Tab{values, codes}, bad},
	} {
		f, err := open(buildFile(dtype, layoutTab(lStruct, 4, nil, metaLayout(lDict, 4, meta, c.kids...)), primSeg(8, 10, 20, 30), c.seg))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = scanAll(t, f, 8); err == nil || !strings.Contains(err.Error(), "vortex.") || !errors.Is(err, errors.ErrUnsupported) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	// Fields of another wire type, and the other fields of the metadata, are ignored.
	p, nullable, err := parseDict([]byte{0x08, 0x01, 0x10, 0x01, 0x18, 0x01, 0x22, 0x00}, false)
	if err != nil || p != array.U16 || !nullable {
		t.Fatalf("%v %v %v", p, nullable, err)
	}
	spec, err := parseAggregateSpec([]byte{0x08, 0x01, 0x0a, 0x01, 'x', 0x12, 0x01, 0xff})
	if err != nil || spec.id != "x" || !bytes.Equal(spec.options, []byte{0xff}) {
		t.Fatalf("%+v %v", spec, err)
	}

	// No rows at all.
	f, err := open(buildFile(dtype, layoutTab(lChunked, 0, nil)))
	if err != nil {
		t.Fatal(err)
	}
	rd, err := f.newReader(f.layout, array.DType{Kind: array.Prim, PType: array.I64}, array.Options{}, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := readAll(rd, array.DType{Kind: array.Prim, PType: array.I64}); err != nil || a.Len != 0 {
		t.Fatal(a, err)
	}
}
