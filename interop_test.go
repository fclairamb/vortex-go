//go:build interop

package vortex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// interopDir is where the files go: INTEROP_DIR, a temporary folder when it is not set.
func interopDir(t *testing.T) string {
	if dir := os.Getenv("INTEROP_DIR"); dir != "" {
		return dir
	}
	return t.TempDir()
}

// TestInterop writes files and the canonical hashes of their source data into INTEROP_DIR:
// `make interop` then has the reference reader open them, with tools/oracle/oracle.py check.
// One file per type, in one chunk and in four, then every type side by side.
func TestInterop(t *testing.T) {
	dir := interopDir(t)
	types := allTypes()
	var fields []Field
	for _, name := range slices.Sorted(maps.Keys(types)) {
		fields = append(fields, Field{Name: name, Type: types[name]})
		file := "type_" + strings.NewReplacer("?", "_null").Replace(name)
		writeInterop(t, dir, file, fields[len(fields)-1:], []int{200})
		writeInterop(t, dir, file+"_chunks", fields[len(fields)-1:], []int{1, 130, 0, 17})
	}
	writeInterop(t, dir, "wide", fields, []int{500, 1, 77})
	writeInterop(t, dir, "wide_one_row", fields, []int{1})
	// The reference's Python package cannot make an empty arrow array of a list of structs with
	// an extension type in it, so the files of no row leave that one out.
	empty := slices.DeleteFunc(slices.Clone(fields), func(f Field) bool { return f.Name == "list_struct" })
	writeInterop(t, dir, "wide_empty", empty, nil)
	writeInterop(t, dir, "wide_empty_write", empty, []int{0})
}

