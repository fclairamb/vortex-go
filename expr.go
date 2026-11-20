package vortex

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/vortex-go/internal/array"
)

// exprOp is the operator of a node of an Expr.
type exprOp uint8

// The operators of an Expr. The zero one keeps every row.
const (
	opKeep exprOp = iota
	opEq
	opNe
	opLt
	opLe
	opGt
	opGe
	opIn
	opIsNull
	opAnd
	opOr
	opNot
)

// Expr is a filter: a small tree of comparisons of columns with Go values, and of And, Or and
// Not. The zero Expr keeps every row. Build one with Col and the methods of Ref.
//
// A comparison with a null is unknown, like in SQL, and And, Or and Not are three-valued: a
// row is kept only when the filter is true for it. IsNull is never unknown. For the floats,
// NaN equals nothing (not even itself), is neither less nor greater than anything, and so is
// different from everything, like in Go.
//
// The literals are Go values: any integer or float type, bool, string (for a Utf8 column),
// []byte (Binary, UUID), Decimal, time.Time (date and timestamp) and time.Duration (time),
// compared exactly even when finer than the unit of the column (Eq never matches then). An
// integer can be compared with a float column. The filter is checked against the schema before
// any read: a literal that does not fit the column, and for Eq, Ne and In an integer out of the range of the column, are errors.
type Expr struct{ n *exprNode }

// exprNode is a node of an Expr.
type exprNode struct {
	path []string
	vals []any
	kids []Expr
	op   exprOp
}

// Ref is a column, or a field of a struct column, to compare.
type Ref struct{ path []string }

// Col is the column called path[0], or its field path[1], and so on for nested structs.
func Col(path ...string) Ref { return Ref{path: path} }

func (r Ref) cmp(op exprOp, vals ...any) Expr {
	return Expr{&exprNode{op: op, path: r.path, vals: vals}}
}

// Eq is true for the rows whose value equals v.
func (r Ref) Eq(v any) Expr { return r.cmp(opEq, v) }

// Ne is true for the rows whose non-null value differs from v.
func (r Ref) Ne(v any) Expr { return r.cmp(opNe, v) }

// Lt is true for the rows whose value is less than v.
func (r Ref) Lt(v any) Expr { return r.cmp(opLt, v) }

// Le is true for the rows whose value is at most v.
func (r Ref) Le(v any) Expr { return r.cmp(opLe, v) }

// Gt is true for the rows whose value is greater than v.
func (r Ref) Gt(v any) Expr { return r.cmp(opGt, v) }

// Ge is true for the rows whose value is at least v.
func (r Ref) Ge(v any) Expr { return r.cmp(opGe, v) }

// In is true for the rows whose value equals one of vs.
func (r Ref) In(vs ...any) Expr { return r.cmp(opIn, vs...) }

// IsNull is true for the rows that are null, and false for the others.
func (r Ref) IsNull() Expr { return r.cmp(opIsNull) }

// And is true when every expression is. With none, it keeps every row.
func And(e ...Expr) Expr { return Expr{&exprNode{op: opAnd, kids: e}} }

// Or is true when one of the expressions is. With none, it keeps no row.
func Or(e ...Expr) Expr { return Expr{&exprNode{op: opOr, kids: e}} }

// Not is true when e is false, false when e is true and unknown when e is.
func Not(e Expr) Expr { return Expr{&exprNode{op: opNot, kids: []Expr{e}}} }

// tri is a three-valued truth.
type tri uint8

const (
	triFalse tri = iota
	triTrue
	triUnknown
)

func triOf(b bool) tri {
	if b {
		return triTrue
	}
	return triFalse
}

// node is a checked Expr.
type node struct {
	leaf *leaf
	kids []*node
	op   exprOp
}

// compile checks e against the schema and binds its columns. It lists the top-level fields
// it reads in need.
func (e Expr) compile(root Type, need []bool) (*node, error) {
	if e.n == nil {
		return &node{}, nil
	}
	n := &node{op: e.n.op}
	switch e.n.op {
	case opAnd, opOr, opNot:
		for _, k := range e.n.kids {
			c, err := k.compile(root, need)
			if err != nil {
				return nil, err
			}
			n.kids = append(n.kids, c)
		}
		return n, nil
	}
	l, err := newLeaf(e.n, root)
	if err != nil {
		return nil, err
	}
	need[l.field] = true
	n.leaf = l
	return n, nil
}

