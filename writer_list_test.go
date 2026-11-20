package vortex

import (
	"bytes"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/compress"
)

// listCases are lists of every shape the writer meets.
func listCases() map[string]struct {
	typ  Type
	data Data
} {
	i32 := Of([]int32{1, 2, 3, 4, 5})
	words := OfStrings([]string{"a", "bc", "d"})
	inner := OfList([]int{0, 2, 2, 3}, Of([]int64{7, 8, 9}))
	type c = struct {
		typ  Type
		data Data
	}
	return map[string]c{
		"i32":      {ListOf(Int32Type()), OfList([]int{0, 2, 2, 5}, i32)},
		"utf8":     {ListOf(Utf8Type()), OfList([]int{0, 1, 3, 3}, words)},
		"nested":   {ListOf(ListOf(Int64Type())), OfList([]int{0, 2, 3}, inner)},
		"nullable": {ListOf(Int32Type()).AsNullable(), OfList([]int{0, 2, 2, 5}, i32).Nulls([]bool{false, true, false})},
		"empty":    {ListOf(Int32Type()), OfList([]int{0, 0, 0}, Of([]int32{}))},
		"no rows":  {ListOf(Int32Type()), OfList([]int{0}, Of([]int32{}))},
	}
}

func TestCompressedListIsAVortexList(t *testing.T) {
	for name, tc := range listCases() {
		for _, c := range []Compression{Default, Compact, None} {
			s := NewSchema(Field{"c", tc.typ})
			f := openBytes(t, writeWith(t, c, s, []Data{tc.data}, tc.data.n))
			want, err := readBack(openBytes(t, writeWith(t, None, s, []Data{tc.data}, tc.data.n)), tc.typ)
			if err != nil {
				t.Fatal(err)
			}
			got, err := readBack(f, tc.typ)
			if err != nil || len(got) != len(want) {
				t.Fatalf("%s level %d: %d rows, %v", name, c, len(got), err)
			}
			for i := range got {
				if !eqVal(got[i], want[i]) {
					t.Fatalf("%s level %d: row %d is %s, want %s", name, c, i, describeVal(got[i]), describeVal(want[i]))
				}
			}
			if tc.data.n == 0 {
				continue
			}
			plain := c == None
			if slices.Contains(f.arrayEncodings, "vortex.list") == plain ||
				slices.Contains(f.arrayEncodings, "vortex.listview") != plain {
				t.Errorf("%s level %d: encodings %v", name, c, f.arrayEncodings)
			}
		}
	}
}

// TestListChunksStartAtZero writes a list column in several chunks: each has its own offsets.
func TestListChunksStartAtZero(t *testing.T) {
	lowered(t, &chunkBytes, 1) // a chunk per write
	ty := ListOf(Int32Type())
	s := NewSchema(Field{"c", ty})
	var buf bytes.Buffer
	w := NewWriter(&buf, s, WriteOptions{})
	var want []val
	for k := range 3 {
		base := int32(10 * k)
		d := OfList([]int{0, 1, 3}, Of([]int32{base, base + 1, base + 2}))
		if err := w.Write(d); err != nil {
			t.Fatal(err)
		}
		rows, err := readBack(openBytes(t, writeWith(t, None, s, []Data{d}, 2)), ty)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, rows...)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := readBack(openBytes(t, buf.Bytes()), ty)
	if err != nil || len(got) != len(want) {
		t.Fatalf("%d rows, %v", len(got), err)
	}
	for i := range got {
		if !eqVal(got[i], want[i]) {
			t.Fatalf("row %d is %s, want %s", i, describeVal(got[i]), describeVal(want[i]))
		}
	}
}

func TestOffsetType(t *testing.T) {
	for last, want := range map[int]compress.PType{0: compress.U8, 200: compress.U8, 255: compress.U8,
		256: compress.U16, 65535: compress.U16, 70_000: compress.U32} {
		if got := offsetType(last); got != want {
			t.Errorf("offsetType(%d) = %d, want %d", last, got, want)
		}
	}
}

// TestListWidths round-trips lists whose offsets need u8, u16 and u32.
func TestListWidths(t *testing.T) {
	for _, n := range []int{200, 1000, 70_000} {
		ty := ListOf(Int32Type())
		offs := make([]int, n+1)
		vals := make([]int32, n)
		for i := range n {
			offs[i+1], vals[i] = i+1, int32(i)
		}
		s := NewSchema(Field{"c", ty})
		d := OfList(offs, Of(vals))
		f := openBytes(t, writeWith(t, Default, s, []Data{d}, n))
		got, err := readBack(f, ty)
		if err != nil || len(got) != n {
			t.Fatalf("%d: %d rows, %v", n, len(got), err)
		}
		if !slices.Contains(f.arrayEncodings, "vortex.list") {
			t.Errorf("%d: %v", n, f.arrayEncodings)
		}
	}
}

// TestListFileIsNotLarger: without the sizes, a compressed list column is smaller than the plain one.
func TestListFileIsNotLarger(t *testing.T) {
	n := 100_000
	offs := make([]int, n+1)
	vals := make([]int32, 2*n)
	for i := range n {
		offs[i+1] = 2 * (i + 1)
		vals[2*i], vals[2*i+1] = int32(i), int32(i%7)
	}
	s := NewSchema(Field{"c", ListOf(Int32Type())})
	d := OfList(offs, Of(vals))
	plain := len(writeWith(t, None, s, []Data{d}, n))
	if got := len(writeWith(t, Default, s, []Data{d}, n)); got > plain {
		t.Fatalf("compressed %d bytes, plain %d", got, plain)
	}
}
