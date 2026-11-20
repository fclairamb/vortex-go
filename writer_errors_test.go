package vortex

import (
	"bytes"
	"errors"
	"io"
	"math/big"
	"strings"
	"testing"
)

func TestWriteMismatches(t *testing.T) {
	dec := DecimalFromInt64
	bigDec, _ := DecimalFromBig(new(big.Int).Lsh(big.NewInt(1), 130))
	i64 := Of([]int64{1, 2, 3})
	tests := []struct {
		name   string
		fields []Field
		cols   []Data
		want   string
	}{
		{"too few columns", []Field{{"a", Int64Type()}, {"b", Int64Type()}}, []Data{i64},
			`vortex: Write: 1 columns for a schema of 2 fields`},
		{"too many columns", []Field{{"a", Int64Type()}}, []Data{i64, i64},
			`vortex: Write: 2 columns for a schema of 1 fields`},
		{"length", []Field{{"a", Int64Type()}, {"b", Int64Type()}}, []Data{i64, Of([]int64{1})},
			`vortex: Write: field "b": 1 rows, want 3`},
		{"number kind", []Field{{"a", Int32Type()}}, []Data{i64},
			`vortex: Write: field "a": the type i32 cannot take numbers of type i64 (Of[int32])`},
		{"float kind", []Field{{"a", Float64Type()}}, []Data{i64},
			`field "a": the type f64 cannot take numbers of type i64 (Of[float64])`},
		{"unsigned kind", []Field{{"a", Uint8Type()}}, []Data{i64},
			`field "a": the type u8 cannot take numbers of type i64 (Of[uint8])`},
		{"float16 takes uint16", []Field{{"a", Float16Type()}}, []Data{Of([]float32{1, 2, 3})},
			`field "a": the type f16 cannot take numbers of type f32 (Of[uint16])`},
		{"date storage", []Field{{"a", DateType(Day)}}, []Data{i64},
			`field "a": the type date[days] cannot take numbers of type i64 (Of[int32])`},
		{"strings for numbers", []Field{{"a", Int64Type()}}, []Data{OfStrings([]string{"a", "b", "c"})},
			`field "a": the type i64 cannot take strings (Of[int64])`},
		{"numbers for strings", []Field{{"a", Utf8Type()}}, []Data{i64},
			`field "a": the type utf8 cannot take numbers of type i64 (OfStrings or OfBytes)`},
		{"numbers for bool", []Field{{"a", BoolType()}}, []Data{i64},
			`field "a": the type bool cannot take numbers of type i64 (OfBools)`},
		{"bools for null", []Field{{"a", NullType()}}, []Data{OfBools([]bool{true, false, true})},
			`field "a": the type null cannot take bools (OfNulls)`},
		{"strings for uuid", []Field{{"a", UUIDType()}}, []Data{OfStrings([]string{"a", "b", "c"})},
			`field "a": the type uuid cannot take strings (OfBytes)`},
		{"bytes for decimal", []Field{{"a", DecimalOf(5, 2)}}, []Data{OfBytes([][]byte{nil, nil, nil})},
			`field "a": the type decimal(5,2) cannot take byte slices (OfDecimals)`},
		{"numbers for struct", []Field{{"a", StructOf(Field{"x", Int64Type()})}}, []Data{i64},
			`field "a": the type struct{x: i64} cannot take numbers of type i64 (OfStruct)`},
		{"numbers for list", []Field{{"a", ListOf(Int64Type())}}, []Data{i64},
			`field "a": the type list<i64> cannot take numbers of type i64 (OfList)`},
		{"decimals for numbers", []Field{{"a", Int64Type()}}, []Data{OfDecimals([]Decimal{dec(1), dec(2), dec(3)})},
			`field "a": the type i64 cannot take decimals (Of[int64])`},
		{"nulls for numbers", []Field{{"a", Int64Type()}}, []Data{OfNulls(3)},
			`field "a": the type i64 cannot take nulls (Of[int64])`},
		{"zero Data", []Field{{"a", Int64Type()}}, []Data{{}}, `field "a": no values: the Data is the zero value`},
		{"zero Data in a struct", []Field{{"a", StructOf(Field{"x", Int64Type()})}}, []Data{OfStruct(Data{})},
			`field "a.x": no values: the Data is the zero value`},
		{"zero Data as the elements of a list", []Field{{"a", ListOf(Int64Type())}}, []Data{OfList([]int{0}, Data{})},
			`field "a": OfList has no elements`},
		{"no values", []Field{{"a", Int64Type()}}, []Data{{&body{n: 3}}},
			`field "a": the type i64 cannot take no values (Of[int64])`},
		{"struct for numbers", []Field{{"a", Int64Type()}}, []Data{OfStruct(i64)},
			`field "a": the type i64 cannot take a struct (Of[int64])`},
		{"list for numbers", []Field{{"a", Int64Type()}}, []Data{OfList([]int{0, 1, 2, 3}, i64)},
			`field "a": the type i64 cannot take a list (Of[int64])`},
		{"null in a non-nullable field", []Field{{"a", Int64Type()}}, []Data{i64.Nulls([]bool{false, true, false})},
			`vortex: Write: field "a": row 1 is null and the field is not nullable`},
		{"Nulls of the wrong length", []Field{{"a", Int64Type().AsNullable()}}, []Data{i64.Nulls([]bool{true})},
			`field "a": Nulls has 1 values for 3 rows`},
		{"invalid utf8", []Field{{"a", Utf8Type()}}, []Data{OfStrings([]string{"ok", "bad\xff", "ok"})},
			`field "a": row 1 is not valid UTF-8`},
		{"invalid utf8 in bytes", []Field{{"a", Utf8Type()}}, []Data{OfBytes([][]byte{[]byte("ok"), []byte("ok"), {0xc0}})},
			`field "a": row 2 is not valid UTF-8`},
		{"uuid of the wrong size", []Field{{"a", UUIDType()}}, []Data{OfBytes([][]byte{make([]byte, 16), make([]byte, 15), make([]byte, 16)})},
			`field "a": row 1 is 15 bytes, a uuid is 16`},
		{"decimal too wide", []Field{{"a", DecimalOf(30, 0)}}, []Data{OfDecimals([]Decimal{dec(1), dec(2), bigDec})},
			`field "a": row 2 does not fit the 16 bytes of a decimal of precision 30`},
		{"decimal too wide, negative", []Field{{"a", DecimalOf(2, 0)}}, []Data{OfDecimals([]Decimal{dec(1), dec(-129), dec(2)})},
			`field "a": row 1 does not fit the 1 bytes of a decimal of precision 2`},
		{"struct with too few fields", []Field{{"a", StructOf(Field{"x", Int64Type()}, Field{"y", Int64Type()})}},
			[]Data{OfStruct(i64)}, `field "a": OfStruct has 1 fields, the type has 2`},
		{"struct field of the wrong type", []Field{{"a", StructOf(Field{"x", Int64Type()}, Field{"y", Utf8Type()})}},
			[]Data{OfStruct(i64, i64)}, `field "a.y": the type utf8 cannot take numbers of type i64`},
		{"struct field of the wrong length", []Field{{"a", StructOf(Field{"x", Int64Type()}, Field{"y", Int64Type()})}},
			[]Data{OfStruct(i64, Of([]int64{1}))}, `field "a.y": 1 rows, want 3`},
		{"list without offsets", []Field{{"a", ListOf(Int64Type())}}, []Data{{&body{kind: dList, n: 3, kids: []Data{i64}}}},
			`field "a": OfList has 0 offsets, 3 rows need 4`},
		{"offsets that decrease", []Field{{"a", ListOf(Int64Type())}}, []Data{OfList([]int{0, 2, 1, 3}, i64)},
			`field "a": offset 2 is 1: offsets must not decrease and stay within the 3 elements`},
		{"offsets past the elements", []Field{{"a", ListOf(Int64Type())}}, []Data{OfList([]int{0, 1, 2, 4}, i64)},
			`field "a": offset 3 is 4: offsets must not decrease and stay within the 3 elements`},
		{"list element of the wrong type", []Field{{"a", ListOf(Utf8Type())}}, []Data{OfList([]int{0, 1, 2, 3}, i64)},
			`field "a[]": the type utf8 cannot take numbers of type i64`},
		{"fixed-size list offsets", []Field{{"a", FixedSizeListOf(Int64Type(), 2)}}, []Data{OfList([]int{0, 2, 3}, i64).Nulls(nil)},
			`field "a": 3 elements for 2 rows of a fixed-size list of 2`},
		{"fixed-size list irregular", []Field{{"a", FixedSizeListOf(Int64Type(), 1)}}, []Data{OfList([]int{0, 2, 2}, Of([]int64{1, 2}))},
			`field "a": offset 1 is 2, a fixed-size list of 1 has regular offsets (1)`},
		{"map elements", []Field{{"a", MapOf(Utf8Type(), Int64Type())}}, []Data{OfList([]int{0, 1, 2, 3}, i64)},
			`field "a": the elements of a map are OfStruct(keys, values), got numbers of type i64`},
		{"map with one field", []Field{{"a", MapOf(Utf8Type(), Int64Type())}}, []Data{OfList([]int{0, 1, 2, 3}, OfStruct(i64))},
			`field "a": the elements of a map are OfStruct(keys, values), got a struct`},
		{"map key of the wrong type", []Field{{"a", MapOf(Utf8Type(), Int64Type())}}, []Data{OfList([]int{0, 1, 2, 3}, OfStruct(i64, i64))},
			`field "a.key": the type utf8 cannot take numbers`},
		{"map value of the wrong type", []Field{{"a", MapOf(Int64Type(), Utf8Type())}}, []Data{OfList([]int{0, 1, 2, 3}, OfStruct(i64, i64))},
			`field "a.value": the type utf8 cannot take numbers`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf, NewSchema(tc.fields...), WriteOptions{})
			err := w.Write(tc.cols...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			// The writer is closed for writing, and Close says why.
			if again := w.Write(tc.cols...); again != err {
				t.Fatalf("the second Write returned %v", again)
			}
			if closeErr := w.Close(); closeErr != err {
				t.Fatalf("Close returned %v", closeErr)
			}
		})
	}
}

