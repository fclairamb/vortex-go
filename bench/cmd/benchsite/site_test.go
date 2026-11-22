package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const page = "<!-- bench:begin -->\nold\n<!-- bench:end -->\n<!-- history:begin -->\nold\n<!-- history:end -->\n<!-- size:begin -->\nold\n<!-- size:end -->\n"

const benchJSON = `{"commit":"a1d8357cdae3fbe2e35b755c5efd7529d377a149","date":"2026-11-20T10:56:13Z","vortex_version":"0.93.0","go_version":"go1.27.4","rust_version":"rustc 1.100.0","machine":{"os":"linux","cpu":"Acme CPU","cores":4},"cases":[
{"name":"a/fast","threads":1,"go":{"ns_per_op":500000},"rust":{"ns_per_op":1000000,"rows_per_s":1},"ratio_to_rust":0.5},
{"name":"b/slow","threads":4,"go":{"ns_per_op":3000000000},"rust":{"ns_per_op":1000000000},"go_rows":{"ns_per_op":400000000},"parquet_go":{"ns_per_op":2000000000},"ratio_to_rust":3,"ratio_to_parquet_go":0.2},
{"name":"c/norust","threads":1,"go":{"ns_per_op":900}}]}`

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustReport(t *testing.T, s string) *report {
	t.Helper()
	p := write(t, t.TempDir(), "bench.json", s)
	r, err := loadReport(p)
	if err != nil || r == nil {
		t.Fatalf("%v, %v", r, err)
	}
	return r
}

func TestSpeedThreeCases(t *testing.T) {
	out, err := renderSpeed(mustReport(t, benchJSON))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, `<tr data-testid="speed-row-`); n != 3 {
		t.Errorf("%d rows, want 3", n)
	}
	if n := strings.Count(out, `<g data-testid="speed-bars-`); n != 3 {
		t.Errorf("%d groups of bars, want 3", n)
	}
	for _, want := range []string{`data-testid="speed-table"`, `data-testid="speed-chart"`, "Acme CPU, linux, 4 cores", "<code>a1d8357</code>", "2026-11-20T10:56:13Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing", want)
		}
	}
	// The slower ratio is marked, the faster one is not, and the case we lose is first.
	if !strings.Contains(out, `<td class="num slower">3.00x slower</td>`) {
		t.Error("a ratio above 1 is not marked as slower")
	}
	if !strings.Contains(out, `<td class="num faster">2.00x faster</td>`) {
		t.Error("a ratio below 1 is not marked as faster")
	}
	if i, j := strings.Index(out, "b/slow"), strings.Index(out, "a/fast"); i < 0 || j < 0 || i > j {
		t.Error("the case where we lose is not first")
	}
	// The case with no Rust number: a dash for Rust and the ratio, and 1 bar.
	row := out[strings.Index(out, "c/norust"):]
	row = row[:strings.Index(row, "</tr>")]
	if n := strings.Count(row, dash); n != 2 {
		t.Errorf("%d dashes in %q, want 2", n, row)
	}
	if !strings.Contains(out, "b/slow, 4 threads") || !strings.Contains(out, "c/norust, 1 thread<") {
		t.Error("labels of the bars")
	}
}

// TestSpeedParquet is the table of the ratios to parquet-go: one line per case that both
// libraries read, with the two times and our time over the parquet-go one.
func TestSpeedParquet(t *testing.T) {
	out, err := renderSpeed(mustReport(t, benchJSON))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, `<tr data-testid="parquet-row-`); n != 1 {
		t.Fatalf("%d rows, want 1", n)
	}
	row := out[strings.Index(out, `data-testid="parquet-row-0"`):]
	row = row[:strings.Index(row, "</tr>")]
	for _, want := range []string{"b/slow", `<td class="num">4</td>`, "400.0 ms", "2.00 s", `<td class="num faster">5.00x faster</td>`} {
		if !strings.Contains(row, want) {
			t.Errorf("%q is missing in %q", want, row)
		}
	}
	// A report with no case that parquet-go read says so, and has no table.
	none := strings.ReplaceAll(benchJSON, `"parquet_go"`, `"other"`)
	out, err = renderSpeed(mustReport(t, none))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `data-testid="parquet-empty"`) || strings.Contains(out, `data-testid="parquet-table"`) {
		t.Error("no parquet-go number: the table must be replaced by a sentence")
	}
}

func TestSpeedEmpty(t *testing.T) {
	for _, r := range []*report{nil, {}} {
		out, err := renderSpeed(r)
		if err != nil || !strings.Contains(out, "no measurement yet") {
			t.Errorf("%q, %v", out, err)
		}
	}
}

