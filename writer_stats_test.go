package vortex

import (
	"encoding/binary"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/fbs"
	"github.com/fclairamb/vortex-go/internal/scalar"
)

// rootStats reads the statistics of the root node of the array of a segment.
func rootStats(t *testing.T, f *File, file []byte, seg uint32) fbs.ArrayStats {
	t.Helper()
	s := f.segments[seg]
	b := file[s.Offset : s.Offset+uint64(s.Length)]
	n := int(binary.LittleEndian.Uint32(b[len(b)-4:]))
	arr, err := fbs.ReadArray(b[len(b)-4-n:len(b)-4], maxDTypeDepth)
	if err != nil {
		t.Fatal(err)
	}
	root, ok, err := arr.Root()
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	st, ok, err := root.Stats()
	if !ok || err != nil {
		t.Fatalf("no statistics on the root node: %v %v", ok, err)
	}
	return st
}

// sv decodes the values the statistics return.
type sv struct{ t *testing.T }

func (s sv) of(b []byte, ok bool, err error) scalar.Value {
	t := s.t
	t.Helper()
	if !ok || err != nil {
		t.Fatalf("no value: %v %v", ok, err)
	}
	v, err := scalar.Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestArrayStatistics(t *testing.T) {
	long := strings.Repeat("z", 70)
	f, file := writeFile(t, NewSchema(Field{"k", Int64Type()}, Field{"s", Utf8Type().AsNullable()}, Field{"f", Float64Type()},
		Field{"c", Int8Type()}, Field{"d", DecimalOf(5, 2).AsNullable()}, Field{"n", NullType()}),
		[]Data{Of([]int64{5, 3, 9}), OfStrings([]string{"b", long, ""}).Nulls([]bool{false, false, true}),
			Of([]float64{1, math.NaN(), 3}), Of([]int8{4, 5, 6}), OfDecimals([]Decimal{DecimalFromInt64(7), DecimalFromInt64(-2), {}}).Nulls([]bool{false, false, true}),
			OfNulls(3)})
	stats := func(col int) fbs.ArrayStats {
		return rootStats(t, f, file, f.layout.children[col].children[0].segments[0])
	}
	k := stats(0)
	if v := (sv{t}).of(k.Min()); v.Int != 3 {
		t.Errorf("min %+v", v)
	}
	if v := (sv{t}).of(k.Max()); v.Int != 9 {
		t.Errorf("max %+v", v)
	}
	if p, _ := k.MinPrecision(); p != fbs.Exact {
		t.Error("an exact min")
	}
	if n, ok, _ := k.NullCount(); !ok || n != 0 {
		t.Errorf("null count %d", n)
	}
	if sorted, ok, _ := k.IsSorted(); !ok || sorted {
		t.Error("5, 3, 9 is not sorted")
	}
	if c, ok, _ := k.IsConstant(); !ok || c {
		t.Error("5, 3, 9 is not constant")
	}
	c := stats(3)
	if sorted, _, _ := c.IsSorted(); !sorted {
		t.Error("4, 5, 6 is sorted")
	}
	if constant, _, _ := c.IsConstant(); constant {
		t.Error("4, 5, 6 is not constant")
	}
	// Two rows of the same value are not worth a dictionary.
	f2, file2 := writeFile(t, NewSchema(Field{"c", Int8Type()}), []Data{Of([]int8{4, 4})})
	if constant, ok, _ := rootStats(t, f2, file2, f2.layout.children[0].children[0].segments[0]).IsConstant(); !ok || !constant {
		t.Error("4, 4 is constant")
	}
	s := stats(1)
	if n, _, _ := s.NullCount(); n != 1 {
		t.Errorf("%d nulls", n)
	}
	if v := (sv{t}).of(s.Max()); len(v.Str) != 64 || v.Str <= long[:64] {
		t.Errorf("the max is cut to 64 bytes and rounded up: %q", v.Str)
	}
	if p, _ := s.MaxPrecision(); p != fbs.Inexact {
		t.Error("a cut max is inexact")
	}
	if p, _ := s.MinPrecision(); p != fbs.Exact {
		t.Error("a min that was not cut is exact")
	}
	fl := stats(2)
	if n, ok, _ := fl.NanCount(); !ok || n != 1 {
		t.Errorf("%d NaNs", n)
	}
	if _, ok, _ := fl.IsConstant(); ok {
		t.Error("a column with a NaN says nothing of its order")
	}
	if v := (sv{t}).of(fl.Max()); v.F64 != 3 {
		t.Errorf("the max skips the NaN: %+v", v)
	}
	d := stats(4)
	if v := (sv{t}).of(d.Max()); len(v.Bytes) != 4 || v.Bytes[0] != 7 {
		t.Errorf("a decimal of 4 bytes: %+v", v)
	}
	if n, _, _ := stats(5).NullCount(); n != 3 {
		t.Errorf("%d nulls of a null column", n)
	}
}

// fileStats reads the file statistics of a file.
func fileStats(t *testing.T, file []byte) fbs.FileStatistics {
	t.Helper()
	psLen := int(binary.LittleEndian.Uint16(file[len(file)-6:]))
	ps, err := fbs.ReadPostscript(file[len(file)-8-psLen : len(file)-8])
	if err != nil {
		t.Fatal(err)
	}
	seg, ok, err := ps.Statistics()
	if !ok || err != nil {
		t.Fatal("no file statistics", ok, err)
	}
	off, _ := seg.Offset()
	n, _ := seg.Length()
	st, err := fbs.ReadFileStatistics(file[off : off+uint64(n)])
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// entries lists, for each entry of the file statistics, its aggregates and their values.
func entries(t *testing.T, st fbs.FileStatistics) []map[string]scalar.Value {
	t.Helper()
	specs, _ := st.AggregateSpecs()
	sets, _ := st.AggregateSets()
	entrySets, _ := st.EntrySets()
	partials, _ := st.Partials()
	var out []map[string]scalar.Value
	for _, e := range entrySets {
		set, _ := sets.At(int(e))
		ids, _ := set.Specs()
		m := map[string]scalar.Value{}
		for _, i := range ids {
			n, size := binary.Uvarint(partials)
			v, err := scalar.Decode(partials[size : size+int(n)])
			if err != nil {
				t.Fatal(err)
			}
			partials = partials[size+int(n):]
			spec, _ := specs.At(int(i))
			id, _ := spec.ID()
			m[id] = v
		}
		out = append(out, m)
	}
	if len(partials) != 0 {
		t.Errorf("%d bytes of partials left", len(partials))
	}
	return out
}

func TestFileStatistics(t *testing.T) {
	long := strings.Repeat("z", 70)
	_, file := writeFile(t, NewSchema(Field{"k", Int64Type()}, Field{"s", Utf8Type().AsNullable()}, Field{"f", Float32Type()},
		Field{"g", StructOf(Field{"x", Int32Type()}, Field{"y", BinaryType()}).AsNullable()}, Field{"u", UUIDType()}),
		[]Data{Of([]int64{5, 3, 9}), OfStrings([]string{"b", long, ""}).Nulls([]bool{false, false, true}),
			Of([]float32{1, float32(math.NaN()), 3}),
			OfStruct(Of([]int32{1, 2, 3}), OfBytes([][]byte{{1}, {2}, {3}})).Nulls([]bool{false, true, false}),
			OfBytes([][]byte{make([]byte, 16), make([]byte, 16), make([]byte, 16)})})
	st := fileStats(t, file)
	legacy, _ := st.FieldStats()
	if legacy.Len() != 5 {
		t.Fatalf("%d legacy entries, want one per field", legacy.Len())
	}
	got := entries(t, st)
	// k, s, f, g.x, g.y, g (the null count of the struct), u: post-order.
	if len(got) != 7 {
		t.Fatalf("%d entries: %v", len(got), got)
	}
	k := got[0]
	if k[aggMax].Int != 9 || k[aggMin].Int != 3 || k[aggNullCount].Uint != 0 {
		t.Errorf("k: %v", k)
	}
	if s := got[1]; s[aggNullCount].Uint != 1 || s[aggBoundedMin].Str != "b" || len(s[aggBoundedMax].List) != 2 ||
		len(s[aggBoundedMax].List[0].Str) != 64 || s[aggBoundedMax].List[1].Bool {
		t.Errorf("s: %v", s)
	}
	if len(got[3]) != 0 || len(got[4]) != 0 { // under a nullable struct
		t.Errorf("the fields of a nullable struct have no statistics: %v %v", got[3], got[4])
	}
	if got[5][aggNullCount].Uint != 1 || len(got[5]) != 1 {
		t.Errorf("the null count of the struct: %v", got[5])
	}
	if f := got[2]; f[aggNanCount].Uint != 1 || f[aggMax].F32 != 3 {
		t.Errorf("f: %v", f)
	}
	if u := got[6]; len(u) != 3 || u[aggNullCount].Uint != 0 || len(u[aggMin].List) != uuidSize || len(u[aggMax].List) != uuidSize {
		t.Errorf("u has a min and a max of 16 bytes: %v", u)
	}
}

func TestFileStatisticsOfUnknownBounds(t *testing.T) {
	// A bound that cannot be represented is unknown, and the column has no max.
	_, file := writeFile(t, NewSchema(Field{"b", BinaryType()}, Field{"e", Int8Type()}),
		[]Data{OfStrings([]string{strings.Repeat("\xff", 70), "a"}), Of([]int8{-1, 1})}, []Data{OfStrings([]string{"x"}), Of([]int8{0})})
	got := entries(t, fileStats(t, file))
	list := got[0][aggBoundedMax].List
	if len(list) != 2 || list[0].Kind != scalar.Null || !list[1].Bool {
		t.Errorf("an unknown bound: %v", got[0])
	}
	if got[1][aggMin].Int != -1 || got[1][aggMax].Int != 1 {
		t.Errorf("%v", got[1])
	}
	// A file with no row, and no field.
	_, file = writeFile(t, NewSchema(Field{"a", Int8Type()}))
	if got := entries(t, fileStats(t, file)); len(got) != 1 || len(got[0]) != 0 {
		t.Errorf("%v", got)
	}
	_, file = writeFile(t, NewSchema())
	if got := entries(t, fileStats(t, file)); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

// A time keeps its min and max in the array and file statistics only within its day: the
// reference reader (0.93.0) fails to open a file whose time scalar overflows its day.
func TestFileStatisticsOfTimes(t *testing.T) {
	_, file := writeFile(t, NewSchema(Field{"in", TimeType(Second)}, Field{"out", TimeType(Second)}, Field{"ms", TimeType(Millisecond)}),
		[]Data{Of([]int32{10, 86399, 5}), Of([]int32{10, 86400, 5}), Of([]int32{-1, 3, 4})})
	got := entries(t, fileStats(t, file))
	if in := got[0]; in[aggMin].Int != 5 || in[aggMax].Int != 86399 {
		t.Errorf("a time within its day: %v", in)
	}
	for _, i := range []int{1, 2} {
		if _, ok := got[i][aggMin]; ok || len(got[i]) != 1 {
			t.Errorf("a time out of its day has the null count only: %v", got[i])
		}
	}
}

// TestMaskedRootStatistics: the chunk of one value among nulls is a constant under a validity,
// and its statistics are on the masked root node, as upstream puts them on the root of whatever
// encoding it is.
func TestMaskedRootStatistics(t *testing.T) {
	n := 300
	v, nulls := make([]int64, n), make([]bool, n)
	for i := range v {
		v[i], nulls[i] = 42, i%3 != 1
	}
	f, file := writeFile(t, NewSchema(Field{"c", Int64Type().AsNullable()}), []Data{Of(v).Nulls(nulls)})
	if !slices.Contains(f.arrayEncodings, "vortex.masked") {
		t.Fatalf("the file has %v, no vortex.masked", f.arrayEncodings)
	}
	st := rootStats(t, f, file, f.layout.children[0].children[0].segments[0])
	if got, _, _ := st.NullCount(); got != 200 {
		t.Errorf("%d nulls, want 200", got)
	}
	if got := (sv{t}).of(st.Min()); got.Int != 42 {
		t.Errorf("min %+v", got)
	}
	if got := (sv{t}).of(st.Max()); got.Int != 42 {
		t.Errorf("max %+v", got)
	}
}
