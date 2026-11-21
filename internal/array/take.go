package array

import "slices"

// take returns the rows idx of a, in that order, in buffers of the arena ar. An index of -1 is a null row. The indexes
// are not checked: the caller has.
func take(ar *Arena, a *Array, idx []int) *Array {
	return takeRows(ar, a, idx, slices.ContainsFunc(idx, func(i int) bool { return i < 0 }))
}

// takeRows is take, the caller knowing whether an index is -1.
func takeRows(ar *Arena, a *Array, idx []int, nulls bool) *Array {
	out := &Array{Type: a.Type, Len: len(idx), Width: a.Width}
	if a.Skipped != nil {
		out.Skipped = a.Skipped
		return out
	}
	if nulls || a.Validity != nil {
		out.Validity = takeValidity(ar, a, idx)
	}
	switch a.Type.Kind {
	case Bool:
		w := &bitWriter{bytes: ar.Bytes((len(idx)+7)/8, true)}
		for _, i := range idx {
			w.set(i >= 0 && a.Bits.Get(i))
		}
		out.Bits = Bitmap{Bytes: w.bytes}
	case Prim:
		out.Data = takeFixed(ar, a.Data, a.Type.PType.Width(), idx)
	case Decimal:
		out.Data = takeFixed(ar, a.Data, a.Width, idx)
	case Utf8, Binary, List, Map:
		out.Data, out.Elems = a.Data, a.Elems
		out.Starts, out.Ends = takeRanges(ar, a, idx, nulls)
	case FixedSizeList:
		size := a.Type.Size
		elems := make([]int, 0, len(idx)*size)
		for _, i := range idx {
			for k := range size {
				elems = append(elems, takeElem(i, size, k))
			}
		}
		out.Elems = takeRows(ar, a.Elems, elems, nulls)
	case Struct:
		out.Fields = make([]*Array, len(a.Fields))
		for k, f := range a.Fields {
			out.Fields[k] = takeRows(ar, f, idx, nulls)
		}
	}
	return out
}

// takeElem is element k of the list at row i of a fixed size list of the given size, -1 for a
// null row.
func takeElem(i, size, k int) int {
	if i < 0 {
		return -1
	}
	return i*size + k
}

// takeValidity is the validity of the rows idx of a.
func takeValidity(ar *Arena, a *Array, idx []int) *Bitmap {
	w := &bitWriter{bytes: ar.Bytes((len(idx)+7)/8, true)}
	for _, i := range idx {
		w.set(i >= 0 && (a.Validity == nil || a.Validity.Get(i)))
	}
	return &Bitmap{Bytes: w.bytes}
}

// takeFixed takes the values of w bytes at the rows idx of data.
func takeFixed(ar *Arena, data []byte, w int, idx []int) []byte {
	out := ar.Bytes(len(idx)*w, true)
	for k, i := range idx {
		if i >= 0 {
			copy(out[k*w:], data[i*w:(i+1)*w])
		}
	}
	return out
}

// takeRanges takes the ranges of the rows idx of a.
func takeRanges(ar *Arena, a *Array, idx []int, nulls bool) (starts, ends []int) {
	if !nulls {
		starts, ends = ar.Ints(len(idx), false), ar.Ints(len(idx), false)
		from, to := a.Starts, a.Ends
		ends = ends[:len(starts)]
		for k, i := range idx[:len(starts)] {
			starts[k], ends[k] = from[i], to[i]
		}
		return starts, ends
	}
	starts, ends = ar.Ints(len(idx), true), ar.Ints(len(idx), true)
	for k, i := range idx {
		if i >= 0 {
			starts[k], ends[k] = a.Starts[i], a.Ends[i]
		}
	}
	return starts, ends
}

// withType gives a the type t: its own, with the nullability of t.
func withType(a *Array, t DType) *Array {
	out := *a
	out.Type.Nullable = t.Nullable
	return &out
}
