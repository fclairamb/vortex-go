package vortex

import (
	"fmt"
	"reflect"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// Number is the set of the Go types a numeric column can be read as.
type Number interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~float32 | ~float64
}

// Batch is a group of rows of the columns of a scan. It and its columns are valid for one
// iteration of the loop.
type Batch struct {
	cols []*Column
	n    int
}

// Len is the number of rows of the batch.
func (b *Batch) Len() int { return b.n }

// Col returns the column i of the batch, in the order the columns were asked for. It
// panics when i is out of range.
func (b *Batch) Col(i int) *Column { return b.cols[i] }

// Column is a view of the values of one column in a batch.
type Column struct {
	arr *array.Array
	typ Type
}

// Type is the logical type of the column.
func (c *Column) Type() Type { return c.typ }

// checkRow panics when row i is not in the column.
func (c *Column) checkRow(i int) {
	if i < 0 || i >= c.arr.Len {
		panic(fmt.Sprintf("vortex: row %d out of range [0, %d)", i, c.arr.Len))
	}
}

// Null reports whether row i is null. It panics when i is out of range.
func (c *Column) Null(i int) bool {
	c.checkRow(i)
	return c.arr.Null(i)
}

// Bool returns the value of row i of a Bool column. It panics when the column is of
// another type, and when i is out of range.
func (c *Column) Bool(i int) bool {
	if c.arr.Type.Kind != array.Bool {
		panic(fmt.Sprintf("vortex: Bool on a column of type %s", c.typ))
	}
	c.checkRow(i)
	return c.arr.Bits.Get(i)
}

// Field returns the field i of a Struct column. It panics when the column is of another
// type, and when i is out of range.
func (c *Column) Field(i int) *Column {
	if c.arr.Type.Kind != array.Struct {
		panic(fmt.Sprintf("vortex: Field on a column of type %s", c.typ))
	}
	if i < 0 || i >= len(c.arr.Fields) {
		panic(fmt.Sprintf("vortex: field %d out of range [0, %d)", i, len(c.arr.Fields)))
	}
	return &Column{arr: c.arr.Fields[i], typ: c.typ.fields[i].Type}
}

// Bytes returns the bytes of row i of a Utf8, Binary or UUID column: a view, valid until the
// next batch. It panics when the column is of another type, and when i is out of range.
func (c *Column) Bytes(i int) []byte {
	k := c.typ.kind
	if k != Utf8 && k != Binary && k != UUID {
		panic(fmt.Sprintf("vortex: Bytes on a column of type %s", c.typ))
	}
	c.checkRow(i)
	if k == UUID {
		return c.arr.Elems.Data[i*uuidSize : (i+1)*uuidSize : (i+1)*uuidSize]
	}
	return c.arr.Data[c.arr.Starts[i]:c.arr.Ends[i]:c.arr.Ends[i]]
}

// uuidSize is the number of bytes of a UUID.
const uuidSize = 16

// Decimal returns the unscaled value of row i of a Decimal column, whatever its precision
// and the width it is stored at. It panics when the column is of another type, and when i
// is out of range.
func (c *Column) Decimal(i int) Decimal {
	if c.typ.kind != KindDecimal {
		panic(fmt.Sprintf("vortex: Decimal on a column of type %s", c.typ))
	}
	c.checkRow(i)
	w := c.arr.Width
	return decimalFromLE(c.arr.Data[i*w : (i+1)*w])
}

// Elems returns the elements of a List, FixedSizeList or Map column, all the rows' together.
// For a map, they are structs of two fields, the key and the value. List gives the range of
// a row. It panics when the column is of another type.
func (c *Column) Elems() *Column {
	switch c.typ.kind {
	case List, FixedSizeList:
		return &Column{arr: c.arr.Elems, typ: *c.typ.elem}
	case Map:
		entry := Type{kind: Struct, fields: []Field{{Name: "key", Type: *c.typ.elem}, {Name: "value", Type: *c.typ.value}}}
		return &Column{arr: c.arr.Elems, typ: entry}
	}
	panic(fmt.Sprintf("vortex: Elems on a column of type %s", c.typ))
}

// List returns the range of Elems that holds row i of a List, FixedSizeList or Map column:
// the elements from start to end, end excluded. Ranges of different rows may overlap. It
// panics when the column is of another type, and when i is out of range.
func (c *Column) List(i int) (start, end int) {
	switch c.typ.kind {
	case List, Map:
		c.checkRow(i)
		return c.arr.Starts[i], c.arr.Ends[i]
	case FixedSizeList:
		c.checkRow(i)
		return i * c.typ.Size(), (i + 1) * c.typ.Size()
	}
	panic(fmt.Sprintf("vortex: List on a column of type %s", c.typ))
}

