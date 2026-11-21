package encode

import (
	"math"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
)

// maskedPatterns are the nulls of 40 rows: one valid row among nulls, nulls at both ends, half.
func maskedPatterns() map[string][]bool {
	one, ends, half := make([]bool, 40), make([]bool, 40), make([]bool, 40)
	for i := range one {
		one[i] = i != 17
		ends[i] = i < 5 || i >= 35
		half[i] = i%2 == 0
	}
	return map[string][]bool{"one valid": one, "both ends": ends, "half": half}
}

func TestMaskedNumbers(t *testing.T) {
	e := New()
	for name, nulls := range maskedPatterns() {
		v := make([]int32, 40)
		for i := range v {
			v[i] = 7
			if nulls[i] {
				v[i] = int32(i) // a null row holds anything
			}
		}
		n, ok := ConstantMasked(e, v, nulls)
		if !ok || n.ID != IDMasked || n.Kids[0].ID != IDConstant || len(n.Bufs) != 0 || n.Meta != nil {
			t.Fatalf("%s: %v %+v", name, ok, n)
		}
		checkDecode(t, n, array.PType(array.I32), rawOfInts(v, nulls, 7), nulls)
	}
}

// rawOfInts is the bytes of v where a null row is want.
func rawOfInts(v []int32, nulls []bool, want int32) []byte {
	out := make([]byte, 0, 4*len(v))
	for i := range v {
		x := v[i]
		if nulls[i] {
			x = want
		}
		out = append(out, byte(x), byte(x>>8), byte(x>>16), byte(x>>24))
	}
	return out
}

func TestMaskedFloatsByBits(t *testing.T) {
	e := New()
	nan, negZero := math.NaN(), math.Copysign(0, -1)
	nulls := []bool{true, false, false, true, false}
	n, ok := ConstantMasked(e, []float64{1, nan, nan, 2, nan}, nulls)
	if !ok || n.ID != IDMasked {
		t.Fatalf("NaNs: %v", ok)
	}
	a := decode(t, n, array.DType{Kind: array.Prim, PType: array.F64, Nullable: true}, 5)
	if a.Null(1) || !math.IsNaN(math.Float64frombits(leU64(a.Data[8:]))) {
		t.Fatal("NaN lost")
	}
	n, ok = ConstantMasked(e, []float64{1, negZero, negZero, 2, negZero}, nulls)
	if !ok {
		t.Fatal("-0 refused")
	}
	a = decode(t, n, array.DType{Kind: array.Prim, PType: array.F64, Nullable: true}, 5)
	if leU64(a.Data[8:]) != math.Float64bits(negZero) {
		t.Fatalf("-0 became %x", leU64(a.Data[8:]))
	}
	if _, ok = ConstantMasked(e, []float64{1, 0, negZero, 2, 0}, nulls); ok {
		t.Fatal("0 and -0 are two values")
	}
}

func leU64(b []byte) uint64 {
	var x uint64
	for i := 7; i >= 0; i-- {
		x = x<<8 | uint64(b[i])
	}
	return x
}

func TestMaskedNegatives(t *testing.T) {
	e := New()
	two := []int32{1, 2, 1, 1}
	if n, ok := ConstantMasked(e, two, []bool{false, false, true, true}); ok {
		t.Fatalf("two valid values gave %s", n.ID)
	}
	if n, ok := ConstantMasked(e, []int32{4, 4}, nil); !ok || n.ID != IDConstant {
		t.Fatal("no null must be the constant alone")
	}
	if n, ok := ConstantMasked(e, []int32{4, 5}, []bool{true, true}); !ok || n.ID != IDConstant {
		t.Fatal("all null must be the null constant")
	}
	if _, ok := ConstantMasked(e, []int32{}, nil); ok {
		t.Fatal("empty")
	}
	if _, ok := ConstantBytesMasked(e, [][]byte{[]byte("a"), []byte("b")}, []bool{false, false}, true); ok {
		t.Fatal("two strings")
	}
	if _, ok := ConstantBoolsMasked(e, []bool{true, false, true}, []bool{false, false, true}); ok {
		t.Fatal("two bools")
	}
}

func TestMaskedStringsAndBools(t *testing.T) {
	e := New()
	for name, nulls := range maskedPatterns() {
		v := make([][]byte, 40)
		bools := make([]bool, 40)
		for i := range v {
			v[i], bools[i] = []byte("ab"), true
			if nulls[i] {
				v[i], bools[i] = []byte("zz"), false
			}
		}
		for _, utf8 := range []bool{true, false} {
			n, ok := ConstantBytesMasked(e, v, nulls, utf8)
			if !ok || n.ID != IDMasked {
				t.Fatalf("%s: %v", name, ok)
			}
			kind := array.Binary
			if utf8 {
				kind = array.Utf8
			}
			a := decode(t, n, array.DType{Kind: kind, Nullable: true}, 40)
			for i := range v {
				if a.Null(i) != nulls[i] || !nulls[i] && string(a.Data[a.Starts[i]:a.Ends[i]]) != "ab" {
					t.Fatalf("%s: row %d", name, i)
				}
			}
		}
		n, ok := ConstantBoolsMasked(e, bools, nulls)
		if !ok || n.ID != IDMasked {
			t.Fatalf("%s bools: %v", name, ok)
		}
		a := decode(t, n, array.DType{Kind: array.Bool, Nullable: true}, 40)
		for i := range bools {
			if a.Null(i) != nulls[i] || !nulls[i] && !a.Bits.Get(i) {
				t.Fatalf("%s bools: row %d", name, i)
			}
		}
	}
}

func TestNoAllocationMasked(t *testing.T) {
	e := New()
	nulls := make([]bool, 3000)
	v := make([]int64, 3000)
	bins := make([][]byte, 3000)
	bools := make([]bool, 3000)
	for i := range v {
		nulls[i], v[i], bins[i], bools[i] = i%3 != 0, 5, []byte("same"), true
	}
	for name, f := range map[string]func(){
		"numbers": func() { e.Reset(); ConstantMasked(e, v, nulls) },
		"bytes":   func() { e.Reset(); ConstantBytesMasked(e, bins, nulls, true) },
		"bools":   func() { e.Reset(); ConstantBoolsMasked(e, bools, nulls) },
	} {
		f()
		if got := testing.AllocsPerRun(5, f); got != 0 {
			t.Errorf("%s: %v allocations", name, got)
		}
	}
}
