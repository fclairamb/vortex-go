package array

import (
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/fclairamb/vortex-go/internal/protowire"
	"github.com/fclairamb/vortex-go/internal/variant"
)

// VariantData is what a Variant array holds. There are three shapes.
//
// A Parquet one (vortex.parquet.variant) has Metadata, the key dictionary of each row, Value,
// the encoded value of each row (nil when the array has none), and Typed, the shredded
// typed_value (nil when it has none), in the layout of Parquet: the fields of a shredded
// object are structs of a "value" and a "typed_value".
//
// A logical one (vortex.variant with a shredded child) has Core, a Variant array of the first
// shape without typed_value, and Shredded, the typed values with no wrapper: a shredded
// object is a struct of its fields, a field that is itself partly shredded being a Variant
// array.
//
// A chunked one has the Parts it was joined from.
type VariantData struct {
	Metadata, Value, Typed *Array
	Core, Shredded         *Array
	Parts                  []*Array
	// TypedType is the type of Typed or of Shredded, which the arrays alone cannot tell: an
	// extension type decodes as its storage.
	TypedType DType
}

// decodeParquetVariant decodes vortex.parquet.variant: metadata {has_value, typed_value_dtype,
// value_nullable}; children the validity when there is one, the metadata (binary), the value
// when has_value and the typed_value when it has a type.
func decodeParquetVariant(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Variant {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if n.NumBuffers() != 0 {
		return nil, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	hasValue, valueNullable, typedType, err := parquetVariantMetadata(n.Metadata())
	if err != nil {
		return nil, err
	}
	want := 1 + b2i(hasValue) + b2i(typedType != nil)
	if c := n.NumChildren(); c != want && c != want+1 {
		return nil, fmt.Errorf("expected %d or %d children, got %d", want, want+1, n.NumChildren())
	}
	a := &Array{Type: t, Len: rows, Var: &VariantData{}}
	next := n.NumChildren() - want
	if next == 1 {
		if a.Validity, err = n.Validity(0, rows); err != nil {
			return nil, err
		}
	}
	d := a.Var
	if d.Metadata, err = n.Child(next, DType{Kind: Binary}, rows); err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	next++
	if hasValue {
		if d.Value, err = n.Child(next, DType{Kind: Binary, Nullable: valueNullable}, rows); err != nil {
			return nil, fmt.Errorf("value: %w", err)
		}
		next++
	}
	if typedType != nil {
		d.TypedType = *typedType
		if d.Typed, err = n.Child(next, d.TypedType, rows); err != nil {
			return nil, fmt.Errorf("typed_value: %w", err)
		}
	}
	return a, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// parquetVariantMetadata reads the protobuf of vortex.parquet.variant.
func parquetVariantMetadata(meta []byte) (hasValue, valueNullable bool, typed *DType, err error) {
	it := protowire.NewIter(meta)
	for it.Next() {
		switch {
		case it.Num() == 1 && it.Type() == protowire.Varint:
			hasValue = it.Uint() != 0
		case it.Num() == 2 && it.Type() == protowire.Bytes:
			var t DType
			if t, err = ParseDType(it.Bytes()); err != nil {
				return false, false, nil, fmt.Errorf("typed_value_dtype: %w", err)
			}
			typed = &t
		case it.Num() == 3 && it.Type() == protowire.Varint:
			valueNullable = it.Uint() != 0
		}
	}
	if err = it.Err(); err != nil {
		return false, false, nil, err
	}
	if !hasValue && typed == nil {
		return false, false, nil, errors.New("neither a value nor a typed_value")
	}
	return hasValue, valueNullable, typed, nil
}

// decodeVariant decodes vortex.variant: metadata {shredded_dtype}; child 0 the core storage,
// of the type of the array, child 1 the shredded values when there is a type for them.
func decodeVariant(n *Node, t DType, rows int) (*Array, error) {
	if t.Kind != Variant {
		return nil, fmt.Errorf("cannot be a %s", t)
	}
	if n.NumBuffers() != 0 {
		return nil, fmt.Errorf("expected no buffer, got %d", n.NumBuffers())
	}
	var shredded *DType
	it := protowire.NewIter(n.Metadata())
	for it.Next() {
		if it.Num() == 1 && it.Type() == protowire.Bytes {
			s, err := ParseDType(it.Bytes())
			if err != nil {
				return nil, fmt.Errorf("shredded_dtype: %w", err)
			}
			shredded = &s
		}
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	want := 1 + b2i(shredded != nil)
	if n.NumChildren() != want {
		return nil, fmt.Errorf("expected %d children, got %d", want, n.NumChildren())
	}
	core, err := n.Child(0, t, rows)
	if err != nil {
		return nil, fmt.Errorf("core storage: %w", err)
	}
	if shredded == nil {
		return core, nil
	}
	sh, err := n.Child(1, *shredded, rows)
	if err != nil {
		return nil, fmt.Errorf("shredded: %w", err)
	}
	return &Array{Type: t, Len: rows, Validity: core.Validity,
		Var: &VariantData{Core: core, Shredded: sh, TypedType: *shredded}}, nil
}

// VariantValue returns row i of a Variant array as a Go value: nil, bool, int64, float64,
// string, []byte, time.Time, []any, map[string]any, or what dec returns for a decimal given
// its unscaled little-endian bytes and its scale. It allocates.
func (a *Array) VariantValue(i int, dec variant.DecimalFunc) (any, error) {
	if a.Null(i) {
		return nil, nil
	}
	j := i + a.VOff
	d := a.Var
	switch {
	case d.Parts != nil:
		p := d.Parts[0]
		for k := 1; j >= p.Len; k++ {
			j -= p.Len
			p = d.Parts[k]
		}
		return p.VariantValue(j, dec)
	case d.Shredded != nil:
		return d.logical(j, dec)
	}
	r := &rowMeta{meta: d.Metadata.bytes(j), dec: dec}
	v, _, err := r.shredded(d.Value, d.Typed, d.TypedType, j)
	return v, err
}

// bytes returns the bytes of row j of a Utf8 or Binary array.
func (a *Array) bytes(j int) []byte { return a.Data[a.Starts[j]:a.Ends[j]] }

// rowMeta decodes the values of one row of a Parquet array: the metadata of the row is
// parsed once, when a value needs it.
type rowMeta struct {
	dec    variant.DecimalFunc
	meta   []byte
	keys   variant.Keys
	parsed bool
}

func (r *rowMeta) decode(value []byte) (any, error) {
	if !r.parsed {
		var err error
		if r.keys, err = variant.ParseMetadata(r.meta); err != nil {
			return nil, err
		}
		r.parsed = true
	}
	return variant.DecodeWith(r.keys, value, r.dec)
}

// shredded decodes row j from a value and a typed_value, following the table of the Parquet
// shredding: neither is a missing value, only the value is itself, only the typed_value is the
// typed one, both is an object with some fields of each. present is false for a missing value.
func (r *rowMeta) shredded(value, typed *Array, t DType, j int) (v any, present bool, err error) {
	hasValue := value != nil && !value.Null(j)
	hasTyped := typed != nil && !typed.Null(j)
	switch {
	case !hasValue && !hasTyped:
		return nil, false, nil
	case !hasTyped:
		v, err = r.decode(value.bytes(j))
		return v, true, err
	}
	tv, err := r.typed(typed, t, j)
	if err != nil || !hasValue {
		return tv, true, err
	}
	raw, err := r.decode(value.bytes(j))
	if err != nil {
		return nil, true, err
	}
	rawObj, typedObj := asMap(raw), asMap(tv)
	if rawObj == nil || typedObj == nil {
		return nil, true, errors.New("a value and a typed_value that are not both objects")
	}
	for k, f := range rawObj {
		if _, dup := typedObj[k]; dup {
			return nil, true, fmt.Errorf("field %q is in both the value and the typed_value", k)
		}
		typedObj[k] = f
	}
	return typedObj, true, nil
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// typed converts row j of a typed_value in the Parquet layout.
func (r *rowMeta) typed(a *Array, t DType, j int) (any, error) {
	switch t.Kind {
	case Struct:
		out := make(map[string]any, len(t.Fields))
		for k, f := range t.Fields {
			v, ok, err := r.field(a.Fields[k], f.Type, j)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", f.Name, err)
			}
			if ok {
				out[f.Name] = v
			}
		}
		return out, nil
	case List, FixedSizeList:
		start, end := a.listRange(t, j)
		out := make([]any, end-start)
		for e := range out {
			v, _, err := r.field(a.Elems, *t.Elem, start+e)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", e, err)
			}
			out[e] = v
		}
		return out, nil
	}
	return leafValue(a, t, j, r.dec)
}

// field converts the field or the element j of a typed_value: a struct of a value and a
// typed_value, or, when it is not that, a typed value of its own.
func (r *rowMeta) field(a *Array, t DType, j int) (any, bool, error) {
	if a.Null(j) {
		return nil, false, nil
	}
	if t.Kind != Struct || len(t.Fields) == 0 {
		return r.shredded(nil, a, t, j)
	}
	var value, typed *Array
	var tt DType
	for k, f := range t.Fields {
		switch f.Name {
		case "value":
			value = a.Fields[k]
		case "typed_value":
			typed, tt = a.Fields[k], f.Type
		default:
			return r.shredded(nil, a, t, j)
		}
	}
	return r.shredded(value, typed, tt, j)
}

// listRange is the range of the elements of row j of a list.
func (a *Array) listRange(t DType, j int) (start, end int) {
	if t.Kind == FixedSizeList {
		return j * t.Size, (j + 1) * t.Size
	}
	return a.Starts[j], a.Ends[j]
}

// logical converts row j of a Variant array that has a shredded child.
func (d *VariantData) logical(j int, dec variant.DecimalFunc) (any, error) {
	raw := func() (any, error) { return d.Core.VariantValue(j, dec) }
	return logicalValue(d.Shredded, d.TypedType, j, raw, dec)
}

// logicalValue converts row j of the shredded values of a Variant array. raw gives the value
// of the core storage of the row, which stands in for a null shredded value and completes a
// shredded object.
func logicalValue(a *Array, t DType, j int, raw func() (any, error), dec variant.DecimalFunc) (any, error) {
	if a.Null(j) {
		return raw()
	}
	switch t.Kind {
	case Variant:
		return a.VariantValue(j, dec)
	case Struct:
		return logicalStruct(a, t, j, raw, dec)
	case List, FixedSizeList:
		start, end := a.listRange(t, j)
		out := make([]any, end-start)
		for e := range out {
			v, err := logicalValue(a.Elems, *t.Elem, start+e, nilRaw, dec)
			if err != nil {
				return nil, fmt.Errorf("element %d: %w", e, err)
			}
			out[e] = v
		}
		return out, nil
	}
	return leafValue(a, t, j, dec)
}

func nilRaw() (any, error) { return nil, nil }

// logicalStruct merges the fields of a shredded object with the ones of the object of the
// core storage that are not shredded. A shredded field that is null is not there.
func logicalStruct(a *Array, t DType, j int, raw func() (any, error), dec variant.DecimalFunc) (any, error) {
	rv, err := raw()
	if err != nil {
		return nil, err
	}
	rawObj := asMap(rv)
	out := make(map[string]any, len(t.Fields)+len(rawObj))
	for k, f := range t.Fields {
		fa := a.Fields[k]
		if fa.Null(j) {
			continue
		}
		fraw := nilRaw
		if rf, ok := rawObj[f.Name]; ok && f.Type.Kind == Struct && asMap(rf) != nil {
			fraw = func() (any, error) { return rf, nil }
		}
		v, err := logicalValue(fa, f.Type, j, fraw, dec)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Name, err)
		}
		out[f.Name] = v
	}
	for name, v := range rawObj {
		if _, shredded := out[name]; !shredded && v != nil {
			out[name] = v
		}
	}
	return out, nil
}

// leafValue converts row j of a non-null primitive, bool, string, binary, decimal, date, time,
// timestamp or UUID array of type t.
func leafValue(a *Array, t DType, j int, dec variant.DecimalFunc) (any, error) {
	if t.Kind == Ext {
		return extValue(a, t, j, dec)
	}
	switch t.Kind {
	case Bool:
		return a.Bits.Get(j), nil
	case Utf8:
		return string(a.bytes(j)), nil
	case Binary:
		return append([]byte(nil), a.bytes(j)...), nil
	case Decimal:
		if dec == nil {
			return nil, errors.New("a decimal and no way to build one")
		}
		return dec(append([]byte(nil), a.Data[j*a.Width:(j+1)*a.Width]...), int(t.Scale)), nil
	case Prim:
		return primValue(a, t.PType, j)
	}
	return nil, fmt.Errorf("a value of type %s: %w", t, errors.ErrUnsupported)
}

func primValue(a *Array, p PType, j int) (any, error) {
	w := p.Width()
	b := a.Data[j*w : (j+1)*w]
	var u uint64
	for i := w - 1; i >= 0; i-- {
		u = u<<8 | uint64(b[i])
	}
	switch {
	case p == F16:
		return nil, fmt.Errorf("a %s value: %w", p, errors.ErrUnsupported)
	case p == F32:
		return float64(math.Float32frombits(uint32(u))), nil
	case p == F64:
		return math.Float64frombits(u), nil
	case p == U64 && u > math.MaxInt64:
		return nil, fmt.Errorf("the u64 value %d does not fit in an int64", u)
	case p.Signed():
		return int64(u<<(64-8*w)) >> (64 - 8*w), nil
	}
	return int64(u), nil
}

// extValue converts a date, a time, a timestamp or a UUID. Another extension type is its
// storage.
func extValue(a *Array, t DType, j int, dec variant.DecimalFunc) (any, error) {
	st := *t.Storage
	if t.Label == extUUID {
		if st.Kind != FixedSizeList || st.Size != 16 {
			return nil, fmt.Errorf("a uuid of type %s", st)
		}
		return append([]byte(nil), a.Elems.Data[j*16:(j+1)*16]...), nil
	}
	v, err := leafValue(a, st, j, dec)
	n, isInt := v.(int64)
	if err != nil || !isInt || (t.Label != extDate && t.Label != extTime && t.Label != extTimestamp) {
		return v, err
	}
	switch {
	case t.Label == extDate && st.PType == I32:
		return time.Unix(n*86400, 0).UTC(), nil
	case t.Label == extDate:
		return time.UnixMilli(n).UTC(), nil
	case t.Ticks == 0:
		return nil, fmt.Errorf("%s with no unit", t.Label)
	}
	return time.Unix(0, 0).UTC().Add(time.Duration(n) * (time.Second / time.Duration(t.Ticks))), nil
}
