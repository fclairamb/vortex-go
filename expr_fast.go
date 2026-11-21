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

// rangeOf is the range of the values v of type p for which "v op c" holds. It is not ok for
// an operator that is not a comparison with one literal.
func rangeOf(p array.PType, op exprOp, c *lit) (intRange, bool) {
	if op < opEq || op > opGe {
		return intRange{}, false
	}
	bits := 8 * p.Width()
	lowest, highest := uint64(0), ^uint64(0)>>(64-bits)
	if p.Signed() {
		highest >>= 1
		lowest = ^highest // the sign-extended minimum
	}
	all, none := intRange{lo: lowest, hi: highest}, intRange{lo: lowest, hi: highest, out: true}
	if !fits(c, bits, p.Signed()) { // the literal is under every value, or over
		if op == opNe || op != opEq && (op == opLt || op == opLe) != c.neg {
			return all, true
		}
		return none, true
	}
	v := c.mag
	if c.neg {
		v = -v
	}
	switch op {
	case opEq:
		return intRange{lo: v, hi: v}, true
	case opNe:
		return intRange{lo: v, hi: v, out: true}, true
	case opLt:
		if v == lowest {
			return none, true
		}
		return intRange{lo: lowest, hi: v - 1}, true
	case opLe:
		return intRange{lo: lowest, hi: v}, true
	case opGt:
		if v == highest {
			return none, true
		}
		return intRange{lo: v + 1, hi: highest}, true
	}
	return intRange{lo: v, hi: highest}, true
}

// selectFast appends to sel the rows of the n of the batch for which the filter is true, when
// the filter is one it has a fast path for: it is not ok otherwise, and sel is as it was.
func (n *node) selectFast(rows int, sel []int) ([]int, bool) {
	l := n.leaf
	if l == nil || l.kind != valInt || len(l.lits) != 1 {
		return sel, false
	}
	a := l.chain[len(l.chain)-1]
	if a.Type.Kind != array.Prim || a.Skipped != nil {
		return sel, false
	}
	r, ok := rangeOf(a.Type.PType, l.op, &l.lits[0])
	if !ok {
		return sel, false
	}
	sel = sel[:rows]
	var k int
	switch a.Type.PType {
	case array.I8:
		k = selectRange(sel, unsafex.Slice[int8](a.Data), int8(r.lo), int8(r.hi), r.out)
	case array.I16:
		k = selectRange(sel, unsafex.Slice[int16](a.Data), int16(r.lo), int16(r.hi), r.out)
	case array.I32:
		k = selectRange(sel, unsafex.Slice[int32](a.Data), int32(r.lo), int32(r.hi), r.out)
	case array.I64:
		k = selectRange(sel, unsafex.Slice[int64](a.Data), int64(r.lo), int64(r.hi), r.out)
	case array.U8:
		k = selectRange(sel, a.Data, uint8(r.lo), uint8(r.hi), r.out)
	case array.U16:
		k = selectRange(sel, unsafex.Slice[uint16](a.Data), uint16(r.lo), uint16(r.hi), r.out)
	case array.U32:
		k = selectRange(sel, unsafex.Slice[uint32](a.Data), uint32(r.lo), uint32(r.hi), r.out)
	default:
		k = selectRange(sel, unsafex.Slice[uint64](a.Data), r.lo, r.hi, r.out)
	}
	sel = sel[:k]
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

// selectRange writes in sel the rows of vals, one per row of sel, that are from lo to hi, or
// outside when out is set. It returns their number.
func selectRange[T unsafex.Number](sel []int, vals []T, lo, hi T, out bool) int {
	k := 0
	for i, v := range vals[:len(sel)] {
		sel[k] = i
		if (v >= lo && v <= hi) != out {
			k++
		}
	}
	return k
}
