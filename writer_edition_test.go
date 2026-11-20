package vortex

import (
	"bytes"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fclairamb/vortex-go/internal/edition"
)

// TestEditionRoundTrip: for each edition, every type it holds is written and read back, and
// every identifier of the file, the ones the reader meets and the ones the file lists, is in
// the edition.
func TestEditionRoundTrip(t *testing.T) {
	types := allTypes()
	names := slices.Sorted(maps.Keys(types))
	for _, ed := range edition.Names() {
		for _, c := range []Compression{Default, Compact, None} {
			t.Run(fmt.Sprintf("%s %d", ed, c), func(t *testing.T) {
				lowered(t, &testOptions, WriteOptions{Compression: c, Edition: ed})
				var mu sync.Mutex
				met := map[string]bool{}
				edition.Observe(func(kind, id string) {
					mu.Lock()
					defer mu.Unlock()
					met[key(kind, id)] = true
				})
				defer edition.Observe(nil)
				held := 0
				for _, n := range names {
					if validEdition(types[n], "", ed) != nil {
						continue
					}
					held++
					checkRoundTrip(t, ed+" "+n, types[n], &gen{r: rand.New(rand.NewPCG(3, 4))}, 300)
					f, _ := writeFile(t, NewSchema(Field{"c", types[n]}), []Data{mustData(types[n])})
					for _, id := range f.arrayEncodings {
						met[key(edition.KindArray, id)] = true
					}
					for _, id := range f.layoutEncodings {
						met[key(edition.KindLayout, id)] = true
					}
				}
				if held < 60 {
					t.Errorf("%d types", held)
				}
				for k := range met {
					kind, id, _ := strings.Cut(k, " ")
					if !edition.Allows(ed, kind, id) {
						t.Errorf("%s: %s is not in the edition", ed, k)
					}
				}
			})
		}
	}
}

// mustData is a few rows of a type.
func mustData(ty Type) Data {
	d, _ := (&gen{r: rand.New(rand.NewPCG(9, 9))}).make(ty, 40)
	return d
}

// TestEditionLayouts: the zone maps are vortex.stats before core 2026.08.0, vortex.zoned from
// it, the lists vortex.list before core 2025.10.0, and the file statistics have no aggregate
// before core 2026.08.0.
func TestEditionLayouts(t *testing.T) {
	schema := NewSchema(Field{"n", Int64Type()}, Field{"l", ListOf(Int32Type())}, Field{"s", Utf8Type()})
	g := &gen{r: rand.New(rand.NewPCG(1, 1))}
	n, _ := g.make(Int64Type(), 100)
	l, _ := g.make(ListOf(Int32Type()), 100)
	s, _ := g.make(Utf8Type(), 100)
	for _, c := range []Compression{Default, None} {
		for _, ed := range edition.Names() {
			lowered(t, &testOptions, WriteOptions{Compression: c, Edition: ed})
			f, file := writeFile(t, schema, []Data{n, l, s})
			has := func(ids []string, id string) bool { return slices.Contains(ids, id) }
			zoned := edition.Allows(ed, edition.KindLayout, "vortex.zoned")
			if has(f.layoutEncodings, "vortex.zoned") != zoned || has(f.layoutEncodings, "vortex.stats") == zoned {
				t.Errorf("%s: the layouts are %v", ed, f.layoutEncodings)
			}
			listview := edition.Allows(ed, edition.KindArray, "vortex.listview")
			if c == None || !listview {
				if has(f.arrayEncodings, "vortex.listview") != listview || has(f.arrayEncodings, "vortex.list") == listview {
					t.Errorf("%s %d: the arrays are %v", ed, c, f.arrayEncodings)
				}
			}
			st := fileStats(t, file)
			specs, _ := st.AggregateSpecs()
			entrySets, _ := st.EntrySets()
			legacy, _ := st.FieldStats()
			if legacy.Len() != 3 {
				t.Errorf("%s: %d legacy field statistics, want 3", ed, legacy.Len())
			}
			if aggregates := specs.Len() > 0 && len(entrySets) > 0; aggregates != zoned {
				t.Errorf("%s: aggregates %v", ed, aggregates)
			}
		}
	}
}

