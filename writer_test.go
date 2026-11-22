package vortex

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/edition"
	"github.com/fclairamb/vortex-go/internal/fbs"
)

// writeFile writes the chunks of columns, one Write each, and opens the result.
// testOptions are the options of the writer of writeFile: a test that checks what the writer
// does without the compressor lowers them to None.
var testOptions = WriteOptions{}

func writeFile(t testing.TB, s *Schema, chunks ...[]Data) (*File, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, s, testOptions)
	for _, c := range chunks {
		if err := w.Write(c...); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return f, buf.Bytes()
}

func TestSmoke(t *testing.T) {
	g := &gen{r: rand.New(rand.NewPCG(1, 2))}
	for _, ty := range []Type{Int32Type(), Utf8Type().AsNullable()} {
		d, vals := g.make(ty, 50)
		f, _ := writeFile(t, NewSchema(Field{"c", ty}), []Data{d})
		got, err := readBack(f, ty)
		if err != nil {
			t.Fatal(err)
		}
		for i := range vals {
			if !eqVal(vals[i], got[i]) {
				t.Fatalf("%s row %d: %s vs %s", ty, i, describeVal(vals[i]), describeVal(got[i]))
			}
		}
	}
}

// allTypes is every logical type, alone and nested, not nullable and nullable.
func allTypes() map[string]Type {
	base := map[string]Type{
		"null": NullType(), "bool": BoolType(), "i8": Int8Type(), "i16": Int16Type(), "i32": Int32Type(),
		"i64": Int64Type(), "u8": Uint8Type(), "u16": Uint16Type(), "u32": Uint32Type(), "u64": Uint64Type(),
		"f16": Float16Type(), "f32": Float32Type(), "f64": Float64Type(), "utf8": Utf8Type(),
		"binary": BinaryType(), "uuid": UUIDType(),
		"dec1": DecimalOf(1, 0), "dec3": DecimalOf(3, 1), "dec5": DecimalOf(5, 2), "dec9": DecimalOf(9, 9), "dec18": DecimalOf(18, 4),
		"dec20": DecimalOf(20, 0), "dec38": DecimalOf(38, 10), "dec50": DecimalOf(50, -3), "dec76": DecimalOf(76, 20),
		"date_d": DateType(Day), "date_ms": DateType(Millisecond), "time_s": TimeType(Second),
		"time_ms": TimeType(Millisecond), "time_us": TimeType(Microsecond), "time_ns": TimeType(Nanosecond),
		"ts_ns": TimestampType(Nanosecond, ""), "ts_s_utc": TimestampType(Second, "UTC"),
		"ts_us_paris": TimestampType(Microsecond, "Europe/Paris"),
	}
	out := map[string]Type{}
	for name, t := range base {
		out[name] = t
		out[name+"?"] = t.AsNullable()
	}
	nested := map[string]Type{
		"list_i32":     ListOf(Int32Type()),
		"list_str?":    ListOf(Utf8Type().AsNullable()).AsNullable(),
		"fsl_f64":      FixedSizeListOf(Float64Type(), 3),
		"fsl_str?":     FixedSizeListOf(Utf8Type().AsNullable(), 2).AsNullable(),
		"fsl_0":        FixedSizeListOf(Int8Type(), 0),
		"map":          MapOf(Utf8Type(), Int64Type().AsNullable()),
		"map?":         MapOf(Int32Type(), ListOf(BoolType())).AsNullable(),
		"struct":       StructOf(Field{"a", Int32Type()}, Field{"b", Utf8Type().AsNullable()}),
		"struct?":      StructOf(Field{"a", Int32Type().AsNullable()}, Field{"b", Utf8Type()}).AsNullable(),
		"list_list":    ListOf(ListOf(Int16Type().AsNullable())),
		"list_struct":  ListOf(StructOf(Field{"x", Float32Type()}, Field{"y", UUIDType().AsNullable()}).AsNullable()),
		"struct_list?": StructOf(Field{"l", ListOf(Int64Type())}, Field{"m", MapOf(Utf8Type(), Utf8Type())}).AsNullable(),
		"list_ts":      ListOf(TimestampType(Millisecond, "UTC").AsNullable()),
	}
	for name, t := range nested {
		out[name] = t
	}
	deep := Int32Type().AsNullable()
	for range 5 {
		deep = StructOf(Field{"v", deep}, Field{"l", ListOf(deep)}).AsNullable()
	}
	out["deep5"] = deep
	return out
}