// VariantDecimal is a decimal inside a Variant value. A Variant carries the scale of each
// decimal, which Decimal does not: the value is Value divided by 10 to the power Scale.
type VariantDecimal struct {
	Value Decimal
	Scale int
}

// String prints the decimal, like Decimal.Text with the scale of the value.
func (d VariantDecimal) String() string { return d.Value.Text(d.Scale) }

// variantDecimal builds the VariantDecimal of unscaled little-endian bytes.
func variantDecimal(unscaled []byte, scale int) any {
	return VariantDecimal{Value: decimalFromLE(unscaled), Scale: scale}
}

// Variant decodes row i of a Variant column, a null row being nil. The value is nil, a bool, an
// int64, a float64, a string, a []byte, a time.Time (a date, a time of day on 1970-01-01, or a
// timestamp, in UTC), a VariantDecimal, a []any or a map[string]any. Every call allocates: the
// result is a copy, valid after the batch. An error means the value of the file is damaged. It
// panics when the column is not a Variant, and when i is out of range.
func (c *Column) Variant(i int) (any, error) {
	if c.typ.kind != Variant {
		panic(fmt.Sprintf("vortex: Variant on a column of type %s", c.typ))
	}
	c.checkRow(i)
	return c.arr.VariantValue(i, variantDecimal)
}

var ptypeOfGo = map[reflect.Kind]array.PType{
	reflect.Int8: array.I8, reflect.Int16: array.I16, reflect.Int32: array.I32, reflect.Int64: array.I64,
	reflect.Uint8: array.U8, reflect.Uint16: array.U16, reflect.Uint32: array.U32, reflect.Uint64: array.U64,
	reflect.Float32: array.F32, reflect.Float64: array.F64,
}

// Values returns the values of a numeric column as a slice of T, null rows included (their
// value is unspecified: check Null). It returns an error, naming both types, when the column
// is not a T. A f16 column is read as float32. The slice is a view, valid for the iteration.
func Values[T Number](c *Column) ([]T, error) {
	if c.arr.Skipped != nil {
		return nil, c.arr.Skipped
	}
	want := ptypeOfGo[reflect.TypeFor[T]().Kind()]
	have := c.arr.Type
	if have.Kind == array.Decimal && want == array.I64 && c.typ.precision <= maxInt64Digits {
		return decimalValues[T](c)
	}
	if have.Kind != array.Prim || (have.PType != want && (have.PType != array.F16 || want != array.F32)) {
		return nil, fmt.Errorf("vortex: Values[%s] on a column of type %s", reflect.TypeFor[T](), c.typ)
	}
	if have.PType == array.F16 {
		return unsafex.Slice[T](halves(c.arr.Data)), nil
	}
	return unsafex.Slice[T](c.arr.Data), nil
}

// halves widens little-endian half floats into little-endian float32 values.
func halves(b []byte) []byte {
	out := make([]byte, 2*len(b))
	for i := 0; i < len(b); i += 2 {
		f := half(uint32(b[i]) | uint32(b[i+1])<<8)
		out[2*i], out[2*i+1], out[2*i+2], out[2*i+3] = byte(f), byte(f>>8), byte(f>>16), byte(f>>24)
	}
	return out
}

// half converts the bits of a binary16 into the bits of the binary32 of the same value.
func half(h uint32) uint32 {
	sign, exp, mant := (h&0x8000)<<16, (h>>10)&0x1f, h&0x3ff
	switch {
	case exp == 0x1f: // infinity, NaN
		return sign | 0x7f800000 | mant<<13
	case exp != 0:
		return sign | (exp+112)<<23 | mant<<13
	case mant == 0:
		return sign
	}
	e := uint32(113)
	for mant&0x400 == 0 { // a subnormal: normalize it
		mant <<= 1
		e--
	}
	return sign | e<<23 | (mant&0x3ff)<<13
}

// maxInt64Digits is the greatest precision whose values all fit in an int64.
const maxInt64Digits = 18

// decimalValues reads a decimal column of precision 18 or less as int64, whatever the width
// it is stored at.
func decimalValues[T Number](c *Column) ([]T, error) {
	w := c.arr.Width
	if w == 8 {
		return unsafex.Slice[T](c.arr.Data), nil
	}
	out := make([]T, c.arr.Len)
	for i := range out {
		d := decimalFromLE(c.arr.Data[i*w : (i+1)*w])
		v, ok := d.Int64()
		if !ok {
			return nil, fmt.Errorf("%w: decimal value at row %d does not fit in an int64", ErrCorrupt, i)
		}
		out[i] = T(v)
	}
	return out, nil
}