// TestEditionDropsEncodings: the encodings an edition does not have are not used, and the data
// that calls for them uses them when the edition has them.
func TestEditionDropsEncodings(t *testing.T) {
	const rows = 8192
	r := rand.New(rand.NewPCG(5, 6))
	seq, runs, vals, same := make([]int64, rows), make([]float64, rows), make([]float64, rows), make([]int64, rows)
	nulls := make([]bool, rows)
	for i := range rows {
		seq[i], same[i], nulls[i] = int64(3*i), 7, i%10 < 3
		runs[i] = float64(i/4) * 1.37 * 100003
		vals[i] = float64(r.IntN(50)) / 3
	}
	runRows := runs[:0:0]
	for i := 0; i < rows; {
		x := float64(r.Int64N(5000)*100003) * 1.37
		for k := 1 + r.IntN(8); k > 0 && i < rows; k-- {
			runRows = append(runRows, x)
			i++
		}
	}
	hosts := []string{"www.acme.com", "shop.acme.org", "api.example.net"}
	urls := make([]string, rows)
	for i := range urls {
		urls[i] = fmt.Sprintf("https://%s/p/%d?q=%d&ref=%s", hosts[r.IntN(3)], r.IntN(5000), i%7, strings.Repeat("x", r.IntN(10)))
	}
	schema := NewSchema(Field{"seq", Int64Type()}, Field{"masked", Int64Type().AsNullable()}, Field{"runs", Float64Type()},
		Field{"vals", Float64Type()}, Field{"urls", Utf8Type()})
	data := []Data{Of(seq), Of(same).Nulls(nulls), Of(runRows), Of(vals), OfStrings(urls)}
	for _, id := range []string{"vortex.sequence", "vortex.masked", "fastlanes.rle", "vortex.zstd", "vortex.onpair"} {
		mode := Default
		if id == "vortex.zstd" {
			mode = Compact // only a candidate of the compact level
		}
		lowered(t, &testOptions, WriteOptions{Compression: mode})
		f, _ := writeFile(t, schema, data)
		if !slices.Contains(f.arrayEncodings, id) {
			t.Errorf("the newest edition does not use %s: %v", id, f.arrayEncodings)
		}
		for _, ed := range edition.Names() {
			lowered(t, &testOptions, WriteOptions{Compression: mode, Edition: ed})
			f, _ := writeFile(t, schema, data)
			if got, want := slices.Contains(f.arrayEncodings, id), edition.Allows(ed, edition.KindArray, id); got != want {
				t.Errorf("%s: %s is used: %v, the edition has it: %v", ed, id, got, want)
			}
		}
	}
}

