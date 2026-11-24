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

// Read columns, batch by batch, without structs. Values copies a numeric column into a slice.
func ExampleFile_Scan() {
	f, err := vortex.OpenFile("testdata/fixtures/0.93.0/extra/default_ints.vortex")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	for b, err := range f.Scan(context.Background(), vortex.Query{Rows: vortex.Range{End: 4}}, "sorted") {
		if err != nil {
			log.Fatal(err)
		}
		v, err := vortex.Values[int64](b.Col(0))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(b.Len(), v)
	}
	// Output: 4 [1000 1001 1002 1003]
}

// Write a file that Vortex 0.83 and older can open: the edition is the oldest one.
func ExampleWriteOptions_edition() {
	s := vortex.NewSchema(vortex.Field{Name: "id", Type: vortex.Int64Type()})
	var buf bytes.Buffer
	w := vortex.NewWriter(&buf, s, vortex.WriteOptions{Edition: "core 2025.05.0"})
	if err := w.Write(vortex.Of([]int64{1, 2, 3})); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	f, err := vortex.Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(f.NumRows(), f.Schema())

	// A type the edition does not have is an error.
	u := vortex.NewSchema(vortex.Field{Name: "id", Type: vortex.UUIDType()})
	err = vortex.NewWriter(&buf, u, vortex.WriteOptions{Edition: "core 2025.05.0"}).Close()
	fmt.Println(err)
	// Output:
	// 3 struct{id: i64}
	// vortex: NewWriter: field "id": a uuid is not in the edition "core 2025.05.0" (dtype vortex.uuid is newer)
}

// Write columns: a schema, then one Write per chunk with a Data per column.
func ExampleNewWriter() {
	s := vortex.NewSchema(
		vortex.Field{Name: "id", Type: vortex.Int64Type()},
		vortex.Field{Name: "name", Type: vortex.Utf8Type()},
	)
	var buf bytes.Buffer
	w := vortex.NewWriter(&buf, s, vortex.WriteOptions{})
	if err := w.Write(vortex.Of([]int64{1, 2, 3}), vortex.OfStrings([]string{"a", "b", "c"})); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}
	f, err := vortex.Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(f.NumRows(), f.Schema())
	// Output: 3 struct{id: i64, name: utf8}
}

// Store values that are not rows in the file: a version known up front, an index known once
// the rows are written. The reader gets them back by their key.
func ExampleFile_Metadata() {
	type Row struct {
		Query int64  `vortex:"query"`
		Data  string `vortex:"data"`
	}
	var buf bytes.Buffer
	w := vortex.NewRowWriter[Row](&buf, vortex.WriteOptions{
		Metadata: map[string][]byte{"schema.version": []byte("3")},
	})
	if _, err := w.Write([]Row{{1, "a"}, {1, "b"}, {2, "c"}}); err != nil {
		log.Fatal(err)
	}
	if err := w.SetMetadata("query_offsets", []byte(`{"1":0,"2":2}`)); err != nil {
		log.Fatal(err)
	}
	if err := w.Close(); err != nil {
		log.Fatal(err)
	}

	f, err := vortex.Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		log.Fatal(err)
	}
	for _, key := range f.MetadataKeys() {
		v, _, err := f.Metadata(key)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s: %s\n", key, v)
	}
	// Output:
	// query_offsets: {"1":0,"2":2}
	// schema.version: 3
}
