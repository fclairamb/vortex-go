package array

// take returns the rows idx of a, in that order. An index of -1 is a null row. The indexes
// are not checked: the caller has.
func take(a *Array, idx []int) *Array {
	out := &Array{Type: a.Type, Len: len(idx), Width: a.Width}
	if a.Skipped != nil {
		out.Skipped = a.Skipped
		return out
	}
	out.Validity = takeValidity(a, idx)
	switch a.Type.Kind {
	case Bool:
		w := newBitWriter(len(idx))
		for _, i := range idx {
			w.set(i >= 0 && a.Bits.Get(i))
		}
		out.Bits = Bitmap{Bytes: w.bytes}
	case Prim:
		out.Data = takeFixed(a.Data, a.Type.PType.Width(), idx)
	case Decimal:
		out.Data = takeFixed(a.Data, a.Width, idx)
	case Utf8, Binary, List, Map:
		out.Data, out.Elems = a.Data, a.Elems
		out.Starts, out.Ends = takeRanges(a, idx)
	case FixedSizeList:
		size := a.Type.Size
		elems := make([]int, 0, len(idx)*size)
		for _, i := range idx {
			for k := range size {
				elems = append(elems, takeElem(i, size, k))
			}
		}
		out.Elems = take(a.Elems, elems)
	case Struct:
		out.Fields = make([]*Array, len(a.Fields))
		for k, f := range a.Fields {
			out.Fields[k] = take(f, idx)
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

// takeValidity is the validity of the rows idx of a, nil when they are all valid.
func takeValidity(a *Array, idx []int) *Bitmap {
	need := a.Validity != nil
	for _, i := range idx {
		need = need || i < 0
	}
	if !need {
		return nil
	}
	w := newBitWriter(len(idx))
	for _, i := range idx {
		w.set(i >= 0 && (a.Validity == nil || a.Validity.Get(i)))
	}
	return &Bitmap{Bytes: w.bytes}
}

// takeFixed takes the values of w bytes at the rows idx of data.
func takeFixed(data []byte, w int, idx []int) []byte {
	out := make([]byte, len(idx)*w)
	for k, i := range idx {
		if i >= 0 {
			copy(out[k*w:], data[i*w:(i+1)*w])
		}
	}
	return out
}

// takeRanges takes the ranges of the rows idx of a.
func takeRanges(a *Array, idx []int) (starts, ends []int) {
	starts, ends = make([]int, len(idx)), make([]int, len(idx))
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
