package vortex

import (
	"bytes"
	"fmt"
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
	"sequence": "vortex.sequence", "constant": "vortex.constant", "sparse": "vortex.sparse",
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
		"constant": func(int) int64 { return 42 },
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
	d, vals := numbersOf(k, n, value, nulls)
	return ty, d, vals
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
	checkForced(t, "dict", Utf8Type().AsNullable(), OfStrings(strs).Nulls(nulls), vals, 300)
	checkForced(t, "dict", BinaryType().AsNullable(), OfBytes(bins).Nulls(nulls), vals, 150, 150)
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
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := writeForced(t, c.enc, NewSchema(Field{"c", c.ty}), []Data{c.d}); err == nil {
				t.Fatal("no error")
			}
		})
	}
}