func TestHTMLIsEscaped(t *testing.T) {
	r := mustReport(t, `{"machine":{"cpu":"<script>x</script>"},"cases":[{"name":"a&b","threads":1,"go":{"ns_per_op":1}}]}`)
	out, _ := renderSpeed(r)
	if strings.Contains(out, "<script>") || !strings.Contains(out, "a&amp;b") {
		t.Errorf("not escaped: %s", out)
	}
}

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	line := strings.ReplaceAll(benchJSON, "\n", "")
	other := strings.Replace(line, "a1d8357", "b2e9468", 1)
	h, err := loadHistory(write(t, dir, "history.jsonl", line+"\n\n"+other+"\n"))
	if err != nil || len(h) != 2 {
		t.Fatalf("%d lines, %v", len(h), err)
	}
	out, err := renderHistory(h)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-testid="history-chart"`, "<polyline", "2 run(s)", "1 thread: 0.50x at b2e9468", "all cores: 3.00x at b2e9468"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from %s", want, out)
		}
	}
	// One run is a dot, not a line with nowhere to go.
	one, err := renderHistory(h[:1])
	if err != nil || !strings.Contains(one, "<circle") {
		t.Errorf("%q, %v", one, err)
	}
	for _, none := range [][]report{nil, {{Cases: []benchCase{{Name: "x", Threads: 1}}}}} {
		out, err := renderHistory(none)
		if err != nil || !strings.Contains(out, "no measurement yet") {
			t.Errorf("%q, %v", out, err)
		}
	}
}

func TestHistoryDamagedLine(t *testing.T) {
	line := strings.ReplaceAll(benchJSON, "\n", "")
	p := write(t, t.TempDir(), "history.jsonl", line+"\n"+line+"\n{not json\n"+line+"\n")
	_, err := loadHistory(p)
	if err == nil || !strings.Contains(err.Error(), "history.jsonl:3:") {
		t.Fatalf("error %v does not name line 3", err)
	}
}

func TestMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if r, err := loadReport(filepath.Join(dir, "nope")); r != nil || err != nil {
		t.Errorf("%v, %v", r, err)
	}
	if h, err := loadHistory(filepath.Join(dir, "nope")); h != nil || err != nil {
		t.Errorf("%v, %v", h, err)
	}
	if s, err := loadSizes(filepath.Join(dir, "nope")); s != nil || err != nil {
		t.Errorf("%v, %v", s, err)
	}
	if _, err := loadReport(write(t, dir, "bad.json", "{")); err == nil {
		t.Error("a damaged bench.json: no error")
	}
	if _, err := loadHistory(dir); err == nil {
		t.Error("a history that is a directory: no error")
	}
}

func TestSizes(t *testing.T) {
	rows, err := loadSizes(write(t, t.TempDir(), "s.json", `{"b.regular":1.05,"a.regular":0.9,"a.compact":1.2}`))
	if err != nil || len(rows) != 2 || rows[0].Name != "a" {
		t.Fatalf("%v, %v", rows, err)
	}
	out, err := renderSizes(rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-testid="size-table"`, `<td class="num faster">0.900</td>`, `<td class="num slower">1.200</td>`, `<td class="num slower">1.050</td><td class="num ">` + dash} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing from %s", want, out)
		}
	}
	if out, _ := renderSizes(nil); !strings.Contains(out, "no measurement yet") {
		t.Error("no sizes: not empty")
	}
	for _, bad := range []string{`{"x":1}`, `{"x.fast":1}`, `[`} {
		if _, err := loadSizes(write(t, t.TempDir(), "s.json", bad)); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestFill(t *testing.T) {
	got, err := fill("a<!-- x:begin -->old<!-- x:end -->b", "x", "new")
	if err != nil || got != "a<!-- x:begin -->\nnew\n<!-- x:end -->b" {
		t.Errorf("%q, %v", got, err)
	}
	for _, bad := range []string{"", "<!-- x:begin -->"} {
		if _, err := fill(bad, "x", ""); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "rust.html", page)
	args := []string{"-page", p, "-bench", filepath.Join(dir, "none.json"), "-history", filepath.Join(dir, "none.jsonl"), "-sizes", filepath.Join(dir, "none-sizes.json")}
	// No data: every section says so, and the build does not fail.
	if err := run(args); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if n := strings.Count(string(b), "no measurement yet"); n != 3 {
		t.Errorf("%d sections with no measurement, want 3", n)
	}
	// With data.
	write(t, dir, "bench.json", benchJSON)
	write(t, dir, "history.jsonl", strings.ReplaceAll(benchJSON, "\n", "")+"\n")
	write(t, dir, "sizes.json", `{"x.regular":1}`)
	p = write(t, dir, "rust.html", page)
	err := run([]string{"-page", p, "-bench", filepath.Join(dir, "bench.json"), "-history", filepath.Join(dir, "history.jsonl"), "-sizes", filepath.Join(dir, "sizes.json")})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), "no measurement yet") || !strings.Contains(string(b), "speed-table") {
		t.Errorf("not filled: %s", b)
	}
	// Errors: a missing page, a page with no markers, a damaged history, a bad flag.
	if run([]string{"-page", filepath.Join(dir, "missing.html")}) == nil {
		t.Error("a missing page: no error")
	}
	if run([]string{"-page", write(t, dir, "plain.html", "x"), "-bench", filepath.Join(dir, "none")}) == nil {
		t.Error("a page with no marker: no error")
	}
	write(t, dir, "bad.jsonl", "oops\n")
	if err := run([]string{"-page", p, "-history", filepath.Join(dir, "bad.jsonl"), "-bench", filepath.Join(dir, "bench.json"), "-sizes", filepath.Join(dir, "sizes.json")}); err == nil || !strings.Contains(err.Error(), "bad.jsonl:1") {
		t.Errorf("damaged history: %v", err)
	}
	if run([]string{"-nope"}) == nil {
		t.Error("a bad flag: no error")
	}
}
