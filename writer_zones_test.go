package vortex

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/big"
	"math/rand/v2"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fclairamb/vortex-go/internal/array"
)

// layoutTree prints the layout of a column: its encoding and rows, then its children.
func layoutTree(f *File, l *layoutNode) string {
	s := fmt.Sprintf("%s(%d)", strings.TrimPrefix(f.layoutEncodings[l.encoding], "vortex."), l.rowCount)
	if len(l.children) == 0 {
		return s
	}
	kids := make([]string, len(l.children))
	for i, k := range l.children {
		kids[i] = layoutTree(f, k)
	}
	return s + "[" + strings.Join(kids, " ") + "]"
}

// chunkRows lists the rows of the chunks of the data of column i: the children of the chunked
// layout, or the layout itself.
func chunkRows(f *File, col int) []uint64 {
	l := f.layout.children[col]
	if f.layoutEncodings[l.encoding] == "vortex.zoned" {
		l = l.children[0]
	}
	if f.layoutEncodings[l.encoding] != "vortex.chunked" {
		return []uint64{l.rowCount}
	}
	var out []uint64
	for _, k := range l.children {
		out = append(out, k.rowCount)
	}
	return out
}

func TestChunksDoNotDependOnWrites(t *testing.T) {
	lowered(t, &chunkBytes, 64<<10)
	const rows = 100_000
	a, s := make([]int64, rows), make([]string, rows)
	for i := range a {
		a[i], s[i] = int64(i)*7919, fmt.Sprintf("row-%d", i*31)
	}
	schema := NewSchema(Field{"a", Int64Type()}, Field{"s", Utf8Type()})
	one := make([][]Data, rows)
	for i := range one {
		one[i] = []Data{Of(a[i : i+1]), OfStrings(s[i : i+1])}
	}
	whole, _ := writeFile(t, schema, []Data{Of(a), OfStrings(s)})
	single, _ := writeFile(t, schema, one...)
	odd, _ := writeFile(t, schema, []Data{Of(a[:8191]), OfStrings(s[:8191])}, []Data{Of(a[8191:50000]), OfStrings(s[8191:50000])},
		[]Data{Of(a[50000:]), OfStrings(s[50000:])})
	want := layoutTree(whole, whole.layout)
	for name, f := range map[string]*File{"one row a Write": single, "odd Writes": odd} {
		if got := layoutTree(f, f.layout); got != want {
			t.Errorf("%s: the layout is\n%s\nwant\n%s", name, got, want)
		}
	}
	for col := range 2 {
		rows := chunkRows(whole, col)
		if len(rows) < 3 {
			t.Fatalf("column %d has %d chunks", col, len(rows))
		}
		for _, n := range rows[:len(rows)-1] {
			if n%8192 != 0 {
				t.Errorf("column %d: a chunk of %d rows is not a multiple of 8,192", col, n)
			}
		}
	}
	for name, f := range map[string]*File{"whole": whole, "single": single, "odd": odd} {
		got, err := readBack(f, Int64Type())
		if err != nil || len(got) != rows {
			t.Fatalf("%s: %d rows, %v", name, len(got), err)
		}
		for i, v := range got {
			if int64(leUint64(v.b)) != a[i] {
				t.Fatalf("%s: row %d is %d, want %d", name, i, leUint64(v.b), a[i])
			}
		}
	}
}

func leUint64(b []byte) uint64 {
	var v uint64
	for i := len(b) - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[i])
	}
	return v
}

func TestLayoutOfAColumn(t *testing.T) {
	const rows = 20_000
	ids, few, uniq := make([]int32, rows), make([]string, rows), make([]string, rows)
	for i := range ids {
		ids[i], few[i], uniq[i] = int32(i), fmt.Sprintf("group-%d", i%10), fmt.Sprintf("unique-%d", i)
	}
	f, _ := writeFile(t, NewSchema(Field{"id", Int32Type()}, Field{"few", Utf8Type()}, Field{"uniq", Utf8Type()},
		Field{"n", StructOf(Field{"x", Int8Type()}).AsNullable()}),
		[]Data{Of(ids), OfStrings(few), OfStrings(uniq), OfStruct(Of(make([]int8, rows))).Nulls(make([]bool, rows))})
	want := []string{
		"zoned(20000)[flat(20000) flat(3)]",
		"zoned(20000)[dict(20000)[flat(10) flat(20000)] flat(3)]",
		"zoned(20000)[flat(20000) flat(3)]",
		"struct(20000)[flat(20000) dict(20000)[flat(1) flat(20000)]]", // no zones under a nullable struct
	}
	for i, w := range want {
		if got := layoutTree(f, f.layout.children[i]); got != w {
			t.Errorf("column %d: %s, want %s", i, got, w)
		}
	}
	if got := layoutTree(f, f.layout); !strings.HasPrefix(got, "struct(20000)[zoned") {
		t.Errorf("the root is %s", got)
	}
}

