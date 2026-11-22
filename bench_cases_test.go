package vortex_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	vortex "github.com/fclairamb/vortex-go"
)

// The cases of bench/cases.json, run by BenchmarkCases. The same list drives the benchmarks
// of parquet-go (bench/) and the Rust harness (tools/bench-rust), so a result is matched by
// its name: BenchmarkCases/<case>. A case that reads runs twice: on a file OpenFile opened, and
// as BenchmarkCases/<case>/mapped on a file OpenMapped opened. `make bench-json` runs it once
// with -cpu 1 and once with -cpu <cores>.

// benchSpec is bench/cases.json.
type benchSpec struct {
	Data  map[string]benchData `json:"data"`
	Cases []benchCase          `json:"cases"`
}

type benchData struct {
	File      string   `json:"file"`
	Generated bool     `json:"generated"`
	Project   []string `json:"project"`
	Filter    struct {
		Column string `json:"column"`
		Type   string `json:"type"`
		Lt     int64  `json:"lt"`
	} `json:"filter"`
	Range [2]float64 `json:"range"`
}

type benchCase struct {
	Name string `json:"name"`
	Data string `json:"data"`
	Op   string `json:"op"`
}

func loadBenchSpec(tb testing.TB) benchSpec {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join("bench", "cases.json"))
	if err != nil {
		tb.Fatal(err)
	}
	var s benchSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		tb.Fatal(err)
	}
	return s
}

// benchOpen opens the file of a case: OpenFile, or OpenMapped for the mapped run of the case.
type benchOpen func(path string) (*vortex.File, error)

// benchOps are the operations of a case. Each one runs the timed loop on a file.
var benchOps = map[string]func(b *testing.B, path string, d benchData, open benchOpen){
	"open": func(b *testing.B, path string, _ benchData, open benchOpen) {
		b.ReportAllocs()
		for b.Loop() {
			f, err := open(path)
			if err != nil {
				b.Fatal(err)
			}
			f.Close()
		}
	},
	"scan_full": func(b *testing.B, path string, _ benchData, open benchOpen) {
		benchScan(b, openWith(b, path, open), vortex.Query{})
	},
	"scan_3col": func(b *testing.B, path string, d benchData, open benchOpen) {
		benchScan(b, openWith(b, path, open), vortex.Query{}, d.Project...)
	},
	"scan_filter": func(b *testing.B, path string, d benchData, open benchOpen) {
		q := vortex.Query{Filter: vortex.Col(d.Filter.Column).Lt(benchLiteral(b, d.Filter.Type, d.Filter.Lt))}
		benchScan(b, openWith(b, path, open), q, d.Project...)
	},
	"scan_range": func(b *testing.B, path string, d benchData, open benchOpen) {
		f := openWith(b, path, open)
		n := float64(f.NumRows())
		q := vortex.Query{Rows: vortex.Range{Start: int64(n * d.Range[0]), End: int64(n * d.Range[1])}}
		benchScan(b, f, q)
	},
	"write_default": func(b *testing.B, path string, _ benchData, _ benchOpen) { benchWriteFile(b, path, vortex.Default) },
	"write_none":    func(b *testing.B, path string, _ benchData, _ benchOpen) { benchWriteFile(b, path, vortex.None) },
	"write_compact": func(b *testing.B, path string, _ benchData, _ benchOpen) { benchWriteFile(b, path, vortex.Compact) },
}

// openWith opens the file of a benchmark with open, and closes it when the benchmark ends.
func openWith(b *testing.B, path string, open benchOpen) *vortex.File {
	b.Helper()
	f, err := open(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { f.Close() })
	return f
}

// benchLiteral is the value of a filter, of the Go type the column has.
func benchLiteral(tb testing.TB, kind string, v int64) any {
	tb.Helper()
	switch kind {
	case "i32":
		return int32(v)
	case "i64":
		return v
	}
	tb.Fatalf("literal type %q", kind)
	return nil
}

