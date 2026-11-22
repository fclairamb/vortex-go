package vortex_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	vortex "github.com/fclairamb/vortex-go"
)

// dbbatRow is a row of the archive of dbbat, the schema of specs/todos/...-compact-json-text-size:
// one row per captured result row, row_data being the JSON text of the row.
type dbbatRow struct {
	ConnectionUID [16]byte `vortex:"connection_uid"`
	QueryUID      [16]byte `vortex:"query_uid"`
	RowUID        [16]byte `vortex:"row_uid"`
	RowNumber     int32    `vortex:"row_number"`
	RowData       string   `vortex:"row_data"`
	RowSizeBytes  int64    `vortex:"row_size_bytes"`
}

// dbbatRows is the generator of the benchmark of dbbat: n rows in stmts runs of queries, with
// narrow or wide JSON. The source is seeded, so a size is reproducible; uuids are made from it
// too, like the SHA-1 uuids of the queries and the random uuids of the rows.
func dbbatRows(n, stmts int, wide bool) []dbbatRow {
	r := rand.New(rand.NewPCG(1, 2))
	var conn [16]byte
	binary.LittleEndian.PutUint64(conn[:], r.Uint64())
	binary.LittleEndian.PutUint64(conn[8:], r.Uint64())
	out := make([]dbbatRow, n)
	for i := range n {
		var m map[string]any
		if wide {
			m = map[string]any{"id": i, "email": fmt.Sprintf("user%d@example.com", r.IntN(50000)),
				"created_at": time.Unix(1.7e9+int64(r.IntN(1e7)), 0).UTC().Format(time.RFC3339),
				"status":     []string{"active", "pending", "closed"}[r.IntN(3)], "amount": r.Float64() * 1000,
				"note": fmt.Sprintf("order %d for customer %d", r.IntN(1e6), r.IntN(5000))}
		} else {
			m = map[string]any{"id": i, "name": fmt.Sprintf("user-%d", i%977), "ok": i%2 == 0}
		}
		b, _ := json.Marshal(m)
		q := conn
		q[0] ^= byte(i * stmts / n)
		q[1] ^= byte(i * stmts / n >> 8)
		var row [16]byte
		binary.LittleEndian.PutUint64(row[:], r.Uint64())
		binary.LittleEndian.PutUint64(row[8:], r.Uint64())
		out[i] = dbbatRow{conn, q, row, int32(i + 1), string(b), int64(len(b))}
	}
	return out
}

