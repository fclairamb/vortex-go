package vortex

import (
	"bytes"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/edition"
)

// The writer with a forced encoding (wforce.go): every encoder of internal/encode, on every type
// it takes, written then read back.

// encodingIDs is the identifier of the array each forced encoding writes.
var encodingIDs = map[string]string{
	"bitpacked": "fastlanes.bitpacked", "for": "fastlanes.for", "zigzag": "vortex.zigzag",
	"rle": "fastlanes.rle", "runend": "vortex.runend", "dict": "vortex.dict",
	"sequence": "vortex.sequence", "constant": "vortex.constant", "masked": "vortex.masked", "sparse": "vortex.sparse",
	"alp": "vortex.alp", "alprd": "vortex.alprd", "zstd": "vortex.zstd", "fsst": "vortex.fsst", "varbin": "vortex.varbin",
	"decimal_byte_parts": "vortex.decimal_byte_parts", "datetimeparts": "vortex.datetimeparts",
}

func numberTypes() map[Kind]Type {
	return map[Kind]Type{
		Int8: Int8Type(), Int16: Int16Type(), Int32: Int32Type(), Int64: Int64Type(),
		Uint8: Uint8Type(), Uint16: Uint16Type(), Uint32: Uint32Type(), Uint64: Uint64Type(),
		Float32: Float32Type(), Float64: Float64Type(),
	}
}

// forcedTypes says which types an encoding takes.
func forcedTypes(enc string) []Kind {
	var out []Kind
	for k := range numberTypes() {
		isFloat := k == Float32 || k == Float64
		isSigned := k >= Int8 && k <= Int64
		switch {
		case enc == "zigzag" && !isSigned, isFloat && slices.Contains([]string{"bitpacked", "for", "sequence"}, enc):
		case !isFloat && (enc == "alp" || enc == "alprd"):
		case enc == "fsst" || enc == "varbin" || enc == "decimal_byte_parts" || enc == "datetimeparts": // not numbers
		default:
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// numbersOf makes the Data of n numbers of kind k, value(i) for row i, and the rows it stands for.
func numbersOf(k Kind, n int, value func(i int) int64, nulls []bool) (Data, []val) {
	switch k {
	case Int8:
		return typed[int8](n, value, nulls)
	case Int16:
		return typed[int16](n, value, nulls)
	case Int32:
		return typed[int32](n, value, nulls)
	case Int64:
		return typed[int64](n, value, nulls)
	case Uint8:
		return typed[uint8](n, value, nulls)
	case Uint16:
		return typed[uint16](n, value, nulls)
	case Uint32:
		return typed[uint32](n, value, nulls)
	case Uint64:
		return typed[uint64](n, value, nulls)
	case Float32:
		return typed[float32](n, value, nulls)
	}
	return typed[float64](n, value, nulls)
}

func typed[T Number](n int, value func(i int) int64, nulls []bool) (Data, []val) {
	v := make([]T, n)
	vals := make([]val, n)
	for i := range v {
		v[i] = T(value(i))
		vals[i] = val{b: rawOf(v[i]), null: nulls != nil && nulls[i]}
	}
	d := Of(v)
	if nulls != nil {
		d = d.Nulls(nulls)
	}
	return d, vals
}

// forcedColumn makes data an encoding takes: the first of the values it is made for.
func forcedColumn(enc string, k Kind, n int, withNulls bool) (Type, Data, []val) {
	w := int64(numberWidth(k))
	value := map[string]func(i int) int64{
		"bitpacked": func(i int) int64 {
			if i%97 == 3 && w > 1 {
				return 1 << (8*w - 3) // a patch
			}
			return int64(i * 37 % 100)
		},
		"for":      func(i int) int64 { return int64(i*1237%10000) - 3000 },
		"zigzag":   func(i int) int64 { return []int64{0, -1, 1, -2, 127, -128}[i%6] * int64(1+i%3) },
		"rle":      func(i int) int64 { return int64(i / 7) },
		"runend":   func(i int) int64 { return int64(i / 5) },
		"dict":     func(i int) int64 { return int64(i % 13) },
		"sequence": func(i int) int64 { return int64(i*3 + 5) },
		"zstd":     func(i int) int64 { return int64(i * 37 % 100) },
		"alp":      func(i int) int64 { return int64(i) },
		"alprd":    func(i int) int64 { return int64(i) },
		"constant": func(int) int64 { return 42 },
		"masked":   func(int) int64 { return 42 },
		"sparse": func(i int) int64 {
			if i%17 == 5 {
				return int64(i % 100)
			}
			return 7
		},
	}[enc]
	var nulls []bool
	if withNulls {
		nulls = make([]bool, n)
		for i := 0; i < n; i += 5 {
			nulls[i] = true
		}
		if enc == "constant" {
			for i := range nulls { // a constant is all equal or all null
				nulls[i] = true
			}
		}
	}
	ty := numberTypes()[k]
	if withNulls {
		ty = ty.AsNullable()
	}
	if enc == "alp" || enc == "alprd" {
		d, vals := floatsOf(k, n, nulls, func(i int) float64 {
			if enc == "alp" {
				return float64(i*37%100000) / 100 // prices, and a few values that are not
			}
			return math.Sqrt(float64(i)+0.5) * 1000 // no short decimal form
		})
		return ty, d, vals
	}
	d, vals := numbersOf(k, n, value, nulls)
	return ty, d, vals
}

// floatsOf makes the Data of n floats of kind k, f(i) for row i, and the rows it stands for.
func floatsOf(k Kind, n int, nulls []bool, f func(i int) float64) (Data, []val) {
	vals := make([]val, n)
	var d Data
	if k == Float32 {
		v := make([]float32, n)
		for i := range v {
			v[i] = float32(f(i))
			vals[i] = val{b: rawOf(v[i]), null: nulls != nil && nulls[i]}
		}
		d = Of(v)
	} else {
		v := make([]float64, n)
		for i := range v {
			v[i] = f(i)
			vals[i] = val{b: rawOf(v[i]), null: nulls != nil && nulls[i]}
		}
		d = Of(v)
	}
	if nulls != nil {
		d = d.Nulls(nulls)
	}
	return d, vals
}

// writeForced writes the chunks with the encoding forced, and opens the file.
func writeForced(t testing.TB, enc string, s *Schema, chunks ...[]Data) (*File, error) {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, s, WriteOptions{})
	w.force = enc
	for _, c := range chunks {
		if err := w.Write(c...); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
}

func checkForced(t *testing.T, enc string, ty Type, d Data, vals []val, sizes ...int) {
	t.Helper()
	var chunks [][]Data
	lo := 0
	for _, n := range sizes {
		chunks = append(chunks, []Data{sliceOf(d, ty, lo, lo+n)})
		lo += n
	}
	f, err := writeForced(t, enc, NewSchema(Field{"c", ty}), chunks...)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readBack(f, ty)
	if err != nil || len(got) != len(vals) {
		t.Fatalf("%d rows, %v, want %d", len(got), err, len(vals))
	}
	for i := range vals {
		if !eqVal(got[i], vals[i]) {
			t.Fatalf("row %d is %s, want %s", i, describeVal(got[i]), describeVal(vals[i]))
		}
	}
	if !slices.Contains(f.arrayEncodings, encodingIDs[enc]) {
		t.Fatalf("the file has %v, no %s", f.arrayEncodings, encodingIDs[enc])
	}
	for _, id := range f.arrayEncodings {
		if !edition.Has(edition.KindArray, id) {
			t.Errorf("array %q is not in the edition", id)
		}
	}
}

func TestForcedEncodings(t *testing.T) {
	for enc := range encodingIDs {
		for _, k := range forcedTypes(enc) {
			for _, withNulls := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%v", enc, k, withNulls), func(t *testing.T) {
					if enc == "sequence" && withNulls {
						return // a sequence has no null
					}
					if enc == "masked" && !withNulls {
						return // without a null it is a constant
					}
					n := 100
					if enc == "sequence" && numberWidth(k) == 1 {
						n = 20 // 3*i+5 stays in a byte, twice
					}
					ty, d, vals := forcedColumn(enc, k, n, withNulls)
					checkForced(t, enc, ty, d, vals, n)
					// two chunks: the memory of the encoders is reused
					ty, d, vals = forcedColumn(enc, k, 2*n, withNulls)
					checkForced(t, enc, ty, d, vals, n, n)
				})
			}
		}
	}
}

