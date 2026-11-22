package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"math"
	"sort"
	"strconv"
	"strings"
)

var (
	//go:embed templates/compare.tmpl
	compareTemplate string
	//go:embed templates/history.tmpl
	historyTemplate string
	//go:embed templates/size.tmpl
	sizeTemplate string
)

const dash = "–"

const emptyHTML = `<p data-testid="%s-empty">no measurement yet</p>`

// fmtNs prints a duration in the unit that reads best.
func fmtNs(ns float64) string {
	switch {
	case ns < 1e3:
		return fmt.Sprintf("%.0f ns", ns)
	case ns < 1e6:
		return fmt.Sprintf("%.1f µs", ns/1e3)
	case ns < 1e9:
		return fmt.Sprintf("%.1f ms", ns/1e6)
	}
	return fmt.Sprintf("%.2f s", ns/1e9)
}

func optNs(r *result) string {
	if r == nil {
		return dash
	}
	return fmtNs(r.NsPerOp)
}

// ratioCell is our time over the time of another implementation, marked when we are slower.
func ratioCell(r *float64) (text, class string) {
	switch {
	case r == nil:
		return dash, ""
	case *r > 1:
		return fmt.Sprintf("%.2fx slower", *r), "slower"
	case *r < 1:
		return fmt.Sprintf("%.2fx faster", 1 / *r), "faster"
	}
	return "1.00x", ""
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

type bar struct {
	Class, Label, Value string
	Y, W                int
}

type group struct {
	Label  string
	Bars   []bar
	Index  int
	Y      int
	Height int
}

// row is a line of a comparison: the time of the other implementation, ours on a file we
// read and on a file mapped in memory, and each of ours over the other.
type row struct {
	Name, Threads, Other string
	Go, Ratio, Class     string
	Mapped, MappedRatio  string
	MappedClass          string
	Index                int
}

// compareView is a comparison with another implementation: the table and the bars.
type compareView struct {
	Prefix                          string // of the data-testid attributes
	Other                           string // the name of the other implementation
	Machine, Commit, Date, Versions string
	Rows                            []row
	Groups                          []group
	Height                          int
}

// versus is one case next to another implementation: its time, ours read and mapped, and
// our times over its. ours is nil for a case that is not in the comparison.
type versus struct {
	c                   *benchCase
	other, ours, mapped *result
	ratio, ratioMapped  *float64
}

// comparison is what the two pages have of their own: who we are compared with, and which
// results of a case are the ones to compare.
type comparison struct {
	pick   func(c *benchCase) versus
	prefix string
	other  string
}

var (
	// vsRust is the scans and the writes of the same Vortex file, in batches.
	vsRust = comparison{prefix: "speed", other: "Rust", pick: func(c *benchCase) versus {
		return versus{
			c: c, other: c.Rust, ours: &c.Go, mapped: c.GoMapped,
			ratio: c.RatioToRust, ratioMapped: c.RatioToRustMapped,
		}
	}}
	// vsParquet is the same rows read into the same structs, from a file of each format.
	vsParquet = comparison{prefix: "parquet", other: "parquet-go", pick: func(c *benchCase) versus {
		if c.ParquetGo == nil {
			return versus{}
		}
		return versus{
			c: c, other: c.ParquetGo, ours: c.GoRows, mapped: c.GoRowsMapped,
			ratio: c.RatioToParquetGo, ratioMapped: c.RatioToParquetGoMapped,
		}
	}}
)

const (
	labelW  = 200
	barMaxW = 280
	barH    = 8
	barGap  = 2
	rowGap  = 14
)

// pickSorted is the cases of a comparison, the ones where we lose first: the highest ratio,
// then the cases that have no ratio.
func pickSorted(cases []benchCase, cmp comparison) []versus {
	var out []versus
	for i := range cases {
		if v := cmp.pick(&cases[i]); v.ours != nil {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].ratio, out[j].ratio
		switch {
		case a == nil || b == nil:
			return a != nil && b == nil
		case *a != *b:
			return *a > *b
		}
		return false
	})
	return out
}

// bars is the bars of a case: one per result that was measured, the longest for the slowest.
func bars(v versus, other string) ([]bar, int) {
	series := []struct {
		r            *result
		class, label string
	}{{v.ours, "bar-go", "vortex-go"}, {v.mapped, "bar-mapped", "vortex-go, mapped"}, {v.other, "bar-rust", other}}
	longest := 0.0
	for _, s := range series {
		if s.r != nil {
			longest = math.Max(longest, s.r.NsPerOp)
		}
	}
	var out []bar
	y := 0
	for _, s := range series {
		if s.r == nil {
			continue
		}
		w := 1
		if longest > 0 {
			w = max(1, int(math.Round(s.r.NsPerOp/longest*barMaxW)))
		}
		out = append(out, bar{Class: s.class, Label: s.label, Value: fmtNs(s.r.NsPerOp), Y: y, W: w})
		y += barH + barGap
	}
	return out, y
}

func newCompareView(r *report, cmp comparison) compareView {
	v := compareView{
		Prefix:   cmp.prefix,
		Other:    cmp.other,
		Machine:  fmt.Sprintf("%s, %s, %d cores", r.Machine.CPU, r.Machine.OS, r.Machine.Cores),
		Commit:   short(r.Commit),
		Date:     r.Date,
		Versions: fmt.Sprintf("Vortex %s, %s, %s", r.VortexVersion, r.GoVersion, r.RustVersion),
	}
	y := 0
	for i, c := range pickSorted(r.Cases, cmp) {
		line := row{
			Index: i, Name: c.c.Name, Threads: strconv.Itoa(c.c.Threads),
			Other: optNs(c.other), Go: fmtNs(c.ours.NsPerOp), Mapped: optNs(c.mapped),
		}
		line.Ratio, line.Class = ratioCell(c.ratio)
		line.MappedRatio, line.MappedClass = ratioCell(c.ratioMapped)
		v.Rows = append(v.Rows, line)
		g := group{Index: i, Label: fmt.Sprintf("%s, %d thread", c.c.Name, c.c.Threads), Y: y}
		if c.c.Threads != 1 {
			g.Label += "s"
		}
		g.Bars, g.Height = bars(c, cmp.other)
		y += g.Height + rowGap
		v.Groups = append(v.Groups, g)
	}
	v.Height = y
	return v
}

