package vortex

import (
	"slices"
	"testing"
)

// TestBinaryColumnIsAVarBin: 16-byte binary values are a vortex.varbin in a compressed file,
// with and without nulls, and read back the same; a utf8 column keeps its views.
func TestBinaryColumnIsAVarBin(t *testing.T) {
	const n = 5000
	bins := make([][]byte, n)
	strs := make([]string, n)
	nulls := make([]bool, n)
	for i := range bins {
		bins[i] = []byte{byte(i), byte(i >> 8), 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, byte(i * 7)}
		strs[i] = string(bins[i][2:15]) + string(rune(97+i%26)) + string(rune(97+i/26%26))
		nulls[i] = i%11 == 3
	}
	for _, withNulls := range []bool{false, true} {
		ty, d := BinaryType(), OfBytes(bins)
		if withNulls {
			ty, d = ty.AsNullable(), d.Nulls(nulls)
		}
		s := NewSchema(Field{"c", ty})
		f := openBytes(t, writeWith(t, Default, s, []Data{d}, n))
		if !slices.Contains(f.arrayEncodings, "vortex.varbin") {
			t.Fatalf("nulls %v: %v", withNulls, f.arrayEncodings)
		}
		want, err := readBack(openBytes(t, writeWith(t, None, s, []Data{d}, n)), ty)
		if err != nil {
			t.Fatal(err)
		}
		got, err := readBack(f, ty)
		if err != nil || len(got) != n {
			t.Fatalf("%d rows, %v", len(got), err)
		}
		for i := range got {
			if !eqVal(got[i], want[i]) {
				t.Fatalf("row %d is %s, want %s", i, describeVal(got[i]), describeVal(want[i]))
			}
		}
	}
	s := NewSchema(Field{"c", Utf8Type()})
	f := openBytes(t, writeWith(t, Default, s, []Data{OfStrings(strs)}, n))
	if slices.Contains(f.arrayEncodings, "vortex.varbin") {
		t.Errorf("utf8: %v", f.arrayEncodings)
	}
}