// zoneStat is the statistics of a zone, as text.
type zoneStat struct {
	min, max string
	hasValue bool // the zone has a min and a max
	maxKnown bool // and a max that is a bound
	nulls    uint64
	nans     uint64
}

// readZones reads the zones table of column col of the file, in the form the reader uses.
func readZones(t *testing.T, f *File, col int, ty Type) (stats []zoneStat, tbl zoneTable) {
	t.Helper()
	l := f.layout.children[col]
	if f.layoutEncodings[l.encoding] != "vortex.zoned" {
		t.Fatalf("column %d is %s", col, f.layoutEncodings[l.encoding])
	}
	z, err := parseZoneLayout(l, false)
	if err != nil {
		t.Fatal(err)
	}
	tbl, ok := f.zoneTable(l, &z, arrayType(ty), 0)
	if !ok {
		t.Fatalf("column %d: the zones table cannot be read", col)
	}
	n := int(z.zones.rowCount)
	for k := range n {
		var s zoneStat
		s.nulls, _ = uintAt(tbl.nulls, k)
		s.nans, _ = uintAt(tbl.nans, k)
		if tbl.min != nil && !tbl.min.Null(k) {
			s.min, s.hasValue = renderStat(tbl.min, k, ty), true
		}
		if tbl.max != nil && !tbl.max.Null(k) {
			s.max, s.maxKnown = renderStat(tbl.max, k, ty), true
		}
		stats = append(stats, s)
	}
	return stats, tbl
}

// renderStat prints row k of a statistics array: a number, or the bytes of a string.
func renderStat(a *array.Array, k int, ty Type) string {
	switch ty.kind {
	case Utf8, Binary:
		return string(a.Data[a.Starts[k]:a.Ends[k]])
	case UUID:
		return string(a.Elems.Data[k*uuidSize : (k+1)*uuidSize])
	case Bool:
		return strconv.FormatBool(a.Bits.Get(k))
	case KindDecimal:
		return decimalFromLE(a.Data[k*a.Width : (k+1)*a.Width]).Big().String()
	}
	return renderNumber(ty, a.Data[k*a.Type.PType.Width():(k+1)*a.Type.PType.Width()])
}

// renderNumber prints the little-endian number of the type.
func renderNumber(ty Type, b []byte) string {
	k := ty.kind
	if k == Date || k == Time || k == Timestamp {
		k = storageKind(ty)
	}
	switch k {
	case Int8, Int16, Int32, Int64:
		v := int64(leUint64(b))
		shift := 64 - 8*len(b)
		return strconv.FormatInt(v<<shift>>shift, 10)
	case Float16:
		return strconv.FormatFloat(halfBits(leUint64(b)), 'g', -1, 64)
	case Float32:
		return strconv.FormatFloat(float64(math.Float32frombits(uint32(leUint64(b)))), 'g', -1, 64)
	case Float64:
		return strconv.FormatFloat(math.Float64frombits(leUint64(b)), 'g', -1, 64)
	}
	return strconv.FormatUint(leUint64(b), 10)
}

// halfBits converts a half float without the code of the writer.
func halfBits(h uint64) float64 {
	sign, exp, frac := h>>15, int(h>>10)&0x1f, float64(h&0x3ff)
	var v float64
	switch exp {
	case 0:
		v = frac / (1 << 24)
	case 31:
		v = math.Inf(1)
		if frac != 0 {
			return math.NaN()
		}
	default:
		v = (1 + frac/1024) * math.Pow(2, float64(exp-15))
	}
	if sign != 0 {
		return -v
	}
	return v
}

