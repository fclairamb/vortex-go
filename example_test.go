package vortex_test

import (
	"context"
	"database/sql"
	"fmt"
	"log"

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
