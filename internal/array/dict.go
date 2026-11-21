package array

import "fmt"

// decodeDict decodes vortex.dict: metadata {values_len, codes_ptype, is_nullable_codes?,
// all_values_referenced?}, children the codes and the values. A null code is a null row. The
// values are decoded once, whatever the number of rows.
func decodeDict(n *Node, t DType, rows int) (*Array, error) {
	meta, err := readMessage(n.Metadata())
	if err != nil {
		return nil, err
	}
	vlen, err := count("values_len", meta.varint[1])
	if err != nil {
		return nil, err
	}
	p, err := intType("codes_ptype", meta.varint[2])
	if err != nil {
		return nil, err
	}
	if n.NumBuffers() != 0 || n.NumChildren() != 2 {
		return nil, fmt.Errorf("expected no buffer and 2 children, got %d and %d", n.NumBuffers(), n.NumChildren())
	}
	ct := DType{Kind: Prim, PType: p, Nullable: t.Nullable}
	if meta.hasVarint[3] {
		ct.Nullable = meta.varint[3] != 0
	}
	codes, err := n.Child(0, ct, rows)
	if err != nil {
		return nil, fmt.Errorf("codes: %w", err)
	}
	values, err := n.Child(1, t, vlen)
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}
	return TakeDict(n.ctx.opt.Arena, values, codes, p, t)
}

// maxInt is the largest of v, 0 when there is none.
func maxInt(v []int) int {
	most := 0
	for _, x := range v {
		most = max(most, x)
	}
	return most
}

// TakeDict makes the array of type t, in the arena ar, whose row i is the value of values at the code of row i
// of codes, an array of integers of type p. A null code is a null row. A code past the values
// is an error. When values or codes were skipped, the result is skipped too.
func TakeDict(ar *Arena, values, codes *Array, p PType, t DType) (*Array, error) {
	return TakeDictInto(new(Array), ar, values, codes, p, t)
}

// gathers reports whether takeFast reads values of that kind.
func gathers(k Kind) bool { return k == Prim || k == Utf8 || k == Binary || k == List || k == Map }

// TakeDictInto is TakeDict into out, which it overwrites and returns when the values are
// numbers, bytes or lists: it then allocates nothing but the buffers of the rows.
func TakeDictInto(out *Array, ar *Arena, values, codes *Array, p PType, t DType) (*Array, error) {
	if values.Skipped != nil || codes.Skipped != nil {
		err := values.Skipped
		if err == nil {
			err = codes.Skipped
		}
		return &Array{Type: t, Len: codes.Len, Skipped: err}, nil
	}
	if takeFast(ar, out, values, codes, p) {
		out.Type.Nullable = t.Nullable
		return out, nil
	}
	vlen := values.Len
	idx, err := toInts(ar, codes, "codes", p, codes.Validity)
	if err != nil {
		return nil, err
	}
	if maxInt(idx) >= vlen { // the null rows are code 0 here: only a dictionary without value fails them
		for i, c := range idx {
			if c >= vlen && !codes.Null(i) {
				return nil, fmt.Errorf("code %d at row %d is past the %d values", c, i, vlen)
			}
		}
	}
	if codes.Validity != nil {
		for i := range idx {
			if !codes.Validity.Get(i) {
				idx[i] = -1
			}
		}
	}
	return withType(takeRows(ar, values, idx, codes.Validity != nil), t), nil
}

// takeFast is TakeDict, into out, for values that are numbers, bytes or lists: a code is read
// where it is, and a null row is the value 0 or the empty range. It is not ok when there is no such
// path, or when a code is wrong: the caller then names it.
func takeFast(ar *Arena, out, values, codes *Array, p PType) bool {
	if values.Len == 0 || !gathers(values.Type.Kind) {
		return false
	}
	*out = Array{Type: values.Type, Len: codes.Len}
	switch p.Width() {
	case 1:
		return gather(ar, out, values, codes.Data[:codes.Len], codes.Validity)
	case 2:
		return gather(ar, out, values, words[uint16](codes.Data), codes.Validity)
	case 4:
		return gather(ar, out, values, words[uint32](codes.Data), codes.Validity)
	}
	return gather(ar, out, values, words[uint64](codes.Data), codes.Validity)
}