var compareTmpl = template.Must(template.New("compare").Parse(compareTemplate))

// renderCompare is the table and the bars of a comparison. No report, or a report with no
// case of that comparison, is no measurement yet.
func renderCompare(r *report, cmp comparison) (string, error) {
	if r == nil {
		return fmt.Sprintf(emptyHTML, cmp.prefix), nil
	}
	v := newCompareView(r, cmp)
	if len(v.Rows) == 0 {
		return fmt.Sprintf(emptyHTML, cmp.prefix), nil
	}
	var b bytes.Buffer
	if err := compareTmpl.Execute(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

// geomean is the geometric mean of the ratios to Rust of the cases for which keep is true. ok is
// false when no case has a ratio.
func geomean(cases []benchCase, keep func(benchCase) bool) (g float64, ok bool) {
	sum, n := 0.0, 0
	for _, c := range cases {
		if keep(c) && c.RatioToRust != nil && *c.RatioToRust > 0 {
			sum += math.Log(*c.RatioToRust)
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return math.Exp(sum / float64(n)), true
}

type point struct {
	Title       string
	Class       string
	Commit, Day string
	Value       float64
	X, Y        int
}

type series struct {
	Class, Label, Points string
	Dots                 []point
}

type historyView struct {
	Max    string
	Series []series
	Latest []string
	Y1     int
	Runs   int
}

const (
	chartW = 640
	chartH = 240
	padL   = 40
	padR   = 20
	padT   = 16
	padB   = 36
)

func newHistoryView(h []report) historyView {
	one := func(c benchCase) bool { return c.Threads == 1 }
	many := func(c benchCase) bool { return c.Threads != 1 }
	top := 1.0
	vals := [2][]float64{make([]float64, len(h)), make([]float64, len(h))}
	for i := range h {
		r := &h[i]
		for k, keep := range []func(benchCase) bool{one, many} {
			g, _ := geomean(r.Cases, keep)
			vals[k][i] = g
			top = math.Max(top, g)
		}
	}
	top = math.Ceil(top*1.1*10) / 10
	xOf := func(i int) int {
		if len(h) == 1 {
			return padL + (chartW-padL-padR)/2
		}
		return padL + i*(chartW-padL-padR)/(len(h)-1)
	}
	yOf := func(v float64) int { return padT + int(math.Round((1-v/top)*(chartH-padT-padB))) }
	v := historyView{Y1: yOf(1), Max: strconv.FormatFloat(top, 'f', -1, 64), Runs: len(h)}
	for k, s := range []series{{Class: "1", Label: "1 thread"}, {Class: "n", Label: "all cores"}} {
		var pts []string
		for i := range h {
			r := &h[i]
			if vals[k][i] == 0 {
				continue
			}
			p := point{X: xOf(i), Y: yOf(vals[k][i]), Class: s.Class, Value: vals[k][i], Commit: short(r.Commit), Day: r.Date}
			p.Title = fmt.Sprintf("%s, %s: %.2fx", p.Commit, p.Day, p.Value)
			pts = append(pts, fmt.Sprintf("%d,%d", p.X, p.Y))
			s.Dots = append(s.Dots, p)
		}
		s.Points = strings.Join(pts, " ")
		if len(s.Dots) > 0 {
			last := s.Dots[len(s.Dots)-1]
			v.Latest = append(v.Latest, fmt.Sprintf("%s: %.2fx at %s", s.Label, last.Value, last.Commit))
		}
		v.Series = append(v.Series, s)
	}
	return v
}

var historyTmpl = template.Must(template.New("history").Parse(historyTemplate))

// renderHistory is the line of the ratio to Rust over the runs of history.jsonl.
func renderHistory(h []report) (string, error) {
	v := newHistoryView(h)
	if len(h) == 0 || len(v.Latest) == 0 {
		return fmt.Sprintf(emptyHTML, "history"), nil
	}
	var b bytes.Buffer
	if err := historyTmpl.Execute(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

type sizeView struct{ Name, Default, Compact, DefaultClass, CompactClass string }

func sizeCell(v float64) (text, class string) {
	switch {
	case math.IsNaN(v):
		return dash, ""
	case v > 1:
		return fmt.Sprintf("%.3f", v), "slower"
	}
	return fmt.Sprintf("%.3f", v), "faster"
}

var sizeTmpl = template.Must(template.New("size").Parse(sizeTemplate))

// renderSizes is the table of the file size ratios.
func renderSizes(rows []sizeRow) (string, error) {
	if len(rows) == 0 {
		return fmt.Sprintf(emptyHTML, "size"), nil
	}
	view := make([]sizeView, len(rows))
	for i, r := range rows {
		v := sizeView{Name: r.Name}
		v.Default, v.DefaultClass = sizeCell(r.Default)
		v.Compact, v.CompactClass = sizeCell(r.Compact)
		view[i] = v
	}
	var b bytes.Buffer
	if err := sizeTmpl.Execute(&b, view); err != nil {
		return "", err
	}
	return b.String(), nil
}
