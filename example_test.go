package vortex_test

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	vortex "github.com/fclairamb/vortex-go"
)

// Read one column of the rows that meet a filter. The statistics of the file skip the zones
// that cannot hold a row.
func ExampleFile_Scan_filter() {
	f, err := vortex.OpenFile("testdata/fixtures/0.93.0/extra/default_ints.vortex")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	q := vortex.Query{Filter: vortex.Col("sorted").Lt(1003), Rows: vortex.Range{End: 10}}
	for b, err := range f.Scan(context.Background(), q, "sorted") {
		if err != nil {
			log.Fatal(err)
		}
		v, err := vortex.Values[int64](b.Col(0))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(v)
	}
	// Output: [1000 1001 1002]
}

// Read the rows of a file into structs. A nullable column goes into a sql.Null field.
func ExampleRows() {
	f, err := vortex.OpenFile("testdata/fixtures/0.93.0/extra/default_ints.vortex")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	type Row struct {
		Sorted sql.Null[int64] `vortex:"sorted"`
	}
	for r, err := range vortex.Rows[Row](context.Background(), f, vortex.Query{Rows: vortex.Range{End: 3}}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(r.Sorted.V)
	}
	// Output:
	// 1000
	// 1001
	// 1002
}

// Read the rows that meet a filter. The statistics of the file skip the zones that cannot hold
// a row, and no row is returned that does not match.
func ExampleRows_filter() {
	f, err := vortex.OpenFile("testdata/fixtures/0.93.0/extra/default_ints.vortex")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	type Row struct {
		Sorted sql.Null[int64] `vortex:"sorted"`
	}
	q := vortex.Query{Filter: vortex.Col("sorted").Lt(1003), Rows: vortex.Range{End: 10}}
	for r, err := range vortex.Rows[Row](context.Background(), f, q) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(r.Sorted.V)
	}
	// Output:
	// 1000
	// 1001
	// 1002
}

// Write a slice of structs to a file. The columns come from the fields, and the tags are the
// ones of Rows.
func ExampleNewRowWriter() {
	type Trade struct {
		ID     int64     `vortex:"id"`
		Symbol string    `vortex:"symbol"`
		Price  float64   `vortex:"price"`
		At     time.Time `vortex:"at"`
	}
	f, err := os.CreateTemp("", "trades-*.vortex")
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(f.Name())
	w := vortex.NewRowWriter[Trade](f, vortex.WriteOptions{})
	n, err := w.Write([]Trade{
		{1, "ACME", 10.5, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		{2, "ACME", 10.75, time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
	fmt.Println(n, "rows written")
	// Output: 2 rows written
}

// Write rows, then read them back with Rows. A nullable field is a sql.Null or a pointer, and
// the tag picks the logical type of a decimal.
func ExampleNewRowWriter_readBack() {
	type Row struct {
		Name  string           `vortex:"name"`
		Price vortex.Decimal   `vortex:"price,decimal(10,2)"`
		Note  sql.Null[string] `vortex:"note"`
		Tags  []string         `vortex:"tags"`
	}
	var buf bytes.Buffer
	w := vortex.NewRowWriter[Row](&buf, vortex.WriteOptions{})
	if _, err := w.Write([]Row{
		{"a", vortex.DecimalFromInt64(1050), sql.Null[string]{V: "first", Valid: true}, []string{"x", "y"}},
		{"b", vortex.DecimalFromInt64(299), sql.Null[string]{}, nil},
	}); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	f, err := vortex.Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		log.Fatal(err)
	}
	for r, err := range vortex.Rows[Row](context.Background(), f, vortex.Query{}) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(r.Name, r.Price.Text(2), r.Note.Valid, r.Tags)
	}
	// Output:
	// a 10.50 true [x y]
	// b 2.99 false []
}
