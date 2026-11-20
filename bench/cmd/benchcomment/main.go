// Command benchcomment turns the CSV output of benchstat (two inputs labeled main and PR) into
// the markdown of the benchmark comment of a pull request: a summary per metric, the significant
// changes, and every result in a folded section. The first line is a marker, so the workflow can
// find the comment again and edit it instead of adding a new one.
//
//	benchstat -format csv main=old.txt PR=new.txt > bench.csv
//	benchcomment -in bench.csv -head <sha> -base <sha> -repo <url> -run <url> [-threshold 5] > comment.md
package main

import (
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Marker is the first line of the comment. The Post step of .github/workflows/build.yml looks
// for it.
const Marker = "<!-- vortex-go:bench -->"

// maxBody stays under the 65536 characters GitHub accepts for a comment.
const maxBody = 60000

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "benchcomment:", err)
		os.Exit(1)
	}
}

// Meta is what the comment says about the run, and the noise threshold.
type Meta struct {
	Head, Base, BaseRef, Repo, Run string
	// Threshold, in percent: a significant change smaller than this counts as unchanged. A
	// shared runner moves by a few percent from one run to the next.
	Threshold float64
}

// counts says whether a row is a change: significant for benchstat and above the threshold.
func (m *Meta) counts(row *Row) bool { return row.Significant && math.Abs(row.Delta) >= m.Threshold }

func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("benchcomment", flag.ContinueOnError)
	var m Meta
	in := fs.String("in", "", "CSV output of benchstat (required)")
	fs.StringVar(&m.Head, "head", "", "commit of the PR")
	fs.StringVar(&m.Base, "base", "", "commit of the base branch")
	fs.StringVar(&m.BaseRef, "base-ref", "main", "name of the base branch")
	fs.StringVar(&m.Repo, "repo", "", "URL of the repository, for the commit links")
	fs.StringVar(&m.Run, "run", "", "URL of the workflow run")
	fs.Float64Var(&m.Threshold, "threshold", 5, "smallest change that counts, in percent")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("-in is required")
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := Parse(f)
	if err != nil {
		return err
	}
	_, err = io.WriteString(out, Render(r, &m))
	return err
}

// Row is one benchmark of one table.
type Row struct {
	Name           string
	OldCI, NewCI   string
	P              string
	Old, New       float64
	Delta          float64 // percent, meaningful when Significant
	HasOld, HasNew bool
	Significant    bool
}

// Table is one metric of one package.
type Table struct {
	Pkg, Unit string
	Geomean   string // the change of the geomean, "" without a base
	Rows      []Row
}

// Result is the whole benchstat output.
type Result struct {
	CPU     string
	Tables  []*Table
	Compare bool // both sides have results
}

// Parse reads benchstat -format csv: a few "key: value" lines, then one block per metric, made
// of a row of labels, a row of units and the benchmarks.
func Parse(r io.Reader) (*Result, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	res := &Result{}
	var (
		pkg    string
		labels []string // the label row of the current block
		t      *Table
		col    map[string]int
	)
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch {
		case len(rec) == 1:
			k, v, _ := strings.Cut(rec[0], ":")
			v = strings.TrimSpace(v)
			switch k {
			case "pkg":
				pkg = v
			case "cpu":
				res.CPU = v
			}
		case rec[0] == "" && labels == nil:
			labels = rec
		case rec[0] == "":
			t, col = header(labels, rec)
			t.Pkg = pkg
			res.Compare = res.Compare || (col["main"] > 0 && col["PR"] > 0)
			res.Tables = append(res.Tables, t)
			labels = nil
		case t == nil:
			return nil, fmt.Errorf("row %q before any header", rec[0])
		case rec[0] == "geomean":
			t.Geomean = field(rec, col["vs base"])
		default:
			t.Rows = append(t.Rows, row(rec, col))
		}
	}
	if len(res.Tables) == 0 {
		return nil, errors.New("no benchmark in the input")
	}
	return res, nil
}

// header maps "main", "PR", "vs base" and "P" to their column, 0 when absent.
func header(labels, units []string) (*Table, map[string]int) {
	col := map[string]int{}
	for i, l := range labels {
		if l == "main" || l == "PR" {
			col[l] = i
		}
	}
	t := &Table{}
	for i, u := range units {
		switch {
		case u == "vs base" || u == "P":
			col[u] = i
		case i > 0 && u != "CI" && t.Unit == "":
			t.Unit = u
		}
	}
	return t, col
}

func field(rec []string, i int) string {
	if i <= 0 || i >= len(rec) {
		return ""
	}
	return rec[i]
}