// eval is the truth of the node for row i of the bound arrays.
func (n *node) eval(i int) tri {
	switch n.op {
	case opKeep:
		return triTrue
	case opAnd:
		out := triTrue
		for _, k := range n.kids {
			switch k.eval(i) {
			case triFalse:
				return triFalse
			case triUnknown:
				out = triUnknown
			}
		}
		return out
	case opOr:
		out := triFalse
		for _, k := range n.kids {
			switch k.eval(i) {
			case triTrue:
				return triTrue
			case triUnknown:
				out = triUnknown
			}
		}
		return out
	case opNot:
		switch n.kids[0].eval(i) {
		case triTrue:
			return triFalse
		case triFalse:
			return triTrue
		}
		return triUnknown
	}
	return n.leaf.eval(i)
}

// bind points the leaves at the arrays of a batch, by top-level field.
func (n *node) bind(fields []*array.Array) {
	if n.leaf != nil {
		n.leaf.bind(fields)
	}
	for _, k := range n.kids {
		k.bind(fields)
	}
}

// valueKind is how the values of a column are compared.
type valueKind uint8

const (
	valInt valueKind = iota
	valFloat
	valBool
	valBytes
	valDecimal
)

// lit is a literal of a filter, in the form of its valueKind.
type lit struct {
	b    []byte
	d    Decimal
	f    float64
	mag  uint64 // the absolute value of an integer
	neg  bool
	bool bool
	frac bool // a time finer than the unit of the column: mag is the floor
}

// leaf is a comparison of a column with literals.
type leaf struct {
	name   string
	chain  []*array.Array // the column and its parents' arrays, bound per batch
	lits   []lit
	path   []int // field indexes below the top-level field
	tables []zoneTable
	field  int // the top-level field
	cur    int // the table of the last batch
	op     exprOp
	kind   valueKind
	float  bool // the column holds floats
}

// intBits is the width in bits of the integer kinds.
var intBits = map[Kind]int{
	Int8: 8, Int16: 16, Int32: 32, Int64: 64, Uint8: 8, Uint16: 16, Uint32: 32, Uint64: 64,
}

// valueKindOf is how the values of a column of type t are compared, when they can be.
func valueKindOf(t Type) (valueKind, bool) {
	switch k := t.kind; {
	case k >= Int8 && k <= Uint64, k == Date, k == Time, k == Timestamp:
		return valInt, true
	case k >= Float16 && k <= Float64:
		return valFloat, true
	case k == Bool:
		return valBool, true
	case k == Utf8, k == Binary, k == UUID:
		return valBytes, true
	case k == KindDecimal:
		return valDecimal, true
	}
	return 0, false
}

func newLeaf(e *exprNode, root Type) (*leaf, error) {
	l := &leaf{op: e.op, name: strings.Join(e.path, ".")}
	if len(e.path) == 0 {
		return nil, fmt.Errorf("vortex: filter without a column name")
	}
	t, err := l.resolve(e.path, root)
	if err != nil {
		return nil, err
	}
	l.chain = make([]*array.Array, 0, len(l.path)+1)
	if e.op == opIsNull {
		return l, nil
	}
	var ok bool
	if l.kind, ok = valueKindOf(t); !ok {
		return nil, fmt.Errorf("vortex: filter on column %q of type %s: this type cannot be compared", l.name, t)
	}
	l.float = l.kind == valFloat
	if e.op != opIn && len(e.vals) != 1 {
		return nil, fmt.Errorf("vortex: filter on column %q: a comparison takes one literal", l.name)
	}
	strict := e.op == opEq || e.op == opNe || e.op == opIn
	for _, v := range e.vals {
		c, ok := l.literal(t, v, strict)
		if !ok {
			return nil, fmt.Errorf("vortex: filter on column %q of type %s: literal %v (%T) does not fit", l.name, t, v, v)
		}
		l.lits = append(l.lits, c)
	}
	l.exactTimes()
	return l, nil
}

// resolve finds the field of the path and returns its type.
func (l *leaf) resolve(path []string, root Type) (Type, error) {
	t := root
	for depth, name := range path {
		idx := slices.IndexFunc(t.fields, func(f Field) bool { return f.Name == name })
		if t.kind != Struct || idx < 0 {
			what := "unknown column"
			if depth > 0 {
				what = "unknown field"
			}
			return t, fmt.Errorf("vortex: filter: %s %q", what, strings.Join(path[:depth+1], "."))
		}
		if depth == 0 {
			l.field = idx
		} else {
			l.path = append(l.path, idx)
		}
		t = t.fields[idx].Type
	}
	return t, nil
}

