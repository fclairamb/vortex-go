package vortex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// selFile is a file of 60,000 rows for the scans that keep few rows: f is what the filter
// reads (one row in a hundred is under 10), and the other columns are of the kinds a reader
// decodes some rows of (numbers, a dictionary of strings) and of the kinds it decodes whole
// (strings, a struct).
func selFile(t *testing.T, c Compression) []byte {
	t.Helper()
	const n = 60_000
	f, a, p := make([]int32, n), make([]int64, n), make([]float64, n)
	city, note := make([]string, n), make([]string, n)
	x := make([]int32, n)
	nulls := make([]bool, n)
	for i := range n {
		f[i] = int32(i * 7919 % 1000)
		a[i], p[i] = int64(i)*3-50_000, float64(i%9973)/100
		city[i] = []string{"paris", "lyon", "nantes", "lille"}[i%4]
		note[i] = fmt.Sprintf("note %d of the row", i*31%977)
		x[i], nulls[i] = int32(i%50), i%7 == 0
	}
	s := NewSchema(Field{"f", Int32Type()}, Field{"a", Int64Type()}, Field{"p", Float64Type()},
		Field{"city", Utf8Type()}, Field{"note", Utf8Type()}, Field{"n", Int32Type().AsNullable()},
		Field{"s", StructOf(Field{"x", Int32Type()})})
	var buf bytes.Buffer
	w := NewWriter(&buf, s, WriteOptions{Compression: c})
	if err := w.Write(Of(f), Of(a), Of(p), OfStrings(city), OfStrings(note), Of(x).Nulls(nulls), OfStruct(Of(x))); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestScanKeepsFewRows checks the scans whose filter keeps few rows, where the columns that the
// filter does not read decode the rows kept only: they return what a scan of every row
// returns for these rows, with one thread and with several, compressed or not, and when the
// share of the rows kept changes from one batch to the next.
func TestScanKeepsFewRows(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	for _, comp := range []Compression{Default, None, Compact} {
		f := openBytes(t, selFile(t, comp))
		cols := []string{"a", "p", "city", "note", "n", "s"}
		for name, c := range map[string]struct {
			q    Query
			keep func(f int32, a int64) bool
		}{
			"few":   {Query{Filter: Col("f").Lt(int32(10))}, func(f int32, _ int64) bool { return f < 10 }},
			"range": {Query{Filter: Col("f").Lt(int32(10)), Rows: Range{Start: 9000, End: 31_000}}, func(f int32, _ int64) bool { return f < 10 }},
			"none":  {Query{Filter: Col("f").Lt(int32(0))}, func(int32, int64) bool { return false }},
			"mixed": {Query{Filter: Or(Col("f").Lt(int32(3)), And(Col("a").Ge(int64(0)), Col("a").Lt(int64(9000))))},
				func(f int32, a int64) bool { return f < 3 || a >= 0 && a < 9000 }},
		} {
			want, err := digestOf(f, Query{Rows: c.q.Rows}, c.keep, append([]string{"f"}, cols...))
			if err != nil {
				t.Fatal(err)
			}
			for _, procs := range []int{1, 4} {
				runtime.GOMAXPROCS(procs)
				got, err := digestOf(f, c.q, nil, cols)
				if err != nil || got != want {
					t.Errorf("compression %d, %s, %d threads: other rows (%v)", comp, name, procs, err)
				}
			}
		}
	}
}

// digestOf is the rows of a scan of q, as text. With keep, the scan has the column f first,
// which is left out, and only the rows that keep accepts for their f and their a are kept.
func digestOf(f *File, q Query, keep func(f int32, a int64) bool, cols []string) (string, error) {
	var sb strings.Builder
	first := 0
	if keep != nil {
		first = 1
	}
	for b, err := range f.Scan(context.Background(), q, cols...) {
		if err != nil {
			return "", err
		}
		for i := range b.Len() {
			if keep != nil {
				fs, _ := Values[int32](b.Col(0))
				as, _ := Values[int64](b.Col(1))
				if !keep(fs[i], as[i]) {
					continue
				}
			}
			for k := first; k < len(cols); k++ {
				c := b.Col(k)
				switch {
				case c.Null(i):
					sb.WriteString("null,")
				case c.Type().Kind() == Utf8:
					fmt.Fprintf(&sb, "%s,", c.Bytes(i))
				case c.Type().Kind() == Struct:
					v, _ := Values[int32](c.Field(0))
					fmt.Fprintf(&sb, "{%d},", v[i])
				case c.Type().Kind() == Float64:
					v, _ := Values[float64](c)
					fmt.Fprintf(&sb, "%v,", v[i])
				case c.Type().Kind() == Int64:
					v, _ := Values[int64](c)
					fmt.Fprintf(&sb, "%d,", v[i])
				default:
					v, _ := Values[int32](c)
					fmt.Fprintf(&sb, "%d,", v[i])
				}
			}
			sb.WriteByte('\n')
		}
	}
	return sb.String(), nil
}

// firstFlat is the segment of the first flat layout of the data under l.
func firstFlat(f *File, l *layoutNode, values bool) uint32 {
	for len(l.segments) == 0 {
		k := 0
		if f.layoutEncodings[l.encoding] == layoutDict && !values {
			k = 1 // the codes
		}
		l = l.children[k]
	}
	return l.segments[0]
}

// TestScanKeepsFewRowsErrors breaks the segments of the columns that a filter does not read,
// one at a time: the scan that decodes the rows kept only reports them like the others.
func TestScanKeepsFewRowsErrors(t *testing.T) {
	raw, packed := selFile(t, None), selFile(t, Default)
	q := Query{Filter: Col("f").Lt(int32(10))}
	field := map[string]int{"a": 1, "p": 2, "city": 3, "s": 6}
	for name, c := range map[string]struct {
		col    string
		values bool
		spec   string // the encoding of the arrays that is read as another one
	}{
		"segment cut short":   {col: "a"},
		"another encoding":    {col: "p", spec: "vortex.alp"},
		"values of a dict":    {col: "city", values: true},
		"field of a struct":   {col: "s"},
		"codes of a dict cut": {col: "city"},
	} {
		f := openBytes(t, raw)
		if c.spec != "" { // the floats of the compressed file are read as zigzag integers
			f = openBytes(t, packed)
			f.arrayEncodings[slices.Index(f.arrayEncodings, c.spec)] = "vortex.zigzag"
		} else {
			f.segments[firstFlat(f, f.layout.children[field[c.col]], c.values)].Length -= 4
		}
		_, err := digestOf(f, q, nil, []string{c.col})
		if err == nil || !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestScanPartOfSegments checks the scans of a range of rows that is a part of the segments
// it crosses, whose rows are decoded without the others: the rows are the ones of a scan of
// the file at the same places, in the 3 compression modes, with one thread and with several.
func TestScanPartOfSegments(t *testing.T) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(0))
	cols := []string{"f", "a", "p", "city", "note", "n", "s"}
	for _, comp := range []Compression{Default, None, Compact} {
		f := openBytes(t, selFile(t, comp))
		all, err := digestOf(f, Query{}, nil, cols)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.SplitAfter(all, "\n")
		for _, r := range []Range{{Start: 100, End: 150}, {Start: 25_000, End: 29_000}, {Start: 59_990, End: 60_000}, {Start: 8191, End: 8193}} {
			for _, procs := range []int{1, 4} {
				runtime.GOMAXPROCS(procs)
				got, err := digestOf(f, Query{Rows: r}, nil, cols)
				if want := strings.Join(lines[r.Start:r.End], ""); err != nil || got != want {
					t.Errorf("compression %d, rows %d to %d, %d threads: other rows (%v)", comp, r.Start, r.End, procs, err)
				}
			}
		}
	}
}
