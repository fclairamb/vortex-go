package vortex_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	vortex "github.com/fclairamb/vortex-go"
)

// The benchmarks: `make bench`, or go test -bench . -benchmem.
//
// The data is a file of 16 columns generated from a seed, 10 million rows, written once into
// .cache/ (VORTEX_BENCH_ROWS lowers the count). The TPC-H and ClickBench files of
// .cache/fixtures/ are used when they are there. A benchmark skips without its data.

const benchSeed = 20261120

// benchRows is the number of rows of the generated file.
func benchRows() int {
	if v, err := strconv.Atoi(os.Getenv("VORTEX_BENCH_ROWS")); err == nil && v > 0 {
		return v
	}
	return 10_000_000
}

var benchSchema = vortex.NewSchema(
	vortex.Field{Name: "id", Type: vortex.Int64Type()},
	vortex.Field{Name: "ts", Type: vortex.Int64Type()},
	vortex.Field{Name: "user", Type: vortex.Int32Type()},
	vortex.Field{Name: "qty", Type: vortex.Int16Type()},
	vortex.Field{Name: "small", Type: vortex.Uint8Type()},
	vortex.Field{Name: "big", Type: vortex.Uint64Type()},
	vortex.Field{Name: "price", Type: vortex.Float64Type()},
	vortex.Field{Name: "score", Type: vortex.Float32Type()},
	vortex.Field{Name: "gauss", Type: vortex.Float64Type()},
	vortex.Field{Name: "flag", Type: vortex.BoolType()},
	vortex.Field{Name: "flag2", Type: vortex.BoolType()},
	vortex.Field{Name: "city", Type: vortex.Utf8Type()},
	vortex.Field{Name: "name", Type: vortex.Utf8Type()},
	vortex.Field{Name: "note", Type: vortex.Utf8Type()},
	vortex.Field{Name: "maybe", Type: vortex.Int32Type().AsNullable()},
	vortex.Field{Name: "maybe_text", Type: vortex.Utf8Type().AsNullable()},
)

// benchTrade is the struct of the row benchmarks: the columns id, price, city and name.
type benchTrade struct {
	ID    int64   `vortex:"id"`
	Price float64 `vortex:"price"`
	City  string  `vortex:"city"`
	Name  string  `vortex:"name"`
}

var benchCities = []string{"paris", "lyon", "nantes", "lille", "nice", "brest", "metz", "tours"}

// benchColumns makes the columns of rows [from, from+n) of the generated data.
func benchColumns(rng *rand.Rand, from, n int) []vortex.Data {
	id, ts := make([]int64, n), make([]int64, n)
	user, maybe := make([]int32, n), make([]int32, n)
	qty := make([]int16, n)
	small := make([]uint8, n)
	big := make([]uint64, n)
	price, gauss := make([]float64, n), make([]float64, n)
	score := make([]float32, n)
	flag, flag2 := make([]bool, n), make([]bool, n)
	city, name, note, text := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	nullInt, nullText := make([]bool, n), make([]bool, n)
	for i := range n {
		row := from + i
		id[i], ts[i] = int64(row), 1_700_000_000_000+int64(row)*37+int64(rng.IntN(30))
		user[i], qty[i], small[i] = int32(rng.IntN(100_000)), int16(rng.IntN(500)), uint8(rng.IntN(256))
		big[i] = rng.Uint64()
		price[i], gauss[i], score[i] = float64(rng.IntN(1_000_000))/100, rng.NormFloat64(), rng.Float32()
		flag[i], flag2[i] = rng.IntN(2) == 0, rng.IntN(20) == 0
		city[i] = benchCities[rng.IntN(len(benchCities))]
		name[i] = "customer " + strconv.Itoa(rng.IntN(5000)) + " of " + city[i]
		note[i] = "row " + strconv.Itoa(row) + " " + strconv.FormatUint(rng.Uint64(), 36)
		maybe[i], nullInt[i] = int32(rng.IntN(1000)), rng.IntN(10) == 0
		text[i], nullText[i] = city[i], rng.IntN(4) == 0
	}
	return []vortex.Data{
		vortex.Of(id), vortex.Of(ts), vortex.Of(user), vortex.Of(qty), vortex.Of(small), vortex.Of(big),
		vortex.Of(price), vortex.Of(score), vortex.Of(gauss), vortex.OfBools(flag), vortex.OfBools(flag2),
		vortex.OfStrings(city), vortex.OfStrings(name), vortex.OfStrings(note),
		vortex.Of(maybe).Nulls(nullInt), vortex.OfStrings(text).Nulls(nullText),
	}
}