// literal converts a Go value to the form of the column of type t. A strict literal is also
// range-checked against an integer column. It is false when the value does not fit.
func (l *leaf) literal(t Type, v any, strict bool) (lit, bool) {
	switch x := v.(type) {
	case time.Time:
		v, frac := ticksFrac(x, t.unit, t.kind == Date)
		c := intLit(v)
		c.frac = frac
		return c, t.kind == Date || t.kind == Timestamp
	case time.Duration:
		if t.kind != Time {
			return lit{}, false
		}
		per := perTick(t.unit)
		c := intLit(floorDiv(int64(x), per))
		c.frac = int64(x)%per != 0
		return c, true
	case Decimal:
		return lit{d: x}, t.kind == KindDecimal
	case []byte:
		return lit{b: x}, t.kind == Binary || t.kind == UUID && len(x) == uuidSize
	case string:
		return lit{b: []byte(x)}, t.kind == Utf8
	case bool:
		return lit{bool: x}, t.kind == Bool
	}
	return l.number(t, reflect.ValueOf(v), strict)
}

// perTick is the duration of a tick of a time unit, in nanoseconds.
func perTick(u Unit) int64 { return int64(time.Second) / int64(ticksPerSecond[u]) }

// number converts an integer or a float of any Go type.
func (l *leaf) number(t Type, rv reflect.Value, strict bool) (lit, bool) {
	var c lit
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		c = intLit(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		c = lit{mag: rv.Uint()}
	case reflect.Float32, reflect.Float64:
		return lit{f: rv.Float()}, l.kind == valFloat
	default:
		return lit{}, false
	}
	switch l.kind {
	case valFloat:
		c.f = float64(c.mag)
		if c.neg {
			c.f = -c.f
		}
		return c, true
	case valInt:
		bits, isInt := intBits[t.kind]
		return c, !isInt || !strict || fits(&c, bits, t.kind <= Int64)
	}
	return lit{}, false
}

// intLit is the literal of an integer.
func intLit(v int64) lit {
	if v < 0 {
		return lit{neg: true, mag: uint64(-v)}
	}
	return lit{mag: uint64(v)}
}

// fits reports whether the integer of c is in the range of an integer of that many bits.
func fits(c *lit, bits int, signed bool) bool {
	switch {
	case !signed:
		return !c.neg && (bits == 64 || c.mag < 1<<bits)
	case c.neg:
		return c.mag <= 1<<(bits-1)
	}
	return c.mag < 1<<(bits-1)
}

// floorDiv is a / b rounded towards minus infinity, for b > 0.
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b < 0 {
		q--
	}
	return q
}

// ticksFrac is the time in the unit of a date or a timestamp, since the epoch, rounded down,
// and whether t is finer than the unit (so the value is not exact).
func ticksFrac(t time.Time, u Unit, date bool) (int64, bool) {
	sec, nsec := t.Unix(), int64(t.Nanosecond())
	switch {
	case date && u == Day:
		return floorDiv(sec, 86400), sec%86400 != 0 || nsec != 0
	case u == Second:
		return sec, nsec != 0
	case u == Millisecond:
		return sec*1000 + nsec/1e6, nsec%1e6 != 0
	case u == Microsecond:
		return sec*1000000 + nsec/1e3, nsec%1e3 != 0
	}
	return t.UnixNano(), false
}

// exactTimes rewrites the literals finer than the unit of the column, so that the
// comparisons stay exact on the floor: Eq never matches, Ne matches every value, Lt and Le
// become Le, and Gt and Ge become Gt.
func (l *leaf) exactTimes() {
	if l.kind != valInt {
		return
	}
	switch l.op {
	case opEq, opNe, opIn:
		kept := l.lits[:0]
		for _, c := range l.lits {
			if !c.frac {
				kept = append(kept, c)
			}
		}
		l.lits = kept
		if l.op == opEq {
			l.op = opIn
		}
	case opLt, opLe:
		if l.lits[0].frac {
			l.op = opLe
		}
	case opGt, opGe:
		if l.lits[0].frac {
			l.op = opGt
		}
	}
}

