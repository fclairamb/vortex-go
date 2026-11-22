package bench

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/parquet-go/parquet-go"

	vortex "github.com/fclairamb/vortex-go"
)

// The comparison with parquet-go, on the same data: the generated file of the benchmarks of
// the root module (`make bench-parquet` makes it), read once and written as Parquet into
// .cache/bench/. The cases are the ones of the root benchmarks that a Parquet library has too:
// the full scan, 3 columns out of 16, a filter that keeps 1%, and writing rows. parquet-go
// has no row-level filter, so that case reads the three columns and filters in Go.

// vRow is a row with the Vortex tags.
type vRow struct {
	ID        int64            `vortex:"id"`
	TS        int64            `vortex:"ts"`
	User      int32            `vortex:"user"`
	Qty       int16            `vortex:"qty"`
	Small     uint8            `vortex:"small"`
	Big       uint64           `vortex:"big"`
	Price     float64          `vortex:"price"`
	Score     float32          `vortex:"score"`
	Gauss     float64          `vortex:"gauss"`
	Flag      bool             `vortex:"flag"`
	Flag2     bool             `vortex:"flag2"`
	City      string           `vortex:"city"`
	Name      string           `vortex:"name"`
	Note      string           `vortex:"note"`
	Maybe     sql.Null[int32]  `vortex:"maybe"`
	MaybeText sql.Null[string] `vortex:"maybe_text"`
}

// pRow is the same row with the Parquet tags.
type pRow struct {
	ID        int64   `parquet:"id"`
	TS        int64   `parquet:"ts"`
	User      int32   `parquet:"user"`
	Qty       int16   `parquet:"qty"`
	Small     uint8   `parquet:"small"`
	Big       uint64  `parquet:"big"`
	Price     float64 `parquet:"price"`
	Score     float32 `parquet:"score"`
	Gauss     float64 `parquet:"gauss"`
	Flag      bool    `parquet:"flag"`
	Flag2     bool    `parquet:"flag2"`
	City      string  `parquet:"city"`
	Name      string  `parquet:"name"`
	Note      string  `parquet:"note"`
	Maybe     *int32  `parquet:"maybe,optional"`
	MaybeText *string `parquet:"maybe_text,optional"`
}

// p3 and v3 are three columns out of 16.
type p3 struct {
	ID    int64   `parquet:"id"`
	Price float64 `parquet:"price"`
	City  string  `parquet:"city"`
}

type v3 struct {
	ID    int64   `vortex:"id"`
	Price float64 `vortex:"price"`
	City  string  `vortex:"city"`
}

// pFilter is the columns of the filter case.
type pFilter struct {
	ID    int64   `parquet:"id"`
	User  int32   `parquet:"user"`
	Price float64 `parquet:"price"`
}

type vFilter struct {
	ID    int64   `vortex:"id"`
	User  int32   `vortex:"user"`
	Price float64 `vortex:"price"`
}

func toParquet(v vRow) pRow {
	p := pRow{v.ID, v.TS, v.User, v.Qty, v.Small, v.Big, v.Price, v.Score, v.Gauss, v.Flag, v.Flag2,
		v.City, v.Name, v.Note, nil, nil}
	if v.Maybe.Valid {
		p.Maybe = &v.Maybe.V
	}
	if v.MaybeText.Valid {
		p.MaybeText = &v.MaybeText.V
	}
	return p
}

// data returns the paths of the Vortex file and of its Parquet copy, and skips without data.
func data(b *testing.B) (vpath, ppath string) {
	b.Helper()
	matches, _ := filepath.Glob(filepath.Join("..", ".cache", "bench", "gen16-*.vortex"))
	if len(matches) == 0 {
		b.Skip("no data: run make bench first, it writes .cache/bench/gen16-<rows>.vortex")
	}
	vpath = matches[0]
	for _, m := range matches {
		if a, b2 := stat(m), stat(vpath); a > b2 {
			vpath = m
		}
	}
	ppath = vpath[:len(vpath)-len(".vortex")] + ".parquet"
	if _, err := os.Stat(ppath); err == nil {
		return vpath, ppath
	}
	if err := writeParquet(vpath, ppath); err != nil {
		b.Fatal(err)
	}
	return vpath, ppath
}

