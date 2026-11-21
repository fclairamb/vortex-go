package vortex

import "github.com/fclairamb/vortex-go/internal/variant"

// The buffer of a column: the rows that are not in a segment yet. Write copies the rows of
// the caller into it (the caller may reuse its slices), a block at a time.

// newBody returns an empty buffer for a column of type t.
func newBody(t Type) *body {
	switch t.kind {
	case Null:
		return &body{kind: dNull}
	case Bool:
		return &body{kind: dBool}
	case Utf8, Binary, UUID:
		return &body{kind: dBytes}
	case KindDecimal:
		return &body{kind: dDec}
	case Variant:
		return &body{kind: dVars, venc: &variant.Encoder{Decimal: variantDecimalOf}}
	case Struct:
		b := &body{kind: dStruct, kids: make([]Data, len(t.fields))}
		for i, f := range t.fields {
			b.kids[i] = Data{newBody(f.Type)}
		}
		return b
	case List, FixedSizeList:
		return &body{kind: dList, offs: []int{0}, kids: []Data{{newBody(*t.elem)}}}
	case Map:
		entries := &body{kind: dStruct, kids: []Data{{newBody(*t.elem)}, {newBody(*t.value)}}}
		return &body{kind: dList, offs: []int{0}, kids: []Data{{entries}}}
	default:
		return &body{kind: dPrim, pk: storageKind(t)}
	}
}

// resetBody empties a buffer and keeps its memory.
func resetBody(b *body) {
	b.raw, b.bools, b.nulls, b.decs = b.raw[:0], b.bools[:0], b.nulls[:0], b.decs[:0]
	clear(b.strs)
	clear(b.bins)
	clear(b.vbins)
	clear(b.vars)
	b.strs, b.bins, b.vbins, b.vars = b.strs[:0], b.bins[:0], b.vbins[:0], b.vars[:0]
	b.n = 0
	if b.offs != nil {
		b.offs = b.offs[:1]
	}
	for _, k := range b.kids {
		resetBody(k.body)
	}
}

// copyBytes copies v into the arena, which only grows by new blocks: the slices handed out
// before stay valid.
func copyBytes[S ~string | ~[]byte](arena *[]byte, v S) []byte {
	if cap(*arena)-len(*arena) < len(v) {
		*arena = make([]byte, 0, max(len(v), 2*cap(*arena), arenaBlock))
	}
	at := len(*arena)
	*arena = append(*arena, v...)
	return (*arena)[at:len(*arena):len(*arena)]
}

// appendRows adds the rows lo to hi of src, a Data of type t, to the buffer dst.
func appendRows(dst *body, src *body, t Type, lo, hi int, arena *[]byte) {
	if t.nullable {
		if src.nulls != nil && t.kind != Null {
			dst.nulls = append(dst.nulls, src.nulls[lo:hi]...)
		} else {
			dst.nulls = append(dst.nulls, make([]bool, hi-lo)...)
		}
	}
	switch dst.kind {
	case dPrim:
		w := numberWidth(dst.pk)
		dst.raw = append(dst.raw, src.raw[lo*w:hi*w]...)
	case dBool:
		dst.bools = append(dst.bools, src.bools[lo:hi]...)
	case dBytes:
		appendBytes(dst, src, lo, hi, arena)
	case dDec:
		dst.decs = append(dst.decs, src.decs[lo:hi]...)
	case dVars:
		appendVariants(dst, src, lo, hi, arena)
	case dStruct:
		for i, f := range t.fields {
			appendRows(dst.kids[i].body, src.kids[i].body, f.Type, lo, hi, arena)
		}
	case dList:
		appendList(dst, src, t, lo, hi, arena)
	}
	dst.n += hi - lo
}

// appendVariants encodes the values lo to hi of src, which checkVariants checked, into the
// metadata and the value of each row of dst.
func appendVariants(dst, src *body, lo, hi int, arena *[]byte) {
	for _, v := range src.vars[lo:hi] {
		meta, value, _ := dst.venc.Encode(v)
		dst.bins = append(dst.bins, copyBytes(arena, meta))
		dst.vbins = append(dst.vbins, copyBytes(arena, value))
	}
}

// appendBytes copies the strings of a utf8, binary or uuid column.
func appendBytes(dst, src *body, lo, hi int, arena *[]byte) {
	for i := lo; i < hi; i++ {
		if src.kind == dStr {
			dst.bins = append(dst.bins, copyBytes(arena, src.strs[i]))
		} else {
			dst.bins = append(dst.bins, copyBytes(arena, src.bins[i]))
		}
	}
}

// appendList adds the rows of a list, a fixed-size list or a map: the offsets, rebased on the
// elements already in the buffer, and the elements the rows cover.
func appendList(dst, src *body, t Type, lo, hi int, arena *[]byte) {
	from, to := src.offs[lo], src.offs[hi]
	base := dst.kids[0].n - from
	for i := lo + 1; i <= hi; i++ {
		dst.offs = append(dst.offs, base+src.offs[i])
	}
	d, s := dst.kids[0].body, src.kids[0].body
	if t.kind == Map {
		appendRows(d.kids[0].body, s.kids[0].body, *t.elem, from, to, arena)
		appendRows(d.kids[1].body, s.kids[1].body, *t.value, from, to, arena)
		d.n += to - from
		return
	}
	appendRows(d, s, *t.elem, from, to, arena)
}

// sizeOf estimates the bytes the rows lo to hi of a buffer take uncompressed.
func sizeOf(b *body, t Type, lo, hi int) int {
	n := hi - lo
	size := 0
	if t.nullable {
		size = (n + 7) / 8
	}
	switch t.kind {
	case Null:
	case Bool:
		size += (n + 7) / 8
	case Utf8, Binary:
		for _, v := range b.bins[lo:hi] {
			size += len(v) + 16
		}
	case UUID:
		size += 16 * n
	case KindDecimal:
		size += n * decimalWidth(t.precision)
	case Variant:
		for i := lo; i < hi; i++ {
			size += len(b.bins[i]) + len(b.vbins[i]) + 32
		}
	case Struct:
		for i, f := range t.fields {
			size += sizeOf(b.kids[i].body, f.Type, lo, hi)
		}
	case List, FixedSizeList, Map:
		size += 8*n + sizeOfElems(b, t, lo, hi)
	default:
		size += n * numberWidth(storageKind(t))
	}
	return size
}

func sizeOfElems(b *body, t Type, lo, hi int) int {
	from, to := b.offs[lo], b.offs[hi]
	elems := b.kids[0].body
	if t.kind == Map {
		return sizeOf(elems.kids[0].body, *t.elem, from, to) + sizeOf(elems.kids[1].body, *t.value, from, to)
	}
	return sizeOf(elems, *t.elem, from, to)
}
