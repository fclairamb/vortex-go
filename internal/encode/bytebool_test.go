package encode

import (
	"encoding/binary"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
)

func boolsAndNulls(n int, withNulls bool) ([]bool, []bool) {
	v := make([]bool, n)
	var nulls []bool
	if withNulls {
		nulls = make([]bool, n)
	}
	for i := range v {
		v[i] = i%3 != 0
		if withNulls {
			nulls[i] = i%4 == 1
		}
	}
	return v, nulls
}

func checkBools(t *testing.T, a *array.Array, v, nulls []bool) {
	t.Helper()
	for i := range v {
		if isNull(nulls, i) {
			if !a.Null(i) {
				t.Fatalf("row %d is valid, want null", i)
			}
			continue
		}
		if a.Null(i) || a.Bits.Get(i) != v[i] {
			t.Fatalf("row %d is %v (null %v), want %v", i, a.Bits.Get(i), a.Null(i), v[i])
		}
	}
}

func TestByteBool(t *testing.T) {
	e := New()
	for _, n := range []int{0, 1, 8193} {
		for _, withNulls := range []bool{false, true} {
			v, nulls := boolsAndNulls(n, withNulls)
			node := ByteBool(e, v, nulls)
			if node.ID != IDByteBool || len(node.Bufs) != 1 || len(node.Bufs[0].Data) != n {
				t.Fatalf("%d rows: %+v", n, node)
			}
			for i, b := range node.Bufs[0].Data {
				if b > 1 || (b == 1 && (!v[i] || isNull(nulls, i))) {
					t.Fatalf("byte %d is %d", i, b)
				}
			}
			a := decode(t, node, array.DType{Kind: array.Bool, Nullable: hasNull(nulls)}, n)
			checkBools(t, a, v, nulls)
		}
	}
}

func TestChunked(t *testing.T) {
	e := New()
	typ := func(nulls bool) array.DType { return array.DType{Kind: array.Bool, Nullable: nulls} }
	for name, sizes := range map[string][]int{
		"one part": {10}, "three parts": {4, 5, 6}, "an empty part": {4, 0, 6}, "only empty": {0, 0},
	} {
		for _, withNulls := range []bool{false, true} {
			total := 0
			for _, s := range sizes {
				total += s
			}
			v, nulls := boolsAndNulls(total, withNulls)
			var parts []*Node
			lo := 0
			for _, s := range sizes {
				var pn []bool
				if withNulls {
					pn = nulls[lo : lo+s]
				}
				parts = append(parts, Bools(e, v[lo:lo+s], pn))
				lo += s
			}
			node := Chunked(e, parts, sizes)
			if node.ID != IDChunked || len(node.Kids) != len(sizes)+1 || len(node.Bufs) != 0 {
				t.Fatalf("%s: %+v", name, node)
			}
			offs := node.Kids[0].Bufs[0].Data
			if len(offs) != 8*(len(sizes)+1) || binary.LittleEndian.Uint64(offs[len(offs)-8:]) != uint64(total) {
				t.Fatalf("%s: offsets %v", name, offs)
			}
			a := decode(t, node, typ(withNulls), total)
			checkBools(t, a, v, nulls)
		}
	}
	// a primitive part
	raw := []byte{1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0}
	node := Chunked(e, []*Node{Primitive(e, raw[:4], 4, nil, 1), Primitive(e, raw[4:], 4, nil, 2)}, []int{1, 2})
	a := decode(t, node, array.DType{Kind: array.Prim, PType: array.I32}, 3)
	if string(a.Data) != string(raw) {
		t.Fatalf("%x", a.Data)
	}
}
