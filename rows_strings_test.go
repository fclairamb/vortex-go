package vortex

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
)

// strColumn is a Utf8 column over data, with the ranges of its rows, and the nulls of valid
// (nil: no null).
func strColumn(data []byte, starts, ends []int, valid []bool) *Column {
	a := &array.Array{Type: aUtf8, Len: len(starts), Data: data, Starts: starts, Ends: ends}
	if valid != nil {
		bits := make([]byte, (len(valid)+7)/8)
		for i, v := range valid {
			if v {
				bits[i/8] |= 1 << (i % 8)
			}
		}
		a.Validity = &array.Bitmap{Bytes: bits}
	}
	return &Column{arr: a, typ: Type{kind: Utf8}}
}

// TestBatchStrings reads the strings of a batch in the ways a block is made, and checks that
// none of them shares the memory of the column, which the next batch reuses.
func TestBatchStrings(t *testing.T) {
	cases := []struct {
		name         string
		data         string
		starts, ends []int
		valid        []bool
		want         []string
	}{
		{"end to end", "parislyonnice", []int{0, 5, 9}, []int{5, 9, 13}, nil, []string{"paris", "lyon", "nice"}},
		{"dictionary", "parislyon", []int{5, 0, 5, 5}, []int{9, 5, 9, 9}, nil, []string{"lyon", "paris", "lyon", "lyon"}},
		{"constant", "metz", []int{0, 0, 0}, []int{4, 4, 4}, nil, []string{"metz", "metz", "metz"}},
		{"scattered", "aaaa......................bbbb", []int{0, 26}, []int{4, 30}, nil, []string{"aaaa", "bbbb"}},
		{"empty", "", []int{0, 0}, []int{0, 0}, nil, []string{"", ""}},
		// The range of a null row is not read: here it is out of the data.
		{"nulls end to end", "parisnice", []int{0, 99, 5}, []int{5, 120, 9}, []bool{true, false, true}, []string{"paris", "", "nice"}},
		{"nulls scattered", "aaaa......................bbbb", []int{0, 99, 26}, []int{4, 120, 30}, []bool{true, false, true}, []string{"aaaa", "", "bbbb"}},
		{"all null", "", []int{7}, []int{9}, []bool{false}, []string{""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := []byte(c.data)
			get := batchStrings(strColumn(data, c.starts, c.ends, c.valid))
			got := make([]string, len(c.want))
			for i := range got {
				if c.valid == nil || c.valid[i] {
					got[i] = get(i)
				}
			}
			for i := range data {
				data[i] = 'X' // the scan reuses its memory: the strings must not see it
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestBatchStringsBound checks that the block of a batch is no larger than its strings, when
// they are a few bytes far apart in a large column.
func TestBatchStringsBound(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1<<20)
	col := strColumn(data, []int{0, 1 << 19, 1<<20 - 4}, []int{4, 1<<19 + 4, 1 << 20}, nil)
	var get func(int) string
	res := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			get = batchStrings(col)
		}
	})
	if per := res.AllocedBytesPerOp(); per > 1024 {
		t.Fatalf("%d bytes for 12 bytes of strings in a column of 1 MiB", per)
	}
	if get(1) != "xxxx" {
		t.Fatal(get(1))
	}
}

// TestRowsStringsOutliveTheirBatch keeps every string of a scan of many batches, and checks
// them after the loop: a string never points into memory that the next batch reuses. The scan
// allocates per batch, not per row: a copy per string would be 2.7 allocations per row.
func TestRowsStringsOutliveTheirBatch(t *testing.T) {
	type row struct {
		City  string           `vortex:"city"`
		Name  string           `vortex:"name"`
		Maybe sql.Null[string] `vortex:"maybe"`
	}
	const n = 30000
	cities := []string{"paris", "lyon", "nantes", "lille"}
	rows := make([]row, n)
	for i := range rows {
		rows[i] = row{City: cities[i%len(cities)], Name: fmt.Sprintf("name-%d", i*7919%n)}
		if i%3 != 0 {
			rows[i].Maybe = sql.Null[string]{V: fmt.Sprintf("m%d", i), Valid: true}
		}
	}
	raw := writeRowsOf(t, rows, WriteOptions{}, n)
	path := filepath.Join(t.TempDir(), "strings.vortex")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// Mapped, the column of a batch may be the memory of the file: the strings must not be.
	for name, open := range map[string]func() (*File, error){
		"reader": func() (*File, error) { return Open(bytes.NewReader(raw), int64(len(raw))) },
		"mapped": func() (*File, error) { return OpenMapped(path) },
	} {
		f, err := open()
		if err != nil {
			t.Fatal(err)
		}
		got := make([]row, 0, n)
		read := func() {
			got = got[:0]
			for r, err := range Rows[row](context.Background(), f, Query{}) {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, r)
			}
		}
		read()
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, rows) {
			t.Fatalf("%s: the rows read are not the rows written", name)
		}
		f, err = open()
		if err != nil {
			t.Fatal(err)
		}
		if allocs := testing.AllocsPerRun(3, read); allocs > n/20 {
			t.Fatalf("%s: %d rows, %v allocations", name, n, allocs)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
