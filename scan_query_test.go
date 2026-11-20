package vortex

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// ----- the model: a fully decoded file, and a plain Go filter over it -----

// cellString is a canonical text of row i of a column, whatever its type.
func cellString(c *Column, i int) string {
	if c.Null(i) {
		return "null"
	}
	switch c.arr.Type.Kind {
	case array.Bool:
		return fmt.Sprint(c.Bool(i))
	case array.Prim:
		w := c.arr.Type.PType.Width()
		return string(c.arr.Data[i*w : (i+1)*w])
	case array.Struct:
		var parts []string
		for k := range c.arr.Fields {
			parts = append(parts, cellString(c.Field(k), i))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case array.Utf8, array.Binary:
		return string(c.Bytes(i))
	case array.Decimal:
		return c.Decimal(i).Text(0)
	case array.List, array.Map, array.FixedSizeList:
		if c.typ.kind == UUID {
			return fmt.Sprintf("%x", c.Bytes(i))
		}
		start, end := c.List(i)
		var parts []string
		for j := start; j < end; j++ {
			parts = append(parts, cellString(c.Elems(), j))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case array.Variant:
		v, err := c.Variant(i)
		return fmt.Sprint(v, err)
	}
	return "?"
}

// modelPath is a column, or a field of a struct column, with the value of each row: nil for
// a null (or a null parent), the Go value for the types that can be compared, and a marker
// for the others.
type modelPath struct {
	path []string
	typ  Type
	vals []any
}

type model struct {
	paths []*modelPath
	fp    [][]string // per top-level column, per row
	names []string
	rows  int
}

// isComparable reports whether a filter can compare a column of this type with a value.
func isComparable(t Type) bool {
	k := t.kind
	return k >= Bool && k <= Utf8 && k != List || k == Binary || k >= Date && k <= UUID
}

func cellOf(c *Column, i int) any {
	if c.Null(i) {
		return nil
	}
	switch k := c.typ.kind; {
	case k >= Int8 && k <= Int64 || k == Date || k == Time || k == Timestamp:
		switch c.arr.Type.PType.Width() {
		case 1:
			return int64(mustV[int8](c)[i])
		case 2:
			return int64(mustV[int16](c)[i])
		case 4:
			return int64(mustV[int32](c)[i])
		}
		return mustV[int64](c)[i]
	case k >= Uint8 && k <= Uint64:
		switch c.arr.Type.PType.Width() {
		case 1:
			return uint64(mustV[uint8](c)[i])
		case 2:
			return uint64(mustV[uint16](c)[i])
		case 4:
			return uint64(mustV[uint32](c)[i])
		}
		return mustV[uint64](c)[i]
	case k >= Float16 && k <= Float64:
		if k == Float64 {
			return mustV[float64](c)[i]
		}
		return float64(mustV[float32](c)[i])
	case k == Bool:
		return c.Bool(i)
	case k == Utf8:
		return string(c.Bytes(i))
	case k == Binary || k == UUID:
		return slices.Clone(c.Bytes(i))
	case k == KindDecimal:
		return c.Decimal(i)
	}
	return struct{}{}
}

func mustV[T Number](c *Column) []T {
	v, err := Values[T](c)
	if err != nil {
		panic(err)
	}
	return v
}

func (m *model) add(path []string, c *Column, n int, parentNull func(int) bool) {
	var mp *modelPath
	for _, p := range m.paths {
		if slices.Equal(p.path, path) {
			mp = p
		}
	}
	if mp == nil {
		mp = &modelPath{path: slices.Clone(path), typ: c.typ}
		m.paths = append(m.paths, mp)
	}
	for i := range n {
		if parentNull(i) {
			mp.vals = append(mp.vals, nil)
		} else {
			mp.vals = append(mp.vals, cellOf(c, i))
		}
	}
	if c.typ.kind == Struct {
		for k, f := range c.typ.fields {
			m.add(append(path, f.Name), c.Field(k), n, func(i int) bool { return parentNull(i) || c.Null(i) })
		}
	}
}

// loadModel decodes the whole file with a plain scan. It returns nil when the file cannot be
// decoded (an encoding that is pending).
func loadModel(f *File) *model {
	m := &model{}
	for _, fl := range f.Schema().Fields() {
		m.names = append(m.names, fl.Name)
	}
	m.fp = make([][]string, len(m.names))
	for b, err := range f.scan(context.Background(), scanOptions{batchRows: 4096}) {
		if err != nil {
			return nil
		}
		for k, name := range m.names {
			c := b.Col(k)
			if c.arr.Skipped != nil {
				return nil
			}
			for i := range b.Len() {
				m.fp[k] = append(m.fp[k], cellString(c, i))
			}
			m.add([]string{name}, c, b.Len(), func(int) bool { return false })
		}
		m.rows += b.Len()
	}
	return m
}

// modelLit is a literal as the model compares it, with the Go value the filter is given.
type modelLit struct {
	model any
	given any
}

// litOf makes a literal of the type of a column from a value of the file.
func litOf(rng *rand.Rand, t Type, v any) modelLit {
	switch x := v.(type) {
	case int64:
		switch t.kind {
		case Date:
			if t.unit == Day {
				return modelLit{x, time.Unix(x*86400, 0).UTC()}
			}
			return modelLit{x, time.UnixMilli(x).UTC()}
		case Timestamp:
			return modelLit{x, time.Unix(0, x*perTick(t.unit)).UTC()}
		case Time:
			return modelLit{x, time.Duration(x * perTick(t.unit))}
		}
		if rng.IntN(2) == 0 {
			return modelLit{x, int(x)}
		}
	case uint64:
		if rng.IntN(2) == 0 {
			return modelLit{x, uint(x)}
		}
	}
	return modelLit{v, v}
}

// modelCmp orders a value of the file against a literal. It is false when unordered (NaN).
func modelCmp(a, b any) (int, bool) {
	switch x := a.(type) {
	case int64:
		return cmpOrdered(x, b.(int64)), true
	case uint64:
		return cmpOrdered(x, b.(uint64)), true
	case float64:
		y := b.(float64)
		if math.IsNaN(x) || math.IsNaN(y) {
			return 0, false
		}
		return cmpOrdered(x, y), true
	case bool:
		return cmpOrdered(btoi(x), btoi(b.(bool))), true
	case string:
		return strings.Compare(x, b.(string)), true
	case []byte:
		return strings.Compare(string(x), string(b.([]byte))), true
	}
	return a.(Decimal).Cmp(b.(Decimal)), true
}

func cmpOrdered[T int64 | uint64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func btoi(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// modelExpr is an Expr with its meaning in plain Go.
type modelExpr struct {
	e    Expr
	eval func(row int) tri
}

func not3(t tri) tri {
	if t == triUnknown {
		return t
	}
	return 1 - t
}

func (m *model) genLeaf(rng *rand.Rand) (modelExpr, bool) {
	p := m.paths[rng.IntN(len(m.paths))]
	ref := Col(p.path...)
	if rng.IntN(5) == 0 {
		return modelExpr{ref.IsNull(), func(row int) tri { return triOf(p.vals[row] == nil) }}, true
	}
	if !isComparable(p.typ) {
		return modelExpr{}, false
	}
	pick := func() (modelLit, bool) {
		for range 20 {
			if v := p.vals[rng.IntN(len(p.vals))]; v != nil {
				return litOf(rng, p.typ, v), true
			}
		}
		return modelLit{}, false
	}
	l, ok := pick()
	if !ok {
		return modelExpr{}, false
	}
	var e Expr
	op := rng.IntN(7)
	switch op {
	case 0:
		e = ref.Eq(l.given)
	case 1:
		e = ref.Ne(l.given)
	case 2:
		e = ref.Lt(l.given)
	case 3:
		e = ref.Le(l.given)
	case 4:
		e = ref.Gt(l.given)
	case 5:
		e = ref.Ge(l.given)
	}
	lits := []modelLit{l}
	if op == 6 {
		given := []any{l.given}
		for range rng.IntN(3) {
			if l2, ok := pick(); ok {
				lits, given = append(lits, l2), append(given, l2.given)
			}
		}
		e = ref.In(given...)
	}
	return modelExpr{e, func(row int) tri {
		v := p.vals[row]
		if v == nil {
			return triUnknown
		}
		if op == 6 {
			for _, l := range lits {
				if c, ok := modelCmp(v, l.model); ok && c == 0 {
					return triTrue
				}
			}
			return triFalse
		}
		c, ok := modelCmp(v, l.model)
		switch {
		case !ok:
			return triOf(op == 1)
		case op == 0:
			return triOf(c == 0)
		case op == 1:
			return triOf(c != 0)
		case op == 2:
			return triOf(c < 0)
		case op == 3:
			return triOf(c <= 0)
		case op == 4:
			return triOf(c > 0)
		}
		return triOf(c >= 0)
	}}, true
}

func (m *model) genExpr(rng *rand.Rand, depth int) modelExpr {
	if depth == 0 || rng.IntN(3) == 0 {
		for range 50 {
			if e, ok := m.genLeaf(rng); ok {
				return e
			}
		}
		return modelExpr{Expr{}, func(int) tri { return triTrue }}
	}
	switch rng.IntN(3) {
	case 0:
		a, b := m.genExpr(rng, depth-1), m.genExpr(rng, depth-1)
		return modelExpr{And(a.e, b.e), func(r int) tri {
			x, y := a.eval(r), b.eval(r)
			switch {
			case x == triFalse || y == triFalse:
				return triFalse
			case x == triUnknown || y == triUnknown:
				return triUnknown
			}
			return triTrue
		}}
	case 1:
		a, b := m.genExpr(rng, depth-1), m.genExpr(rng, depth-1)
		return modelExpr{Or(a.e, b.e), func(r int) tri {
			x, y := a.eval(r), b.eval(r)
			switch {
			case x == triTrue || y == triTrue:
				return triTrue
			case x == triUnknown || y == triUnknown:
				return triUnknown
			}
			return triFalse
		}}
	}
	a := m.genExpr(rng, depth-1)
	return modelExpr{Not(a.e), func(r int) tri { return not3(a.eval(r)) }}
}

// ----- random queries -----

// runQuery scans with q and checks the rows against the model. It returns the number of rows.
func runQuery(f *File, m *model, q Query, cols []string, batchRows int, want func(row int) bool) error {
	lo, hi := int64(0), int64(m.rows)
	if q.Rows.End != 0 {
		hi = min(q.Rows.End, hi)
	}
	lo = min(q.Rows.Start, hi)
	idx := []int{}
	for _, c := range cols {
		idx = append(idx, slices.Index(m.names, c))
	}
	if len(cols) == 0 {
		for i := range m.names {
			idx = append(idx, i)
		}
	}
	var wantRows []int
	for r := lo; r < hi; r++ {
		if want(int(r)) {
			wantRows = append(wantRows, int(r))
		}
	}
	got := make([][]string, len(idx))
	for b, err := range f.scan(context.Background(), scanOptions{batchRows: batchRows, q: q, columns: cols}) {
		if err != nil {
			return err
		}
		if b.Len() == 0 || b.Len() > batchRows {
			return fmt.Errorf("a batch of %d rows (at most %d)", b.Len(), batchRows)
		}
		for k := range idx {
			c := b.Col(k)
			if c.arr.Len != b.Len() {
				return fmt.Errorf("column %d has %d rows, the batch %d", k, c.arr.Len, b.Len())
			}
			for i := range b.Len() {
				got[k] = append(got[k], cellString(c, i))
			}
		}
	}
	for k, col := range idx {
		var exp []string
		for _, r := range wantRows {
			exp = append(exp, m.fp[col][r])
		}
		if !slices.Equal(exp, got[k]) {
			return fmt.Errorf("column %d: %d rows, want %d", k, len(got[k]), len(exp))
		}
	}
	if len(idx) == 0 && len(wantRows) != 0 {
		return fmt.Errorf("no column")
	}
	return nil
}

// describe prints an Expr for a failure message.
func describe(e Expr) string {
	names := map[exprOp]string{opEq: "Eq", opNe: "Ne", opLt: "Lt", opLe: "Le", opGt: "Gt", opGe: "Ge", opIn: "In",
		opIsNull: "IsNull", opAnd: "And", opOr: "Or", opNot: "Not"}
	if e.n == nil {
		return "Keep()"
	}
	var kids []string
	for _, k := range e.n.kids {
		kids = append(kids, describe(k))
	}
	if e.n.path != nil {
		kids = append(kids, strings.Join(e.n.path, "."), fmt.Sprint(e.n.vals))
	}
	return names[e.n.op] + "(" + strings.Join(kids, ", ") + ")"
}

func TestScanRandomQueries(t *testing.T) {
	for _, path := range corpus(t) {
		t.Run(fixtureName(path), func(t *testing.T) {
			f, err := OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			randomQueries(t, f, uint64(len(path))*7919)
		})
	}
}

// randomQueries runs 200 seeded random queries on the file and checks each against the model.
func randomQueries(t *testing.T, f *File, seed uint64) {
	m := loadModel(f)
	if m == nil || m.rows == 0 || len(m.paths) == 0 {
		t.Skip("nothing to decode")
	}
	for i := range 200 {
		seed := seed + uint64(i)
		rng := rand.New(rand.NewPCG(seed, 1))
		batchRows := []int{maxBatchRows, 1, 7, 1024, 100000}[i%5]
		if i%5 == 1 && m.rows > 300 {
			continue // batches of one row: the small files only
		}
		// A subset of the columns, in a random order.
		var cols []string
		if rng.IntN(3) > 0 {
			for _, name := range m.names {
				if rng.IntN(2) == 0 {
					cols = append(cols, name)
				}
			}
			rng.Shuffle(len(cols), func(a, b int) { cols[a], cols[b] = cols[b], cols[a] })
		}
		if m.rows > 1000 && (len(cols) == 0 || len(cols) > 3) { // keep the checks cheap
			cols = slices.Clone(m.names)
			rng.Shuffle(len(cols), func(a, b int) { cols[a], cols[b] = cols[b], cols[a] })
			cols = cols[:min(3, len(cols))]
		}
		var q Query
		if rng.IntN(2) == 0 {
			q.Rows = Range{Start: int64(rng.IntN(m.rows + 5))}
			if rng.IntN(2) == 0 {
				q.Rows.End = q.Rows.Start + int64(rng.IntN(m.rows+5)) + 1
			}
		}
		me := m.genExpr(rng, 2)
		if rng.IntN(4) > 0 {
			q.Filter = me.e
		} else {
			me = modelExpr{eval: func(int) tri { return triTrue }}
		}
		if err := runQuery(f, m, q, cols, batchRows, func(r int) bool { return me.eval(r) == triTrue }); err != nil {
			t.Fatalf("seed %d, batch %d, columns %v, rows %+v, filter %s: %v", seed, batchRows, cols, q.Rows, describe(q.Filter), err)
		}
	}
}

// ----- null semantics -----

// The file of flatFile: a i64 [10 20 30], b i8? [1 null 3], s {x i32 [7 8 9], y bool [false true true]}.
func TestFilterNullSemantics(t *testing.T) {
	f, err := open(flatFile(0))
	if err != nil {
		t.Fatal(err)
	}
	a, b, x := Col("a"), Col("b"), Col("s", "x")
	cases := []struct {
		name string
		e    Expr
		want []int64 // the values of a that are kept
	}{
		{"nothing", Expr{}, []int64{10, 20, 30}},
		{"eq", b.Eq(1), []int64{10}},
		{"ne skips the null", b.Ne(1), []int64{30}},
		{"lt", b.Lt(3), []int64{10}},
		{"le", b.Le(3), []int64{10, 30}},
		{"gt", b.Gt(1), []int64{30}},
		{"ge", b.Ge(1), []int64{10, 30}},
		{"in", b.In(1, 3), []int64{10, 30}},
		{"is null", b.IsNull(), []int64{20}},
		{"not is null", Not(b.IsNull()), []int64{10, 30}},
		{"not unknown is unknown", Not(b.Eq(1)), []int64{30}},
		{"or true unknown is true", Or(b.Eq(3), a.Eq(20)), []int64{20, 30}},
		{"or false unknown is unknown", Or(b.Eq(3), a.Eq(10)), []int64{10, 30}},
		{"and true unknown is unknown", And(a.Eq(20), b.Eq(1)), nil},
		{"not and unknown", Not(And(a.Eq(20), b.Eq(1))), []int64{10, 30}},
		{"not and false unknown is true", Not(And(a.Eq(10), b.Eq(1))), []int64{20, 30}},
		{"empty and", And(), []int64{10, 20, 30}},
		{"empty or", Or(), nil},
		{"zero expr inside", And(Expr{}, a.Gt(10)), []int64{20, 30}},
		{"nested field", x.Ge(8), []int64{20, 30}},
		{"nested field is null", Col("s", "y").IsNull(), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []int64
			for batch, err := range f.Scan(context.Background(), Query{Filter: c.e}, "a") {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, mustValues[int64](t, batch.Col(0))...)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// ----- projection, range, errors -----

func TestScanProjectionAndRange(t *testing.T) {
	f, err := open(flatFile(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// The order of the names, a column twice, and a column only the filter needs.
	for b, err := range f.Scan(ctx, Query{Filter: Col("a").Ge(20)}, "b", "a", "b") {
		if err != nil || b.Len() != 2 || len(b.cols) != 3 {
			t.Fatal(b, err)
		}
		equal(t, mustValues[int64](t, b.Col(1)), []int64{20, 30})
		if !b.Col(0).Null(0) || b.Col(2).Null(1) {
			t.Fatal("column b")
		}
	}
	// The range is clamped and half-open.
	for _, c := range []struct {
		r    Range
		want []int64
	}{
		{Range{}, []int64{10, 20, 30}},
		{Range{Start: 1}, []int64{20, 30}},
		{Range{End: 2}, []int64{10, 20}},
		{Range{Start: 1, End: 2}, []int64{20}},
		{Range{Start: 2, End: 100}, []int64{30}},
		{Range{Start: 5}, nil},
		{Range{Start: 5, End: 9}, nil},
	} {
		var got []int64
		for b, err := range f.Scan(ctx, Query{Rows: c.r}, "a") {
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, mustValues[int64](t, b.Col(0))...)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("range %+v: %v, want %v", c.r, got, c.want)
		}
	}
	// A struct column and its field filtered, in a file whose root is a struct layout.
	g, err := open(layoutFile())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for b, err := range g.Scan(ctx, Query{Filter: Col("a").Gt(0)}, "b") {
		if err != nil {
			t.Fatal(err)
		}
		n += b.Len()
	}
	if n == 0 {
		t.Fatal("no row")
	}
}

func TestScanQueryErrors(t *testing.T) {
	f, err := open(flatFile(0))
	if err != nil {
		t.Fatal(err)
	}
	a := Col("a")
	cases := []struct {
		name    string
		q       Query
		columns []string
		want    string
	}{
		{"unknown column", Query{}, []string{"zz"}, `unknown column "zz"`},
		{"unknown filter column", Query{Filter: Col("zz").Eq(1)}, nil, `unknown column "zz"`},
		{"unknown nested field", Query{Filter: Col("s", "zz").Eq(1)}, nil, `unknown field "s.zz"`},
		{"field of a number", Query{Filter: Col("a", "x").Eq(1)}, nil, `unknown field "a.x"`},
		{"no column", Query{Filter: Col().Eq(1)}, nil, "without a column name"},
		{"string against i64", Query{Filter: a.Eq("x")}, nil, `column "a" of type i64: literal x (string) does not fit`},
		{"float against int", Query{Filter: a.Lt(1.5)}, nil, "literal 1.5 (float64) does not fit"},
		{"nil literal", Query{Filter: a.Eq(nil)}, nil, "does not fit"},
		{"struct literal", Query{Filter: a.Eq(struct{}{})}, nil, "does not fit"},
		{"i8 out of range", Query{Filter: Col("b").Eq(300)}, nil, `column "b" of type i8?: literal 300 (int) does not fit`},
		{"i8 out of range, negative", Query{Filter: Col("b").Ne(-129)}, nil, "does not fit"},
		{"i32 out of range for in", Query{Filter: Col("s", "x").In(1, 1<<40)}, nil, "does not fit"},
		{"bool against string", Query{Filter: Col("s", "y").Eq("true")}, nil, "does not fit"},
		{"struct is not comparable", Query{Filter: Col("s").Eq(1)}, nil, "this type cannot be compared"},
		{"two literals", Query{Filter: Expr{&exprNode{op: opEq, path: []string{"a"}, vals: []any{1, 2}}}}, nil, "takes one literal"},
		{"start after end", Query{Rows: Range{Start: 5, End: 3}}, nil, "invalid row range [5, 3)"},
		{"negative start", Query{Rows: Range{Start: -1}}, nil, "invalid row range"},
		{"negative end", Query{Rows: Range{End: -1}}, nil, "invalid row range"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := 0
			for b, err := range f.Scan(context.Background(), c.q, c.columns...) {
				n++
				if b != nil || err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("batch %v, err = %v, want %q", b, err, c.want)
				}
			}
			if n != 1 {
				t.Fatalf("%d iterations, want the error only", n)
			}
		})
	}
}

// An error of the readers comes back, whichever phase finds it.
func TestScanReadErrors(t *testing.T) {
	for _, file := range [][]byte{flatFile(0), layoutFile()} {
		f, err := open(file)
		if err != nil {
			t.Fatal(err)
		}
		f.r = failAfter{r: f.r, ok: 0}
		for _, q := range []Query{{}, {Filter: Col("a").Gt(0)}, {Filter: Col("a").Gt(0), Rows: Range{Start: 1}}} {
			for _, cols := range [][]string{nil, {"b"}} {
				n := 0
				for _, err := range f.Scan(context.Background(), q, cols...) {
					if !errors.Is(err, os.ErrClosed) {
						t.Fatalf("err = %v", err)
					}
					n++
				}
				if n != 1 {
					t.Fatalf("%d iterations", n)
				}
			}
		}
	}
}

type failAfter struct {
	r interface {
		ReadAt([]byte, int64) (int, error)
	}
	ok int
}

func (f failAfter) ReadAt(p []byte, off int64) (int, error) {
	if f.ok == 0 {
		return 0, os.ErrClosed
	}
	return f.r.ReadAt(p, off)
}

// ----- literals -----

func TestFilterLiterals(t *testing.T) {
	f, err := open(extraFixture(t, "types_matrix.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	m := loadModel(f)
	if m == nil {
		t.Fatal("no model")
	}
	n := 0
	for _, p := range m.paths {
		if !isComparable(p.typ) || len(p.path) != 1 {
			continue
		}
		var v any
		for _, x := range p.vals {
			if x != nil {
				v = x
				break
			}
		}
		if v == nil {
			continue
		}
		l := litOf(rand.New(rand.NewPCG(1, 1)), p.typ, v)
		for _, e := range []Expr{Col(p.path...).Eq(l.given), Col(p.path...).In(l.given), Col(p.path...).Ge(l.given)} {
			count := 0
			for b, err := range f.Scan(context.Background(), Query{Filter: e}, p.path[0]) {
				if err != nil {
					t.Fatalf("%s: %v", p.typ, err)
				}
				count += b.Len()
			}
			if count == 0 {
				t.Errorf("%s: the filter on a value of the file keeps no row", p.typ)
			}
		}
		n++
	}
	if n < 8 {
		t.Fatalf("only %d columns checked", n)
	}
}

func TestLiteralConversions(t *testing.T) {
	ts := time.Date(1969, 12, 31, 23, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		u    Unit
		date bool
		want int64
	}{
		{Day, true, -1}, {Millisecond, true, -3600000}, {Second, false, -3600},
		{Millisecond, false, -3600000}, {Microsecond, false, -3600000000}, {Nanosecond, false, -3600000000000},
	} {
		if got := ticksOf(ts, c.u, c.date); got != c.want {
			t.Errorf("ticksOf(%v, %v) = %d, want %d", c.u, c.date, got, c.want)
		}
	}
	if got := ticksOf(time.Unix(86400*3+5, 0), Day, true); got != 3 {
		t.Errorf("a day after the epoch = %d", got)
	}
	for _, c := range []struct {
		bits   int
		signed bool
		v      lit
		want   bool
	}{
		{8, true, intLit(127), true}, {8, true, intLit(128), false}, {8, true, intLit(-128), true},
		{8, true, intLit(-129), false}, {8, false, intLit(255), true}, {8, false, intLit(256), false},
		{8, false, intLit(-1), false}, {64, false, lit{mag: math.MaxUint64}, true},
		{64, true, lit{mag: 1 << 63}, false}, {64, true, lit{mag: 1 << 63, neg: true}, true},
	} {
		if got := fits(&c.v, c.bits, c.signed); got != c.want {
			t.Errorf("fits(%+v, %d, %v) = %v", c.v, c.bits, c.signed, got)
		}
	}
}

// ----- allocations -----

func TestFilterAllocations(t *testing.T) {
	f, err := open(extraFixture(t, "default_ints.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	var arrs []*array.Array
	rd, err := f.newReader(f.layout, arrayType(f.schema.root), array.Options{}, "", 0, 4096)
	if err != nil {
		t.Fatal(err)
	}
	root, err := rd.take(4096)
	if err != nil {
		t.Fatal(err)
	}
	arrs = root.Fields
	need := make([]bool, len(arrs))
	e := And(Or(Col("sorted").Lt(2000), Col("nullable").IsNull()), Not(Col("random").Eq(5)), Col("low_card").In(1, 2, 3))
	n, err := e.compile(f.schema.root, need)
	if err != nil {
		t.Fatal(err)
	}
	sel := make([]int, 0, 4096)
	pass := func() {
		n.bind(arrs)
		sel = sel[:0]
		for i := range 4096 {
			if n.eval(i) == triTrue {
				sel = append(sel, i)
			}
		}
		n.bounds(100)
	}
	pass()
	if got := testing.AllocsPerRun(20, pass); got != 0 {
		t.Fatalf("a filtered batch allocates %v times", got)
	}
}

// ----- pruning -----

// countReads scans with q and returns the rows and the segments read.
func countReads(t *testing.T, f *File, q Query, cols ...string) (rows int, reads int) {
	t.Helper()
	log := &logReader{r: f.r}
	f.r = log
	defer func() { f.r = log.r }()
	for b, err := range f.Scan(context.Background(), q, cols...) {
		if err != nil {
			t.Fatal(err)
		}
		rows += b.Len()
	}
	return rows, len(log.offs)
}

// sortedFile is a file of {k: i64, v: i64} on chunks*chunkRows rows. k counts from 0 and is
// zoned, a zone per chunk, with its min, max and null count. v is 2*k and has no statistics.
// The segments alternate: chunk c of k is 2c, of v 2c+1, then comes the table of the zones.
func sortedFile(chunks, chunkRows int, nullableRoot bool, zoneStats ...string) []byte {
	rows := uint64(chunks * chunkRows)
	var segs [][]byte
	var kFlats, vFlats []fbtest.Tab
	for c := range chunks {
		k, v := make([]uint64, chunkRows), make([]uint64, chunkRows)
		for i := range k {
			k[i], v[i] = uint64(c*chunkRows+i), uint64(2*(c*chunkRows+i))
		}
		segs = append(segs, primSeg(8, k...))
		kFlats = append(kFlats, layoutTab(lFlat, uint64(chunkRows), []uint32{uint32(2 * c)}))
		segs = append(segs, primSeg(8, v...))
		vFlats = append(vFlats, layoutTab(lFlat, uint64(chunkRows), []uint32{uint32(2*c + 1)}))
	}
	mins, maxs, nulls := make([]uint64, chunks), make([]uint64, chunks), make([]uint64, chunks)
	for c := range chunks {
		mins[c], maxs[c] = uint64(c*chunkRows), uint64((c+1)*chunkRows-1)
	}
	var kids []fbtest.Tab
	var bufs []abuf
	for i, name := range zoneStats {
		data := mins
		if name == "vortex.max" {
			data = maxs
		} else if name == "vortex.null_count" {
			data = nulls
		}
		kids = append(kids, aNode(aPrim, []uint16{uint16(i)}))
		bufs = append(bufs, abuf{data: bytesOf(8, data...)})
	}
	segs = append(segs, aSeg(aNode(aStruct, nil, kids...), bufs...))
	zones := layoutTab(lFlat, uint64(chunks), []uint32{uint32(2 * chunks)})
	var valid []fbtest.Tab
	if nullableRoot { // a validity child first, all true
		segs = append(segs, aSeg(aNode(aBool, []uint16{0}), abuf{data: bytes.Repeat([]byte{0xff}, (int(rows)+7)/8)}))
		valid = append(valid, layoutTab(lFlat, rows, []uint32{uint32(2*chunks + 1)}))
	}
	k := metaLayout(lZoned, rows, zonedMeta(1, uint64(chunkRows), zoneStats...),
		layoutTab(lChunked, rows, nil, kFlats...), zones)
	v := layoutTab(lChunked, rows, nil, vFlats...)
	dtype := structType(nullableRoot, []string{"k", "v"}, prim(7, false), prim(7, false))
	return buildFile(dtype, layoutTab(lStruct, rows, nil, append(valid, k, v)...), segs...)
}

func TestScanGeneratedZonedFile(t *testing.T) {
	for _, stats := range [][]string{
		{"vortex.min", "vortex.max", "vortex.null_count"},
		{"vortex.max", "vortex.min"},
		{"vortex.null_count"},
		{},
	} {
		f, err := open(sortedFile(50, 40, false, stats...))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(strings.Join(stats, ","), func(t *testing.T) { randomQueries(t, f, 99) })
	}
}

func TestPruningReadsLess(t *testing.T) {
	f, err := open(sortedFile(100, 100, false, "vortex.min", "vortex.max", "vortex.null_count"))
	if err != nil {
		t.Fatal(err)
	}
	all, allReads := countReads(t, f, Query{}, "k", "v")
	if all != 10000 || allReads != 200 {
		t.Fatalf("%d rows, %d segments", all, allReads)
	}
	// 1% of the rows are in the first or in the last chunk.
	for _, c := range []struct {
		e    Expr
		rows int
	}{
		{Col("k").Lt(100), 100},
		{Col("k").Le(99), 100},
		{Col("k").Eq(55), 1},
		{Col("k").In(5, 10), 2},
		{And(Col("k").Ge(0), Col("k").Lt(100)), 100},
		{Or(Col("k").Lt(50), Col("k").Lt(60)), 60},
		{Not(Col("k").Ge(100)), 100},
		{Col("k").Gt(9899), 100},
		{Col("k").Ge(9900), 100},
		{Col("k").IsNull(), 0},
		{Col("k").Eq(-5), 0},
		{Col("k").Eq(1 << 40), 0},
	} {
		rows, reads := countReads(t, f, Query{Filter: c.e}, "k", "v")
		if rows != c.rows {
			t.Errorf("%s kept %d rows, want %d", describe(c.e), rows, c.rows)
		}
		if reads > 5 { // the zones, and the chunks that can hold the rows
			t.Errorf("%s: %d segments read, a full scan %d", describe(c.e), reads, allReads)
		}
	}
	// A filter that every zone can meet reads as much as a scan, and the zones.
	rows, reads := countReads(t, f, Query{Filter: Col("k").Ge(0)}, "k", "v")
	if rows != 10000 || reads != allReads+1 {
		t.Errorf("a filter that keeps everything: %d rows, %d segments", rows, reads)
	}
	// The rows range skips the chunks too, with or without a filter.
	if rows, reads := countReads(t, f, Query{Rows: Range{Start: 5000, End: 5100}}, "k", "v"); rows != 100 || reads > 4 {
		t.Errorf("a range of 100 rows: %d rows, %d segments", rows, reads)
	}
	// A filter on a column without statistics reads everything, and is exact.
	rows, reads = countReads(t, f, Query{Filter: Col("v").Lt(200)}, "k")
	if rows != 100 || reads < 100 {
		t.Errorf("no statistics: %d rows, %d segments", rows, reads)
	}
}

// Truncated bounds of a string column never give an exact value: the filters keep what the
// rows say.
func TestTruncatedStringBounds(t *testing.T) {
	f, err := open(extraFixture(t, "default_mixed.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	m := loadModel(f)
	var vals []any
	for _, p := range m.paths {
		if p.path[0] == "str_high" {
			vals = p.vals
		}
	}
	lf := &leaf{field: slices.Index(m.names, "str_high")}
	f.loadZones(f.layout, f.schema.root, lf, [2]int64{0, int64(m.rows)})
	if len(lf.tables) != 1 || lf.tables[0].exactBound {
		t.Fatal("the bounds are expected to be truncated")
	}
	tb := lf.tables[0]
	bound := string(bytesAt(tb.max, 0)) // 64 bytes, the values have 80
	for _, v := range vals {
		if v != nil && len(v.(string)) <= len(bound) {
			t.Fatalf("a value of %d bytes is not truncated", len(v.(string)))
		}
	}
	for _, e := range []Expr{
		Col("str_high").Eq(bound), Col("str_high").Ne(bound), Col("str_high").Lt(bound), Col("str_high").Le(bound),
		Col("str_high").Gt(bound), Col("str_high").Ge(bound), Col("str_high").Eq(string(bytesAt(tb.min, 0))),
		Col("str_high").In(bound, string(bytesAt(tb.min, 0))),
	} {
		want := 0
		me := modelExprOf(m, e)
		for r := range m.rows {
			if me(r) == triTrue {
				want++
			}
		}
		got, _ := countReads(t, f, Query{Filter: e}, "str_high")
		if got != want {
			t.Errorf("%s kept %d rows, want %d", describe(e), got, want)
		}
	}
}

// modelExprOf evaluates a comparison of a string column on the model.
func modelExprOf(m *model, e Expr) func(int) tri {
	var p *modelPath
	for _, mp := range m.paths {
		if mp.path[0] == e.n.path[0] && len(mp.path) == 1 {
			p = mp
		}
	}
	return func(r int) tri {
		if p.vals[r] == nil {
			return triUnknown
		}
		hit := false
		var last int
		for _, l := range e.n.vals {
			c, _ := modelCmp(p.vals[r], l)
			last, hit = c, hit || c == 0
		}
		switch e.n.op {
		case opEq, opIn:
			return triOf(hit)
		case opNe:
			return triOf(last != 0)
		case opLt:
			return triOf(last < 0)
		case opLe:
			return triOf(last <= 0)
		case opGt:
			return triOf(last > 0)
		}
		return triOf(last >= 0)
	}
}

// zoneOf makes a zone table of one zone with the given statistics.
func zoneOf(rows int64, min, max array.Array, nulls, nans uint64, exact bool) *zoneTable {
	u := func(v uint64) *array.Array {
		return &array.Array{Type: array.DType{Kind: array.Prim, PType: array.U64}, Len: 1, Data: binary.LittleEndian.AppendUint64(nil, v)}
	}
	tb := &zoneTable{min: &min, max: &max, nulls: u(nulls), zoneLen: rows, rows: rows, exactBound: exact}
	if nans != math.MaxUint64 {
		tb.nans = u(nans)
	}
	return tb
}

func ints(v int64) array.Array {
	return array.Array{Type: array.DType{Kind: array.Prim, PType: array.I64}, Len: 1, Data: binary.LittleEndian.AppendUint64(nil, uint64(v))}
}

func floats(v float64) array.Array {
	return array.Array{Type: array.DType{Kind: array.Prim, PType: array.F64}, Len: 1, Data: binary.LittleEndian.AppendUint64(nil, math.Float64bits(v))}
}

func strs(v string) array.Array {
	return array.Array{Type: array.DType{Kind: array.Utf8}, Len: 1, Data: []byte(v), Starts: []int{0}, Ends: []int{len(v)}}
}

func TestZoneBounds(t *testing.T) {
	leafOf := func(op exprOp, kind valueKind, float bool, lits ...lit) *leaf {
		return &leaf{op: op, kind: kind, float: float, lits: lits}
	}
	i := func(v int64) lit { return intLit(v) }
	cases := []struct {
		name string
		l    *leaf
		tb   *zoneTable
		want bounds
	}{
		{"eq inside", leafOf(opEq, valInt, false, i(5)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{true, true}},
		{"eq below", leafOf(opEq, valInt, false, i(0)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{false, true}},
		{"eq above", leafOf(opEq, valInt, false, i(10)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{false, true}},
		{"eq constant", leafOf(opEq, valInt, false, i(5)), zoneOf(10, ints(5), ints(5), 0, 0, true), bounds{true, false}},
		{"eq constant but a bound", leafOf(opEq, valInt, false, i(5)), zoneOf(10, ints(5), ints(5), 0, 0, false), bounds{true, true}},
		{"ne constant", leafOf(opNe, valInt, false, i(5)), zoneOf(10, ints(5), ints(5), 0, 0, true), bounds{false, true}},
		{"ne outside", leafOf(opNe, valInt, false, i(50)), zoneOf(10, ints(5), ints(5), 0, 0, true), bounds{true, false}},
		{"ne constant bound", leafOf(opNe, valInt, false, i(5)), zoneOf(10, ints(5), ints(5), 0, 0, false), bounds{true, true}},
		{"in", leafOf(opIn, valInt, false, i(0), i(5)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{true, true}},
		{"in outside", leafOf(opIn, valInt, false, i(0), i(10)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{false, true}},
		{"lt", leafOf(opLt, valInt, false, i(1)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{false, true}},
		{"lt all", leafOf(opLt, valInt, false, i(10)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{true, false}},
		{"le", leafOf(opLe, valInt, false, i(0)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{false, true}},
		{"le all", leafOf(opLe, valInt, false, i(9)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{true, false}},
		{"gt", leafOf(opGt, valInt, false, i(9)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{false, true}},
		{"gt all", leafOf(opGt, valInt, false, i(0)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{true, false}},
		{"ge", leafOf(opGe, valInt, false, i(10)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{false, true}},
		{"ge all", leafOf(opGe, valInt, false, i(1)), zoneOf(10, ints(1), ints(9), 0, 0, true), bounds{true, false}},
		{"all null", leafOf(opEq, valInt, false, i(5)), zoneOf(10, ints(5), ints(5), 10, 0, true), bounds{}},
		{"is null, none", leafOf(opIsNull, valInt, false), zoneOf(10, ints(5), ints(5), 0, 0, true), bounds{false, true}},
		{"is null, some", leafOf(opIsNull, valInt, false), zoneOf(10, ints(5), ints(5), 3, 0, true), bounds{true, true}},
		{"is null, all", leafOf(opIsNull, valInt, false), zoneOf(10, ints(5), ints(5), 10, 0, true), bounds{true, false}},
		{"nan makes ne possible", leafOf(opNe, valFloat, true, lit{f: 5}), zoneOf(10, floats(5), floats(5), 0, 2, true), bounds{true, true}},
		{"no nan, ne", leafOf(opNe, valFloat, true, lit{f: 5}), zoneOf(10, floats(5), floats(5), 0, 0, true), bounds{false, true}},
		{"unknown nan count", leafOf(opNe, valFloat, true, lit{f: 5}), zoneOf(10, floats(5), floats(5), 0, math.MaxUint64, true), bounds{true, true}},
		{"nan makes lt false possible", leafOf(opLt, valFloat, true, lit{f: 50}), zoneOf(10, floats(5), floats(6), 0, 1, true), bounds{true, true}},
		{"nan literal", leafOf(opEq, valFloat, true, lit{f: math.NaN()}), zoneOf(10, floats(5), floats(6), 0, 0, true), bounds{true, true}},
		{"nan bound", leafOf(opLt, valFloat, true, lit{f: 3}), zoneOf(10, floats(math.NaN()), floats(6), 0, 0, true), bounds{true, true}},
		{"truncated string bound", leafOf(opEq, valBytes, false, lit{b: []byte("abc")}), zoneOf(10, strs("abc"), strs("abc"), 0, 0, false), bounds{true, true}},
		{"string above the bound", leafOf(opGt, valBytes, false, lit{b: []byte("zzz")}), zoneOf(10, strs("a"), strs("m"), 0, 0, false), bounds{false, true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.l.zoneBounds(c.tb, 0, c.tb.rows); got != c.want {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
	// A statistic that is missing, or null, says nothing.
	l := leafOf(opEq, valInt, false, i(5))
	tb := zoneOf(10, ints(1), ints(9), 0, 0, true)
	tb.min = nil
	if got := l.zoneBounds(tb, 0, 10); got != (bounds{true, true}) {
		t.Errorf("no min: %+v", got)
	}
	tb = zoneOf(10, ints(1), ints(9), 0, 0, true)
	tb.nulls = nil
	tb.max.Validity = &array.Bitmap{Bytes: []byte{0}}
	if got := l.zoneBounds(tb, 0, 10); got != (bounds{true, true}) {
		t.Errorf("null max: %+v", got)
	}
	tb.nulls = &array.Array{Type: array.DType{Kind: array.Prim, PType: array.I8}, Len: 1, Data: []byte{0}}
	if _, ok := uintAt(tb.nulls, 0); ok {
		t.Error("a null count that is not a u64")
	}
}

// at and bounds: the zones of a leaf, and And, Or, Not of leaves.
func TestZoneLookup(t *testing.T) {
	tb := func(start, rows int64, v int64) zoneTable {
		z := zoneOf(rows, ints(v), ints(v), 0, 0, true)
		z.start = start
		return *z
	}
	l := &leaf{op: opEq, kind: valInt, lits: []lit{intLit(5)}, tables: []zoneTable{tb(10, 4, 5), tb(20, 4, 6)}}
	n := &node{op: opAnd, kids: []*node{{op: opEq, leaf: l}}}
	for _, c := range []struct {
		pos   int64
		want  bounds
		limit int64
	}{
		{0, bounds{true, true}, 10},   // before the first table
		{10, bounds{true, false}, 14}, // the first table
		{12, bounds{true, false}, 14},
		{14, bounds{true, true}, 20}, // between the tables
		{21, bounds{false, true}, 24},
		{24, bounds{true, true}, math.MaxInt64}, // after the last
	} {
		got, limit := n.bounds(c.pos)
		if got != c.want || limit != c.limit {
			t.Errorf("row %d: %+v until %d, want %+v until %d", c.pos, got, limit, c.want, c.limit)
		}
	}
	yes := &node{op: opKeep}
	no := &node{op: opOr}
	for _, c := range []struct {
		n    *node
		want bounds
	}{
		{yes, bounds{true, false}},
		{no, bounds{false, true}},
		{&node{op: opNot, kids: []*node{yes}}, bounds{false, true}},
		{&node{op: opAnd, kids: []*node{yes, no}}, bounds{false, true}},
		{&node{op: opOr, kids: []*node{yes, no}}, bounds{true, false}},
	} {
		if got, _ := c.n.bounds(0); got != c.want {
			t.Errorf("%+v: %+v, want %+v", c.n, got, c.want)
		}
	}
}

// ----- more errors and paths -----

func TestFilterLiteralErrors(t *testing.T) {
	f, err := open(extraFixture(t, "types_matrix.vortex"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		e    Expr
	}{
		{"unknown column inside And", And(Col("i8_r").Eq(1), Col("zz").Eq(1))},
		{"time against an integer", Col("i8_r").Eq(time.Unix(0, 0))},
		{"duration against a date", Col("date_r").Eq(time.Second)},
		{"time against a time", Col("time_r").Eq(time.Unix(0, 0))},
		{"decimal against an integer", Col("i8_r").Eq(DecimalFromInt64(1))},
		{"bytes against a string", Col("utf8_r").Eq([]byte("a"))},
		{"string against binary", Col("binary_r").Eq("a")},
		{"short uuid", Col("uuid_r").Eq([]byte("a"))},
		{"bool against a string", Col("bool_r").Eq("a")},
		{"float against a bool", Col("bool_r").Eq(1.5)},
		{"integer against a bool", Col("bool_r").Eq(1)},
		{"integer against a string", Col("utf8_r").Eq(1)},
		{"string against a float", Col("f32_r").Eq("a")},
		{"nan against an integer", Col("i32_r").Eq(math.NaN())},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, err := range f.Scan(context.Background(), Query{Filter: c.e}) {
				if err == nil || !strings.Contains(err.Error(), "vortex: filter") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			t.Fatal("no error")
		})
	}
	// What does fit: integers against floats (negative too), every integer type, a literal
	// out of range for an ordering.
	for _, e := range []Expr{
		Col("f32_r").Lt(-1), Col("f64_r").Ge(uint8(3)), Col("f16_r").Gt(int16(-2)), Col("u8_r").Lt(-5),
		Col("u8_r").Gt(1000), Col("i8_r").Le(int64(1) << 40), Col("u64_r").Eq(uint64(math.MaxUint64)), Col("f64_r").Eq(math.NaN()),
		Col("f64_r").Ne(math.NaN()), Col("uuid_r").Eq(make([]byte, 16)), Col("date_r").Gt(time.Unix(0, 0)),
		Col("time_r").Lt(time.Hour), Col("timestamp_r").Ge(time.Unix(0, 0)), Col("decimal_r").Gt(DecimalFromInt64(1)),
		Col("binary_r").Gt([]byte("a")), Col("bool_r").Ge(true), Col("u16_r").Le(uint(7)),
	} {
		for _, err := range f.Scan(context.Background(), Query{Filter: e}, "null_n") {
			if err != nil {
				t.Fatalf("%s: %v", describe(e), err)
			}
		}
	}
}

// A nullable root struct has a validity that a scan reads and ignores.
func TestScanNullableRoot(t *testing.T) {
	f, err := open(sortedFile(10, 10, true, "vortex.min", "vortex.max", "vortex.null_count"))
	if err != nil {
		t.Fatal(err)
	}
	randomQueries(t, f, 5)
	if rows, reads := countReads(t, f, Query{Filter: Col("k").Lt(10)}, "v"); rows != 10 || reads > 4 {
		t.Errorf("%d rows, %d segments", rows, reads)
	}
	f.r = failAfter{r: f.r}
	for _, err := range f.Scan(context.Background(), Query{}) {
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("err = %v", err)
		}
	}
}

// An error of a field of a nested struct comes back from its layout.
func TestScanNestedStructError(t *testing.T) {
	dtype := structType(false, []string{"s"}, structType(false, []string{"x"}, prim(7, false)))
	bad := aSeg(aNode(aAlp, nil))
	layout := layoutTab(lStruct, 1, nil, layoutTab(lStruct, 1, nil, layoutTab(lFlat, 1, []uint32{0})))
	f, err := open(buildFile(dtype, layout, bad))
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range f.Scan(context.Background(), Query{}) {
		if !errors.Is(err, errors.ErrUnsupported) {
			t.Fatalf("err = %v", err)
		}
	}
}

// The fields a scan tolerates are skipped, and gathered, like the others.
func TestScanTolerateWithFilter(t *testing.T) {
	dtype := structType(false, []string{"a", "b"}, prim(7, false), prim(7, false))
	bad := aSeg(aNode(aAlp, nil))
	good := aSeg(aNode(aPrim, []uint16{0}), abuf{data: bytesOf(8, 1, 2)})
	layout := layoutTab(lStruct, 2, nil, layoutTab(lFlat, 2, []uint32{0}), layoutTab(lFlat, 2, []uint32{1}))
	f, err := open(buildFile(dtype, layout, bad, good))
	if err != nil {
		t.Fatal(err)
	}
	tolerate := array.Options{Tolerate: func(string, error) bool { return true }}
	// Batches of one row: the first is dropped before a is read, then a is skipped.
	rows := 0
	for b, err := range f.scan(context.Background(), scanOptions{batchRows: 1, array: tolerate, q: Query{Filter: Col("b").Eq(2)}}) {
		if err != nil || b.Col(0).arr.Skipped == nil {
			t.Fatal(b, err)
		}
		rows += b.Len()
	}
	// The other way: a is skipped, then the batch has a row less than its rows.
	for b, err := range f.scan(context.Background(), scanOptions{batchRows: 1, array: tolerate, q: Query{Filter: Col("b").Eq(1)}}) {
		if err != nil || b.Col(0).arr.Skipped == nil {
			t.Fatal(b, err)
		}
		rows += b.Len()
	}
	for b, err := range f.scan(context.Background(), scanOptions{batchRows: 8, array: tolerate, q: Query{Filter: Col("b").Eq(2)}}) {
		if err != nil || b.Col(0).arr.Skipped == nil || b.Len() != 1 {
			t.Fatal(b, err)
		}
		rows += b.Len()
	}
	if rows != 3 {
		t.Fatalf("%d rows", rows)
	}
}

// Filtering a Variant column: rows are gathered one by one.
func TestScanVariantGather(t *testing.T) {
	f, err := open(variantFile())
	if err != nil {
		t.Fatal(err)
	}
	var got []any
	for b, err := range f.Scan(context.Background(), Query{Filter: And(Not(Col("v").IsNull()), Not(Col("v").IsNull()))}) {
		if err != nil {
			t.Fatal(err)
		}
		for i := range b.Len() {
			v, _ := b.Col(0).Variant(i)
			got = append(got, v)
		}
	}
	if len(got) != 4 || got[0].(map[string]any)["k"] != int64(5) {
		t.Fatalf("%v", got)
	}
}

// Zone tables that cannot be used are the same as none.
func TestUnusableZoneTables(t *testing.T) {
	f, err := open(sortedFile(4, 10, false, "vortex.min", "vortex.max", "vortex.null_count"))
	if err != nil {
		t.Fatal(err)
	}
	l := f.layout.children[0] // the zoned layout of k
	t64 := array.DType{Kind: array.Prim, PType: array.I64}
	z, err := parseZoneLayout(l, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.zoneTable(l, &z, t64, 0); !ok {
		t.Fatal("the table is usable")
	}
	bad := z
	bad.aggregates = []aggregateSpec{{id: "an.unknown.aggregate"}}
	if _, ok := f.zoneTable(l, &bad, t64, 0); ok {
		t.Error("an unknown aggregate")
	}
	bad = z
	bad.zoneLen = 0
	if _, ok := f.zoneTable(l, &bad, t64, 0); ok {
		t.Error("a zone length of 0")
	}
	bad = z
	bad.zones = &layoutNode{encoding: uint16(slices.Index(f.layoutEncodings, "vortex.list")), rowCount: 4}
	if _, ok := f.zoneTable(l, &bad, t64, 0); ok {
		t.Error("a zones layout that cannot be read")
	}
	bad = z
	bad.aggregates = z.aggregates[:1] // the table has three fields
	if _, ok := f.zoneTable(l, &bad, t64, 0); ok {
		t.Error("a table of another shape")
	}
	bad = z
	bad.aggregates = []aggregateSpec{{id: "vortex.min"}, {id: "vortex.max"}, {id: "vortex.null_count"}}
	bad.zones = &layoutNode{encoding: l.children[1].encoding, rowCount: 4, segments: []uint32{0}}
	if _, ok := f.zoneTable(l, &bad, t64, 0); ok {
		t.Error("a zones segment that does not decode")
	}
	// A zoned layout with bad metadata, and a column that is not zoned, have no table.
	var out []zoneTable
	broken := *l
	broken.metadata = []byte{9}
	all := [2]int64{0, 40}
	f.leafZones(&broken, t64, 0, all, &out)
	f.leafZones(l.children[0], t64, 0, all, &out)
	f.leafZones(l, t64, 0, [2]int64{40, 50}, &out) // outside the rows
	if len(out) != 0 {
		t.Errorf("%d tables", len(out))
	}
}

func TestZoneFields(t *testing.T) {
	i64 := array.DType{Kind: array.Prim, PType: array.I64}
	f64 := array.DType{Kind: array.Prim, PType: array.F64}
	u8 := array.DType{Kind: array.Prim, PType: array.U8}
	legacy := func(bits ...int) *zoneMap {
		z := &zoneMap{legacy: true, stats: make([]byte, 2)}
		for _, b := range bits {
			z.stats[b/8] |= 1 << (b % 8)
		}
		return z
	}
	cases := []struct {
		name string
		z    *zoneMap
		t    array.DType
		want []string
		ok   bool
	}{
		{"aggregates", &zoneMap{aggregates: []aggregateSpec{{id: "vortex.min"}, {id: "vortex.bounded_max"}, {id: "vortex.bounded_min"}, {id: "vortex.nan_count"}, {id: "vortex.null_count"}}}, f64,
			[]string{"min", "bounded_max", "bounded_min", "nan_count", "null_count"}, true},
		{"nan count of an integer", &zoneMap{aggregates: []aggregateSpec{{id: "vortex.nan_count"}}}, i64, nil, true},
		{"unknown aggregate", &zoneMap{aggregates: []aggregateSpec{{id: "x"}}}, i64, nil, false},
		{"legacy", legacy(statIsConstant, statMax, statMin, statSum, statNullCount, statUncompressedSize, statNanCount), f64,
			[]string{"", "max", "", "min", "", "", "null_count", "", "nan_count"}, true},
		{"legacy integer", legacy(statSum, statNanCount, statIsSorted, statIsStrictSorted), i64, []string{"", "", ""}, true},
		{"legacy unsigned sum", legacy(statSum), u8, []string{""}, true},
		{"legacy sum of a string", legacy(statSum), array.DType{Kind: array.Utf8}, nil, true},
		{"legacy unknown stat", legacy(9), i64, nil, false},
	}
	for _, c := range cases {
		fields, ok := c.z.zoneFields(c.t)
		var got []string
		for _, f := range fields {
			got = append(got, f.stat)
		}
		if ok != c.ok || !slices.Equal(got, c.want) {
			t.Errorf("%s: %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
	if s, _ := sumType(f64); s.PType != array.F64 {
		t.Error("sum of floats")
	}
	if s, _ := sumType(u8); s.PType != array.U64 {
		t.Error("sum of unsigned")
	}
	if s, _ := sumType(array.DType{PType: array.I8, Kind: array.Prim}); s.PType != array.I64 {
		t.Error("sum of signed")
	}
	// The bound of a bounded_max statistic is null where the struct is.
	s := &array.Array{Type: array.DType{Kind: array.Struct}, Len: 3, Validity: &array.Bitmap{Bytes: []byte{0b101}}, Fields: []*array.Array{{
		Type: array.DType{Kind: array.Prim, PType: array.I64}, Len: 3, Validity: &array.Bitmap{Bytes: []byte{0b011}},
		Data: make([]byte, 24)}}}
	b := boundOf(s)
	if !b.Null(1) || !b.Null(2) || b.Null(0) {
		t.Error("the bound is valid where both the struct and the bound are")
	}
	if boundOf(&array.Array{Fields: []*array.Array{{Len: 1}}}).Validity != nil {
		t.Error("a struct without nulls gives its bound")
	}
	if n := nullable(array.DType{Kind: array.Ext, Storage: &array.DType{Kind: array.Prim}}); !n.Nullable || !n.Storage.Nullable {
		t.Error("nullable extension")
	}
}