// gather sets the rows of out to the values at codes, and reports whether every code of a row
// that valid does not mark null (nil marks none) is one of the values. There is a value.
func gather[C word](ar *Arena, out, values *Array, codes []C, valid *Bitmap) bool {
	last := ^C(0) // the largest code that is a value
	if n := uint64(values.Len - 1); n < uint64(last) {
		last = C(n)
	}
	var most C
	if values.Type.Kind != Prim {
		out.Data, out.Elems = values.Data, values.Elems
		out.Starts, out.Ends = ar.Ints(len(codes), false), ar.Ints(len(codes), false)
		most = gatherRanges(out.Starts, out.Ends, values.Starts, values.Ends, codes, valid, last)
	} else {
		w := values.Type.PType.Width()
		out.Data = ar.Bytes(len(codes)*w, false)
		switch w {
		case 1:
			most = gatherAt(out.Data, values.Data, codes, valid, last)
		case 2:
			most = gatherAt(words[uint16](out.Data), words[uint16](values.Data), codes, valid, last)
		case 4:
			most = gatherAt(words[uint32](out.Data), words[uint32](values.Data), codes, valid, last)
		default:
			most = gatherAt(words[uint64](out.Data), words[uint64](values.Data), codes, valid, last)
		}
	}
	if most > last {
		return false
	}
	switch {
	case values.Validity != nil:
		out.vbits = Bitmap{Bytes: gatherValidity(ar, values.Validity, codes, valid)}
		out.Validity = &out.vbits
	case valid != nil: // the codes go back to the scan before the rows do: their validity is copied
		bytes := ar.Bytes((valid.Off+len(codes)+7)/8, false)
		copy(bytes, valid.Bytes)
		out.vbits = Bitmap{Bytes: bytes, Off: valid.Off}
		out.Validity = &out.vbits
	}
	return true
}

// rowMask is all ones when row i of valid is set, and 0 otherwise.
func rowMask[T integer | int](valid *Bitmap, i int) T {
	var m T
	if valid.Get(i) {
		m = ^m
	}
	return m
}

// gatherAt sets dst[i] to vals[codes[i]], and to 0 for a row that valid marks null: nil marks
// none. A code above last is read as last. It returns the largest code of the rows that are
// not null.
func gatherAt[V integer, C word](dst, vals []V, codes []C, valid *Bitmap, last C) C {
	var most C
	dst = dst[:len(codes)]
	if valid == nil {
		for i, c := range codes {
			most = max(most, c)
			dst[i] = vals[min(c, last)]
		}
		return most
	}
	for i, c := range codes {
		c &= rowMask[C](valid, i)
		most = max(most, c)
		dst[i] = vals[min(c, last)] & rowMask[V](valid, i)
	}
	return most
}

// gatherRanges is gatherAt for the ranges of the values: a null row is the empty range.
func gatherRanges[C word](starts, ends, from, to []int, codes []C, valid *Bitmap, last C) C {
	var most C
	starts, ends = starts[:len(codes)], ends[:len(codes)]
	if valid == nil {
		for i, c := range codes {
			most = max(most, c)
			k := min(c, last)
			starts[i], ends[i] = from[k], to[k]
		}
		return most
	}
	for i, c := range codes {
		c &= rowMask[C](valid, i)
		most = max(most, c)
		k, m := min(c, last), rowMask[int](valid, i)
		starts[i], ends[i] = from[k]&m, to[k]&m
	}
	return most
}

// gatherValidity is the validity of the rows whose values are at codes: a row is valid when
// its value is, and when valid does not mark it null (nil marks none). The codes are values.
func gatherValidity[C word](ar *Arena, values *Bitmap, codes []C, valid *Bitmap) []byte {
	bytes := ar.Bytes((len(codes)+7)/8, true)
	for i, c := range codes {
		if (valid == nil || valid.Get(i)) && values.Get(int(c)) {
			bytes[i>>3] |= 1 << (i & 7)
		}
	}
	return bytes
}
