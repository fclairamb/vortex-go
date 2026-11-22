package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Result is what one harness measured for a case: the median of its runs.
type Result struct {
	NsPerOp  float64 `json:"ns_per_op"`
	RowsPerS float64 `json:"rows_per_s,omitempty"`
	MBPerS   float64 `json:"mb_per_s,omitempty"`
}

// Case is one case of bench/cases.json at one thread count, with the result of each harness.
//
// Go and Rust read the same Vortex file, in batches. GoMapped is Go again on the file mapped in
// memory (OpenMapped): it is absent for a case that writes. GoRows, GoRowsMapped and ParquetGo
// are the harnesses of bench/, which read the same rows into the same structs, vortex-go from
// a Vortex file, read and mapped, and parquet-go from a Parquet file: they are absent for the
// cases bench/ does not run. A ratio is our time over the time of the other one, and it is
// absent when one of the two is.
type Case struct { //nolint:govet // the order of the fields is the order of bench.json
	Name                   string   `json:"name"`
	Threads                int      `json:"threads"`
	Go                     Result   `json:"go"`
	GoMapped               *Result  `json:"go_mapped,omitempty"`
	Rust                   *Result  `json:"rust,omitempty"`
	GoRows                 *Result  `json:"go_rows,omitempty"`
	GoRowsMapped           *Result  `json:"go_rows_mapped,omitempty"`
	ParquetGo              *Result  `json:"parquet_go,omitempty"`
	RatioToRust            *float64 `json:"ratio_to_rust,omitempty"`
	RatioToRustMapped      *float64 `json:"ratio_to_rust_mapped,omitempty"`
	RatioToParquetGo       *float64 `json:"ratio_to_parquet_go,omitempty"`
	RatioToParquetGoMapped *float64 `json:"ratio_to_parquet_go_mapped,omitempty"`
}

// Machine is what the benchmarks ran on.
type Machine struct {
	OS    string `json:"os"`
	CPU   string `json:"cpu"`
	Cores int    `json:"cores"`
}

// Report is bench.json: one line of history.jsonl.
type Report struct {
	Commit        string  `json:"commit"`
	Date          string  `json:"date"`
	VortexVersion string  `json:"vortex_version"`
	GoVersion     string  `json:"go_version"`
	RustVersion   string  `json:"rust_version"`
	Machine       Machine `json:"machine"`
	Cases         []Case  `json:"cases"`
}

// key identifies the samples of one case at one thread count.
type key struct {
	name    string
	threads int
}

// samples are the runs of every case of one source.
type samples map[key][]Result

// casePrefix is the name of the benchmark of the three Go harnesses: the sub-benchmarks are
// the case names.
const casePrefix = "BenchmarkCases/"

// parseGo reads the output of `go test -bench`: the lines of BenchmarkCases, whose name is the
// case, followed by -<GOMAXPROCS> when it is not 1. With a harness ("parquet-go", for the
// output of bench/), only the lines of that sub-benchmark are kept and the segment is dropped.
// The goos and cpu header lines are returned too.
func parseGo(r io.Reader, harness string) (s samples, header map[string]string, err error) {
	s, header = samples{}, map[string]string{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if k, v, ok := strings.Cut(line, ": "); ok && (k == "goos" || k == "cpu") {
			header[k] = v
			continue
		}
		if !strings.HasPrefix(line, casePrefix) {
			continue
		}
		name, threads, res, err := parseLine(line)
		if err != nil {
			return nil, nil, fmt.Errorf("line %d: %w", n, err)
		}
		if harness != "" {
			if !strings.HasSuffix(name, "/"+harness) {
				continue // the other harness of the same benchmark
			}
			name = strings.TrimSuffix(name, "/"+harness)
		}
		k := key{name, threads}
		s[k] = append(s[k], res)
	}
	return s, header, sc.Err()
}

// parseLine reads a BenchmarkCases line: the name of the case, its thread count and its metrics.
func parseLine(line string) (string, int, Result, error) {
	var res Result
	f := strings.Fields(line)
	if len(f) < 4 || len(f)%2 != 0 {
		return "", 0, res, fmt.Errorf("malformed benchmark line %q", line)
	}
	name, threads := splitName(strings.TrimPrefix(f[0], casePrefix))
	for i := 2; i+1 < len(f); i += 2 {
		v, err := strconv.ParseFloat(f[i], 64)
		if err != nil {
			return "", 0, res, fmt.Errorf("bad value %q in %q", f[i], line)
		}
		switch f[i+1] {
		case "ns/op":
			res.NsPerOp = v
		case "rows/s":
			res.RowsPerS = v
		case "decoded-MB/s":
			res.MBPerS = v
		}
	}
	if res.NsPerOp == 0 {
		return "", 0, res, fmt.Errorf("no ns/op in %q", line)
	}
	return name, threads, res, nil
}

// splitName splits "gen16/scan_full-4" into the case name and the thread count (1 without a
// suffix). The suffix of the last segment only: a case name can hold a dash.
func splitName(s string) (string, int) {
	last := s[strings.LastIndex(s, "/")+1:]
	i := strings.LastIndex(last, "-")
	if i < 0 {
		return s, 1
	}
	n, err := strconv.Atoi(last[i+1:])
	if err != nil {
		return s, 1 // a dash that is not a GOMAXPROCS suffix
	}
	return s[:len(s)-len(last)+i], n
}

