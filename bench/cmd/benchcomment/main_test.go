package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sample is the output of benchstat -format csv on two packages, with a regression, an
// improvement, a change under the threshold, a benchmark only in the PR and the throughputs.
const sample = `goos: linux
goarch: amd64
pkg: github.com/fclairamb/vortex-go
cpu: AMD EPYC 9V45 96-Core Processor                
,main,,PR,,,
,sec/op,CI,sec/op,CI,vs base,P
Open-2,5.9831500000000005e-05,3%,6.0286500000000006e-05,2%,+0.76%,p=0.041 n=6
Decode/alp-2,0.000131108,4%,9.76525e-05,2%,-25.52%,p=0.002 n=6
WriteRows-2,1.096530982,3%,1.3011054954999999,4%,+18.66%,p=0.002 n=6
NewOne-2,,,8.845000000000001e-07,1%
geomean,0.0020489278397476957,,0.0002868986115213623,,-3.79%,

,main,,PR,,,
,B/op,CI,B/op,CI,vs base,P
Open-2,115500,0%,115500,0%,~,p=1.000 n=6
Decode/alp-2,2048,0%,2048,0%,~,p=1.000 n=6
WriteRows-2,3e+08,0%,3.3e+08,0%,+10.00%,p=0.002 n=6
NewOne-2,,,64,0%
geomean,414010.22168979264,,47277.16898871341,,+3.23%,

,main,,PR,,,
,allocs/op,CI,allocs/op,CI,vs base,P
Open-2,120,0%,120,0%,~,p=1.000 n=6
Decode/alp-2,12,0%,12,0%,~,p=1.000 n=6
WriteRows-2,12000,0%,12000,0%,~,p=1.000 n=6
NewOne-2,,,1,0%
geomean,258.532162803826,,64.47419590941254,,+0.00%,

,main,,PR,,,
,decoded-B/s,CI,decoded-B/s,CI,vs base,P
Decode/alp-2,6.1e+07,3%,8.2e+07,2%,+34.43%,p=0.002 n=6
geomean,6.0999999999999925e+07,,8.199999999999991e+07,,+34.43%,

,main,,PR,,,
,rows/s,CI,rows/s,CI,vs base,P
Decode/alp-2,7.6278675335e+09,4%,1.02403872005e+10,2%,+34.25%,p=0.002 n=6
geomean,7.627867533500001e+09,,1.0240387200500017e+10,,+34.25%,

,main,,PR,,,
,file-bytes,CI,file-bytes,CI,vs base,P
WriteRows-2,5.2e+07,0%,5.1e+07,0%,-1.92%,p=0.002 n=6
geomean,5.199999999999996e+07,,5.1e+07,,-1.92%,

pkg: github.com/fclairamb/vortex-go/internal/encode
,main,,PR,,,
,sec/op,CI,sec/op,CI,vs base,P
Encode/bitpacked-2,4.4895e-05,3%,4.5675e-05,3%,~,p=0.589 n=6
geomean,4.489500000000001e-05,,4.5675000000000026e-05,,+1.74%,

,main,,PR,,,
,B/op,CI,B/op,CI,vs base,P
Encode/bitpacked-2,0,0%,0,0%,~,p=1.000 n=6
geomean,,,,,+0.00%,

,main,,PR,,,
,allocs/op,CI,allocs/op,CI,vs base,P
Encode/bitpacked-2,0,0%,0,0%,~,p=1.000 n=6
geomean,,,,,+0.00%,

,main,,PR,,,
,values/s,CI,values/s,CI,vs base,P
Encode/bitpacked-2,2e+10,0%,2e+10,0%,~,p=1.000 n=6
geomean,1.9999999999999973e+10,,1.9999999999999973e+10,,+0.00%,
`

func TestRender(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bench.csv")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"-in", path, "-head", "0123456789abcdef", "-base", "fedcba9876543210",
		"-repo", "https://github.com/o/r", "-run", "https://github.com/o/r/actions/runs/1"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, s := range []string{
		Marker + "\n",
		"[`0123456`](https://github.com/o/r/commit/0123456789abcdef)",
		"Changes under 5% are counted as noise.",
		"| sec/op | -3.79% | **1** | **1** | 2 |",
		"| file-bytes | -1.92% | 0 | 0 | 1 |",
		"#### Better (1)",
		"#### Worse (2)",
		"| `WriteRows` | sec/op | 1.10s ±3% | 1.30s ±4% | 🔴 +18.66% |",
		"| `Decode/alp` | sec/op | 131µs ±4% | 97.7µs ±2% | 🟢 -25.52% |",
		"| `Open` | 59.8µs ±3% | 60.3µs ±2% | +0.76% | p=0.041 n=6 |",
		"| `NewOne` | — | 884ns ±1% | new |",
		"| internal/encode: sec/op |",
		"| `Encode/bitpacked` | 44.9µs ±3% | 45.7µs ±3% | ~ |",
		"82.0MB/s",
		"</details>\n",
	} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in\n%s", s, got)
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
	for _, args := range [][]string{{}, {"-nope"}, {"-in", "missing.csv"}, {"-in", "main_test.go"}} {
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
