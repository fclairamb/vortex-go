package vortex

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// ----- hand-built files -----

// The footer lists of the hand-built files.
var (
	testArraySpecs  = []string{"vortex.primitive", "vortex.bool", "vortex.struct", "vortex.chunked", "vortex.unsupported", "vortex.variant", "vortex.parquet.variant", "vortex.varbinview"}
	testLayoutSpecs = []string{"vortex.flat", "vortex.struct", "vortex.chunked", "vortex.zoned", "vortex.list", "vortex.stats", "vortex.dict"}
)

const (
	aPrim = iota
	aBool
	aStruct
	aChunked
	aAlp
	aVariant
	aParquetVariant
	aVarBinView
)

const (
	lFlat = iota
	lStruct
	lChunked
	lZoned
	lList
	lStats
	lDict
)

// abuf is a data buffer of a serialized array.
type abuf struct {
	data []byte
	pad  int
}

func aNode(enc int, bufs []uint16, kids ...fbtest.Tab) fbtest.Tab {
	t := fbtest.Tab{0: fbtest.U16(uint16(enc))}
	if kids != nil {
		t[2] = fbtest.Tables(kids...)
	}
	if bufs != nil {
		t[3] = fbtest.U16s(bufs...)
	}
	return t
}

// aSeg serializes an array: the buffers, the flatbuffer, its length.
func aSeg(root fbtest.Tab, bufs ...abuf) []byte {
	var body []byte
	var descs [][]byte
	for _, b := range bufs {
		body = append(body, make([]byte, b.pad)...)
		body = append(body, b.data...)
		d := binary.LittleEndian.AppendUint16(nil, uint16(b.pad))
		descs = append(descs, binary.LittleEndian.AppendUint32(append(d, 0, 0), uint32(len(b.data))))
	}
	msg := fbtest.Tab{0: fbtest.T(root)}
	if descs != nil {
		msg[1] = fbtest.Structs(8, descs...)
	}
	fb := fbtest.Build(msg)
	return binary.LittleEndian.AppendUint32(append(body, fb...), uint32(len(fb)))
}

func bytesOf(size int, v ...uint64) []byte {
	var b []byte
	for _, x := range v {
		b = append(b, binary.LittleEndian.AppendUint64(nil, x)[:size]...)
	}
	return b
}

func bits(v ...bool) []byte {
	b := make([]byte, (len(v)+7)/8)
	for i, x := range v {
		if x {
			b[i/8] |= 1 << (i % 8)
		}
	}
	return b
}

func boolType(nullable bool) fbtest.Tab {
	n := uint8(0)
	if nullable {
		n = 1
	}
	return dtypeTab(2, fbtest.Tab{0: fbtest.U8(n)})
}

func structType(nullable bool, names []string, types ...fbtest.Tab) fbtest.Tab {
	n := uint8(0)
	if nullable {
		n = 1
	}
	return dtypeTab(7, fbtest.Tab{0: fbtest.Strs(names...), 1: fbtest.Tables(types...), 2: fbtest.U8(n)})
}

// buildFile writes a file whose segments are segs, in order, in front of the dtype.
func buildFile(dtype, layout fbtest.Tab, segs ...[]byte) []byte {
	p := defaults()
	var specs [][]byte
	off := uint64(len(magic))
	for _, s := range segs {
		specs = append(specs, segSpec(off, uint32(len(s))))
		p.body = append(p.body, s...)
		off += uint64(len(s))
	}
	p.dtype = fbtest.Build(dtype)
	p.layout = fbtest.Build(layout)
	p.footer = fbtest.Build(fbtest.Tab{
		0: fbtest.Tables(specTabs(testArraySpecs)...),
		1: fbtest.Tables(specTabs(testLayoutSpecs)...),
		2: fbtest.Structs(16, specs...),
	})
	return p.build()
}

func specTabs(ids []string) []fbtest.Tab {
	out := make([]fbtest.Tab, len(ids))
	for i, id := range ids {
		out[i] = fbtest.Tab{0: fbtest.Str(id)}
	}
	return out
}

func scanAll(t *testing.T, f *File, batchRows int) (batches []*Batch, err error) {
	t.Helper()
	for b, e := range f.scan(context.Background(), &scanOptions{batchRows: batchRows}) {
		if e != nil {
			return batches, e
		}
		batches = append(batches, keep(b))
	}
	return batches, nil
}

