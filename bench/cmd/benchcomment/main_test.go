package main

import (
	"bytes"
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/comment.md")

// testdata/benchstat.csv is the output of benchstat -format csv on two packages, with a
// regression, an improvement, a benchmark only in the PR and the throughput metrics.
func TestGolden(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"-in", "testdata/benchstat.csv", "-head", "0123456789abcdef", "-base", "fedcba9876543210",
		"-repo", "https://github.com/o/r", "-run", "https://github.com/o/r/actions/runs/1"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile("testdata/comment.md", out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/comment.md")
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("comment differs from testdata/comment.md (go test -update to rewrite):\n%s", out.String())
	}
	got := out.String()
	for _, s := range []string{
		Marker + "\n",
		"[`0123456`](https://github.com/o/r/commit/0123456789abcdef)",
		"| `WriteRows` | sec/op | 1.10s ±3% | 1.30s ±4% | 🔴 +18.66% |",
		"| `Decode/alp` | sec/op | 131µs ±4% | 97.7µs ±2% | 🟢 -25.52% |",
		"| `NewOne` | — | 884ns ±1% | new |",
		"| internal/encode: sec/op |",
		"82.0MB/s",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q", s)
		}
	}
	// The throughputs mirror sec/op: they are not listed again as changes.
	if strings.Contains(got, "| `Decode/alp` | rows/s |") {
		t.Error("rows/s listed among the changes")
	}
}

func TestNoBase(t *testing.T) {
	csv := "cpu: X\n,PR,\n,sec/op,CI\nOpen-2,6e-05,5%\ngeomean,6e-05,\n"
	r, err := Parse(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	got := Render(r, &Meta{Head: "abc", BaseRef: "main"})
	for _, s := range []string{"PR at `abc`.", "`main` gave no result", "| `Open` | — | 60.0µs ±5% | new |"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in\n%s", s, got)
		}
	}
	if strings.Contains(got, "| Metric |") {
		t.Error("summary without a base")
	}
}

func TestNoChange(t *testing.T) {
	csv := ",main,,PR,,,\n,sec/op,CI,sec/op,CI,vs base,P\nOpen-2,6e-05,5%,6e-05,5%,~,p=1.000 n=6\nGone-2,1,0%,,\ngeomean,6e-05,,6e-05,,+0.00%,\n"
	r, err := Parse(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	got := Render(r, &Meta{BaseRef: "main"})
	for _, s := range []string{"No significant change.", "| `Gone` | 1.00s | — | gone |"} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in\n%s", s, got)
		}
	}
}

func TestTooLong(t *testing.T) {
	var b strings.Builder
	b.WriteString(",main,,PR,,,\n,sec/op,CI,sec/op,CI,vs base,P\n")
	for range 2000 {
		b.WriteString("SomeRatherLongBenchmarkName/with/parts-2,1,1%,1,1%,~,p=1.000 n=6\n")
	}
	r, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	got := Render(r, &Meta{BaseRef: "main"})
	if len(got) > maxBody || !strings.Contains(got, "does not fit in a comment") {
		t.Errorf("%d bytes", len(got))
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "goos: linux\n", "Open-2,1,1%\n", "\"unterminated\n"} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

func TestRunErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"-nope"}, {"-in", "testdata/missing.csv"}, {"-in", "main_test.go"}} {
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

func TestHuman(t *testing.T) {
	for _, c := range []struct {
		unit string
		v    float64
		want string
	}{
		{"sec/op", 5e-10, "0.50ns"},
		{"sec/op", 1.5e-6, "1.50µs"},
		{"sec/op", 42, "42.0s"},
		{"sec/op", 0.9997, "1.00s"},
		{"sec/op", 0.0999, "99.9ms"},
		{"sec/op", 3600, "3600s"},
		{"B/op", 1536, "1.50KiB"},
		{"B/op", 0, "0B"},
		{"B/op", 1023, "1023B"},
		{"decoded-B/s", 8.2e7, "82.0MB/s"},
		{"allocs/op", 12000, "12.0k"},
		{"allocs/op", 120, "120"},
		{"rows/s", 1.234e10, "12.3G"},
	} {
		if got := human(c.unit, c.v); got != c.want {
			t.Errorf("human(%q, %g) = %q, want %q", c.unit, c.v, got, c.want)
		}
	}
}