func TestInvalidSchemas(t *testing.T) {
	tests := []struct {
		name string
		typ  Type
		want string
	}{
		{"precision 0", DecimalOf(0, 0), `field "a": decimal of precision 0 and scale 0`},
		{"precision 77", DecimalOf(77, 0), `decimal of precision 77`},
		{"scale over precision", DecimalOf(5, 6), `decimal of precision 5 and scale 6`},
		{"date in seconds", DateType(Second), `a date in s, want ms or days`},
		{"time in days", TimeType(Day), `a time in days, want ns, us, ms or s`},
		{"timestamp in days", TimestampType(Day, ""), `a timestamp in days`},
		{"timestamp zone", TimestampType(Second, strings.Repeat("x", 1<<16)), `a timestamp in s with a zone of 65536 bytes`},
		{"variant", Type{kind: Variant}, `a variant cannot be written`},
		{"unknown kind", Type{kind: Variant + 1}, `unknown kind 25`},
		{"in a list", ListOf(DecimalOf(0, 0)), `field "a[]": decimal`},
		{"in a fixed-size list", FixedSizeListOf(DecimalOf(0, 0), 2), `field "a[]": decimal`},
		{"in a map key", MapOf(DecimalOf(0, 0), Int8Type()), `field "a.key": decimal`},
		{"in a map value", MapOf(Int8Type(), DecimalOf(0, 0)), `field "a.value": decimal`},
		{"in a struct", StructOf(Field{"b", DecimalOf(0, 0)}), `field "a.b": decimal`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := NewWriter(&buf, NewSchema(Field{"a", tc.typ}), WriteOptions{})
			err := w.Write()
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "NewWriter") {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if err := w.Close(); err == nil {
				t.Fatal("Close succeeded")
			}
			if buf.Len() != 0 {
				t.Fatalf("%d bytes written for a schema that cannot be written", buf.Len())
			}
		})
	}
}

