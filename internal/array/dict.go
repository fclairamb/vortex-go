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
		return gather(ar, out, values, codes.Data[:codes.Len], codes.Validity, codes.bound)
	case 2:
		return gather(ar, out, values, words[uint16](codes.Data), codes.Validity, codes.bound)
	case 4:
		return gather(ar, out, values, words[uint32](codes.Data), codes.Validity, codes.bound)
	}
	return gather(ar, out, values, words[uint64](codes.Data), codes.Validity, codes.bound)
}

// gather sets the rows of out to the values at codes, and reports whether every code of a row
// that valid does not mark null (nil marks none) is one of the values. There is a value.
// bound, when not 0, is above every code (Array.bound): when no code can be past the values,
// they are not checked.
func gather[C word](ar *Arena, out, values *Array, codes []C, valid *Bitmap, bound uint64) bool {
	last := ^C(0) // the largest code that is a value
	if n := uint64(values.Len - 1); n < uint64(last) {
		last = C(n)
	}
	checked := bound != 0 && bound-1 <= uint64(last)
	var ok bool
	if values.Type.Kind != Prim {
		out.Data, out.Elems = values.Data, values.Elems
		out.Starts, out.Ends = ar.Ints(len(codes), false), ar.Ints(len(codes), false)
		ok = gatherRanges(out.Starts, out.Ends, values.Starts, values.Ends, codes, valid, last, checked)
	} else {
		w := values.Type.PType.Width()
		out.Data = ar.Bytes(len(codes)*w, false)
		switch w {
		case 1:
			ok = gatherAt(out.Data, values.Data, codes, valid, last, checked)
		case 2:
			ok = gatherAt(words[uint16](out.Data), words[uint16](values.Data), codes, valid, last, checked)
		case 4:
			ok = gatherAt(words[uint32](out.Data), words[uint32](values.Data), codes, valid, last, checked)
		default:
			ok = gatherAt(words[uint64](out.Data), words[uint64](values.Data), codes, valid, last, checked)
		}
	}
	if !ok {
		return false
	}
	switch {
	case values.Validity != nil:
		out.vbits = Bitmap{Bytes: gatherValidity(ar, values.Validity, values.Len, codes, valid)}
		out.Validity = &out.vbits
	case valid != nil: // the codes go back to the scan before the rows do: their validity is copied
		bytes := ar.Bytes((valid.Off+len(codes)+7)/8, false)
		copy(bytes, valid.Bytes)
		out.vbits = Bitmap{Bytes: bytes, Off: valid.Off}
		out.Validity = &out.vbits
	}
	return true
}

// codesIn reports whether every code is at most last, which is below 1<<63. A code past last
// sets the high bit of last minus it, or has its own high bit set: 4 rows at a time, the test
// is a subtraction and an or, with no branch.
func codesIn[C word](codes []C, last C) bool {
	var b0, b1, b2, b3 uint64
	l := uint64(last)
	i := 0
	for ; i+4 <= len(codes); i += 4 {
		c := codes[i : i+4 : i+4]
		b0 |= l - uint64(c[0]) | uint64(c[0])
		b1 |= l - uint64(c[1]) | uint64(c[1])
		b2 |= l - uint64(c[2]) | uint64(c[2])
		b3 |= l - uint64(c[3]) | uint64(c[3])
	}
	for _, c := range codes[i:] {
		b0 |= l - uint64(c) | uint64(c)
	}
	return (b0|b1|b2|b3)>>63 == 0
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
// none. It reports whether every code of a row that is not null is at most last; checked says
// that they are. With null rows, a code above last is read as last.
func gatherAt[V integer, C word](dst, vals []V, codes []C, valid *Bitmap, last C, checked bool) bool {
	dst = dst[:len(codes)]
	if valid == nil {
		if !checked && !codesIn(codes, last) {
			return false
		}
		for i, c := range codes {
			dst[i] = vals[c]
		}
		return true
	}
	var most C
	for i, c := range codes {
		c &= rowMask[C](valid, i)
		most = max(most, c)
		dst[i] = vals[min(c, last)] & rowMask[V](valid, i)
	}
	return most <= last
}

// smallDict is the most values whose table of ranges or of validity a take makes, on the
// stack: for more, the table costs as much as it saves, and it would be memory to allocate.
const smallDict = 256

// gatherRanges is gatherAt for the ranges of the values: a null row is the empty range. It
// reports whether every code of a row that is not null is at most last; checked says that they
// are.
//
// When there are at most smallDict values, no more than rows, and no null code, the codes are
// checked first, and the range of a row is then one lookup in a table of the (start, end) of
// the values, with no clamp: the two ints are next to each other, under one test of the range.
func gatherRanges[C word](starts, ends, from, to []int, codes []C, valid *Bitmap, last C, checked bool) bool {
	starts, ends = starts[:len(codes)], ends[:len(codes)]
	if valid == nil && len(from) <= min(len(codes), smallDict) {
		if !checked && !codesIn(codes, last) {
			return false
		}
		var stack [smallDict][2]int
		pairs := stack[:len(from)]
		for k, s := range from {
			pairs[k] = [2]int{s, to[k]}
		}
		for i, c := range codes {
			p := &pairs[c]
			starts[i], ends[i] = p[0], p[1]
		}
		return true
	}
	var most C
	if valid == nil {
		for i, c := range codes {
			most = max(most, c)
			k := min(c, last)
			starts[i], ends[i] = from[k], to[k]
		}
		return most <= last
	}
	for i, c := range codes {
		c &= rowMask[C](valid, i)
		most = max(most, c)
		k, m := min(c, last), rowMask[int](valid, i)
		starts[i], ends[i] = from[k]&m, to[k]&m
	}
	return most <= last
}

// gatherValidity is the validity of the rows whose values are at codes: a row is valid when
// its value is, and when valid does not mark it null (nil marks none). The codes are values,
// of which there are vlen.
func gatherValidity[C word](ar *Arena, values *Bitmap, vlen int, codes []C, valid *Bitmap) []byte {
	bytes := ar.Bytes((len(codes)+7)/8, true)
	// A byte of the result is made in a register, from 8 codes, with no test: the nulls are
	// where they please, a test per row is one the processor cannot guess. When there are at
	// most smallDict values, no more than rows, the bit of a value is first put in a byte of
	// its own: a row is then one load.
	i := 0
	if vlen <= min(len(codes), smallDict) {
		var stack [smallDict]byte
		tab := stack[:vlen]
		for k := range tab {
			tab[k] = values.bit(k)
		}
		for ; i+8 <= len(codes); i += 8 {
			c := codes[i : i+8 : i+8]
			bytes[i>>3] = tab[c[0]] | tab[c[1]]<<1 | tab[c[2]]<<2 | tab[c[3]]<<3 |
				tab[c[4]]<<4 | tab[c[5]]<<5 | tab[c[6]]<<6 | tab[c[7]]<<7
		}
	}
	for ; i+8 <= len(codes); i += 8 {
		var b byte
		for k, c := range codes[i : i+8] {
			b |= values.bit(int(c)) << k
		}
		bytes[i>>3] = b
	}
	for ; i < len(codes); i++ {
		bytes[i>>3] |= values.bit(int(codes[i])) << (i & 7)
	}
	if valid != nil {
		for j := range bytes {
			bytes[j] &= valid.byteAt(8 * j)
		}
	}
	return bytes
}