// checkRoundTrip writes rows in the given chunk sizes and reads them back.
func checkRoundTrip(t *testing.T, name string, ty Type, g *gen, sizes ...int) {
	t.Helper()
	var chunks [][]Data
	var want []val
	for _, n := range sizes {
		d, vals := g.make(ty, n)
		chunks = append(chunks, []Data{d})
		want = append(want, vals...)
	}
	f, _ := writeFile(t, NewSchema(Field{"c", ty}), chunks...)
	if f.NumRows() != int64(len(want)) {
		t.Fatalf("%s: %d rows, want %d", name, f.NumRows(), len(want))
	}
	if got, ok := f.Schema().Field("c"); !ok || !got.Type.Equal(ty) {
		t.Fatalf("%s: the schema is %s, want %s", name, f.Schema(), ty)
	}
	got, err := readBack(f, ty)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s: read %d rows, want %d", name, len(got), len(want))
	}
	for i := range want {
		if !eqVal(want[i], got[i]) {
			t.Fatalf("%s %s: row %d is %s, want %s", name, ty, i, describeVal(got[i]), describeVal(want[i]))
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for name, ty := range allTypes() {
		t.Run(name, func(t *testing.T) {
			for seed, sizes := range [][]int{{0}, {1}, {100}, {7, 300, 1}, {0, 5, 0, 5}} {
				g := &gen{r: rand.New(rand.NewPCG(uint64(seed), 7))}
				checkRoundTrip(t, name, ty, g, sizes...)
			}
		})
	}
}

func TestRoundTripAllNull(t *testing.T) {
	for name, ty := range allTypes() {
		if !ty.nullable {
			continue
		}
		g := &gen{r: rand.New(rand.NewPCG(3, 3)), allNull: true}
		checkRoundTrip(t, name, ty, g, 40)
	}
}

func TestRoundTripBigStrings(t *testing.T) {
	g := &gen{r: rand.New(rand.NewPCG(5, 5)), bigStr: true}
	checkRoundTrip(t, "big", Utf8Type().AsNullable(), g, 300)
}

func TestRoundTrip200Columns(t *testing.T) {
	g := &gen{r: rand.New(rand.NewPCG(9, 9))}
	types := []Type{Int64Type(), Utf8Type().AsNullable(), Float32Type(), BoolType().AsNullable()}
	var fields []Field
	var cols []Data
	var want [][]val
	for i := range 200 {
		ty := types[i%len(types)]
		d, vals := g.make(ty, 20)
		fields = append(fields, Field{Name: "c" + string(rune('a'+i/26)) + string(rune('a'+i%26)), Type: ty})
		cols = append(cols, d)
		want = append(want, vals)
	}
	f, _ := writeFile(t, NewSchema(fields...), cols)
	if len(f.Schema().Fields()) != 200 {
		t.Fatal(len(f.Schema().Fields()))
	}
	for b, err := range f.Scan(t.Context(), Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		for c := range 200 {
			for i := range b.Len() {
				if got := readRow(b.Col(c), fields[c].Type, i); !eqVal(got, want[c][i]) {
					t.Fatalf("column %d row %d", c, i)
				}
			}
		}
	}
}

var errFault = errors.New("disk on fire")

// failingWriter fails its n-th call to Write, and every one after.
type failingWriter struct {
	calls, failAt int
	short         bool
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls < f.failAt {
		return len(p), nil
	}
	if f.short {
		return len(p) / 2, nil
	}
	return 0, errFault
}

// writeAll writes two chunks of a wide schema, and closes: it returns the first error.
func writeAll(w io.Writer, g *gen, fields []Field) error {
	wr := NewWriter(w, NewSchema(fields...), WriteOptions{})
	for _, n := range []int{50, 3} {
		cols := make([]Data, len(fields))
		for i, f := range fields {
			cols[i], _ = g.make(f.Type, n)
		}
		if err := wr.Write(cols...); err != nil {
			return err
		}
	}
	return wr.Close()
}

func TestWriteFaults(t *testing.T) {
	fields := []Field{{"a", Int64Type()}, {"b", Utf8Type().AsNullable()}, {"c", ListOf(Float64Type())}}
	for _, short := range []bool{false, true} {
		stopped := false
		for n := 1; n < 200; n++ {
			fw := &failingWriter{failAt: n, short: short}
			err := writeAll(fw, &gen{r: rand.New(rand.NewPCG(4, 4))}, fields)
			if err == nil {
				stopped = true
				if fw.calls >= n {
					t.Fatalf("short=%v: call %d failed but no error was returned", short, n)
				}
				break
			}
			want := errFault
			if short {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) {
				t.Fatalf("short=%v, call %d: %v does not wrap %v", short, n, err, want)
			}
		}
		if !stopped {
			t.Fatal("the writer never succeeded")
		}
	}
}

func TestWriteFaultKeepsError(t *testing.T) {
	// Tiny pending buffer and chunks: every Write reaches the io.Writer, so the error is the
	// Write's own.
	lowered(t, &smallWrites, 16)
	lowered(t, &rowBlock, 4)
	lowered(t, &chunkBytes, 1)
	fw := &failingWriter{failAt: 2}
	w := NewWriter(fw, NewSchema(Field{"a", Int64Type()}), WriteOptions{})
	d := Of([]int64{1, 2, 3, 4, 5, 6, 7, 8})
	err := w.Write(d)
	for i := 0; err == nil && i < 5; i++ {
		err = w.Write(d)
	}
	if !errors.Is(err, errFault) {
		t.Fatal(err)
	}
	if again := w.Write(d); !errors.Is(again, errFault) {
		t.Fatalf("after an error Write returns %v", again)
	}
	if err := w.Close(); !errors.Is(err, errFault) {
		t.Fatalf("Close returns %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("a second Close returns %v", err)
	}
	if err := w.Write(d); !errors.Is(err, ErrWriterClosed) {
		t.Fatalf("Write after Close returns %v", err)
	}
}

func TestCloseTwice(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, NewSchema(Field{"a", Int8Type()}), WriteOptions{Compression: Compact})
	if err := w.Write(Of([]int8{1})); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	size := buf.Len()
	if err := w.Close(); err != nil || buf.Len() != size {
		t.Fatal(err, buf.Len(), size)
	}
	if err := w.Write(Of([]int8{1})); !errors.Is(err, ErrWriterClosed) {
		t.Fatal(err)
	}
}

func TestEmptySchemaAndNoWrite(t *testing.T) {
	f, _ := writeFile(t, NewSchema())
	if f.NumRows() != 0 || len(f.Schema().Fields()) != 0 {
		t.Fatal(f.NumRows(), f.Schema())
	}
	f, _ = writeFile(t, NewSchema(Field{"a", Int32Type()}, Field{"s", StructOf(Field{"x", Utf8Type()}).AsNullable()}))
	if f.NumRows() != 0 {
		t.Fatal(f.NumRows())
	}
	n := 0
	for _, err := range f.Scan(t.Context(), Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 0 {
		t.Fatal(n)
	}
}

func TestNoAllocationPerWrite(t *testing.T) {
	if raceEnabled {
		t.Skip("the sync.Pool of the race detector drops what it is given at random, and the encoders use pools")
	}
	w := NewWriter(io.Discard, NewSchema(Field{"a", Int64Type()}, Field{"b", Float64Type().AsNullable()},
		Field{"c", Int8Type()}), WriteOptions{})
	a, b, c := make([]int64, 1000), make([]float64, 1000), make([]int8, 1000)
	null := make([]bool, 1000)
	null[3] = true
	da, db, dc := Of(a), Of(b).Nulls(null), Of(c)
	// the first chunks of each column, compressed, size the memory of the writer
	for range 2200 {
		if err := w.Write(da, db, dc); err != nil {
			t.Fatal(err)
		}
	}
	if n := testing.AllocsPerRun(200, func() {
		if err := w.Write(da, db, dc); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("%v allocations per Write", n)
	}
}

func TestWriterDoesNotKeepSlices(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, NewSchema(Field{"a", Int32Type()}, Field{"s", Utf8Type()}), WriteOptions{})
	nums := []int32{1, 2, 3}
	strs := []string{"one", "two", "three-and-more-than-12"}
	if err := w.Write(Of(nums), OfStrings(strs)); err != nil {
		t.Fatal(err)
	}
	clear(nums)
	strs[2] = "overwritten"
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for b, err := range f.Scan(t.Context(), Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		v, _ := Values[int32](b.Col(0))
		if v[2] != 3 || string(b.Col(1).Bytes(2)) != "three-and-more-than-12" {
			t.Fatal(v, b.Col(1).Bytes(2))
		}
	}
}

// extIDs lists the extension identifiers of a dtype flatbuffer.
func extIDs(t *testing.T, d fbs.DType, out map[string]bool) {
	t.Helper()
	k, err := d.Kind()
	if err != nil {
		t.Fatal(err)
	}
	var kids []fbs.DType
	switch k {
	case fbs.KindExtension:
		id, storage, _, err := d.Extension()
		if err != nil {
			t.Fatal(err)
		}
		out[id] = true
		kids = append(kids, storage)
	case fbs.KindStruct:
		_, types, err := d.Struct()
		if err != nil {
			t.Fatal(err)
		}
		for i := range types.Len() {
			c, _ := types.At(i)
			kids = append(kids, c)
		}
	case fbs.KindList:
		e, _ := d.List()
		kids = append(kids, e)
	case fbs.KindFixedSizeList:
		e, _, _ := d.FixedSizeList()
		kids = append(kids, e)
	case fbs.KindMap:
		key, value, _, _ := d.Map()
		kids = append(kids, key, value)
	}
	for _, c := range kids {
		extIDs(t, c, out)
	}
}

// TestFileUsesTheEdition checks that every identifier in a file we wrote is in the edition:
// the arrays, the layouts and the extension types.
func TestFileUsesTheEdition(t *testing.T) {
	types := allTypes()
	var fields []Field
	for name, ty := range types {
		fields = append(fields, Field{Name: name, Type: ty})
	}
	slices.SortFunc(fields, func(a, b Field) int { return strings.Compare(a.Name, b.Name) })
	g := &gen{r: rand.New(rand.NewPCG(8, 8))}
	cols := make([]Data, len(fields))
	for i, f := range fields {
		cols[i], _ = g.make(f.Type, 10)
	}
	f, file := writeFile(t, NewSchema(fields...), cols, cols)
	for _, id := range f.arrayEncodings {
		if !edition.Has(edition.KindArray, id) {
			t.Errorf("array %q is not in the edition", id)
		}
	}
	for _, id := range f.layoutEncodings {
		if !edition.Has(edition.KindLayout, id) {
			t.Errorf("layout %q is not in the edition", id)
		}
	}
	// The dtype segment: the postscript says where it is.
	psLen := int(binary.LittleEndian.Uint16(file[len(file)-6:]))
	ps, err := fbs.ReadPostscript(file[len(file)-8-psLen : len(file)-8])
	if err != nil {
		t.Fatal(err)
	}
	seg, ok, err := ps.DType()
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	off, _ := seg.Offset()
	n, _ := seg.Length()
	d, err := fbs.ReadDType(file[off:off+uint64(n)], maxDTypeDepth)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	extIDs(t, d, ids)
	if len(ids) != 4 {
		t.Fatalf("extension types %v, want date, time, timestamp and uuid", ids)
	}
	for id := range ids {
		if !edition.Has(edition.KindDType, id) {
			t.Errorf("extension type %q is not in the edition", id)
		}
	}
	for _, want := range []string{"vortex.primitive", "vortex.varbinview", "vortex.listview", "vortex.map", "vortex.decimal",
		"vortex.struct", "vortex.fixed_size_list", "vortex.ext", "vortex.bool", "vortex.null"} {
		if !slices.Contains(f.arrayEncodings, want) {
			t.Errorf("the file has no %s array: %v", want, f.arrayEncodings)
		}
	}
	for _, want := range []string{"vortex.struct", "vortex.zoned", "vortex.dict", "vortex.flat"} {
		if !slices.Contains(f.layoutEncodings, want) {
			t.Errorf("the file has no %s layout: %v", want, f.layoutEncodings)
		}
	}
}

// TestSegmentsAreAligned checks the segment map: sorted, no overlap, every buffer aligned.
func TestSegmentsAreAligned(t *testing.T) {
	g := &gen{r: rand.New(rand.NewPCG(2, 2))}
	d1, _ := g.make(Float64Type().AsNullable(), 33)
	d2, _ := g.make(Utf8Type(), 33)
	d3, _ := g.make(DecimalOf(30, 2), 33)
	f, _ := writeFile(t, NewSchema(Field{"a", Float64Type().AsNullable()}, Field{"b", Utf8Type()}, Field{"c", DecimalOf(30, 2)}),
		[]Data{d1, d2, d3}, []Data{d1, d2, d3})
	var end uint64 = 4
	for i, s := range f.segments {
		if s.Offset < end {
			t.Fatalf("segment %d starts at %d, the one before ends at %d", i, s.Offset, end)
		}
		if s.Offset%(1<<s.AlignmentExponent) != 0 {
			t.Fatalf("segment %d at %d is not aligned to 2^%d", i, s.Offset, s.AlignmentExponent)
		}
		end = s.Offset + uint64(s.Length)
	}
	if len(f.segments) < 6 {
		t.Fatalf("%d segments, want 3 columns of data and of zones", len(f.segments))
	}
}

func TestNoNullsOnANonNullableField(t *testing.T) {
	// Nulls with no null row is fine for a field that is not nullable.
	f, _ := writeFile(t, NewSchema(Field{"a", Int32Type()}), []Data{Of([]int32{1, 2}).Nulls([]bool{false, false})})
	if f.NumRows() != 2 {
		t.Fatal(f.NumRows())
	}
}
