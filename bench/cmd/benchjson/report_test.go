package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func sample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func caseOf(t *testing.T, r Report, name string, threads int) Case {
	t.Helper()
	for _, c := range r.Cases {
		if c.Name == name && c.Threads == threads {
			return c
		}
	}
	t.Fatalf("no case %s with %d threads in %+v", name, threads, r.Cases)
	return Case{}
}

func ptr[T any](v T) *T { return &v }

func inputs(t *testing.T) Inputs {
	return Inputs{
		CaseNames: []string{"gen16/scan_full", "gen16/open", "gen16/write_none"},
		Go:        strings.NewReader(sample(t, "go.txt")),
		ParquetGo: strings.NewReader(sample(t, "parquet.txt")),
		Rust:      strings.NewReader(sample(t, "rust.jsonl")),
		Meta:      Report{Commit: "abc", VortexVersion: "0.93.0"},
	}
}

func TestBuildMergesTheThreeSources(t *testing.T) {
	rep, err := Build(ptr(inputs(t)))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Commit != "abc" || rep.Machine.CPU != "Acme Cpu 9000" || rep.Machine.OS != "linux" {
		t.Errorf("meta: %+v", rep)
	}
	one := caseOf(t, rep, "gen16/scan_full", 1)
	// The median of 5 runs, not the mean (2,600) and not the fastest (1,000).
	if one.Go.NsPerOp != 3000 || one.Go.RowsPerS != 6.5e6 || one.Go.MBPerS != 700.5 {
		t.Errorf("go: %+v", one.Go)
	}
	if one.Rust == nil || one.Rust.NsPerOp != 300 || *one.RatioToRust != 10 {
		t.Errorf("rust: %+v ratio %v", one.Rust, one.RatioToRust)
	}
	if one.ParquetGo == nil || one.ParquetGo.NsPerOp != 3000 {
		t.Errorf("parquet-go: %+v", one.ParquetGo)
	}
	four := caseOf(t, rep, "gen16/scan_full", 4)
	if four.Go.NsPerOp != 12000 || four.Rust.NsPerOp != 1200 {
		t.Errorf("4 threads: %+v", four)
	}
	if four.ParquetGo != nil {
		t.Errorf("parquet-go has no 4-thread line: %+v", four.ParquetGo)
	}
}

func TestBuildCaseWithoutRust(t *testing.T) {
	rep, err := Build(ptr(inputs(t)))
	if err != nil {
		t.Fatal(err)
	}
	open := caseOf(t, rep, "gen16/open", 1)
	if open.Rust != nil || open.RatioToRust != nil || open.Go.NsPerOp != 11 {
		t.Errorf("open: %+v", open)
	}
	raw, err := json.Marshal(open)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "rust") || strings.Contains(string(raw), "ratio") {
		t.Errorf("the rust fields must be absent: %s", raw)
	}
}

func TestBuildWithoutRustOrParquet(t *testing.T) {
	in := inputs(t)
	in.Rust, in.ParquetGo = nil, nil
	rep, err := Build(&in)
	if err != nil {
		t.Fatal(err)
	}
	if c := caseOf(t, rep, "gen16/scan_full", 1); c.Rust != nil || c.ParquetGo != nil {
		t.Errorf("%+v", c)
	}
}

func TestBuildCaseWithoutGoResult(t *testing.T) {
	in := inputs(t)
	in.CaseNames = append(in.CaseNames, "gen16/scan_range")
	if _, err := Build(&in); err == nil || !strings.Contains(err.Error(), "gen16/scan_range has no Go result") {
		t.Fatalf("got %v", err)
	}
	in = inputs(t)
	in.Go = nil
	if _, err := Build(&in); err == nil {
		t.Fatal("no Go output must fail")
	}
}

func TestBuildMalformedLines(t *testing.T) {
	for name, line := range map[string]string{
		"too few fields": "BenchmarkCases/gen16/open-4  5",
		"odd fields":     "BenchmarkCases/gen16/open-4  5 100 ns/op 3",
		"bad value":      "BenchmarkCases/gen16/open-4  5 fast ns/op",
		"no ns/op":       "BenchmarkCases/gen16/open-4  5 100 B/op",
	} {
		in := inputs(t)
		in.Go = strings.NewReader(sample(t, "go.txt") + line + "\n")
		if _, err := Build(&in); err == nil || !strings.Contains(err.Error(), "line") {
			t.Errorf("%s: got %v", name, err)
		}
		in = inputs(t)
		in.ParquetGo = strings.NewReader(line + "\n")
		if _, err := Build(&in); err == nil {
			t.Errorf("%s (parquet-go): no error", name)
		}
	}
	for name, line := range map[string]string{
		"not JSON":   "{not json",
		"no case":    `{"threads":1,"ns_per_op":5}`,
		"no threads": `{"case":"a","ns_per_op":5}`,
		"no time":    `{"case":"a","threads":1}`,
	} {
		in := inputs(t)
		in.Rust = strings.NewReader(line + "\n")
		if _, err := Build(&in); err == nil || !strings.Contains(err.Error(), "rust line 1") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestMedianEvenCount(t *testing.T) {
	m := median([]Result{{NsPerOp: 1}, {NsPerOp: 9}, {NsPerOp: 3}, {NsPerOp: 5}})
	if m.NsPerOp != 4 {
		t.Errorf("median of 1,3,5,9 = %v, want 4", m.NsPerOp)
	}
}

func TestSplitName(t *testing.T) {
	for in, want := range map[string]struct {
		name    string
		threads int
	}{
		"gen16/open":                   {"gen16/open", 1},
		"gen16/open-4":                 {"gen16/open", 4},
		"gen16/scan_full/parquet-go":   {"gen16/scan_full/parquet-go", 1},
		"gen16/scan_full/parquet-go-8": {"gen16/scan_full/parquet-go", 8},
	} {
		name, threads := splitName(in)
		if name != want.name || threads != want.threads {
			t.Errorf("%s: %s %d", in, name, threads)
		}
	}
}

func TestRun(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{
		"-cases", "testdata/cases.json", "-go", "testdata/go.txt", "-parquet", "testdata/parquet.txt",
		"-rust", "testdata/rust.jsonl", "-commit", "abc", "-vortex-version", "0.93.0",
		"-rust-version", "rustc 1.100.0", "-cores", "4",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Commit != "abc" || rep.Machine.Cores != 4 || rep.RustVersion != "rustc 1.100.0" || len(rep.Cases) != 6 {
		t.Errorf("%+v", rep)
	}
	if bytes.Count(out.Bytes(), []byte("\n")) != 1 {
		t.Error("bench.json must be one line, it is appended to history.jsonl")
	}
}

func TestRunErrors(t *testing.T) {
	var out bytes.Buffer
	for name, args := range map[string][]string{
		"no -go":        {"-cases", "testdata/cases.json"},
		"unknown flag":  {"-nope"},
		"no cases file": {"-cases", "testdata/missing.json", "-go", "testdata/go.txt"},
		"bad cases":     {"-cases", "testdata/go.txt", "-go", "testdata/go.txt"},
		"no go file":    {"-cases", "testdata/cases.json", "-go", "testdata/missing.txt"},
	} {
		if err := run(args, &out); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