func row(rec []string, col map[string]int) Row {
	r := Row{Name: rec[0], P: field(rec, col["P"])}
	r.Old, r.HasOld = num(field(rec, col["main"]))
	r.New, r.HasNew = num(field(rec, col["PR"]))
	if r.HasOld {
		r.OldCI = field(rec, col["main"]+1)
	}
	if r.HasNew {
		r.NewCI = field(rec, col["PR"]+1)
	}
	if d := field(rec, col["vs base"]); strings.HasSuffix(d, "%") {
		v, err := strconv.ParseFloat(strings.TrimSuffix(d, "%"), 64)
		r.Delta, r.Significant = v, err == nil
	}
	return r
}

func num(s string) (float64, bool) {
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// higherIsBetter is true for the throughputs (rows/s, decoded-MB/s, values/s).
func higherIsBetter(unit string) bool { return strings.HasSuffix(unit, "/s") }

// better says whether a significant change is an improvement.
func better(unit string, delta float64) bool { return (delta > 0) == higherIsBetter(unit) }

var procs = regexp.MustCompile(`-\d+$`)

// pkgPrefix is the package of a table relative to the shortest one, the module root.
func pkgPrefix(r *Result) func(string) string {
	root := ""
	for _, t := range r.Tables {
		if root == "" || len(t.Pkg) < len(root) {
			root = t.Pkg
		}
	}
	return func(pkg string) string {
		if rel := strings.TrimPrefix(strings.TrimPrefix(pkg, root), "/"); rel != "" && rel != pkg {
			return rel + ": "
		}
		return ""
	}
}

// Render writes the comment.
func Render(r *Result, m *Meta) string {
	var b strings.Builder
	prefix := pkgPrefix(r)
	name := func(t *Table, row Row) string { return prefix(t.Pkg) + procs.ReplaceAllString(row.Name, "") }

	fmt.Fprintf(&b, "%s\n### Benchmarks against `%s`\n\n", Marker, m.BaseRef)
	fmt.Fprintf(&b, "PR at %s", commit(m, m.Head))
	if m.Base != "" {
		fmt.Fprintf(&b, ", `%s` at %s", m.BaseRef, commit(m, m.Base))
	}
	b.WriteString(".")
	if r.CPU != "" {
		fmt.Fprintf(&b, " Shared runner (%s), the numbers are for information.", r.CPU)
	}
	if m.Run != "" {
		fmt.Fprintf(&b, " [Run](%s).", m.Run)
	}
	if m.Threshold > 0 {
		fmt.Fprintf(&b, " Changes under %g%% are counted as noise.", m.Threshold)
	}
	b.WriteString("\n\n")

	if !r.Compare {
		fmt.Fprintf(&b, "> [!WARNING]\n> `%s` gave no result, there is nothing to compare with.\n\n", m.BaseRef)
	} else {
		summary(&b, r, m, prefix)
		changes(&b, r, m, name)
	}

	all := details(r, m)
	if b.Len()+len(all) > maxBody {
		all = "_Every result is in the log of the run: it does not fit in a comment._\n"
	}
	b.WriteString(all)
	return b.String()
}

func commit(m *Meta, sha string) string {
	short := sha
	if len(short) > 7 {
		short = short[:7]
	}
	if m.Repo == "" || sha == "" {
		return "`" + short + "`"
	}
	return fmt.Sprintf("[`%s`](%s/commit/%s)", short, m.Repo, sha)
}

func summary(b *strings.Builder, r *Result, m *Meta, prefix func(string) string) {
	b.WriteString("| Metric | Geomean | Better | Worse | Unchanged |\n|---|---:|---:|---:|---:|\n")
	for _, t := range r.Tables {
		var up, down, same int
		for _, row := range t.Rows {
			switch {
			case !m.counts(&row):
				same++
			case better(t.Unit, row.Delta):
				up++
			default:
				down++
			}
		}
		geo := t.Geomean
		if v, err := strconv.ParseFloat(strings.TrimSuffix(geo, "%"), 64); err == nil && math.Abs(v) >= max(m.Threshold, 1) {
			geo = mark(t.Unit, v) + " " + geo
		}
		fmt.Fprintf(b, "| %s%s | %s | %s | %s | %d |\n", prefix(t.Pkg), t.Unit, geo, count(up), count(down), same)
	}
	b.WriteString("\n")
}

func count(n int) string {
	if n == 0 {
		return "0"
	}
	return fmt.Sprintf("**%d**", n)
}

func mark(unit string, delta float64) string {
	if better(unit, delta) {
		return "🟢"
	}
	return "🔴"
}

type change struct {
	t   *Table
	row Row
}

// changes lists the ones that count, the regressions first, the largest first. The throughputs
// mirror sec/op and are left out.
func changes(b *strings.Builder, r *Result, m *Meta, name func(*Table, Row) string) {
	var worse, good []change
	for _, t := range r.Tables {
		if higherIsBetter(t.Unit) {
			continue
		}
		for _, row := range t.Rows {
			switch {
			case !m.counts(&row):
			case better(t.Unit, row.Delta):
				good = append(good, change{t, row})
			default:
				worse = append(worse, change{t, row})
			}
		}
	}
	if len(worse)+len(good) == 0 {
		b.WriteString("No significant change.\n\n")
		return
	}
	for _, list := range [][]change{worse, good} {
		sort.SliceStable(list, func(i, j int) bool {
			return math.Abs(list[i].row.Delta) > math.Abs(list[j].row.Delta)
		})
	}
	section := func(title string, list []change) {
		if len(list) == 0 {
			return
		}
		fmt.Fprintf(b, "#### %s (%d)\n\n", title, len(list))
		b.WriteString("| Benchmark | Metric | main | PR | Change |\n|---|---|---:|---:|---:|\n")
		for _, c := range list {
			fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s %+.2f%% |\n", name(c.t, c.row), c.t.Unit,
				value(c.t.Unit, c.row.Old, c.row.HasOld, c.row.OldCI),
				value(c.t.Unit, c.row.New, c.row.HasNew, c.row.NewCI),
				mark(c.t.Unit, c.row.Delta), c.row.Delta)
		}
		b.WriteString("\n")
	}
	section("Worse", worse)
	section("Better", good)
}

