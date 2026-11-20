package vortex

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// variantRow is row i of the mixed values of the tests, in the types Column.Variant returns.
func variantRow(r *rand.Rand, i int) any {
	str := func() string { return strings.Repeat(string(rune('a'+r.IntN(26))), r.IntN(90)) }
	scalar := func() any {
		switch r.IntN(9) {
		case 0:
			return r.IntN(2) == 0
		case 1:
			return int64(r.IntN(1<<20) - 1<<19)
		case 2:
			return r.NormFloat64()
		case 3:
			return str()
		case 4:
			return []byte(str())
		case 5:
			return time.UnixMicro(int64(r.IntN(1 << 40))).UTC()
		case 6:
			return VariantDecimal{Value: DecimalFromInt64(int64(r.IntN(1<<30) - 1<<29)), Scale: r.IntN(10)}
		case 7:
			return r.Int64()
		}
		return nil
	}
	switch i % 7 {
	case 0:
		return nil
	case 1:
		return []any{scalar(), scalar(), []any{scalar(), map[string]any{"deep": scalar()}}}
	case 2:
		return map[string]any{"id": int64(i), "name": str(), "tags": []any{str(), str()},
			"nested": map[string]any{"k" + string(rune('a'+i%5)): scalar()}}
	}
	return scalar()
}

func variantRows(n int, seed uint64) []any {
	r := rand.New(rand.NewPCG(seed, 1))
	out := make([]any, n)
	for i := range out {
		out[i] = variantRow(r, i)
	}
	return out
}

// readVariants reads the Variant column of a file with Column.Variant.
func readVariants(t testing.TB, f *File) []any {
	t.Helper()
	var out []any
	for b, err := range f.Scan(context.Background(), Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		c := b.Col(0)
		for i := range b.Len() {
			v, err := c.Variant(i)
			if err != nil {
				t.Fatal(err)
			}
			if (v == nil) != c.Null(i) && c.Type().Nullable() {
				t.Fatalf("row %d: value %#v and null %v", i, v, c.Null(i))
			}
			out = append(out, v)
		}
	}
	return out
}

func writeVariants(t testing.TB, c Compression, ty Type, vals []any, sizes ...int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, NewSchema(Field{"v", ty}), WriteOptions{Compression: c})
	lo := 0
	for _, n := range sizes {
		if err := w.Write(OfVariants(vals[lo : lo+n])); err != nil {
			t.Fatal(err)
		}
		lo += n
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWriteVariantsRoundTrip(t *testing.T) {
	const n = 20000
	vals := variantRows(n, 1)
	sizes := map[Compression]int{}
	for mode, c := range map[string]Compression{"default": Default, "compact": Compact, "none": None} {
		t.Run(mode, func(t *testing.T) {
			b := writeVariants(t, c, VariantType().AsNullable(), vals, 7000, 1, 8191, 4808)
			sizes[c] = len(b)
			f := openBytes(t, b)
			for _, id := range []string{"vortex.variant", "vortex.parquet.variant"} {
				if !slices.Contains(f.arrayEncodings, id) {
					t.Errorf("no %s in %v", id, f.arrayEncodings)
				}
			}
			if f.NumRows() != n || f.Schema().String() != "struct{v: variant?}" {
				t.Fatalf("%d rows, %s", f.NumRows(), f.Schema())
			}
			got := readVariants(t, f)
			if len(got) != n {
				t.Fatalf("%d rows", len(got))
			}
			for i := range got {
				if !reflect.DeepEqual(got[i], vals[i]) {
					t.Fatalf("row %d is %#v, want %#v", i, got[i], vals[i])
				}
			}
			type row struct {
				V any `vortex:"v"`
			}
			i := 0
			for r, err := range Rows[row](context.Background(), f, Query{}) {
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(r.V, vals[i]) {
					t.Fatalf("Rows: row %d is %#v, want %#v", i, r.V, vals[i])
				}
				i++
			}
			if i != n {
				t.Fatalf("Rows gave %d rows", i)
			}
		})
	}
	if sizes[Default] >= sizes[None] {
		t.Errorf("the compressor did not shrink the metadata and the values: %d bytes, %d plain", sizes[Default], sizes[None])
	}
}

func TestWriteVariantsNotNullable(t *testing.T) {
	vals := variantRows(300, 2)
	for i := range vals {
		if vals[i] == nil {
			vals[i] = "x"
		}
	}
	f := openBytes(t, writeVariants(t, Default, VariantType(), vals, 300))
	if got := readVariants(t, f); !reflect.DeepEqual(got, vals) {
		t.Fatal("rows differ")
	}
}

// TestWriteVariantsAnyGoNumber: every Go integer and float widens, and json.Unmarshal's maps
// need no conversion.
func TestWriteVariantsAnyGoNumber(t *testing.T) {
	in := []any{int8(-1), int16(2), int32(3), int(4), uint8(5), uint16(6), uint32(7), uint(8), uint64(9), float32(1.5),
		map[string]any{"a": []any{int8(1), float32(2)}}}
	want := []any{int64(-1), int64(2), int64(3), int64(4), int64(5), int64(6), int64(7), int64(8), int64(9), 1.5,
		map[string]any{"a": []any{int64(1), float64(2)}}}
	f := openBytes(t, writeVariants(t, Default, VariantType(), in, len(in)))
	if got := readVariants(t, f); !reflect.DeepEqual(got, want) {
		t.Fatalf("%#v, want %#v", got, want)
	}
}

func TestWriteVariantsInNestedTypes(t *testing.T) {
	ty := StructOf(Field{"s", VariantType().AsNullable()}, Field{"l", ListOf(VariantType().AsNullable())})
	vals := variantRows(100, 3)
	offs := make([]int, 101)
	for i := range offs {
		offs[i] = i
	}
	var buf bytes.Buffer
	w := NewWriter(&buf, NewSchema(Field{"c", ty}), WriteOptions{})
	if err := w.Write(OfStruct(OfVariants(vals), OfList(offs, OfVariants(vals)))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	type inner struct {
		S any   `vortex:"s"`
		L []any `vortex:"l"`
	}
	type outer struct {
		C inner `vortex:"c"`
	}
	i := 0
	for r, err := range Rows[outer](context.Background(), openBytes(t, buf.Bytes()), Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r.C.S, vals[i]) || !reflect.DeepEqual(r.C.L, []any{vals[i]}) {
			t.Fatalf("row %d is %#v", i, r.C)
		}
		i++
	}
	if i != 100 {
		t.Fatalf("%d rows", i)
	}
}

func TestWriteVariantsErrors(t *testing.T) {
	write := func(ty Type, d Data) error {
		w := NewWriter(io.Discard, NewSchema(Field{"v", ty}), WriteOptions{})
		return w.Write(d)
	}
	for name, c := range map[string]struct {
		ty   Type
		d    Data
		want []string
	}{
		"struct":      {VariantType(), OfVariants([]any{"a", struct{}{}}), []string{`field "v"`, "row 1", "struct {}"}},
		"chan":        {VariantType(), OfVariants([]any{make(chan int)}), []string{"row 0", "chan int"}},
		"map of ints": {VariantType().AsNullable(), OfVariants([]any{nil, map[int]any{1: 1}}), []string{"row 1", "map[int]interface {}"}},
		"nested":      {VariantType(), OfVariants([]any{[]any{map[string]any{"k": make(chan int)}}}), []string{`element 0: field "k"`}},
		"null":        {VariantType(), OfVariants([]any{"a", nil}), []string{"row 1 is null and the field is not nullable"}},
		"shape":       {VariantType(), Of([]int64{1}), []string{"OfVariants"}},
		"variants":    {Utf8Type(), OfVariants([]any{"a"}), []string{"variants"}},
		"uint64":      {VariantType(), OfVariants([]any{uint64(1 << 63)}), []string{"does not fit an int64"}},
	} {
		err := write(c.ty, c.d)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q does not say %q", name, err, w)
			}
		}
	}
}

