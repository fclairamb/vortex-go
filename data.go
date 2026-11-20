package vortex

import (
	"reflect"

	"github.com/fclairamb/vortex-go/internal/unsafex"
	"github.com/fclairamb/vortex-go/internal/variant"
)

type dataKind uint8

const (
	dNone   dataKind = iota
	dPrim            // numbers: raw holds them, little-endian
	dBool            // bools
	dStr             // strings, for utf8, binary
	dBytes           // byte slices, for utf8, binary, uuid
	dDec             // decimals
	dStruct          // kids are the fields
	dList            // offsets and kids[0], the elements; also fixed-size lists and maps
	dNull            // n nulls
	dVars            // variants: the Go values, or, in the buffer of a column, their encoding
)

// Data is the values of one column, in the shape Go holds them. Build one with Of, OfBools,
// OfStrings, OfBytes, OfDecimals, OfNulls, OfStruct or OfList. Which Data a column takes is
// set by its type, see Writer. A Data refers to the slices it was made from: the writer reads
// them during Write and keeps no reference afterwards.
type Data struct{ *body }

// body is what a Data holds: a pointer, so that passing a Data around costs a word.
type body struct {
	raw   []byte // numbers, as the bytes of the caller's slice
	bools []bool
	strs  []string
	bins  [][]byte
	decs  []Decimal
	vars  []any    // the values of a variant column, as the caller gave them
	vbins [][]byte // the encoded values of a variant column: bins holds the metadata
	venc  *variant.Encoder
	kids  []Data // the fields of a struct; the elements of a list, a fixed-size list or a map
	offs  []int  // the offsets of a list: one more than the rows
	nulls []bool // the null rows, nil for none
	n     int
	kind  dataKind
	pk    Kind // the kind of the numbers of a dPrim
}

// Of is the values of a column of numbers: Int8 to Float64, the storage of a date, a time
// or a timestamp (int32 for a date in days and for a time in seconds or milliseconds, else
// int64), and the 16 bits of a Float16 as uint16.
func Of[T Number](v []T) Data {
	var zero T
	k := numberKind(reflect.TypeOf(zero).Kind())
	return Data{&body{kind: dPrim, pk: k, n: len(v), raw: unsafex.Bytes(v)}}
}

func numberKind(k reflect.Kind) Kind {
	switch k {
	case reflect.Int8:
		return Int8
	case reflect.Int16:
		return Int16
	case reflect.Int32:
		return Int32
	case reflect.Int64:
		return Int64
	case reflect.Uint8:
		return Uint8
	case reflect.Uint16:
		return Uint16
	case reflect.Uint32:
		return Uint32
	case reflect.Uint64:
		return Uint64
	case reflect.Float32:
		return Float32
	default:
		return Float64
	}
}

// OfBools is the values of a bool column.
func OfBools(v []bool) Data { return Data{&body{kind: dBool, n: len(v), bools: v}} }

// OfStrings is the values of a utf8 or binary column.
func OfStrings(v []string) Data { return Data{&body{kind: dStr, n: len(v), strs: v}} }

// OfBytes is the values of a binary or utf8 column, and of a uuid column, whose values are
// 16 bytes each.
func OfBytes(v [][]byte) Data { return Data{&body{kind: dBytes, n: len(v), bins: v}} }

// OfDecimals is the values of a decimal column: the unscaled values.
func OfDecimals(v []Decimal) Data { return Data{&body{kind: dDec, n: len(v), decs: v}} }

// OfVariants is the values of a Variant column, one of: nil (a null row), bool, any Go integer
// (an unsigned one up to the largest int64), float32, float64, string, []byte, time.Time
// (a timestamp, in microseconds, or in nanoseconds when it has a finer part), VariantDecimal,
// []any and map[string]any of these, up to 128 levels deep. Another Go type is an error of
// Write that names the type and the row.
func OfVariants(vals []any) Data {
	var nulls []bool
	for i, v := range vals {
		if v == nil {
			if nulls == nil {
				nulls = make([]bool, len(vals))
			}
			nulls[i] = true
		}
	}
	return Data{&body{kind: dVars, n: len(vals), vars: vals, nulls: nulls}}
}

// OfNulls is a column of the null type: n rows, all null.
func OfNulls(n int) Data { return Data{&body{kind: dNull, n: n}} }

// OfStruct is the values of a struct column: one Data per field, of the same length.
func OfStruct(fields ...Data) Data {
	n := 0
	if len(fields) > 0 && fields[0].body != nil {
		n = fields[0].n
	}
	return Data{&body{kind: dStruct, n: n, kids: fields}}
}

// OfList is the values of a list, a fixed-size list or a map column. Row i is the elements
// offsets[i] to offsets[i+1] of elems, so offsets has one more value than there are rows and
// never decreases. For a fixed-size list the offsets are regular, 0, size, 2*size... For a
// map elems is OfStruct(keys, values).
func OfList(offsets []int, elems Data) Data {
	return Data{&body{kind: dList, n: max(len(offsets)-1, 0), offs: offsets, kids: []Data{elems}}}
}

// Nulls returns the Data with these rows marked null: null[i] is true when row i is null.
// It has as many values as the Data has rows. A column that is not nullable takes none.
func (d Data) Nulls(null []bool) Data {
	var b body
	if d.body != nil {
		b = *d.body
	}
	b.nulls = null
	return Data{&b}
}

// describe names the shape of the Data in an error message.
func (d Data) describe() string {
	switch d.kind {
	case dPrim:
		return "numbers of type " + d.pk.String()
	case dBool:
		return "bools"
	case dStr:
		return "strings"
	case dBytes:
		return "byte slices"
	case dDec:
		return "decimals"
	case dStruct:
		return "a struct"
	case dList:
		return "a list"
	case dNull:
		return "nulls"
	case dVars:
		return "variants"
	default:
		return "no values"
	}
}