func isNaNVal(ty Type, b []byte) bool {
	switch ty.kind {
	case Float16:
		return math.IsNaN(halfBits(leUint64(b)))
	case Float32:
		return math.IsNaN(float64(math.Float32frombits(uint32(leUint64(b)))))
	case Float64:
		return math.IsNaN(math.Float64frombits(leUint64(b)))
	}
	return false
}

// cmpStat compares two values of a type, with plain Go: the order of the statistics.
func cmpStat(ty Type, a, b []byte) int {
	switch ty.kind {
	case Utf8, Binary, UUID:
		return bytes.Compare(a, b)
	case Bool:
		return int(a[0]) - int(b[0])
	case KindDecimal:
		return decimalFromLE(a).Big().Cmp(decimalFromLE(b).Big())
	case Float16, Float32, Float64:
		x, _ := strconv.ParseFloat(renderNumber(ty, a), 64)
		y, _ := strconv.ParseFloat(renderNumber(ty, b), 64)
		return big.NewFloat(x).Cmp(big.NewFloat(y))
	}
	x, _ := new(big.Int).SetString(renderNumber(ty, a), 10)
	y, _ := new(big.Int).SetString(renderNumber(ty, b), 10)
	return x.Cmp(y)
}

// wantZone computes the statistics of the rows of a zone with plain Go.
func wantZone(ty Type, rows []val) (s zoneStat, lo, hi []byte) {
	for _, r := range rows {
		switch {
		case r.null:
			s.nulls++
		case isNaNVal(ty, r.b):
			s.nans++
		default:
			if lo == nil || cmpStat(ty, r.b, lo) < 0 {
				lo = r.b
			}
			if hi == nil || cmpStat(ty, r.b, hi) > 0 {
				hi = r.b
			}
		}
	}
	return s, lo, hi
}

func hasStats(ty Type) bool {
	switch ty.kind {
	case Null, List, FixedSizeList, Map, Struct:
		return false
	}
	return true
}

func isFloatType(ty Type) bool { return ty.kind == Float16 || ty.kind == Float32 || ty.kind == Float64 }

// checkZones compares the zones table of column col with the statistics of the rows.
func checkZones(t *testing.T, name string, f *File, col int, ty Type, vals []val) {
	t.Helper()
	got, tbl := readZones(t, f, col, ty)
	if want := (len(vals) + rowBlock - 1) / rowBlock; len(got) != want {
		t.Fatalf("%s: %d zones, want %d", name, len(got), want)
	}
	if !hasStats(ty) && (tbl.min != nil || tbl.max != nil) {
		t.Errorf("%s: a type with no order has a min", name)
	}
	if isFloatType(ty) != (tbl.nans != nil) {
		t.Errorf("%s: NaN counts %v for %s", name, tbl.nans != nil, ty)
	}
	for k, g := range got {
		w, lo, hi := wantZone(ty, vals[k*rowBlock:min((k+1)*rowBlock, len(vals))])
		if g.nulls != w.nulls || g.nans != w.nans {
			t.Errorf("%s zone %d: %d nulls %d NaNs, want %d %d", name, k, g.nulls, g.nans, w.nulls, w.nans)
		}
		if !hasStats(ty) {
			continue
		}
		if g.hasValue != (lo != nil) {
			t.Errorf("%s zone %d: has a min %v, want %v", name, k, g.hasValue, lo != nil)
			continue
		}
		if lo == nil {
			continue
		}
		if ty.kind == Utf8 || ty.kind == Binary {
			if len(g.min) > 64 || bytes.Compare([]byte(g.min), lo) > 0 {
				t.Errorf("%s zone %d: min %q is not a lower bound of %q", name, k, g.min, lo)
			}
			if g.maxKnown && (len(g.max) > 64 || bytes.Compare([]byte(g.max), hi) < 0) {
				t.Errorf("%s zone %d: max %q is not an upper bound of %q", name, k, g.max, hi)
			}
			if ty.kind == Utf8 && (!utf8.ValidString(g.min) || g.maxKnown && !utf8.ValidString(g.max)) {
				t.Errorf("%s zone %d: a bound is not UTF-8", name, k)
			}
			continue
		}
		if wantMin, wantMax := statText(ty, lo), statText(ty, hi); g.min != wantMin || g.max != wantMax {
			t.Errorf("%s zone %d: %s to %s, want %s to %s", name, k, g.min, g.max, wantMin, wantMax)
		}
	}
}

