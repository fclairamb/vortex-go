package vortex

import (
	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// The fast path of a filter: a comparison of an integer column with one literal is a test of
// range, run on the values of the batch as they are in memory.

// intRange is the values an integer comparison keeps: the ones from lo to hi (included), or
// the ones outside when out is set. lo and hi are the bits of integers of the type of the
// column, sign-extended to 64 bits when it is signed.
type intRange struct {
	lo, hi uint64
	out    bool
}

// rangeOf is the range of the values v of type p for which "v op c" holds, op being a
// comparison: Eq, Ne, Lt, Le, Gt or Ge.
func rangeOf(p array.PType, op exprOp, c *lit) intRange {
	bits := 8 * p.Width()
	lowest, highest := uint64(0), ^uint64(0)>>(64-bits)
	if p.Signed() {
		highest >>= 1
		lowest = ^highest // the sign-extended minimum
	}
	all, none := intRange{lo: lowest, hi: highest}, intRange{lo: lowest, hi: highest, out: true}
	if !fits(c, bits, p.Signed()) { // the literal is under every value, or over
		if op == opNe || op != opEq && (op == opLt || op == opLe) != c.neg {
			return all
		}
		return none
	}
	v := c.mag
	if c.neg {
		v = -v
	}
	switch op {
	case opEq:
		return intRange{lo: v, hi: v}
	case opNe:
		return intRange{lo: v, hi: v, out: true}
	case opLt:
		if v == lowest {
			return none
		}
		return intRange{lo: lowest, hi: v - 1}
	case opLe:
		return intRange{lo: lowest, hi: v}
	case opGt:
		if v == highest {
			return none
		}
		return intRange{lo: v + 1, hi: highest}
	}
	return intRange{lo: v, hi: highest}
}

// selectFast appends to sel the rows of the n of the batch for which the filter is true, when
// the filter is one it has a fast path for: it is not ok otherwise, and sel is as it was.
func (n *node) selectFast(rows int, sel []int) ([]int, bool) {
	l := n.leaf
	if l == nil || l.kind != valInt || len(l.lits) != 1 {
		return sel, false
	}
	a := l.chain[len(l.chain)-1]
	if a.Type.Kind != array.Prim {
		return sel, false
	}
	op := l.op
	if op == opIn { // an Eq is kept as an In of one literal: IsNull has none, a real In has more
		op = opEq
	}
	sel = sel[:rows]
	sel = sel[:selectInts(sel, a, rangeOf(a.Type.PType, op, &l.lits[0]))]
	// A null row, or a row of a null parent, compares to nothing.
	for _, c := range l.chain {
		if c.Validity == nil {
			continue
		}
		kept := sel[:0]
		for _, i := range sel {
			if c.Validity.Get(i) {
				kept = append(kept, i)
			}
		}
		sel = kept
	}
	return sel, true
}

// selectInts writes in sel the rows of a, an array of integers, one per row of sel, that are in
// the range r. It returns their number.
func selectInts(sel []int, a *array.Array, r intRange) int {
	switch a.Type.PType {
	case array.I8:
		return selectRange[int8, uint8](sel, unsafex.Slice[int8](a.Data), int8(r.lo), int8(r.hi), r.out)
	case array.I16:
		return selectRange[int16, uint16](sel, unsafex.Slice[int16](a.Data), int16(r.lo), int16(r.hi), r.out)
	case array.I32:
		return selectRange[int32, uint32](sel, unsafex.Slice[int32](a.Data), int32(r.lo), int32(r.hi), r.out)
	case array.I64:
		return selectRange[int64, uint64](sel, unsafex.Slice[int64](a.Data), int64(r.lo), int64(r.hi), r.out)
	case array.U8:
		return selectRange[uint8, uint8](sel, a.Data, uint8(r.lo), uint8(r.hi), r.out)
	case array.U16:
		return selectRange[uint16, uint16](sel, unsafex.Slice[uint16](a.Data), uint16(r.lo), uint16(r.hi), r.out)
	case array.U32:
		return selectRange[uint32, uint32](sel, unsafex.Slice[uint32](a.Data), uint32(r.lo), uint32(r.hi), r.out)
	}
	return selectRange[uint64, uint64](sel, unsafex.Slice[uint64](a.Data), r.lo, r.hi, r.out)
}

// selectRange writes in sel the rows of vals, one per row of sel, that are from lo to hi, or
// outside when out is set. It returns their number. lo is at most hi: a value is in the range
// when it minus lo, as the unsigned integer U of its width, is at most hi minus lo, which is
// one comparison.
func selectRange[T unsafex.Integer, U unsafex.Unsigned](sel []int, vals []T, lo, hi T, out bool) int {
	k, span := 0, U(hi-lo)
	vals = vals[:len(sel)]
	if out {
		for i, v := range vals {
			sel[k] = i
			if U(v-lo) > span {
				k++
			}
		}
		return k
	}
	for i, v := range vals {
		sel[k] = i
		if U(v-lo) <= span {
			k++
		}
	}
	return k
}