var (
	benchOnce sync.Once
	benchPath string
	benchErr  error
)

// benchFile returns the path of the generated file, written on the first call and reused by
// the next runs: the name carries the row count.
func benchFile(b *testing.B) string {
	b.Helper()
	benchOnce.Do(func() {
		rows := benchRows()
		dir := filepath.Join(".cache", "bench")
		if benchErr = os.MkdirAll(dir, 0o755); benchErr != nil {
			return
		}
		benchPath = filepath.Join(dir, fmt.Sprintf("gen16-%d.vortex", rows))
		if _, err := os.Stat(benchPath); err == nil {
			return
		}
		benchErr = writeBenchFile(benchPath, rows)
	})
	if benchErr != nil {
		b.Fatal(benchErr)
	}
	return benchPath
}

// writeBenchFile writes the generated rows to path through a temporary file, so that an
// interrupted run leaves no partial file.
func writeBenchFile(path string, rows int) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "gen-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	bw := bufio.NewWriterSize(tmp, 1<<20)
	w := vortex.NewWriter(bw, benchSchema, vortex.WriteOptions{})
	rng := rand.New(rand.NewPCG(benchSeed, 1))
	const chunk = 1 << 16
	for from := 0; from < rows; from += chunk {
		if err := w.Write(benchColumns(rng, from, min(chunk, rows-from))...); err != nil {
			return err
		}
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// decodedBytes is the size of the values a scan decodes, measured once outside the timed
// loop: the width of the numbers and the length of the strings.
func decodedBytes(b *testing.B, f *vortex.File, q vortex.Query, cols ...string) (rows, size int64) {
	b.Helper()
	ncols := len(cols)
	if ncols == 0 {
		ncols = len(f.Schema().Fields())
	}
	for batch, err := range f.Scan(context.Background(), q, cols...) {
		if err != nil {
			b.Fatal(err)
		}
		rows += int64(batch.Len())
		for i := range ncols {
			size += columnBytes(batch.Col(i), batch.Len())
		}
	}
	return rows, size
}

// columnBytes is the decoded size of n rows of a column.
func columnBytes(c *vortex.Column, n int) int64 {
	switch k := c.Type().Kind(); k {
	case vortex.Utf8, vortex.Binary:
		var s int64
		for i := range n {
			s += int64(len(c.Bytes(i)))
		}
		return s
	case vortex.Bool:
		return int64(n+7) / 8
	case vortex.Int8, vortex.Uint8:
		return int64(n)
	case vortex.Int16, vortex.Uint16, vortex.Float16:
		return 2 * int64(n)
	case vortex.Int32, vortex.Uint32, vortex.Float32:
		return 4 * int64(n)
	default:
		return 8 * int64(n)
	}
}

// report adds the rows and the MB of decoded data per second, for b.N runs of rows and size.
func report(b *testing.B, rows, size int64) {
	b.Helper()
	s := b.Elapsed().Seconds()
	b.ReportMetric(float64(rows)*float64(b.N)/s, "rows/s")
	if size > 0 {
		b.ReportMetric(float64(size)*float64(b.N)/s/1e6, "decoded-MB/s")
	}
}

func openBench(b *testing.B, path string) *vortex.File {
	b.Helper()
	f, err := vortex.OpenFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { f.Close() })
	return f
}

// benchScan times a scan of q, and reports its rows and decoded MB per second.
func benchScan(b *testing.B, f *vortex.File, q vortex.Query, cols ...string) {
	rows, size := decodedBytes(b, f, q, cols...)
	b.ReportAllocs()
	ctx := context.Background()
	for b.Loop() {
		for batch, err := range f.Scan(ctx, q, cols...) {
			if err != nil {
				b.Fatal(err)
			}
			_ = batch.Len()
		}
	}
	report(b, rows, size)
}