// statText prints a value the way renderStat prints the statistic.
func statText(ty Type, b []byte) string {
	switch ty.kind {
	case UUID:
		return string(b)
	case Bool:
		return strconv.FormatBool(b[0] != 0)
	case KindDecimal:
		return decimalFromLE(b).Big().String()
	}
	return renderNumber(ty, b)
}

func TestZoneStatistics(t *testing.T) {
	lowered(t, &rowBlock, 7)
	lowered(t, &chunkBytes, 50)
	types := allTypes()
	for _, name := range slices.Sorted(mapKeys(types)) {
		ty := types[name]
		if ty.kind == Struct {
			continue
		}
		for _, allNull := range []bool{false, true} {
			if allNull && !ty.nullable {
				continue
			}
			g := &gen{r: rand.New(rand.NewPCG(5, uint64(len(name)))), allNull: allNull}
			var chunks [][]Data
			var vals []val
			for _, n := range []int{20, 3, 31, 1} {
				d, v := g.make(ty, n)
				chunks = append(chunks, []Data{d})
				vals = append(vals, v...)
			}
			f, _ := writeFile(t, NewSchema(Field{"c", ty}), chunks...)
			checkZones(t, fmt.Sprintf("%s allNull=%v", name, allNull), f, 0, ty, vals)
		}
	}
}

func mapKeys[K comparable, V any](m map[K]V) func(yield func(K) bool) {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func TestZoneStatisticsOfFloats(t *testing.T) {
	lowered(t, &rowBlock, 4)
	nan := math.NaN()
	f64 := []float64{nan, nan, nan, nan /* only NaNs */, 1, nan, -3, 2 /* both */, math.Copysign(0, -1), 0, math.Inf(-1), math.Inf(1)}
	f32 := make([]float32, len(f64))
	f16 := make([]uint16, len(f64))
	half := map[float64]uint16{1: 0x3c00, -3: 0xc200, 2: 0x4000, 0: 0, math.Inf(1): 0x7c00, math.Inf(-1): 0xfc00}
	for i, v := range f64 {
		f32[i] = float32(v)
		f16[i] = half[v]
		if math.IsNaN(v) {
			f16[i] = 0x7e00
		}
		if i == 8 {
			f16[i] = 0x8000
		}
	}
	f, _ := writeFile(t, NewSchema(Field{"d", Float64Type()}, Field{"s", Float32Type()}, Field{"h", Float16Type()}),
		[]Data{Of(f64), Of(f32), Of(f16)})
	for col, ty := range []Type{Float64Type(), Float32Type(), Float16Type()} {
		got, _ := readZones(t, f, col, ty)
		want := []zoneStat{{nans: 4}, {hasValue: true, maxKnown: true, min: "-3", max: "2", nans: 1},
			{hasValue: true, maxKnown: true, min: "-Inf", max: "+Inf"}}
		if len(got) != 3 {
			t.Fatalf("%d zones", len(got))
		}
		if got[0] != want[0] || got[1] != want[1] {
			t.Errorf("column %d: %+v, want %+v", col, got[:2], want[:2])
		}
		// -0 and 0 are the same value: the zone has them as its min and its max.
		if !got[2].hasValue || got[2].min != "-Inf" || got[2].max != "+Inf" {
			t.Errorf("column %d: %+v", col, got[2])
		}
	}
	// A column with nothing but NaNs has no min and no max at all.
	f, _ = writeFile(t, NewSchema(Field{"d", Float64Type()}), []Data{Of([]float64{nan, nan})})
	got, tbl := readZones(t, f, 0, Float64Type())
	if tbl.min != nil || tbl.max != nil || got[0].nans != 2 {
		t.Errorf("%+v %v %v", got, tbl.min, tbl.max)
	}
}

func TestHalfToFloat(t *testing.T) {
	for _, tc := range []struct {
		bits uint16
		want float64
	}{{0x3c00, 1}, {0xc000, -2}, {0x0001, math.Ldexp(1, -24)}, {0x7bff, 65504}, {0x7c00, math.Inf(1)}, {0xfc00, math.Inf(-1)}, {0, 0}} {
		if got := halfToFloat(tc.bits); got != tc.want || halfBits(uint64(tc.bits)) != tc.want {
			t.Errorf("%#x is %v and %v, want %v", tc.bits, got, halfBits(uint64(tc.bits)), tc.want)
		}
	}
	if !math.IsNaN(halfToFloat(0x7e00)) {
		t.Error("0x7e00 is a NaN")
	}
}

func TestStringBounds(t *testing.T) {
	long := strings.Repeat("a", 70)
	euro := strings.Repeat("a", 62) + "€€€" // 62 + 3 x 3 bytes: byte 64 is inside a character
	top := strings.Repeat("\U0010ffff", 17) // 68 bytes, no greater character
	cases := []struct {
		name string
		ty   Type
		vals []string
	}{
		{"short", Utf8Type(), []string{"b", "a", "abc"}},
		{"long", Utf8Type(), []string{long + "z", long, "b" + long}},
		{"multibyte", Utf8Type(), []string{euro, euro + "x", "é"}},
		{"surrogates", Utf8Type(), []string{strings.Repeat("a", 63) + "퟿" + "tail"}},
		{"no upper bound", Utf8Type(), []string{top, "a"}},
		{"binary", BinaryType(), []string{long, "\xff" + long, "\x00"}},
		{"binary carry", BinaryType(), []string{strings.Repeat("\xff", 63) + "\x01\x02\x03"}},
		{"binary of ff", BinaryType(), []string{strings.Repeat("\xff", 70), "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, _ := writeFile(t, NewSchema(Field{"c", c.ty}), []Data{OfStrings(c.vals)})
			vals := make([]val, len(c.vals))
			for i, s := range c.vals {
				vals[i] = val{b: []byte(s)}
			}
			checkZones(t, c.name, f, 0, c.ty, vals)
			got, _ := readZones(t, f, 0, c.ty)
			if known := c.name != "no upper bound" && c.name != "binary of ff"; got[0].maxKnown != known {
				t.Errorf("the max is known: %v, want %v", got[0].maxKnown, known)
			}
		})
	}
}

func TestIncrementUTF8(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"abc", "abd"}, {"a퟿", "a"}, {"a\U0010ffff", "b"}, {"\U0010ffff", ""},
	} {
		got, ok := incrementUTF8([]byte(tc.in))
		if ok != (tc.want != "") || string(got) != tc.want {
			t.Errorf("%q -> %q, %v", tc.in, got, ok)
		}
	}
	if _, ok := upperBound(bytes.Repeat([]byte{0xff}, 65), false); ok {
		t.Error("an upper bound of 0xff bytes")
	}
}