// parseRust reads the lines of the Rust harness.
func parseRust(r io.Reader) (samples, error) {
	s := samples{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var l struct {
			Case     string  `json:"case"`
			Threads  int     `json:"threads"`
			NsPerOp  float64 `json:"ns_per_op"`
			RowsPerS float64 `json:"rows_per_s"`
			MBPerS   float64 `json:"mb_per_s"`
		}
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			return nil, fmt.Errorf("rust line %d: %w", n, err)
		}
		if l.Case == "" || l.Threads < 1 || l.NsPerOp <= 0 {
			return nil, fmt.Errorf("rust line %d: incomplete line %q", n, line)
		}
		k := key{l.Case, l.Threads}
		s[k] = append(s[k], Result{l.NsPerOp, l.RowsPerS, l.MBPerS})
	}
	return s, sc.Err()
}

// median is the median of the runs of each metric: the middle one, the mean of the two
// middle ones for an even count.
func median(runs []Result) Result {
	pick := func(get func(Result) float64) float64 {
		v := make([]float64, len(runs))
		for i, r := range runs {
			v[i] = get(r)
		}
		slices.Sort(v)
		m := len(v) / 2
		if len(v)%2 == 1 {
			return v[m]
		}
		return (v[m-1] + v[m]) / 2
	}
	return Result{
		NsPerOp:  pick(func(r Result) float64 { return r.NsPerOp }),
		RowsPerS: pick(func(r Result) float64 { return r.RowsPerS }),
		MBPerS:   pick(func(r Result) float64 { return r.MBPerS }),
	}
}

// Inputs are the sources of a report. Rust and ParquetGo may be nil. ParquetGo is the output
// of the benchmarks of bench/, with its harnesses: vortex-go, vortex-go-mapped and parquet-go.
type Inputs struct {
	CaseNames []string
	Go        io.Reader
	ParquetGo io.Reader
	Rust      io.Reader
	Meta      Report // everything but the machine and the cases
}

// optMedian is the median of the runs of a harness, nil when it has none.
func optMedian(runs []Result) *Result {
	if len(runs) == 0 {
		return nil
	}
	m := median(runs)
	return &m
}

// ratio is the time of a over the time of b, nil when one of them is absent.
func ratio(a, b *Result) *float64 {
	if a == nil || b == nil {
		return nil
	}
	r := a.NsPerOp / b.NsPerOp
	return &r
}

// parseAll reads the output of one run of go test -bench once for each of its harnesses,
// the sub-benchmarks of that name of a case ("" is the case itself).
func parseAll(r io.Reader, harnesses ...string) ([]samples, map[string]string, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, nil, err
	}
	out := make([]samples, len(harnesses))
	var header map[string]string
	for i, h := range harnesses {
		if out[i], header, err = parseGo(bytes.NewReader(raw), h); err != nil {
			return nil, nil, err
		}
	}
	return out, header, nil
}

// benchOutput is the samples of the harnesses of bench/: parquet-go, and vortex-go on a file
// it reads and on a file mapped in memory.
type benchOutput struct{ pq, rows, rowsMapped samples }

// parseBench reads the output of the benchmarks of bench/. An output with no parquet-go line
// is an error: the benchmarks did not run, and a report without them would not say so.
func parseBench(r io.Reader) (benchOutput, error) {
	s, _, err := parseAll(r, "parquet-go", "vortex-go", "vortex-go-mapped")
	if err != nil {
		return benchOutput{}, err
	}
	if len(s[0]) == 0 {
		return benchOutput{}, errors.New("no parquet-go result")
	}
	return benchOutput{pq: s[0], rows: s[1], rowsMapped: s[2]}, nil
}

// Build merges the sources by case name and thread count. A case of the list with no Go
// result is an error, one with no Rust result has no rust field.
func Build(in *Inputs) (Report, error) {
	if in.Go == nil {
		return Report{}, errors.New("no Go benchmark output")
	}
	goOut, header, err := parseAll(in.Go, "", "mapped")
	if err != nil {
		return Report{}, fmt.Errorf("go output: %w", err)
	}
	goRes, goMapped := goOut[0], goOut[1]
	var bo benchOutput
	var rs samples
	if in.ParquetGo != nil {
		if bo, err = parseBench(in.ParquetGo); err != nil {
			return Report{}, fmt.Errorf("parquet-go output: %w", err)
		}
	}
	if in.Rust != nil {
		if rs, err = parseRust(in.Rust); err != nil {
			return Report{}, err
		}
	}
	rep := in.Meta
	rep.Machine.OS, rep.Machine.CPU = header["goos"], header["cpu"]
	for _, name := range in.CaseNames {
		var threads []int
		for k := range goRes {
			if k.name == name {
				threads = append(threads, k.threads)
			}
		}
		if len(threads) == 0 {
			return Report{}, fmt.Errorf("case %s has no Go result", name)
		}
		slices.Sort(threads)
		for _, t := range threads {
			k := key{name, t}
			c := Case{
				Name: name, Threads: t, Go: median(goRes[k]), GoMapped: optMedian(goMapped[k]),
				Rust: optMedian(rs[k]), ParquetGo: optMedian(bo.pq[k]),
				GoRows: optMedian(bo.rows[k]), GoRowsMapped: optMedian(bo.rowsMapped[k]),
			}
			c.RatioToRust, c.RatioToRustMapped = ratio(&c.Go, c.Rust), ratio(c.GoMapped, c.Rust)
			c.RatioToParquetGo = ratio(c.GoRows, c.ParquetGo)
			c.RatioToParquetGoMapped = ratio(c.GoRowsMapped, c.ParquetGo)
			rep.Cases = append(rep.Cases, c)
		}
	}
	return rep, nil
}