func writeDBBat[T any](t testing.TB, rows []T, mode vortex.Compression, opts vortex.WriteOptions) []byte {
	t.Helper()
	var buf bytes.Buffer
	opts.Compression = mode
	w := vortex.NewRowWriter[T](&buf, opts)
	if _, err := w.Write(rows); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// dbbatCases are the cases of the benchmark of dbbat: (rows, queries, wide JSON).
var dbbatCases = []struct {
	name     string
	n, stmts int
	wide     bool
}{
	{"3_rows", 3, 2, false},
	{"100k_narrow", 100_000, 40, false},
	{"100k_wide", 100_000, 40, true},
}

// TestDBBatRoundTrip reads back every row of the archive of dbbat, whatever the compression.
func TestDBBatRoundTrip(t *testing.T) {
	for _, c := range dbbatCases {
		rows := dbbatRows(c.n, c.stmts, c.wide)
		for _, mode := range []vortex.Compression{vortex.None, vortex.Default, vortex.Compact} {
			t.Run(fmt.Sprintf("%s/%d", c.name, mode), func(t *testing.T) {
				checkDBBatRows(t, rows, writeDBBat(t, rows, mode, vortex.WriteOptions{}))
			})
		}
	}
}

func checkDBBatRows[T comparable](t *testing.T, rows []T, data []byte) {
	t.Helper()
	f, err := vortex.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := 0
	for r, err := range vortex.Rows[T](context.Background(), f, vortex.Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		if got >= len(rows) || r != rows[got] {
			t.Fatalf("row %d: got %+v", got, r)
		}
		got++
	}
	if got != len(rows) {
		t.Fatalf("read %d rows, wrote %d", got, len(rows))
	}
}

// dbbatText is the row_data column alone.
type dbbatText struct {
	RowData string `vortex:"row_data"`
}

// TestDBBatSize holds the sizes of the archive of dbbat: under Compact the text of the wide case
// stays within 10% of plain zstd on the same chunks (the rows of a block at a time, each the
// bytes of its text then its length), the uuids of a file cost little, and Compact is not bigger
// than Default.
func TestDBBatSize(t *testing.T) {
	rows := dbbatRows(100_000, 40, true)
	text := make([]dbbatText, len(rows))
	for i, r := range rows {
		text[i] = dbbatText{r.RowData}
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		t.Fatal(err)
	}
	plain := 0
	const block = 8192
	for lo := 0; lo < len(rows); lo += block {
		var chunk []byte
		for _, r := range rows[lo:min(lo+block, len(rows))] {
			chunk = append(chunk, r.RowData...)
		}
		for _, r := range rows[lo:min(lo+block, len(rows))] {
			chunk = binary.LittleEndian.AppendUint32(chunk, uint32(len(r.RowData)))
		}
		plain += len(enc.EncodeAll(chunk, nil))
	}
	got := len(writeDBBat(t, text, vortex.Compact, vortex.WriteOptions{}))
	t.Logf("row_data: %d bytes, plain zstd %d bytes (%.1f%%)", got, plain, 100*float64(got)/float64(plain))
	if float64(got) > 1.10*float64(plain) {
		t.Errorf("row_data under Compact is %d bytes, over 110%% of plain zstd, %d", got, plain)
	}

	// 16 bytes a row of random uuid cannot shrink: the other columns are what is left
	whole := len(writeDBBat(t, rows, vortex.Compact, vortex.WriteOptions{}))
	other := whole - got - 16*len(rows)
	t.Logf("whole file: %d bytes, the other columns: %d", whole, other)
	if other > len(rows) { // under a byte a row for the constant, the runs, the numbers and the sizes
		t.Errorf("the columns but the text and the row uuid cost %d bytes for %d rows", other, len(rows))
	}
	if def := len(writeDBBat(t, rows, vortex.Default, vortex.WriteOptions{})); whole >= def {
		t.Errorf("Compact is %d bytes, Default %d", whole, def)
	}
}

// TestDBBatTiny: a file of three rows has no table of zones under Compact, and a filter on it
// still finds its rows.
func TestDBBatTiny(t *testing.T) {
	rows := dbbatRows(3, 2, false)
	compact := writeDBBat(t, rows, vortex.Compact, vortex.WriteOptions{})
	def := writeDBBat(t, rows, vortex.Default, vortex.WriteOptions{})
	if len(compact) >= len(def) {
		t.Errorf("Compact is %d bytes, Default %d", len(compact), len(def))
	}
	f, err := vortex.Open(bytes.NewReader(compact), int64(len(compact)))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got []int32
	q := vortex.Query{Filter: vortex.Col("row_number").Gt(int32(1))}
	for r, err := range vortex.Rows[dbbatRow](context.Background(), f, q) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, r.RowNumber)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("rows %v, want [2 3]", got)
	}
}

// dbbatPlain is a row of the archive of dbbat without a uuid, which an edition before
// "core 2026.08.3" holds.
type dbbatPlain struct {
	RowNumber    int32  `vortex:"row_number"`
	RowData      string `vortex:"row_data"`
	RowSizeBytes int64  `vortex:"row_size_bytes"`
}

// TestDBBatNoZstd: an edition that has no vortex.zstd writes and reads the text with no zstd.
func TestDBBatNoZstd(t *testing.T) {
	rows := make([]dbbatPlain, 20_000)
	for i, r := range dbbatRows(len(rows), 4, true) {
		rows[i] = dbbatPlain{r.RowNumber, r.RowData, r.RowSizeBytes}
	}
	data := writeDBBat(t, rows, vortex.Compact, vortex.WriteOptions{Edition: "core 2025.05.0"})
	if bytes.Contains(data, []byte("vortex.zstd")) {
		t.Error("the file of an edition with no zstd holds vortex.zstd")
	}
	checkDBBatRows(t, rows, data)
	if withZstd := writeDBBat(t, rows, vortex.Compact, vortex.WriteOptions{}); !bytes.Contains(withZstd, []byte("vortex.zstd")) {
		t.Error("the file of the newest edition has no vortex.zstd")
	}
}

// dbbatBenchFile is the file of the benchmarks of the archive of dbbat, the wide case of
// 100,000 rows written with Default, made on the first use.
func dbbatBenchFile(b *testing.B, path string) string {
	b.Helper()
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatal(err)
	}
	data := writeDBBat(b, dbbatRows(100_000, 40, true), vortex.Default, vortex.WriteOptions{})
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		b.Fatal(err)
	}
	return path
}