func TestEditionErrors(t *testing.T) {
	tests := []struct {
		name    string
		edition string
		field   Field
		want    []string
	}{
		{"unknown", "core 2025.07.0", Field{"a", Int32Type()}, []string{`unknown edition "core 2025.07.0"`, "core 2025.05.0", "core 2026.08.3"}},
		{"not core", "preview 2026.08.0", Field{"a", Int32Type()}, []string{`unknown edition "preview 2026.08.0"`}},
		{"uuid", "core 2025.05.0", Field{"id", UUIDType()}, []string{`field "id"`, "uuid", `"core 2025.05.0"`}},
		{"uuid before the last edition", "core 2026.08.2", Field{"id", UUIDType()}, []string{"uuid", `"core 2026.08.2"`}},
		{"nested uuid", "core 2025.10.0", Field{"s", StructOf(Field{"x", ListOf(UUIDType())})}, []string{`field "s.x[]"`, "uuid"}},
		{"fixed-size list", "core 2025.06.0", Field{"f", FixedSizeListOf(Int8Type(), 2)}, []string{"fixed_size_list", `"core 2025.06.0"`}},
		{"fixed-size list of a list", "core 2025.05.0", Field{"f", ListOf(FixedSizeListOf(Int8Type(), 2))}, []string{`field "f[]"`}},
		{"map", "core 2026.08.1", Field{"m", MapOf(Utf8Type(), Int8Type())}, []string{"map", `"core 2026.08.1"`}},
		{"map key", "core 2026.08.2", Field{"m", MapOf(UUIDType(), Int8Type())}, []string{`field "m.key"`}},
		{"map value", "core 2026.08.2", Field{"m", MapOf(Utf8Type(), UUIDType())}, []string{`field "m.value"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf, NewSchema(tc.field), WriteOptions{Edition: tc.edition})
			writeErr, closeErr := w.Write(), w.Close() // the error is that of the first call
			for _, err := range []error{writeErr, closeErr} {
				for _, want := range tc.want {
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("error %v, want %q in it", err, want)
					}
				}
			}
			if buf.Len() != 0 {
				t.Errorf("%d bytes are left behind", buf.Len())
			}
		})
	}
	// the newest edition, named or not, holds all of them
	for _, ed := range []string{"", edition.ID} {
		for _, tc := range tests[2:] {
			if err := NewWriter(new(bytes.Buffer), NewSchema(tc.field), WriteOptions{Edition: ed}).Close(); err != nil {
				t.Errorf("edition %q: %v", ed, err)
			}
		}
	}
}

// legacyOf is the legacy zones table of a column of type ty, one zone for each list of rows.
func legacyOf(ty Type, zones ...Data) ([]Field, []Data, []byte) {
	c := newColumn(ty, true)
	for _, d := range zones {
		lo := c.buf.n
		appendRows(c.buf, d.body, ty, 0, d.n, &c.arena)
		c.zones.add(c.buf, lo, c.buf.n)
	}
	return c.zones.legacyTable()
}

func fieldNames(fields []Field) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return strings.Join(names, " ")
}

// TestLegacyZoneTable: the columns, their order, the flags of the cut bounds and the bitset of a
// vortex.stats layout.
func TestLegacyZoneTable(t *testing.T) {
	long := strings.Repeat("a", 70)
	top := bytes.Repeat([]byte{0xff}, 70)
	strs := func(v ...string) Data { return OfStrings(v) }
	fields, cols, bits := legacyOf(Utf8Type().AsNullable(),
		strs("b", "a"), // a zone of values that fit
		strs(long, "c"),
		strs("x", "y"+long), // a max that is cut
		OfStrings([]string{"", ""}).Nulls([]bool{true, true}),
	)
	if got := fieldNames(fields); got != "max max_is_truncated min min_is_truncated null_count" {
		t.Fatalf("fields %s", got)
	}
	if !bytes.Equal(bits, []byte{0x58, 0x00}) {
		t.Errorf("bits %x", bits)
	}
	if got := cols[1].body.bools; !slices.Equal(got, []bool{false, false, true, false}) {
		t.Errorf("max cut %v", got)
	}
	if got := cols[3].body.bools; !slices.Equal(got, []bool{false, true, false, false}) {
		t.Errorf("min cut %v", got)
	}
	if got := cols[0].nulls; !slices.Equal(got, []bool{false, false, false, true}) {
		t.Errorf("max nulls %v", got)
	}
	if got := cols[0].body.bins[0]; string(got) != "b" {
		t.Errorf("max %q", got)
	}

	// no zone has a bound for the max: the max is left out, and its flag
	fields, _, bits = legacyOf(BinaryType(), OfBytes([][]byte{top, top}))
	if got := fieldNames(fields); got != "min min_is_truncated null_count" || !bytes.Equal(bits, []byte{0x50, 0x00}) {
		t.Errorf("fields %s, bits %x", got, bits)
	}
	// floats have a NaN count, a column of nulls has no bounds, a null column only a null count
	fields, _, bits = legacyOf(Float32Type(), Of([]float32{1, 2}))
	if got := fieldNames(fields); got != "max max_is_truncated min min_is_truncated null_count nan_count" || !bytes.Equal(bits, []byte{0x58, 0x01}) {
		t.Errorf("fields %s, bits %x", got, bits)
	}
	fields, _, bits = legacyOf(Int8Type().AsNullable(), Of([]int8{0, 0}).Nulls([]bool{true, true}))
	if got := fieldNames(fields); got != "null_count" || !bytes.Equal(bits, []byte{0x40, 0x00}) {
		t.Errorf("fields %s, bits %x", got, bits)
	}
	fields, _, _ = legacyOf(NullType().AsNullable(), OfNulls(2))
	if got := fieldNames(fields); got != "null_count" {
		t.Errorf("a null column has %s", got)
	}
	if got := legacyMetadata(8192, bits); !bytes.Equal(got, []byte{0, 0x20, 0, 0, 0x40, 0}) {
		t.Errorf("metadata %x", got)
	}
}
