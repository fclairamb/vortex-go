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
	// Elems is the elements of a List, a FixedSizeList and a Map (a struct of key and value).
	Elems *Array
	// Var is the content of a Variant array.
	Var *VariantData
	// Data is, for Prim, the values, little-endian; for Decimal, the values at Width bytes
	// each, little-endian two's complement; for Utf8 and Binary, the bytes Starts and Ends
	// point into.
	Data []byte
	// Starts and Ends are, per row, the range of Data (Utf8, Binary) or of Elems (List, Map)
	// of the row. Ranges may overlap and come in any order, but stay inside their target.
	Starts, Ends []int
	Fields       []*Array
	Bits         Bitmap // Bool: the values
	vbits        Bitmap // the Validity of an array made by SliceInto
	Type         DType
	Width        int // Decimal: the bytes of a stored value
	Len          int
	// VOff is, for a Variant array, the row of Var that row 0 is.
	VOff int
}

// Null reports whether row i is null.
func (a *Array) Null(i int) bool {
	if a.Type.Kind == Null {
		return true
	}
	return a.Validity != nil && !a.Validity.Get(i)
}

// Slice returns the n rows from row off, sharing the memory.
func (a *Array) Slice(off, n int) *Array { return a.SliceInto(new(Array), off, n) }

// SliceInto is Slice into out, which it overwrites and returns. It allocates nothing when out
// was filled by an earlier SliceInto of an array of the same shape: the children of a struct
// or of a fixed-size list are sliced into the ones out already has. out must not share any
// node with a.
func (a *Array) SliceInto(out *Array, off, n int) *Array {
	fields, elems := out.Fields, out.Elems
	*out = *a
	out.Len = n
	if a.Skipped != nil {
		return out
	}
	if a.Validity != nil {
		out.vbits = a.Validity.Slice(off)
		out.Validity = &out.vbits
	}
	switch a.Type.Kind {
	case Bool:
		out.Bits = a.Bits.Slice(off)
	case Prim:
		w := a.Type.PType.Width()
		out.Data = a.Data[off*w : (off+n)*w]
	case Decimal:
		out.Data = a.Data[off*a.Width : (off+n)*a.Width]
	case Utf8, Binary, List, Map:
		out.Starts, out.Ends = a.Starts[off:off+n], a.Ends[off:off+n]
	case Variant:
		out.VOff += off
	case FixedSizeList:
		if elems == nil {
			elems = new(Array)
		}
		out.Elems = a.Elems.SliceInto(elems, off*a.Type.Size, n*a.Type.Size)
	case Struct:
		if cap(fields) < len(a.Fields) {
			fields = make([]*Array, len(a.Fields))
		}
		out.Fields = fields[:len(a.Fields)]
		for i, f := range a.Fields {
			if out.Fields[i] == nil {
				out.Fields[i] = new(Array)
			}
			f.SliceInto(out.Fields[i], off, n)
		}
	}
	return out
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
	var skipped error
	for _, p := range parts {
		total += p.Len
		anyNull = anyNull || p.Validity != nil
		if p.Skipped != nil {
			skipped = p.Skipped
		}
	}
	if skipped != nil {
		return &Array{Type: t, Len: total, Skipped: skipped}
	}
	out := &Array{Type: t, Len: total}
	if anyNull {
		w := newBitWriter(total)
		for _, p := range parts {
			w.copyBits(p.Validity, p.Len)
		}
		out.Validity = &Bitmap{Bytes: w.bytes}
	}
	concatValues(out, t, parts)
	return out
}

// concatValues joins the values of the arrays, whose type is t, into out.
func concatValues(out *Array, t DType, parts []*Array) {
	switch t.Kind {
	case Bool:
		w := newBitWriter(out.Len)
		for _, p := range parts {
			w.copyBits(&p.Bits, p.Len)
		}
		out.Bits = Bitmap{Bytes: w.bytes}
	case Prim:
		out.Data = make([]byte, 0, out.Len*t.PType.Width())
		for _, p := range parts {
			out.Data = append(out.Data, p.Data...)
		}
	case Decimal:
		concatDecimal(out, parts)
	case Utf8, Binary:
		concatRanges(out, parts)
	case List, Map:
		concatRanges(out, parts)
		out.Elems = concatElems(*t.Elem, parts)
	case FixedSizeList:
		out.Elems = concatElems(*t.Elem, parts)
	case Struct:
		out.Fields = concatFields(t, parts)
	case Variant:
		out.Var = &VariantData{Parts: parts}
	}
}

// concatElems joins the elements of list arrays.
func concatElems(t DType, parts []*Array) *Array {
	elems := make([]*Array, len(parts))
	for i, p := range parts {
		elems[i] = p.Elems
	}
	return Concat(t, elems)
}

// concatRanges joins the ranges of byte and list arrays. A part's ranges point into its Data
// (bytes) or its Elems (lists), which are laid one after the other.
func concatRanges(out *Array, parts []*Array) {
	out.Starts, out.Ends = make([]int, 0, out.Len), make([]int, 0, out.Len)
	base := 0
	for _, p := range parts {
		for i := range p.Starts {
			out.Starts = append(out.Starts, p.Starts[i]+base)
			out.Ends = append(out.Ends, p.Ends[i]+base)
		}
		if p.Elems != nil {
			base += p.Elems.Len
		} else {
			out.Data = append(out.Data, p.Data...)
			base += len(p.Data)
		}
	}
}

// concatDecimal joins decimal arrays, widening the narrower ones to the widest.
func concatDecimal(out *Array, parts []*Array) {
	for _, p := range parts {
		out.Width = max(out.Width, p.Width)
	}
	for _, p := range parts {
		for i := 0; i < len(p.Data); i += p.Width {
			out.Data = append(out.Data, p.Data[i:i+p.Width]...)
			ext := byte(0)
			if p.Data[i+p.Width-1]&0x80 != 0 {
				ext = 0xff
			}
			for range out.Width - p.Width {
				out.Data = append(out.Data, ext)
			}
		}
	}
}

// concatFields joins the fields of struct arrays, field by field.
func concatFields(t DType, parts []*Array) []*Array {
	fields := make([]*Array, len(t.Fields))
	for i, ft := range t.Fields {
		col := make([]*Array, len(parts))
		for j, p := range parts {
			col[j] = p.Fields[i]
		}
		fields[i] = Concat(ft.Type, col)
	}
	return fields
}