// lowered sets a limit for the length of a test.
func lowered[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func TestLimits(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T)
		field Field
		data  Data
		want  string
	}{
		{"segment", func(t *testing.T) { lowered(t, &maxSegment, 100) }, Field{"a", Int64Type()},
			Of(make([]int64, 100)), `vortex: Write: a segment of`},
		{"buffers", func(t *testing.T) { lowered(t, &maxBuffers, 1) }, Field{"a", Int64Type().AsNullable()},
			Of(make([]int64, 3)).Nulls([]bool{true, false, true}), `an array of 2 buffers, at most 1`},
		{"list elements", func(t *testing.T) { lowered(t, &maxListElems, 2) }, Field{"a", ListOf(Int8Type())},
			OfList([]int{0, 1, 3}, Of([]int8{1, 2, 3})), `field "a": 3 elements, at most 2`},
		{"postscript", func(t *testing.T) { lowered(t, &maxPostscript, 4) }, Field{"a", Int8Type()},
			Of([]int8{1}), `a postscript of`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			w := NewWriter(io.Discard, NewSchema(tc.field), WriteOptions{})
			err := w.Write(tc.data)
			if err == nil {
				err = w.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSmallBuffers(t *testing.T) {
	// Data buffers of 40 bytes at most: the long strings are split between several buffers.
	lowered(t, &maxViewBuffer, 40)
	lowered(t, &smallWrites, 64)
	lowered(t, &arenaBlock, 16)
	strs := make([]string, 50)
	for i := range strs {
		strs[i] = strings.Repeat(string(rune('a'+i%26)), 13+i)
	}
	f, _ := writeFile(t, NewSchema(Field{"a", Utf8Type()}), []Data{OfStrings(strs)}, []Data{OfStrings(strs[:3])})
	for b, err := range f.Scan(t.Context(), Query{Rows: Range{End: 50}}) {
		if err != nil {
			t.Fatal(err)
		}
		for i := range b.Len() {
			if string(b.Col(0).Bytes(i)) != strs[i] {
				t.Fatalf("row %d: %q", i, b.Col(0).Bytes(i))
			}
		}
	}
}

func TestFaultInSecondColumn(t *testing.T) {
	// Every Write reaches the io.Writer: the second column fails after the first has been
	// written, and the error is that of the io.Writer.
	lowered(t, &smallWrites, 8)
	lowered(t, &rowBlock, 10)
	lowered(t, &chunkBytes, 1)
	cols := []Field{{"a", Int64Type()}, {"b", Int64Type()}, {"c", Utf8Type()}}
	data := []Data{Of(make([]int64, 100)), Of(make([]int64, 100)), OfStrings(make([]string, 100))}
	for n := 2; n < 12; n++ {
		w := NewWriter(&failingWriter{failAt: n}, NewSchema(cols...), WriteOptions{})
		if err := w.Write(data...); !errors.Is(err, errFault) {
			t.Fatalf("call %d: %v", n, err)
		}
	}
}
