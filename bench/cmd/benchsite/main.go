// Command benchsite fills the benchmark sections of the rust.html page of the website: the
// speed table and bars of bench.json, the history of the ratio to Rust of history.jsonl, and
// the file size ratios of testdata/size-ratios.json. The numbers are not committed in the page,
// they are filled when the site is built. A file that does not exist is no measurement yet,
// not an error.
//
//	benchsite -page _site/rust.html -bench bench.json -history history.jsonl \
//	    -sizes testdata/size-ratios.json
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

func run(args []string) error {
	fs := flag.NewFlagSet("benchsite", flag.ContinueOnError)
	page := fs.String("page", "_site/rust.html", "the page to fill, rewritten in place")
	benchPath := fs.String("bench", "bench.json", "the latest benchmark report")
	historyPath := fs.String("history", "history.jsonl", "one report per line")
	sizesPath := fs.String("sizes", "testdata/size-ratios.json", "the file size ratios")
	if err := fs.Parse(args); err != nil {
		return err
	}
	src, err := os.ReadFile(*page)
	if err != nil {
		return err
	}
	out, err := build(string(src), *benchPath, *historyPath, *sizesPath)
	if err != nil {
		return err
	}
	return os.WriteFile(*page, []byte(out), 0o644)
}

// build returns the page with its three sections filled.
func build(page, benchPath, historyPath, sizesPath string) (string, error) {
	report, err := loadReport(benchPath)
	if err != nil {
		return "", err
	}
	history, err := loadHistory(historyPath)
	if err != nil {
		return "", err
	}
	sizes, err := loadSizes(sizesPath)
	if err != nil {
		return "", err
	}
	for _, s := range []struct {
		name string
		html func() (string, error)
	}{
		{"bench", func() (string, error) { return renderSpeed(report) }},
		{"history", func() (string, error) { return renderHistory(history) }},
		{"size", func() (string, error) { return renderSizes(sizes) }},
	} {
		content, err := s.html()
		if err != nil {
			return "", err
		}
		if page, err = fill(page, s.name, content); err != nil {
			return "", err
		}
	}
	return page, nil
}