// bind sets the arrays of the column of the batch and of its parents.
func (l *leaf) bind(fields []*array.Array) {
	a := fields[l.field]
	l.chain = append(l.chain[:0], a)
	for _, p := range l.path {
		a = a.Fields[p]
		l.chain = append(l.chain, a)
	}
}

// eval is the truth of the leaf for row i of the bound arrays.
func (l *leaf) eval(i int) tri {
	null := false
	for _, a := range l.chain {
		null = null || a.Null(i)
	}
	if l.op == opIsNull {
		return triOf(null)
	}
	if null {
		return triUnknown
	}
	a := l.chain[len(l.chain)-1]
	if l.op == opIn {
		for k := range l.lits {
			if c, ok := l.compare(a, i, &l.lits[k]); ok && c == 0 {
				return triTrue
			}
		}
		return triFalse
	}
	if len(l.lits) == 0 { // Ne of a time finer than the column
		return triTrue
	}
	c, ok := l.compare(a, i, &l.lits[0])
	if !ok { // NaN
		return triOf(l.op == opNe)
	}
	return triOf(holds(l.op, c))
}

// holds is whether a comparison operator is true when the value is ordered c against the literal.
func holds(op exprOp, c int) bool {
	switch op {
	case opEq:
		return c == 0
	case opNe:
		return c != 0
	case opLt:
		return c < 0
	case opLe:
		return c <= 0
	case opGt:
		return c > 0
	}
	return c >= 0
}

// compare orders the value of row i of a against a literal. It is not ok when a float is NaN.
func (l *leaf) compare(a *array.Array, i int, c *lit) (int, bool) {
	switch l.kind {
	case valInt:
		return cmpInt(a, i, c), true
	case valFloat:
		return cmpFloat(floatAt(a, i), c.f)
	case valBool:
		return cmpBool(a.Bits.Get(i), c.bool), true
	case valBytes:
		return bytes.Compare(bytesAt(a, i), c.b), true
	}
	return decimalFromLE(a.Data[i*a.Width : (i+1)*a.Width]).Cmp(c.d), true
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case b:
		return -1
	}
	return 1
}

func cmpFloat(a, b float64) (int, bool) {
	switch {
	case math.IsNaN(a) || math.IsNaN(b):
		return 0, false
	case a < b:
		return -1, true
	case a > b:
		return 1, true
	}
	return 0, true
}

func bytesAt(a *array.Array, i int) []byte {
	if a.Type.Kind == array.FixedSizeList { // a UUID
		return a.Elems.Data[i*uuidSize : (i+1)*uuidSize]
	}
	return a.Data[a.Starts[i]:a.Ends[i]]
}

func floatAt(a *array.Array, i int) float64 {
	switch a.Type.PType {
	case array.F16:
		return float64(math.Float32frombits(half(uint32(binary.LittleEndian.Uint16(a.Data[2*i:])))))
	case array.F32:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(a.Data[4*i:])))
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(a.Data[8*i:]))
}

// cmpInt orders the integer at row i of a primitive array against a literal.
func cmpInt(a *array.Array, i int, c *lit) int {
	var mag uint64
	neg := false
	if p := a.Type.PType; p.Signed() {
		var v int64
		switch p {
		case array.I8:
			v = int64(int8(a.Data[i]))
		case array.I16:
			v = int64(int16(binary.LittleEndian.Uint16(a.Data[2*i:])))
		case array.I32:
			v = int64(int32(binary.LittleEndian.Uint32(a.Data[4*i:])))
		default:
			v = int64(binary.LittleEndian.Uint64(a.Data[8*i:]))
		}
		neg, mag = v < 0, uint64(v)
		if neg {
			mag = -mag
		}
	} else {
		switch p {
		case array.U8:
			mag = uint64(a.Data[i])
		case array.U16:
			mag = uint64(binary.LittleEndian.Uint16(a.Data[2*i:]))
		case array.U32:
			mag = uint64(binary.LittleEndian.Uint32(a.Data[4*i:]))
		default:
			mag = binary.LittleEndian.Uint64(a.Data[8*i:])
		}
	}
	switch {
	case neg != c.neg:
		if neg {
			return -1
		}
		return 1
	case mag == c.mag:
		return 0
	case (mag < c.mag) != neg:
		return -1
	}
	return 1
}