// readValues scans every row of the first column of the file back.
func readValues[T Number](t *testing.T, f *File, q Query) []T {
	t.Helper()
	var out []T
	for b, err := range f.Scan(context.Background(), q) {
		if err != nil {
			t.Fatal(err)
		}
		v, err := Values[T](b.Col(0))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v...)
	}
	return out
}

func TestPruningOnOurFiles(t *testing.T) {
	lowered(t, &chunkBytes, 64<<10) // a chunk a zone
	const rows = 100_000
	k, s := make([]int64, rows), make([]string, rows)
	for i := range k {
		k[i], s[i] = int64(i), fmt.Sprintf("%s%07d", strings.Repeat("q", 40), i)
	}
	f, _ := writeFile(t, NewSchema(Field{"k", Int64Type()}, Field{"s", Utf8Type()}), []Data{Of(k), OfStrings(s)})
	_, full := countReads(t, f, Query{}, "k")
	for _, c := range []struct {
		name string
		col  string
		e    Expr
		keep func(i int) bool
	}{
		{"numbers", "k", And(Col("k").Ge(40_000), Col("k").Lt(41_000)), func(i int) bool { return i >= 40_000 && i < 41_000 }},
		{"strings", "s", And(Col("s").Ge(strings.Repeat("q", 40)+"0070000"), Col("s").Lt(strings.Repeat("q", 40)+"0071000")),
			func(i int) bool { return i >= 70_000 && i < 71_000 }},
	} {
		got, reads := countReads(t, f, Query{Filter: c.e}, c.col)
		want := 0
		for i := range rows {
			if c.keep(i) {
				want++
			}
		}
		if got != want {
			t.Errorf("%s: %d rows, want %d", c.name, got, want)
		}
		if reads*3 > full {
			t.Errorf("%s: %d segments read, a full scan of one column %d", c.name, reads, full)
		}
	}
}