func BenchmarkOpen(b *testing.B) {
	path := benchFile(b)
	b.ReportAllocs()
	for b.Loop() {
		f, err := vortex.OpenFile(path)
		if err != nil {
			b.Fatal(err)
		}
		f.Close()
	}
}

func BenchmarkScanFull(b *testing.B) {
	benchScan(b, openBench(b, benchFile(b)), vortex.Query{})
}

func BenchmarkScan3of16(b *testing.B) {
	benchScan(b, openBench(b, benchFile(b)), vortex.Query{}, "id", "price", "city")
}

// BenchmarkScanFilter keeps 1% of the rows: user is uniform in [0, 100000).
func BenchmarkScanFilter(b *testing.B) {
	q := vortex.Query{Filter: vortex.Col("user").Lt(1000)}
	benchScan(b, openBench(b, benchFile(b)), q, "id", "user", "price")
}

func BenchmarkScanRange(b *testing.B) {
	f := openBench(b, benchFile(b))
	mid := f.NumRows() / 2
	benchScan(b, f, vortex.Query{Rows: vortex.Range{Start: mid, End: mid + f.NumRows()/10}})
}

func BenchmarkRows(b *testing.B) {
	f := openBench(b, benchFile(b))
	var rows, size int64
	for t, err := range vortex.Rows[benchTrade](context.Background(), f, vortex.Query{}) {
		if err != nil {
			b.Fatal(err)
		}
		rows++
		size += 16 + int64(len(t.City)+len(t.Name))
	}
	b.ReportAllocs()
	for b.Loop() {
		for _, err := range vortex.Rows[benchTrade](context.Background(), f, vortex.Query{}) {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
	report(b, rows, size)
}

// writeRows is the number of rows each write benchmark writes per operation.
const writeRows = 1 << 20

func BenchmarkWriteColumns(b *testing.B) {
	rng := rand.New(rand.NewPCG(benchSeed, 2))
	cols := benchColumns(rng, 0, writeRows)
	b.ReportAllocs()
	for b.Loop() {
		w := vortex.NewWriter(io.Discard, benchSchema, vortex.WriteOptions{})
		if err := w.Write(cols...); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
	report(b, writeRows, 0)
}

func BenchmarkWriteRows(b *testing.B) {
	rng := rand.New(rand.NewPCG(benchSeed, 3))
	rows := make([]benchTrade, writeRows)
	var size int64
	for i := range rows {
		city := benchCities[rng.IntN(len(benchCities))]
		rows[i] = benchTrade{int64(i), float64(rng.IntN(1_000_000)) / 100, city, "customer " + strconv.Itoa(rng.IntN(5000))}
		size += 16 + int64(len(rows[i].City)+len(rows[i].Name))
	}
	b.ReportAllocs()
	for b.Loop() {
		w := vortex.NewRowWriter[benchTrade](io.Discard, vortex.WriteOptions{})
		if _, err := w.Write(rows); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
	report(b, writeRows, size)
}

// BenchmarkDecode scans every fixture that is named after an encoding: one decode benchmark
// per encoding.
func BenchmarkDecode(b *testing.B) {
	dir := filepath.Join("testdata", "fixtures", strings.TrimSpace(mustRead(b, ".vortex-version")))
	paths, err := filepath.Glob(filepath.Join(dir, "*.vortex"))
	if err != nil || len(paths) == 0 {
		b.Skip("no fixtures")
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".vortex")
		b.Run(name, func(b *testing.B) { benchScan(b, openBench(b, p), vortex.Query{}) })
	}
}

// BenchmarkTPCH scans the TPC-H and ClickBench files of the reference, when .cache/fixtures/
// has them.
func BenchmarkTPCH(b *testing.B) {
	dir := filepath.Join(".cache", "fixtures", strings.TrimSpace(mustRead(b, ".vortex-version")))
	paths, _ := filepath.Glob(filepath.Join(dir, "*.regular.vortex"))
	if len(paths) == 0 {
		b.Skip("no TPC-H or ClickBench file in .cache/fixtures/")
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".regular.vortex")
		b.Run(name, func(b *testing.B) { benchScan(b, openBench(b, p), vortex.Query{}) })
	}
}

func mustRead(b *testing.B, path string) string {
	b.Helper()
	v, err := os.ReadFile(path)
	if err != nil {
		b.Skip(err)
	}
	return string(v)
}