// writeInterop writes a file with one Write per size, and its hashes.
func writeInterop(t *testing.T, dir, file string, fields []Field, sizes []int) {
	t.Helper()
	g := &gen{r: rand.New(rand.NewPCG(11, uint64(len(file))))}
	var buf bytes.Buffer
	w := NewWriter(&buf, NewSchema(fields...), WriteOptions{})
	all := make([][]val, len(fields))
	total := 0
	for _, n := range sizes {
		cols := make([]Data, len(fields))
		for i, f := range fields {
			var vals []val
			cols[i], vals = g.make(f.Type, n)
			all[i] = append(all[i], vals...)
		}
		if err := w.Write(cols...); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		total += n
	}
	if err := w.Close(); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	hashes := map[string]hashEntry{}
	for i, f := range fields {
		hashColumn(hashes, f.Name, f.Type, all[i])
	}
	out, err := json.MarshalIndent(map[string]any{"rows": total, "columns": hashes}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file+".vortex"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file+".hashes.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// filterRows is the rows of the files of TestInteropFilters.
const filterRows = 100_000

// filterColumn is a column of the filter files: its values, and the Data of a range of them.
type filterColumn struct {
	t    Type
	vals []val
	data func(lo, hi int) Data
}

// filterColumns makes sorted columns (the statistics of their zones are tight) and one with
// NaNs and long strings, which the bounds of a zone must not lose.
func filterColumns() map[string]filterColumn {
	k := make([]int64, filterRows)
	kn := make([]int64, filterRows)
	knNull := make([]bool, filterRows)
	s := make([]string, filterRows)
	f := make([]float64, filterRows)
	for i := range k {
		k[i] = int64(i) * 3
		kn[i], knNull[i] = int64(i)*2-5000, i%10 == 3
		s[i] = fmt.Sprintf("%s%08d", strings.Repeat("p", 70), i)
		f[i] = float64(i) / 100
		if i%1000 == 7 {
			f[i] = math.NaN()
		}
	}
	return map[string]filterColumn{
		"k": {Int64Type(), numVals(k, nil), func(lo, hi int) Data { return Of(k[lo:hi]) }},
		"kn": {Int64Type().AsNullable(), numVals(kn, knNull), func(lo, hi int) Data {
			return Of(kn[lo:hi]).Nulls(knNull[lo:hi])
		}},
		"s": {Utf8Type(), strVals(s), func(lo, hi int) Data { return OfStrings(s[lo:hi]) }},
		"f": {Float64Type(), numVals(f, nil), func(lo, hi int) Data { return Of(f[lo:hi]) }},
	}
}

func numVals[T int64 | float64](v []T, nulls []bool) []val {
	out := make([]val, len(v))
	for i, x := range v {
		out[i] = val{b: rawOf(x)}
		if nulls != nil && nulls[i] {
			out[i] = val{null: true}
		}
	}
	return out
}

func strVals(v []string) []val {
	out := make([]val, len(v))
	for i, x := range v {
		out[i] = val{b: []byte(x)}
	}
	return out
}

// filterCase is a filter of the oracle: the rows it keeps.
type filterCase struct {
	spec map[string]any
	keep func(i int) bool
}

// TestInteropFilters writes a file in writes of every size, and hashes of what a filter
// keeps of it: the reference reader prunes the zones with our statistics, so a wrong bound
// loses rows and the hashes differ.
func TestInteropFilters(t *testing.T) {
	dir := interopDir(t)
	cols := filterColumns()
	names := slices.Sorted(maps.Keys(cols))
	fields := make([]Field, len(names))
	for i, n := range names {
		fields[i] = Field{Name: n, Type: cols[n].t}
	}
	var buf bytes.Buffer
	w := NewWriter(&buf, NewSchema(fields...), WriteOptions{})
	sizes := []int{1, 1, 8191, 3, 40000, 7, 1000}
	for lo, i := 0, 0; lo < filterRows; i++ {
		hi := min(lo+sizes[i%len(sizes)], filterRows)
		data := make([]Data, len(names))
		for j, n := range names {
			data[j] = cols[n].data(lo, hi)
		}
		if err := w.Write(data...); err != nil {
			t.Fatal(err)
		}
		lo = hi
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("p", 70)
	cases := []filterCase{
		{map[string]any{"column": "k", "ge": 30000, "lt": 33000}, func(i int) bool { return i*3 >= 30000 && i*3 < 33000 }},
		{map[string]any{"column": "k", "eq": 150000}, func(i int) bool { return i*3 == 150000 }},
		{map[string]any{"column": "k", "gt": 1 << 40}, func(int) bool { return false }},
		{map[string]any{"column": "kn", "ge": 150000, "le": 151500}, func(i int) bool {
			return i%10 != 3 && i*2-5000 >= 150000 && i*2-5000 <= 151500
		}},
		{map[string]any{"column": "s", "ge": pad + "00050000", "lt": pad + "00050500"},
			func(i int) bool { return i >= 50000 && i < 50500 }},
		{map[string]any{"column": "s", "gt": pad + "00099990"}, func(i int) bool { return i > 99990 }},
		{map[string]any{"column": "f", "gt": 995.0}, func(i int) bool { return i%1000 == 7 || float64(i)/100 > 995.0 }}, // a NaN is greater than any number
		{map[string]any{"column": "f", "lt": 3.0}, func(i int) bool { return i%1000 != 7 && float64(i)/100 < 3.0 }},
	}
	hashes := map[string]hashEntry{}
	for _, n := range names {
		hashColumn(hashes, n, cols[n].t, cols[n].vals)
	}
	filters := make([]map[string]any, len(cases))
	for i, fc := range cases {
		spec := maps.Clone(fc.spec)
		kept := map[string]hashEntry{}
		rows := 0
		for _, n := range names {
			var vals []val
			for r := range filterRows {
				if fc.keep(r) {
					vals = append(vals, cols[n].vals[r])
				}
			}
			rows = len(vals)
			hashColumn(kept, n, cols[n].t, vals)
		}
		spec["rows"], spec["columns"] = rows, kept
		filters[i] = spec
	}
	out, err := json.MarshalIndent(map[string]any{"rows": filterRows, "columns": hashes, "filters": filters}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "filters.vortex"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "filters.hashes.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}