// stat is the size of a file, 0 when it cannot be read.
func stat(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func writeParquet(vpath, ppath string) error {
	f, err := vortex.OpenFile(vpath)
	if err != nil {
		return err
	}
	defer f.Close()
	out, err := os.Create(ppath + ".tmp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	w := parquet.NewGenericWriter[pRow](out)
	buf := make([]pRow, 0, 8192)
	flush := func() error {
		_, err := w.Write(buf)
		buf = buf[:0]
		return err
	}
	for r, err := range vortex.Rows[vRow](context.Background(), f, vortex.Query{}) {
		if err != nil {
			return err
		}
		if buf = append(buf, toParquet(r)); len(buf) == cap(buf) {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), ppath)
}

func report(b *testing.B, rows int64) {
	b.ReportMetric(float64(rows)*float64(b.N)/b.Elapsed().Seconds(), "rows/s")
}

// readParquet reads every row of the file into T, in batches, and calls use on each.
func readParquet[T any](b *testing.B, path string, keep func(*T)) int64 {
	b.Helper()
	fh, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer fh.Close()
	r := parquet.NewGenericReader[T](fh)
	defer r.Close()
	buf := make([]T, 4096)
	var n int64
	for {
		k, err := r.Read(buf)
		for i := range k {
			keep(&buf[i])
		}
		n += int64(k)
		if err == io.EOF {
			return n
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

func readVortex[T any](b *testing.B, open vOpen, path string, q vortex.Query, keep func(*T)) int64 {
	b.Helper()
	f, err := open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	// keep gets the address of a slot reused for every row, as readParquet passes &buf[i]: the
	// address of the loop variable would make it escape, one allocation per row that the
	// harness, not vortex-go, would cost.
	var slot T
	var n int64
	for r, err := range vortex.Rows[T](context.Background(), f, q) {
		if err != nil {
			b.Fatal(err)
		}
		slot = r
		keep(&slot)
		n++
	}
	return n
}

// vOpen opens a Vortex file: OpenFile, or OpenMapped for the mapped harness.
type vOpen func(path string) (*vortex.File, error)

// vortexHarnesses are the two ways vortex-go reads the file of a case: with reads, and mapped
// in memory. parquet-go has one, it reads.
var vortexHarnesses = []struct {
	open vOpen
	name string
}{{vortex.OpenFile, "vortex-go"}, {vortex.OpenMapped, "vortex-go-mapped"}}

func benchFullScan(b *testing.B) {
	vpath, ppath := data(b)
	sink := 0
	for _, h := range vortexHarnesses {
		b.Run(h.name, func(b *testing.B) {
			b.ReportAllocs()
			var n int64
			for b.Loop() {
				n = readVortex(b, h.open, vpath, vortex.Query{}, func(r *vRow) { sink += int(r.ID) })
			}
			report(b, n)
		})
	}
	b.Run("parquet-go", func(b *testing.B) {
		b.ReportAllocs()
		var n int64
		for b.Loop() {
			n = readParquet(b, ppath, func(r *pRow) { sink += int(r.ID) })
		}
		report(b, n)
	})
	_ = sink
}

func bench3of16(b *testing.B) {
	vpath, ppath := data(b)
	sink := 0
	for _, h := range vortexHarnesses {
		b.Run(h.name, func(b *testing.B) {
			b.ReportAllocs()
			var n int64
			for b.Loop() {
				n = readVortex(b, h.open, vpath, vortex.Query{}, func(r *v3) { sink += int(r.ID) })
			}
			report(b, n)
		})
	}
	b.Run("parquet-go", func(b *testing.B) {
		b.ReportAllocs()
		var n int64
		for b.Loop() {
			n = readParquet(b, ppath, func(r *p3) { sink += int(r.ID) })
		}
		report(b, n)
	})
	_ = sink
}

// benchFilter keeps 1% of the rows (user < 1000 of 100000). Vortex filters in the reader,
// parquet-go reads the three columns of every row and filters in Go.
func benchFilter(b *testing.B) {
	vpath, ppath := data(b)
	sink := 0
	for _, h := range vortexHarnesses {
		b.Run(h.name, func(b *testing.B) {
			b.ReportAllocs()
			q := vortex.Query{Filter: vortex.Col("user").Lt(1000)}
			var n int64
			for b.Loop() {
				n = readVortex(b, h.open, vpath, q, func(r *vFilter) { sink += int(r.ID) })
			}
			report(b, n)
		})
	}
	b.Run("parquet-go", func(b *testing.B) {
		b.ReportAllocs()
		var n int64
		for b.Loop() {
			n = readParquet(b, ppath, func(r *pFilter) {
				if r.User < 1000 {
					sink += int(r.ID)
				}
			})
		}
		report(b, n)
	})
	_ = sink
}

// BenchmarkCases runs the cases of bench/cases.json that parquet-go has too, under the names of
// that list: BenchmarkCases/<case>/vortex-go, /vortex-go-mapped and /parquet-go. The
// benchjson tool merges them with the results of the root module and of the Rust harness.
func BenchmarkCases(b *testing.B) {
	b.Run("gen16/scan_full", benchFullScan)
	b.Run("gen16/scan_3col", bench3of16)
	b.Run("gen16/scan_filter", benchFilter)
}

// BenchmarkWriteRows writes 1M rows of the three columns id, price and city.
func BenchmarkWriteRows(b *testing.B) {
	const n = 1 << 20
	cities := []string{"paris", "lyon", "nantes", "lille", "nice", "brest", "metz", "tours"}
	pr, vr := make([]p3, n), make([]v3, n)
	for i := range n {
		pr[i] = p3{int64(i), float64(i%100000) / 100, cities[i%len(cities)]}
		vr[i] = v3{pr[i].ID, pr[i].Price, pr[i].City}
	}
	b.Run("vortex-go", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			w := vortex.NewRowWriter[v3](io.Discard, vortex.WriteOptions{})
			if _, err := w.Write(vr); err != nil {
				b.Fatal(err)
			}
			if err := w.Close(); err != nil {
				b.Fatal(err)
			}
		}
		report(b, n)
	})
	b.Run("parquet-go", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			w := parquet.NewGenericWriter[p3](io.Discard)
			if _, err := w.Write(pr); err != nil {
				b.Fatal(err)
			}
			if err := w.Close(); err != nil {
				b.Fatal(err)
			}
		}
		report(b, n)
	})
}

// BenchmarkFileSize logs the size of the two files, for docs/benchmarks.md. It skips without data.
func BenchmarkFileSize(b *testing.B) {
	vpath, ppath := data(b)
	for _, p := range []string{vpath, ppath} {
		st, err := os.Stat(p)
		if err != nil {
			b.Fatal(err)
		}
		b.Log(filepath.Base(p), strconv.FormatInt(st.Size(), 10), "bytes")
	}
}
