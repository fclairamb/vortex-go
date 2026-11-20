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