func TestZonesOfNestedColumns(t *testing.T) {
	lowered(t, &rowBlock, 5)
	ty := StructOf(Field{"a", Int32Type()}, Field{"inner", StructOf(Field{"b", Utf8Type().AsNullable()})})
	g := &gen{r: rand.New(rand.NewPCG(2, 2))}
	d, vals := g.make(ty, 23)
	f, _ := writeFile(t, NewSchema(Field{"s", ty}), []Data{d})
	root := f.layout.children[0]
	if got := layoutTree(f, root); !strings.HasPrefix(got, "struct(23)[zoned(23)[flat(23) flat(5)] struct(23)[zoned(23)") {
		t.Errorf("a nested struct is split into zoned columns: %s", got)
	}
	a := make([]val, len(vals))
	b := make([]val, len(vals))
	for i, v := range vals {
		a[i], b[i] = v.kids[0], v.kids[1].kids[0]
	}
	f2, _ := writeFile(t, NewSchema(Field{"a", Int32Type()}, Field{"b", Utf8Type().AsNullable()}), []Data{
		d.kids[0], d.kids[1].kids[0]})
	checkZones(t, "a", f2, 0, Int32Type(), a)
	checkZones(t, "b", f2, 1, Utf8Type().AsNullable(), b)
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed)) }

func TestPeakHeapWhileWriting(t *testing.T) {
	const batch, batches = 100_000, 50 // 5 million rows
	a, b, s, f := make([]int64, batch), make([]int32, batch), make([]string, batch), make([]float64, batch)
	w := NewWriter(io.Discard, NewSchema(Field{"a", Int64Type()}, Field{"b", Int32Type()}, Field{"s", Utf8Type()},
		Field{"f", Float64Type()}), WriteOptions{})
	var peak uint64
	var m runtime.MemStats
	for n := range batches {
		for i := range a {
			a[i], b[i], s[i], f[i] = int64(n*batch+i), int32(i%100), fmt.Sprintf("row-%d-%d", n, i), float64(i)*0.5
		}
		if err := w.Write(Of(a), Of(b), OfStrings(s), Of(f)); err != nil {
			t.Fatal(err)
		}
		if n%5 == 4 {
			runtime.GC()
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapAlloc)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The data of a batch is 10 MB, and the writer keeps a few MB of chunks for each column:
	// the file is 5 million rows, 250 MB, and does not weigh on the heap.
	if peak > 64<<20 {
		t.Fatalf("the heap peaks at %d MB", peak>>20)
	}
}

// A column of wide rows is buffered a block of rows at a time, like the reference writer does
// (its repartition into 8,192 rows has no byte limit, strategy.rs step 1): the chunks keep to
// whole blocks, and the heap depends on the width of a block, not on the rows written.
func TestWideRows(t *testing.T) {
	wide := strings.Repeat("w", 2048)
	schema := NewSchema(Field{"s", Utf8Type()})
	write := func(w io.Writer, rows int, bound uint64) {
		wr := NewWriter(w, schema, WriteOptions{})
		var m runtime.MemStats
		peak := uint64(0)
		for i := range rows {
			if err := wr.Write(OfStrings([]string{fmt.Sprintf("%08d", i) + wide})); err != nil {
				t.Fatal(err)
			}
			if i%4096 == 4095 {
				runtime.GC()
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
		if err := wr.Close(); err != nil {
			t.Fatal(err)
		}
		// A block is 16 MB; what is buffered is a block plus the copy that makes a chunk.
		if peak > bound {
			t.Fatalf("the heap peaks at %d MB", peak>>20)
		}
	}
	var buf bytes.Buffer
	write(&buf, 20_000, 1<<40) // the file is in the heap
	f, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if got := chunkRows(f, 0); !slices.Equal(got, []uint64{8192, 8192, 20_000 - 2*8192}) {
		t.Fatalf("chunks of %v rows", got)
	}
	write(io.Discard, 60_000, 96<<20)
}