func TestForcedLargeColumn(t *testing.T) {
	for _, enc := range []string{"bitpacked", "rle", "runend", "dict", "for", "sparse"} {
		ty, d, vals := forcedColumn(enc, Int32, 20000, enc != "bitpacked")
		checkForced(t, enc, ty, d, vals, 20000)
	}
}

func TestForcedStrings(t *testing.T) {
	words := []string{"", "a", "abcdefghijkl", "a string that is longer than twelve bytes", "zz"}
	strs := make([]string, 300)
	bins := make([][]byte, 300)
	vals := make([]val, 300)
	nulls := make([]bool, 300)
	for i := range strs {
		strs[i] = words[i*7%len(words)]
		bins[i] = []byte(strs[i])
		nulls[i] = i%9 == 4
		vals[i] = val{b: bins[i], null: nulls[i]}
	}
	checkForced(t, "varbin", BinaryType().AsNullable(), OfBytes(bins).Nulls(nulls), vals, 150, 150)
	checkForced(t, "varbin", BinaryType(), OfBytes(bins), vals[:0:0], 0)
	for _, enc := range []string{"dict", "fsst", "zstd"} {
		checkForced(t, enc, Utf8Type().AsNullable(), OfStrings(strs).Nulls(nulls), vals, 300)
		checkForced(t, enc, BinaryType().AsNullable(), OfBytes(bins).Nulls(nulls), vals, 150, 150)
	}
}

