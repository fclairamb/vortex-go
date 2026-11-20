package array

// Bitmap is a sequence of bits, the first one being bit Off of Bytes (least significant
// bit first).
type Bitmap struct {
	Bytes []byte
	Off   int
}

// Get returns bit i.
func (b Bitmap) Get(i int) bool {
	i += b.Off
	return b.Bytes[i>>3]&(1<<(i&7)) != 0
}

// Slice returns the bits from bit i. The offset stays under 8.
func (b Bitmap) Slice(i int) Bitmap {
	i += b.Off
	return Bitmap{Bytes: b.Bytes[i>>3:], Off: i & 7}
}

// Array is a decoded array. Only the fields of its Type are set.
type Array struct {
	// Skipped is set when the array was not decoded because of an unsupported encoding,
	// and the caller accepted it (Options.Tolerate). The other fields are empty then.
	Skipped error
	// Validity is nil when every value is valid. It is ignored for the Null type.
	Validity *Bitmap
	Data     []byte // Prim: the values, little-endian
	Fields   []*Array
	Type     DType
	Bits     Bitmap // Bool: the values
	Len      int
}

// Null reports whether row i is null.
func (a *Array) Null(i int) bool {
	if a.Type.Kind == Null {
		return true
	}
	return a.Validity != nil && !a.Validity.Get(i)
}

// Slice returns the n rows from row off, sharing the memory.
func (a *Array) Slice(off, n int) *Array {
	out := *a
	out.Len = n
	if a.Skipped != nil {
		return &out
	}
	if a.Validity != nil {
		v := a.Validity.Slice(off)
		out.Validity = &v
	}
	switch a.Type.Kind {
	case Bool:
		out.Bits = a.Bits.Slice(off)
	case Prim:
		w := a.Type.PType.Width()
		out.Data = a.Data[off*w : (off+n)*w]
	case Struct:
		out.Fields = make([]*Array, len(a.Fields))
		for i, f := range a.Fields {
			out.Fields[i] = f.Slice(off, n)
		}
	}
	return &out
}

// bitWriter appends bits to a growing bitmap.
type bitWriter struct {
	bytes []byte
	n     int
}

func newBitWriter(n int) *bitWriter { return &bitWriter{bytes: make([]byte, (n+7)/8)} }

func (w *bitWriter) set(v bool) {
	if v {
		w.bytes[w.n>>3] |= 1 << (w.n & 7)
	}
	w.n++
}

// copyBits appends the n bits of b from bit 0, all ones when b is nil.
func (w *bitWriter) copyBits(b *Bitmap, n int) {
	for i := range n {
		w.set(b == nil || b.Get(i))
	}
}

// Concat joins arrays of type t into one.
func Concat(t DType, parts []*Array) *Array {
	for t.Kind == Ext {
		t = *t.Storage
	}
	if len(parts) == 1 {
		return parts[0]
	}
	total := 0
	anyNull := false
	for _, p := range parts {
		if p.Skipped != nil {
			return &Array{Type: t, Len: total + p.Len, Skipped: p.Skipped}
		}
		total += p.Len
		anyNull = anyNull || p.Validity != nil
	}
	out := &Array{Type: t, Len: total}
	if anyNull {
		w := newBitWriter(total)
		for _, p := range parts {
			w.copyBits(p.Validity, p.Len)
		}
		out.Validity = &Bitmap{Bytes: w.bytes}
	}
	switch t.Kind {
	case Bool:
		w := newBitWriter(total)
		for _, p := range parts {
			w.copyBits(&p.Bits, p.Len)
		}
		out.Bits = Bitmap{Bytes: w.bytes}
	case Prim:
		out.Data = make([]byte, 0, total*t.PType.Width())
		for _, p := range parts {
			out.Data = append(out.Data, p.Data...)
		}
	case Struct:
		out.Fields = make([]*Array, len(t.Fields))
		for i, ft := range t.Fields {
			col := make([]*Array, len(parts))
			for j, p := range parts {
				col[j] = p.Fields[i]
			}
			out.Fields[i] = Concat(ft, col)
		}
	}
	return out
}