// keep copies a batch, which is valid for one iteration only.
func keep(b *Batch) *Batch {
	out := &Batch{n: b.n}
	for _, c := range b.cols {
		a := array.Concat(c.arr.Type, []*array.Array{c.arr, c.arr.Slice(0, 0)})
		a.Type = c.arr.Type
		out.cols = append(out.cols, &Column{arr: a, typ: c.typ})
	}
	return out
}

func mustValues[T Number](t *testing.T, c *Column) []T {
	t.Helper()
	v, err := Values[T](c)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func equal[T comparable](t *testing.T, got, want []T) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// flatFile is a file whose root layout is one flat layout holding a struct array:
// {a: i64, b: i8?, s: {x: i32, y: bool}} on 3 rows, the first column's buffer padded by pad.
func flatFile(pad int) []byte {
	dtype := structType(false, []string{"a", "b", "s"},
		prim(7, false), prim(4, true), structType(false, []string{"x", "y"}, prim(6, false), boolType(false)))
	arr := aSeg(
		aNode(aStruct, nil,
			aNode(aPrim, []uint16{0}),
			aNode(aPrim, []uint16{1}, aNode(aBool, []uint16{2})),
			aNode(aStruct, nil, aNode(aPrim, []uint16{3}), aNode(aBool, []uint16{4}))),
		abuf{data: bytesOf(8, 10, 20, 30), pad: pad}, abuf{data: bytesOf(1, 1, 2, 3)}, abuf{data: bits(true, false, true)},
		abuf{data: bytesOf(4, 7, 8, 9)}, abuf{data: bits(false, true, true)})
	return buildFile(dtype, layoutTab(lFlat, 3, []uint32{0}), arr)
}

func TestScanFlatRoot(t *testing.T) {
	for _, pad := range []int{0, 1, 4} { // misaligned buffers give the same values
		f, err := open(flatFile(pad))
		if err != nil {
			t.Fatal(err)
		}
		batches, err := scanAll(t, f, maxBatchRows)
		if err != nil || len(batches) != 1 {
			t.Fatalf("pad %d: %d batches, %v", pad, len(batches), err)
		}
		b := batches[0]
		if b.Len() != 3 {
			t.Fatal(b.Len())
		}
		equal(t, mustValues[int64](t, b.Col(0)), []int64{10, 20, 30})
		equal(t, mustValues[int8](t, b.Col(1)), []int8{1, 2, 3})
		if b.Col(1).Null(0) || !b.Col(1).Null(1) || b.Col(1).Type().String() != "i8?" {
			t.Fatal("column b")
		}
		s := b.Col(2)
		equal(t, mustValues[int32](t, s.Field(0)), []int32{7, 8, 9})
		if s.Field(1).Bool(0) || !s.Field(1).Bool(2) || s.Field(1).Null(0) || s.Null(0) {
			t.Fatal("column s")
		}
	}
}

func TestScanMethod(t *testing.T) {
	f, err := open(flatFile(0))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for b, err := range f.Scan(context.Background(), Query{}) {
		if err != nil || b.Len() != 3 {
			t.Fatal(b, err)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("%d batches", n)
	}
	for b, err := range f.Scan(context.Background(), Query{}, "a") {
		if err != nil || b.Len() != 3 {
			t.Fatal(b, err)
		}
	}
	// Breaking out of the loop stops the scan.
	for range f.Scan(context.Background(), Query{}) {
		break
	}
	// A canceled context comes back as the error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range f.Scan(ctx, Query{}) {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	}
}

// layoutFile is a file with the schema {a: i64, b: i8?} and a struct layout: a is chunked
// (2 rows, none, 1 row), b is flat.
func layoutFile() []byte {
	dtype := structType(false, []string{"a", "b"}, prim(7, false), prim(4, true))
	chunk := func(_ int, v ...uint64) []byte { return aSeg(aNode(aPrim, []uint16{0}), abuf{data: bytesOf(8, v...)}) }
	b := aSeg(aNode(aPrim, []uint16{0}, aNode(aBool, []uint16{1})), abuf{data: bytesOf(1, 5, 6, 7)}, abuf{data: bits(true, true, false)})
	layout := layoutTab(lStruct, 3, nil,
		layoutTab(lChunked, 3, nil, layoutTab(lFlat, 2, []uint32{0}), layoutTab(lFlat, 0, []uint32{1}), layoutTab(lFlat, 1, []uint32{2})),
		layoutTab(lFlat, 3, []uint32{3}))
	return buildFile(dtype, layout, chunk(0, 1, 2), chunk(0), chunk(0, 3), b)
}

func TestScanStructAndChunkedLayouts(t *testing.T) {
	f, err := open(layoutFile())
	if err != nil {
		t.Fatal(err)
	}
	batches, err := scanAll(t, f, 2)
	if err != nil || len(batches) != 2 || batches[0].Len() != 2 || batches[1].Len() != 1 {
		t.Fatalf("%d batches, %v", len(batches), err)
	}
	equal(t, mustValues[int64](t, batches[0].Col(0)), []int64{1, 2})
	equal(t, mustValues[int64](t, batches[1].Col(0)), []int64{3})
	equal(t, mustValues[int8](t, batches[1].Col(1)), []int8{7})
	if !batches[1].Col(1).Null(0) || batches[0].Col(1).Null(1) {
		t.Fatal("nulls")
	}
	// One batch is cut where the chunk ends.
	if batches, err = scanAll(t, f, 100); err != nil || len(batches) != 2 || batches[0].Len() != 2 {
		t.Fatalf("%d batches, %v", len(batches), err)
	}
}

func TestScanNullableStructLayout(t *testing.T) {
	dtype := structType(false, []string{"s"}, structType(true, []string{"x"}, prim(6, false)))
	valid := aSeg(aNode(aBool, []uint16{0}), abuf{data: bits(true, false, true)})
	x := aSeg(aNode(aPrim, []uint16{0}), abuf{data: bytesOf(4, 1, 2, 3)})
	layout := layoutTab(lStruct, 3, nil, layoutTab(lStruct, 3, nil, layoutTab(lFlat, 3, []uint32{0}), layoutTab(lFlat, 3, []uint32{1})))
	f, err := open(buildFile(dtype, layout, valid, x))
	if err != nil {
		t.Fatal(err)
	}
	batches, err := scanAll(t, f, 8)
	if err != nil || len(batches) != 1 {
		t.Fatal(len(batches), err)
	}
	s := batches[0].Col(0)
	if s.Null(0) || !s.Null(1) || s.Null(2) {
		t.Fatal("the struct validity comes from child 0")
	}
	equal(t, mustValues[int32](t, s.Field(0)), []int32{1, 2, 3})
}

func TestScanEmptyFile(t *testing.T) {
	dtype := structType(false, []string{"a"}, prim(7, false))
	f, err := open(buildFile(dtype, layoutTab(lChunked, 0, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if batches, err := scanAll(t, f, 8); err != nil || len(batches) != 0 {
		t.Fatalf("%d batches, %v", len(batches), err)
	}
}

func TestScanLayoutErrors(t *testing.T) {
	dtype := structType(false, []string{"a"}, prim(7, false))
	nested := structType(false, []string{"s"}, structType(true, []string{"x"}, prim(6, false)))
	flat := layoutTab(lFlat, 1, []uint32{0})
	seg := aSeg(aNode(aPrim, []uint16{0}), abuf{data: bytesOf(8, 1)})
	cases := []struct {
		name   string
		dtype  fbtest.Tab
		layout fbtest.Tab
		want   string
	}{
		{"unsupported layout", dtype, layoutTab(lList, 1, nil), `unsupported layout "vortex.list"`},
		{"flat with two segments", dtype, layoutTab(lFlat, 1, []uint32{0, 0}), "2 segments and 0 children"},
		{"flat with a child", dtype, layoutTab(lFlat, 1, []uint32{0}, flat), "1 segments and 1 children"},
		{"struct with a missing field", dtype, layoutTab(lStruct, 1, nil), "0 children for 1 fields"},
		{"struct child of another size", dtype, layoutTab(lStruct, 2, nil, flat), "struct child 0 has 1 rows, its parent 2"},
		{"struct layout of a number", dtype, layoutTab(lStruct, 1, nil, layoutTab(lStruct, 1, nil)), "struct layout for the type i64"},
		{"chunks of another size", dtype, layoutTab(lStruct, 3, nil, layoutTab(lChunked, 3, nil, flat)), "chunked children have 1 rows"},
		{"chunk rows overflow", dtype, layoutTab(lStruct, 0, nil, layoutTab(lChunked, 0, nil,
			layoutTab(lFlat, 1<<63, []uint32{0}), layoutTab(lFlat, 1<<63, []uint32{0}))), "overflow"},
		{"unsupported chunk", dtype, layoutTab(lChunked, 1, nil, layoutTab(lList, 1, nil)), "vortex.list"},
		{"unsupported validity", nested, layoutTab(lStruct, 1, nil, layoutTab(lStruct, 1, nil, layoutTab(lList, 1, nil), flat)), "vortex.list"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := open(buildFile(c.dtype, c.layout, seg))
			if err != nil {
				t.Fatal(err)
			}
			_, err = scanAll(t, f, 8)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if c.name == "unsupported layout" && !errors.Is(err, errors.ErrUnsupported) {
				t.Fatalf("err = %v, want ErrUnsupported", err)
			}
		})
	}
}

// A field whose array no decoder knows is skipped when the scan is told to tolerate it, for
// every batch: the columns the harness hashes are the other ones.
func TestTolerateSkipsAField(t *testing.T) {
	dtype := structType(false, []string{"a", "b"}, prim(7, false), prim(7, false))
	bad := aSeg(aNode(aAlp, nil))
	good := aSeg(aNode(aPrim, []uint16{0}), abuf{data: bytesOf(8, 1, 2)})
	layout := layoutTab(lStruct, 2, nil, layoutTab(lFlat, 2, []uint32{0}), layoutTab(lFlat, 2, []uint32{1}))
	f, err := open(buildFile(dtype, layout, bad, good))
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	opt := scanOptions{batchRows: 1, array: array.Options{Tolerate: func(path string, err error) bool {
		asked = append(asked, path)
		return true
	}}}
	n := 0
	for b, e := range f.scan(context.Background(), &opt) {
		if e != nil {
			t.Fatal(e)
		}
		n++
		if a, c := b.Col(0), b.Col(1); a.arr.Skipped == nil || c.arr.Skipped != nil || mustValues[int64](t, c)[0] != int64(n) {
			t.Fatalf("batch %d: skipped a = %v, b = %v", n, a.arr.Skipped, c.arr.Skipped)
		}
	}
	if n != 2 || len(asked) != 1 || asked[0] != "a" {
		t.Fatalf("%d batches, asked about %v", n, asked)
	}
	// A scan that refuses to tolerate it fails.
	opt.array.Tolerate = func(string, error) bool { return false }
	for _, e := range f.scan(context.Background(), &opt) {
		if !errors.Is(e, errors.ErrUnsupported) {
			t.Fatalf("err = %v", e)
		}
		break
	}
}

func TestScanSegmentErrors(t *testing.T) {
	dtype := structType(false, []string{"a"}, prim(7, false))
	good := aSeg(aNode(aPrim, []uint16{0}), abuf{data: bytesOf(8, 1)})
	layout := layoutTab(lFlat, 1, []uint32{0})
	// The array holds an encoding no decoder knows: unsupported, named, not corrupt.
	f, err := open(buildFile(structType(false, []string{"a"}, prim(7, false)), layout, aSeg(aNode(aStruct, nil, aNode(aAlp, nil)))))
	if err != nil {
		t.Fatal(err)
	}
	_, err = scanAll(t, f, 8)
	if !errors.Is(err, errors.ErrUnsupported) || errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "vortex.unsupported") {
		t.Fatalf("err = %v", err)
	}
	// A segment that is no array is corrupt.
	f, err = open(buildFile(dtype, layout, []byte("not an array at all")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scanAll(t, f, 8); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
	// A field of a struct layout that is corrupt, and its validity.
	nested := structType(false, []string{"s"}, structType(true, []string{"x"}, prim(6, false)))
	for _, segs := range [][][]byte{{good, []byte("xxxxxxxx")}, {[]byte("xxxxxxxx"), good}} {
		l := layoutTab(lStruct, 1, nil, layoutTab(lStruct, 1, nil, layoutTab(lFlat, 1, []uint32{0}), layoutTab(lFlat, 1, []uint32{1})))
		if f, err = open(buildFile(nested, l, segs...)); err != nil {
			t.Fatal(err)
		}
		if _, err = scanAll(t, f, 8); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("err = %v", err)
		}
	}
	// A read that fails after the open.
	raw := buildFile(dtype, layout, good)
	boom := errors.New("boom")
	f, err = Open(&failingReader{r: bytes.NewReader(raw), ok: 1, err: boom}, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scanAll(t, f, 8); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

// ----- columns -----

func TestColumnAccessors(t *testing.T) {
	f, err := open(flatFile(0))
	if err != nil {
		t.Fatal(err)
	}
	batches, _ := scanAll(t, f, 8)
	b := batches[0]
	a := b.Col(0)
	type myInt int64
	if v, err := Values[myInt](a); err != nil || v[2] != 30 {
		t.Fatalf("a named type must work: %v %v", v, err)
	}
	_, err = Values[int32](a)
	if err == nil || !strings.Contains(err.Error(), "int32") || !strings.Contains(err.Error(), "i64") {
		t.Fatalf("the error must name both types: %v", err)
	}
	if _, err = Values[int64](b.Col(2)); err == nil {
		t.Fatal("a struct is not a number")
	}
	if _, err = Values[float32](&Column{arr: &array.Array{Skipped: errors.ErrUnsupported}}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("a skipped column gives its error: %v", err)
	}
	mustPanic(t, "Bool on a number", func() { a.Bool(0) })
	mustPanic(t, "Field on a number", func() { a.Field(0) })
	mustPanic(t, "Field out of range", func() { b.Col(2).Field(2) })
	mustPanic(t, "Field negative", func() { b.Col(2).Field(-1) })
	mustPanic(t, "Null out of range", func() { a.Null(3) })
	mustPanic(t, "Bool out of range", func() { b.Col(2).Field(1).Bool(3) })
	mustPanic(t, "Col out of range", func() { b.Col(3) })
}

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		t.Helper()
		if recover() == nil {
			t.Errorf("%s: no panic", what)
		}
	}()
	fn()
}

func TestHalfFloats(t *testing.T) {
	cases := map[uint16]float32{
		0x3c00: 1, 0xc000: -2, 0x0000: 0, 0x7bff: 65504, 0x0400: float32(math.Ldexp(1, -14)),
		0x0001: float32(math.Ldexp(1, -24)), 0x03ff: float32(math.Ldexp(1023, -24)), 0x7c00: float32(math.Inf(1)),
		0xfc00: float32(math.Inf(-1)),
	}
	var raw []byte
	var want []float32
	for h, f := range cases {
		raw = binary.LittleEndian.AppendUint16(raw, h)
		want = append(want, f)
	}
	col := &Column{arr: &array.Array{Type: array.DType{Kind: array.Prim, PType: array.F16}, Len: len(want), Data: raw}}
	got := mustValues[float32](t, col)
	equal(t, got, want)
	if v, err := Values[float64](col); err == nil {
		t.Fatalf("f16 is only read as float32: %v", v)
	}
	nan := &Column{arr: &array.Array{Type: array.DType{Kind: array.Prim, PType: array.F16}, Len: 2, Data: []byte{0x00, 0x7e, 0x00, 0x80}}}
	v := mustValues[float32](t, nan)
	if !math.IsNaN(float64(v[0])) || math.Float32bits(v[1]) != 0x80000000 {
		t.Fatalf("nan and -0: %v", v)
	}
}

func TestArrayTypeOfDatesAndOthers(t *testing.T) {
	cases := []struct {
		t    Type
		want array.PType
	}{
		{Type{kind: Date, unit: Day}, array.I32},
		{Type{kind: Date, unit: Millisecond}, array.I64},
		{Type{kind: Time, unit: Second}, array.I32},
		{Type{kind: Time, unit: Millisecond}, array.I32},
		{Type{kind: Time, unit: Microsecond}, array.I64},
		{Type{kind: Time, unit: Nanosecond}, array.I64},
		{Type{kind: Timestamp, unit: Second}, array.I64},
	}
	for _, c := range cases {
		d := arrayType(c.t)
		if d.Kind != array.Ext || d.Storage.PType != c.want {
			t.Errorf("%s: %+v", c.t, d)
		}
	}
	if d := arrayType(Type{kind: Variant, nullable: true}); d.Kind != array.Variant || !d.Nullable {
		t.Fatalf("variant: %+v", d)
	}
}

func TestScanDatetimeFixture(t *testing.T) {
	f, err := OpenFile("testdata/fixtures/0.93.0/datetime.vortex")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	batches, err := scanAll(t, f, maxBatchRows)
	if err != nil || len(batches) != 1 {
		t.Fatalf("%d batches, %v", len(batches), err)
	}
	byName := map[string]*Column{}
	for i, fld := range f.Schema().Fields() {
		byName[fld.Name] = batches[0].Col(i)
	}
	if k := byName["date_days"].Type().Kind(); k != Date {
		t.Fatalf("date_days is a %s", k)
	}
	if len(mustValues[int32](t, byName["date_days"])) != 3 || len(mustValues[int32](t, byName["time_seconds"])) != 3 {
		t.Fatal("a date in days and a time in seconds are stored on 32 bits")
	}
	if len(mustValues[int64](t, byName["ts_nanos"])) != 3 {
		t.Fatal("a timestamp is stored on 64 bits")
	}
	if z := byName["ts_eastern"].Type().Zone(); z == "" {
		t.Fatal("the time zone of a timestamp comes from Type().Zone()")
	}
	if _, err := Values[int32](byName["ts_nanos"]); err == nil {
		t.Fatal("a timestamp is not an int32")
	}
}
