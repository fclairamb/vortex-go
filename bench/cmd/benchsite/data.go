package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"sort"
	"strings"
)

// result is what one harness measured for a case.
type result struct {
	NsPerOp float64 `json:"ns_per_op"`
}

// benchCase is one case at one thread count. Rust and ParquetGo are absent when that harness
// has no result for it.
type benchCase struct {
	Name        string   `json:"name"`
	Threads     int      `json:"threads"`
	Go          result   `json:"go"`
	Rust        *result  `json:"rust"`
	ParquetGo   *result  `json:"parquet_go"`
	RatioToRust *float64 `json:"ratio_to_rust"`
}

type machine struct {
	OS    string `json:"os"`
	CPU   string `json:"cpu"`
	Cores int    `json:"cores"`
}

// report is bench.json, and one line of history.jsonl.
type report struct {
	Commit        string      `json:"commit"`
	Date          string      `json:"date"`
	VortexVersion string      `json:"vortex_version"`
	GoVersion     string      `json:"go_version"`
	RustVersion   string      `json:"rust_version"`
	Machine       machine     `json:"machine"`
	Cases         []benchCase `json:"cases"`
}

// readOpt reads a file, and reports a file that does not exist as nil.
func readOpt(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

// loadReport reads bench.json. No file, or an empty one, is no report.
func loadReport(path string) (*report, error) {
	b, err := readOpt(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return nil, err
	}
	var r report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

// loadHistory reads history.jsonl, one report per line, in the order of the file. A line that is
// not a report is an error that names it.
func loadHistory(path string) ([]report, error) {
	b, err := readOpt(path)
	if err != nil {
		return nil, err
	}
	var out []report
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(nil, len(b)+1)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r report
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// sizeRow is the size ratios of one dataset, by mode. A mode that was not measured is NaN.
type sizeRow struct {
	Name             string
	Default, Compact float64
}

// loadSizes reads size-ratios.json: keys such as "tpch_lineitem.regular" and
// "tpch_lineitem.compact", our size over the reference's.
func loadSizes(path string) ([]sizeRow, error) {
	b, err := readOpt(path)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return nil, err
	}
	var m map[string]float64
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	rows := map[string]*sizeRow{}
	for k, v := range m {
		name, mode, ok := strings.Cut(k, ".")
		if !ok || (mode != "regular" && mode != "compact") {
			return nil, fmt.Errorf("%s: key %q is not <dataset>.regular or <dataset>.compact", path, k)
		}
		r := rows[name]
		if r == nil {
			r = &sizeRow{Name: name, Default: math.NaN(), Compact: math.NaN()}
			rows[name] = r
		}
		if mode == "regular" {
			r.Default = v
		} else {
			r.Compact = v
		}
	}
	out := make([]sizeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// fill returns page with what is between the markers of the block name replaced.
func fill(page, name, content string) (string, error) {
	begin, end := "<!-- "+name+":begin -->", "<!-- "+name+":end -->"
	before, rest, ok := strings.Cut(page, begin)
	if !ok {
		return "", fmt.Errorf("no %s marker", begin)
	}
	_, after, ok := strings.Cut(rest, end)
	if !ok {
		return "", fmt.Errorf("no %s marker", end)
	}
	return before + begin + "\n" + content + "\n" + end + after, nil
}
