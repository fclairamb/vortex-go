package encode

import (
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
)

func TestConstantBytesAndBools(t *testing.T) {
	e := New()
	for _, tc := range []struct {
		v     [][]byte
		nulls []bool
		utf8  bool
		ok    bool
	}{
		{[][]byte{[]byte("ab"), []byte("ab")}, nil, true, true},
		{[][]byte{{1, 2}, {1, 2}, {1, 2}}, nil, false, true},
		{[][]byte{nil, nil}, []bool{true, true}, true, true},
		{[][]byte{[]byte("ab"), []byte("ac")}, nil, true, false},
		{[][]byte{[]byte("ab"), nil}, []bool{false, true}, true, false},
		{nil, nil, true, false},
	} {
		n, ok := ConstantBytes(e, tc.v, tc.nulls, tc.utf8)
		if ok != tc.ok {
			t.Fatalf("%q: %v", tc.v, ok)
		}
		if !ok {
			continue
		}
		kind := array.Binary
		if tc.utf8 {
			kind = array.Utf8
		}
		a := decode(t, n, array.DType{Kind: kind, Nullable: hasNull(tc.nulls)}, len(tc.v))
		for i, x := range tc.v {
			if a.Null(i) != isNull(tc.nulls, i) || !isNull(tc.nulls, i) && string(a.Data[a.Starts[i]:a.Ends[i]]) != string(x) {
				t.Fatalf("%q: row %d", tc.v, i)
			}
		}
	}
	for _, tc := range []struct {
		v, nulls []bool
		ok       bool
	}{
		{[]bool{true, true}, nil, true}, {[]bool{false}, nil, true}, {[]bool{false, false}, []bool{true, true}, true},
		{[]bool{true, false}, nil, false}, {[]bool{true, true}, []bool{false, true}, false}, {nil, nil, false},
	} {
		n, ok := ConstantBools(e, tc.v, tc.nulls)
		if ok != tc.ok {
			t.Fatalf("%v %v: %v", tc.v, tc.nulls, ok)
		}
		if !ok {
			continue
		}
		a := decode(t, n, array.DType{Kind: array.Bool, Nullable: hasNull(tc.nulls)}, len(tc.v))
		for i, x := range tc.v {
			if a.Null(i) != isNull(tc.nulls, i) || !isNull(tc.nulls, i) && a.Bits.Get(i) != x {
				t.Fatalf("%v %v: row %d", tc.v, tc.nulls, i)
			}
		}
	}
}

// TestZstdSmallFrames: a frame of under 256 bytes declares its content size, which the
// reference reader needs (the single segment flag of the frame header).
func TestZstdSmallFrames(t *testing.T) {
	e := New()
	for _, n := range []int{1, 7, 100, 3000} {
		node := Zstd(e, make([]int8, n), nil)
		if fhd := node.Bufs[0].Data[4]; fhd&0x20 == 0 {
			t.Errorf("%d bytes: frame header %08b, no single segment", n, fhd)
		}
	}
}

// TestArenaFitsTheLastRound: after a Reset, the arena holds in one block all that the calls
// before it asked for.
func TestArenaFitsTheLastRound(t *testing.T) {
	e := New()
	for range 3 {
		e.alloc(100 << 10)
		e.alloc(200 << 10)
	}
	e.Reset()
	if len(e.arena) < 900<<10 {
		t.Fatalf("an arena of %d bytes", len(e.arena))
	}
	before := len(e.arena)
	e.alloc(300 << 10)
	e.Reset()
	if len(e.arena) != before {
		t.Fatalf("the arena changed: %d, %d", before, len(e.arena))
	}
}
