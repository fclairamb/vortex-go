// Command benchsite fills the benchmark sections of the website: on rust.html, the speed table
// and bars of bench.json next to the Rust implementation, the history of the ratio to Rust of
// history.jsonl, and the file size ratios of testdata/size-ratios.json; on parquet.html, the
// table and bars of bench.json next to parquet-go. The numbers are not committed in the pages,
// they are filled when the site is built. A file that does not exist is no measurement yet,
// not an error.
//
//	benchsite -page _site/rust.html -parquet-page _site/parquet.html -bench bench.json \
//	    -history history.jsonl -sizes testdata/size-ratios.json
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "benchsite:", err)
		os.Exit(1)
	}
}

// section is a part of a page, between its two markers.
type section struct {
	html func() (string, error)
	name string
}

// rewrite fills the sections of the page at path, in place.
func rewrite(path string, sections []section) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	page := string(src)
	for _, s := range sections {
		content, err := s.html()
		if err != nil {
			return err
		}
		if page, err = fill(page, s.name, content); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return os.WriteFile(path, []byte(page), 0o644)
}

func run(args []string) error {
	fs := flag.NewFlagSet("benchsite", flag.ContinueOnError)
	page := fs.String("page", "_site/rust.html", "the page of the comparison with Rust, rewritten in place")
	parquetPage := fs.String("parquet-page", "", "the page of the comparison with parquet-go: none when empty")
	benchPath := fs.String("bench", "bench.json", "the latest benchmark report")
	historyPath := fs.String("history", "history.jsonl", "one report per line")
	sizesPath := fs.String("sizes", "testdata/size-ratios.json", "the file size ratios")
	if err := fs.Parse(args); err != nil {
		return err
	}
	report, err := loadReport(*benchPath)
	if err != nil {
		return err
	}
	history, err := loadHistory(*historyPath)
	if err != nil {
		return err
	}
	sizes, err := loadSizes(*sizesPath)
	if err != nil {
		return err
	}
	err = rewrite(*page, []section{
		{func() (string, error) { return renderCompare(report, vsRust) }, "bench"},
		{func() (string, error) { return renderHistory(history) }, "history"},
		{func() (string, error) { return renderSizes(sizes) }, "size"},
	})
	if err != nil || *parquetPage == "" {
		return err
	}
	return rewrite(*parquetPage, []section{
		{func() (string, error) { return renderCompare(report, vsParquet) }, "parquet"},
	})
}
