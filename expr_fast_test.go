package vortex

import (
	"encoding/binary"
	"math"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
)

// intColumn is an array of integers of type p: the low bytes of vals.
func intColumn(p array.PType, vals []int64) *array.Array {
	w := p.Width()
	a := &array.Array{Type: array.DType{Kind: array.Prim, PType: p}, Len: len(vals), Data: make([]byte, 8*len(vals))[:w*len(vals)]}
	var b [8]byte
	for i, v := range vals {
		binary.LittleEndian.PutUint64(b[:], uint64(v))
		copy(a.Data[i*w:], b[:w])
	}
	return a
}

// TestSelectFast checks the fast path of a filter against the row by row one: every integer
// type, every comparison, literals at the ends of the type, inside it and outside it.
func TestSelectFast(t *testing.T) {
	ops := map[string]func(Ref, any) Expr{"Eq": Ref.Eq, "Ne": Ref.Ne, "Lt": Ref.Lt, "Le": Ref.Le, "Gt": Ref.Gt, "Ge": Ref.Ge}
	types := []struct {
		typ      Type
		p        array.PType
		min, max int64
	}{
		{Int8Type(), array.I8, math.MinInt8, math.MaxInt8},
		{Int16Type(), array.I16, math.MinInt16, math.MaxInt16},
		{Int32Type(), array.I32, math.MinInt32, math.MaxInt32},
		{Int64Type(), array.I64, math.MinInt64, math.MaxInt64},
		{Uint8Type(), array.U8, 0, math.MaxUint8},
		{Uint16Type(), array.U16, 0, math.MaxUint16},
		{Uint32Type(), array.U32, 0, math.MaxUint32},
		{Uint64Type(), array.U64, 0, math.MaxInt64},
	}
	for _, c := range types {
		root := Type{kind: Struct, fields: []Field{{Name: "x", Type: c.typ.AsNullable()}}}
		vals := []int64{c.min, c.min + 1, 3, 4, 5, c.max - 1, c.max, 4}
		col := intColumn(c.p, vals)
		lits := []any{c.min, c.min + 1, int64(4), c.max - 1, c.max}
		if c.p == array.U64 {
			vals = append(vals, -1) // the largest value
			col = intColumn(c.p, vals)
			lits = append(lits, uint64(math.MaxUint64))
		}
		for name, mk := range ops {
			all := lits
			if name != "Eq" && name != "Ne" { // out of the range of the type: under it, and over it
				all = append(slices.Clone(lits), int64(-200), int64(-70000), int64(math.MinInt64), uint64(math.MaxUint64), int64(300), int64(70000), int64(1)<<40)
			}
			for _, lit := range all {
				n, err := mk(Col("x"), lit).compile(root, make([]bool, 1))
				if err != nil {
					continue // a literal that does not fit the type
				}
				for _, valid := range []*array.Bitmap{nil, {Bytes: []byte{0b10110101, 0b1}}} {
					col.Validity = valid
					n.bind([]*array.Array{col})
					var want []int
					for i := range vals {
						if n.eval(i) == triTrue {
							want = append(want, i)
						}
					}
					got, ok := n.selectFast(len(vals), make([]int, 0, len(vals)))
					if !ok || !slices.Equal(got, want) {
						t.Errorf("%s %s %v (nulls: %v): rows %v, %v, want %v", c.typ, name, lit, valid != nil, got, ok, want)
					}
				}
			}
		}
	}
}

// TestSelectFastDeclines lists the filters that have no fast path.
func TestSelectFastDeclines(t *testing.T) {
	root := Type{kind: Struct, fields: []Field{{Name: "x", Type: Int32Type()}, {Name: "f", Type: Float64Type()}}}
	ints := intColumn(array.I32, []int64{1, 2})
	floats := &array.Array{Type: array.DType{Kind: array.Prim, PType: array.F64}, Len: 2, Data: make([]byte, 16)}
	for name, e := range map[string]Expr{
		"And":    And(Col("x").Lt(2), Col("x").Gt(0)),
		"In":     Col("x").In(1, 2),
		"IsNull": Col("x").IsNull(),
		"float":  Col("f").Lt(1.5),
		"keep":   {},
	} {
		n, err := e.compile(root, make([]bool, 2))
		if err != nil {
			t.Fatal(name, err)
		}
		n.bind([]*array.Array{ints, floats})
		if sel, ok := n.selectFast(2, make([]int, 0, 2)); ok || len(sel) != 0 {
			t.Errorf("%s has a fast path: %v", name, sel)
		}
	}
	// An array that is not one of numbers is left to the row by row path.
	n, err := Col("x").Lt(2).compile(root, make([]bool, 2))
	if err != nil {
		t.Fatal(err)
	}
	n.bind([]*array.Array{{Type: array.DType{Kind: array.Ext}, Len: 2}, floats})
	if _, ok := n.selectFast(2, make([]int, 0, 2)); ok {
		t.Error("an extension array has a fast path")
	}
}

// TestSelectFastNullParent checks that a row of a null struct is not kept.
func TestSelectFastNullParent(t *testing.T) {
	inner := Type{kind: Struct, nullable: true, fields: []Field{{Name: "x", Type: Int32Type()}}}
	root := Type{kind: Struct, fields: []Field{{Name: "s", Type: inner}}}
	n, err := Col("s", "x").Ge(2).compile(root, make([]bool, 1))
	if err != nil {
		t.Fatal(err)
	}
	parent := &array.Array{Type: arrayType(inner), Len: 4, Fields: []*array.Array{intColumn(array.I32, []int64{1, 2, 3, 4})},
		Validity: &array.Bitmap{Bytes: []byte{0b1011}}}
	n.bind([]*array.Array{parent})
	if got, ok := n.selectFast(4, make([]int, 0, 4)); !ok || !slices.Equal(got, []int{1, 3}) {
		t.Errorf("rows %v, %v, want [1 3]", got, ok)
	}
}