// TestForcedMaskedStringsAndBools: strings and booleans of one value among nulls, as a constant
// under a validity, and the other arrays are an error.
func TestForcedMaskedStringsAndBools(t *testing.T) {
	nulls := make([]bool, 300)
	strs := make([]string, 300)
	bins := make([][]byte, 300)
	bools := make([]bool, 300)
	vals := make([]val, 300)
	bvals := make([]val, 300)
	for i := range strs {
		nulls[i] = i%3 != 1
		strs[i], bools[i] = "a string that is longer than twelve bytes", true
		bins[i] = []byte(strs[i])
		vals[i] = val{b: bins[i], null: nulls[i]}
		bvals[i] = val{b: []byte{1}, null: nulls[i]}
	}
	checkForced(t, "masked", Utf8Type().AsNullable(), OfStrings(strs).Nulls(nulls), vals, 300)
	checkForced(t, "masked", BinaryType().AsNullable(), OfBytes(bins).Nulls(nulls), vals, 150, 150)
	checkForced(t, "masked", BoolType().AsNullable(), OfBools(bools).Nulls(nulls), bvals, 300)
}

// TestForcedDecimalsAndTimestamps writes decimals that fit an i64, and timestamps of every unit,
// with decimal byte parts and datetime parts, then reads them back.
func TestForcedDecimalsAndTimestamps(t *testing.T) {
	g := &gen{r: rand.New(rand.NewPCG(24, 25))}
	for _, nullable := range []bool{false, true} {
		types := map[string]Type{
			"decimal(5,2)": DecimalOf(5, 2), "decimal(18,4)": DecimalOf(18, 4), "decimal(2,0)": DecimalOf(2, 0),
		}
		for name, ty := range types {
			if nullable {
				ty = ty.AsNullable()
			}
			d, vals := g.make(ty, 700)
			checkForced(t, "decimal_byte_parts", ty, d, vals, 700)
			_ = name
		}
		for _, unit := range []Unit{Second, Millisecond, Microsecond, Nanosecond} {
			ty := TimestampType(unit, "")
			if nullable {
				ty = ty.AsNullable()
			}
			d, vals := g.make(ty, 600)
			checkForced(t, "datetimeparts", ty, d, vals, 300, 300)
		}
	}
}

// TestForcedWideDecimals: values that need lower parts are an error, and so is a type that
// the encoding does not take.
func TestForcedWideDecimals(t *testing.T) {
	big := DecimalFromInt64(1)
	big.w[1] = 1 // 2^64 + 1
	d := OfDecimals([]Decimal{big})
	if _, err := writeForced(t, "decimal_byte_parts", NewSchema(Field{"c", DecimalOf(30, 0)}), []Data{d}); err == nil {
		t.Fatal("a decimal above an i64 is written")
	}
}

// TestForcedErrors checks that an encoding that cannot take the data is an error of the write.
func TestForcedErrors(t *testing.T) {
	cases := map[string]struct {
		enc string
		ty  Type
		d   Data
	}{
		"not a sequence":     {"sequence", Int32Type(), Of([]int32{1, 5, 2})},
		"not a constant":     {"constant", Int32Type(), Of([]int32{1, 5, 2})},
		"negative":           {"bitpacked", Int32Type(), Of([]int32{1, -5, 2})},
		"zigzag of unsigned": {"zigzag", Uint32Type(), Of([]uint32{1})},
		"sequence of floats": {"sequence", Float64Type(), Of([]float64{1, 2})},
		"all equal sparse":   {"sparse", Int32Type(), Of([]int32{})},
		"unknown":            {"nothing", Int32Type(), Of([]int32{1})},
		"alp of integers":    {"alp", Int32Type(), Of([]int32{1})},
		"alprd of no row":    {"alprd", Float64Type(), Of([]float64{})},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := writeForced(t, c.enc, NewSchema(Field{"c", c.ty}), []Data{c.d}); err == nil {
				t.Fatal("no error")
			}
		})
	}
}

// TestForcedMaskedBoolsThatDiffer: booleans of two values among nulls are not a constant under
// a validity, so they are written with another encoding and read back unchanged.
func TestForcedMaskedBoolsThatDiffer(t *testing.T) {
	bools := make([]bool, 300)
	nulls := make([]bool, 300)
	vals := make([]val, 300)
	for i := range bools {
		bools[i], nulls[i] = i%2 == 0, i%3 != 1
		vals[i] = val{b: []byte{boolByte(bools[i])}, null: nulls[i]}
	}
	ty := BoolType().AsNullable()
	f, err := writeForced(t, "masked", NewSchema(Field{"c", ty}), []Data{OfBools(bools).Nulls(nulls)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := readBack(f, ty)
	if err != nil || len(got) != len(vals) {
		t.Fatalf("%d rows, %v, want %d", len(got), err, len(vals))
	}
	for i := range vals {
		if !eqVal(got[i], vals[i]) {
			t.Fatalf("row %d is %s, want %s", i, describeVal(got[i]), describeVal(vals[i]))
		}
	}
	if slices.Contains(f.arrayEncodings, encodingIDs["masked"]) {
		t.Fatalf("the file has %v, with a masked constant", f.arrayEncodings)
	}
}