func details(r *Result, m *Meta) string {
	var b strings.Builder
	prefix := pkgPrefix(r)
	b.WriteString("<details><summary>Every result</summary>\n\n")
	for _, t := range r.Tables {
		fmt.Fprintf(&b, "#### %s%s\n\n", prefix(t.Pkg), t.Unit)
		b.WriteString("| Benchmark | main | PR | Change | p |\n|---|---:|---:|---:|---|\n")
		for _, row := range t.Rows {
			d := "~"
			switch {
			case m.counts(&row):
				d = fmt.Sprintf("%s %+.2f%%", mark(t.Unit, row.Delta), row.Delta)
			case row.Significant:
				d = fmt.Sprintf("%+.2f%%", row.Delta)
			case !row.HasOld:
				d = "new"
			case !row.HasNew:
				d = "gone"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s |\n", procs.ReplaceAllString(row.Name, ""),
				value(t.Unit, row.Old, row.HasOld, row.OldCI), value(t.Unit, row.New, row.HasNew, row.NewCI), d, row.P)
		}
		b.WriteString("\n")
	}
	b.WriteString("</details>\n")
	return b.String()
}

// value is a number with its unit scaled and its confidence interval, "—" when absent.
func value(unit string, v float64, ok bool, ci string) string {
	if !ok {
		return "—"
	}
	s := human(unit, v)
	if ci != "" && ci != "0%" {
		s += " ±" + ci
	}
	return s
}

func human(unit string, v float64) string {
	switch {
	case unit == "sec/op":
		return scale(v, 1e3, "ns", "µs", "ms", "s")
	case strings.HasSuffix(unit, "B/s"):
		return scale(v, 1e3, "B/s", "kB/s", "MB/s", "GB/s", "TB/s")
	case unit == "B/op" || strings.HasSuffix(unit, "bytes"):
		return scale(v, 1024, "B", "KiB", "MiB", "GiB", "TiB")
	default:
		return scale(v, 1e3, "", "k", "M", "G", "T")
	}
}

// scale prints v, a multiple of suffixes[0] (seconds start at ns), with three significant
// digits in the largest suffix it reaches, each suffix step times the one before. A plain count
// keeps no decimal.
func scale(v, step float64, suffixes ...string) string {
	f := 1.0
	if suffixes[0] == "ns" {
		f = 1e-9
	}
	i := 0
	// 0.9995 so that 999.7ms is printed 1.00s, not 1000ms.
	for i < len(suffixes)-1 && math.Abs(v) >= f*step*0.9995 {
		f *= step
		i++
	}
	x := v / f
	switch {
	case math.Abs(x) >= 99.95 || (i == 0 && x == math.Trunc(x)):
		return strconv.FormatFloat(x, 'f', 0, 64) + suffixes[i]
	case math.Abs(x) >= 9.995:
		return strconv.FormatFloat(x, 'f', 1, 64) + suffixes[i]
	default:
		return strconv.FormatFloat(x, 'f', 2, 64) + suffixes[i]
	}
}
