// Command benchjson merges the three benchmark harnesses of vortex-go into bench.json: the Go
// benchmarks of the root module (go test -bench), the parquet-go ones of bench/ and the lines
// of tools/bench-rust, matched by case name (bench/cases.json). The median of the runs is kept.
//
//	benchjson -cases bench/cases.json -go go.txt -parquet parquet.txt -rust rust.jsonl \
//	    -commit abc123 -vortex-version 0.93.0 > bench.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "benchjson:", err)
		os.Exit(1)
	}
}

func openOpt(path string) (io.ReadCloser, error) {
	if path == "" {
		return nil, nil
	}
	return os.Open(path)
}

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("benchjson", flag.ContinueOnError)
	var (
		casesPath = fs.String("cases", "bench/cases.json", "the list of cases")
		goPath    = fs.String("go", "", "output of go test -bench, root module (required)")
		pqPath    = fs.String("parquet", "", "output of go test -bench, bench/ module")
		rustPath  = fs.String("rust", "", "lines of tools/bench-rust")
		meta      Report
	)
	fs.StringVar(&meta.Commit, "commit", "", "commit that was measured")
	fs.StringVar(&meta.Date, "date", time.Now().UTC().Format(time.RFC3339), "date of the run")
	fs.StringVar(&meta.VortexVersion, "vortex-version", "", "the pinned upstream release")
	fs.StringVar(&meta.GoVersion, "go-version", runtime.Version(), "Go version")
	fs.StringVar(&meta.RustVersion, "rust-version", "", "rustc version")
	cores := fs.Int("cores", runtime.NumCPU(), "number of cores")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *goPath == "" {
		return fmt.Errorf("-go is required")
	}
	raw, err := os.ReadFile(*casesPath)
	if err != nil {
		return err
	}
	var list struct {
		Cases []struct {
			Name string `json:"name"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("%s: %w", *casesPath, err)
	}
	in := Inputs{Meta: meta}
	for _, c := range list.Cases {
		in.CaseNames = append(in.CaseNames, c.Name)
	}
	for _, src := range []struct {
		dst  *io.Reader
		path string
	}{{&in.Go, *goPath}, {&in.ParquetGo, *pqPath}, {&in.Rust, *rustPath}} {
		f, err := openOpt(src.path)
		if err != nil {
			return err
		}
		if f != nil {
			defer f.Close()
			*src.dst = f
		}
	}
	rep, err := Build(&in)
	if err != nil {
		return err
	}
	rep.Machine.Cores = *cores
	enc := json.NewEncoder(out)
	return enc.Encode(rep)
}