// benchDataPath is the file of a data entry: {version} is .vortex-version and {rows} the
// number of rows of the generated file.
func benchDataPath(b *testing.B, d benchData) string {
	b.Helper()
	if d.Generated {
		return benchFile(b)
	}
	version := strings.TrimSpace(mustRead(b, ".vortex-version"))
	p := strings.NewReplacer("{version}", version, "{rows}", strconv.Itoa(benchRows())).Replace(d.File)
	if _, err := os.Stat(p); err != nil {
		b.Skip("no data: ", err)
	}
	return p
}

func BenchmarkCases(b *testing.B) {
	spec := loadBenchSpec(b)
	for _, c := range spec.Cases {
		d := spec.Data[c.Data]
		op := benchOps[c.Op]
		b.Run(c.Name, func(b *testing.B) { op(b, benchDataPath(b, d), d, vortex.OpenFile) })
		// A case that reads runs a second time on the file mapped in memory. A case that
		// writes reads nothing in its timed loop.
		if !strings.HasPrefix(c.Op, "write_") {
			b.Run(c.Name+"/mapped", func(b *testing.B) { op(b, benchDataPath(b, d), d, vortex.OpenMapped) })
		}
	}
}

// benchWriteFile writes, to a discarded stream, the first writeRows rows of a file, decoded
// once outside the timed loop: the Rust harness writes the same rows.
func benchWriteFile(b *testing.B, path string, mode vortex.Compression) {
	f := openBench(b, path)
	cols, err := readColumns(f, writeRows)
	if err != nil {
		b.Skip(err)
	}
	b.ReportAllocs()
	var out int64
	for b.Loop() {
		var cw countWriter
		w := vortex.NewWriter(&cw, f.Schema(), vortex.WriteOptions{Compression: mode})
		if err := w.Write(cols...); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
		out = cw.n
	}
	b.ReportMetric(float64(out), "file-bytes")
	report(b, min(f.NumRows(), writeRows), 0)
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// colAcc gathers the rows of one column across batches.
type colAcc struct {
	add  func(c *vortex.Column, n int) error
	data func() vortex.Data
}

func numAcc[T vortex.Number](t vortex.Type) colAcc {
	var vals []T
	var nulls []bool
	return colAcc{
		add: func(c *vortex.Column, n int) error {
			v, err := vortex.Values[T](c)
			if err != nil {
				return err
			}
			vals = append(vals, v[:n]...)
			for i := range n {
				nulls = append(nulls, c.Null(i))
			}
			return nil
		},
		data: func() vortex.Data {
			d := vortex.Of(vals)
			if t.Nullable() {
				d = d.Nulls(nulls)
			}
			return d
		},
	}
}

// accOf is the accumulator of a column type, or an error for a type the benchmarks do not copy.
func accOf(t vortex.Type) (colAcc, error) {
	switch t.Kind() {
	case vortex.Int8:
		return numAcc[int8](t), nil
	case vortex.Int16:
		return numAcc[int16](t), nil
	case vortex.Int32:
		return numAcc[int32](t), nil
	case vortex.Int64:
		return numAcc[int64](t), nil
	case vortex.Uint8:
		return numAcc[uint8](t), nil
	case vortex.Uint16:
		return numAcc[uint16](t), nil
	case vortex.Uint32:
		return numAcc[uint32](t), nil
	case vortex.Uint64:
		return numAcc[uint64](t), nil
	case vortex.Float32:
		return numAcc[float32](t), nil
	case vortex.Float64:
		return numAcc[float64](t), nil
	case vortex.Date, vortex.Time, vortex.Timestamp:
		if t.Unit() == vortex.Day || t.Unit() == vortex.Second || t.Unit() == vortex.Millisecond && t.Kind() == vortex.Time {
			return numAcc[int32](t), nil
		}
		return numAcc[int64](t), nil
	case vortex.Bool:
		return gatherAcc(t, func(c *vortex.Column, i int) bool { return c.Bool(i) }, vortex.OfBools), nil
	case vortex.Utf8:
		return gatherAcc(t, func(c *vortex.Column, i int) string { return string(c.Bytes(i)) }, vortex.OfStrings), nil
	case vortex.Binary:
		return gatherAcc(t, func(c *vortex.Column, i int) []byte { return bytes.Clone(c.Bytes(i)) }, vortex.OfBytes), nil
	case vortex.KindDecimal:
		return gatherAcc(t, func(c *vortex.Column, i int) vortex.Decimal { return c.Decimal(i) }, vortex.OfDecimals), nil
	}
	return colAcc{}, fmt.Errorf("no copy of a %s column", t)
}

func gatherAcc[T any](t vortex.Type, get func(*vortex.Column, int) T, of func([]T) vortex.Data) colAcc {
	var vals []T
	var nulls []bool
	return colAcc{
		add: func(c *vortex.Column, n int) error {
			for i := range n {
				vals = append(vals, get(c, i))
				nulls = append(nulls, c.Null(i))
			}
			return nil
		},
		data: func() vortex.Data {
			d := of(vals)
			if t.Nullable() {
				d = d.Nulls(nulls)
			}
			return d
		},
	}
}

// readColumns reads the first rows of a file into Data the writer takes.
func readColumns(f *vortex.File, rows int64) ([]vortex.Data, error) {
	fields := f.Schema().Fields()
	accs := make([]colAcc, len(fields))
	for i, fl := range fields {
		a, err := accOf(fl.Type)
		if err != nil {
			return nil, err
		}
		accs[i] = a
	}
	n := min(f.NumRows(), rows)
	for batch, err := range f.Scan(context.Background(), vortex.Query{Rows: vortex.Range{Start: 0, End: n}}) {
		if err != nil {
			return nil, err
		}
		for i, a := range accs {
			if err := a.add(batch.Col(i), batch.Len()); err != nil {
				return nil, err
			}
		}
	}
	out := make([]vortex.Data, len(accs))
	for i, a := range accs {
		out[i] = a.data()
	}
	return out, nil
}

// TestBenchCases checks the list of cases: every case names a data entry that has a file, a
// filter and a range, and an operation the Go benchmarks run.
func TestBenchCases(t *testing.T) {
	spec := loadBenchSpec(t)
	if len(spec.Cases) == 0 {
		t.Fatal("no case")
	}
	seen := map[string]bool{}
	for _, c := range spec.Cases {
		if seen[c.Name] {
			t.Errorf("case %s is listed twice", c.Name)
		}
		seen[c.Name] = true
		d, ok := spec.Data[c.Data]
		if !ok || d.File == "" {
			t.Errorf("case %s: data %q has no file", c.Name, c.Data)
		}
		if _, ok := benchOps[c.Op]; !ok {
			t.Errorf("case %s: the Go benchmarks have no operation %q", c.Name, c.Op)
		}
		if c.Name != c.Data+"/"+c.Op {
			t.Errorf("case %s is not named <data>/<op>", c.Name)
		}
		if len(d.Project) == 0 || d.Filter.Column == "" || d.Range[1] <= d.Range[0] {
			t.Errorf("case %s: data %q lacks a projection, a filter or a range", c.Name, c.Data)
		}
		benchLiteral(t, d.Filter.Type, d.Filter.Lt)
	}
}

// TestBenchWriteCopy writes the rows of a fixture back with every compression, and reads them.
func TestBenchWriteCopy(t *testing.T) {
	spec := loadBenchSpec(t)
	raw, err := os.ReadFile(".vortex-version")
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(raw))
	for name, d := range spec.Data {
		if d.Generated {
			continue
		}
		path := strings.ReplaceAll(d.File, "{version}", version)
		f, err := vortex.OpenFile(path)
		if err != nil {
			t.Skip(err)
		}
		defer f.Close()
		cols, err := readColumns(f, 1<<20)
		if err != nil {
			t.Fatal(name, err)
		}
		for _, mode := range []vortex.Compression{vortex.Default, vortex.None, vortex.Compact} {
			var buf bytes.Buffer
			w := vortex.NewWriter(&buf, f.Schema(), vortex.WriteOptions{Compression: mode})
			if err := w.Write(cols...); err != nil {
				t.Fatal(name, err)
			}
			if err := w.Close(); err != nil {
				t.Fatal(name, err)
			}
			g, err := vortex.Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
			if err != nil {
				t.Fatal(name, err)
			}
			if g.NumRows() != f.NumRows() {
				t.Errorf("%s: %d rows written, %d read", name, f.NumRows(), g.NumRows())
			}
		}
	}
}