func TestWriteVariantsEdition(t *testing.T) {
	for _, ty := range []Type{VariantType(), ListOf(VariantType())} {
		w := NewWriter(io.Discard, NewSchema(Field{"v", ty}), WriteOptions{Edition: "core 2026.08.2"})
		err := w.Write()
		if err == nil || !strings.Contains(err.Error(), "variant is not in the edition") {
			t.Errorf("%s: %v", ty, err)
		}
	}
	w := NewWriter(io.Discard, NewSchema(Field{"v", VariantType()}), WriteOptions{Edition: "core 2026.08.3"})
	if err := w.Write(OfVariants([]any{"a"})); err != nil {
		t.Fatal(err)
	}
}

func TestVariantRowWriter(t *testing.T) {
	type row struct {
		ID   int64
		V    any
		List []any
	}
	rows := make([]row, 3000)
	for i, v := range variantRows(len(rows), 4) {
		rows[i] = row{int64(i), v, []any{v, nil, int64(i)}}
	}
	var buf bytes.Buffer
	w := NewRowWriter[row](&buf, WriteOptions{})
	if n, err := w.Write(rows); err != nil || n != len(rows) {
		t.Fatalf("%d rows, %v", n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	if got := f.Schema().String(); got != "struct{ID: i64, V: variant?, List: list<variant?>}" {
		t.Fatal(got)
	}
	i := 0
	for r, err := range Rows[row](context.Background(), f, Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r, rows[i]) {
			t.Fatalf("row %d is %#v, want %#v", i, r, rows[i])
		}
		i++
	}
	if i != len(rows) {
		t.Fatalf("%d rows", i)
	}
}

func TestVariantRowWriterErrors(t *testing.T) {
	type nullable struct{ V *any }
	type wrong struct{ V any }
	for name, w := range map[string]interface{ Close() error }{
		"pointer":   NewRowWriter[nullable](io.Discard, WriteOptions{}),
	} {
		if err := w.Close(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	w := NewRowWriter[wrong](io.Discard, WriteOptions{})
	if _, err := w.Write([]wrong{{1}, {make(chan int)}}); err == nil || !strings.Contains(err.Error(), "row 1") {
		t.Fatalf("%v", err)
	}
}

func TestNoAllocationPerVariantWrite(t *testing.T) {
	w := NewWriter(io.Discard, NewSchema(Field{"v", VariantType().AsNullable()}), WriteOptions{})
	vals := make([]any, 1000)
	for i := range vals {
		vals[i] = []any{nil, true, int64(i * 1000), 1.5, "a string", []byte("bytes"), time.UnixMicro(int64(i)).UTC()}[i%7]
	}
	d := OfVariants(vals)
	for range 300 {
		if err := w.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	if n := testing.AllocsPerRun(200, func() {
		if err := w.Write(d); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("%v allocations per Write", n)
	}
}
