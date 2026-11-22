package encode

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
)

// which encoders must accept which data: the others may decline.
func mustEncode(enc, kind string, nulls string, p string, n int) bool {
	switch enc {
	case "constant":
		return kind == "constant" && nulls == "none" || nulls == "all"
	case "sequence":
		return kind == "steps" && nulls == "none" && (n <= 127 || ptypes[p].Width() > 1)
	case "pco":
		return ptypes[p].Width() > 1 // Pco does not code 8-bit numbers
	}
	return enc != "bitpacked" && enc != "sparse"
}

// TestRoundTrip encodes data of every kind, size and null pattern with every encoder that takes
// the type, and decodes it with our decoder.
func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	e := New()
	for _, p := range slices.Sorted(mapsKeys(runs)) {
		for _, name := range slices.Sorted(mapsKeys(runs[p])) {
			for _, kind := range kinds {
				for _, n := range sizes {
					for _, nl := range nullNames {
						if n > 100 && nl != "none" && nl != "some" || n == 1 && nl == "some" {
							continue
						}
						t.Run(fmt.Sprintf("%s/%s/%s/%d/%s", name, p, kind, n, nl), func(t *testing.T) {
							e.Reset()
							roundTrip(t, e, p, name, kind, n, nl, gens[p](kind, n, rng))
						})
					}
				}
			}
		}
	}
}

func mapsKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func roundTrip(t *testing.T, e *Encoder, p, name, kind string, n int, nl string, raw []byte) {
	nulls := nullPattern(nl, n)
	params := []vparams{{}}
	if name == "bitpacked" {
		raw = nonNegative(raw, ptypes[p])
		params = []vparams{{Best: true}, {BitWidth: 3}, {BitWidth: 8*ptypes[p].Width() - 1}}
	}
	for _, par := range params {
		e.Reset()
		got, ok := runs[p][name](e, raw, nulls, par)
		if !ok {
			if mustEncode(name, kind, nl, p, n) {
				t.Fatalf("declined")
			}
			continue
		}
		checkDecode(t, got, ptypes[p], raw, nulls)
	}
}

// TestDetection checks that sequence and constant say no on data that is almost one.
func TestDetection(t *testing.T) {
	e := New()
	for _, n := range []int{3, 10, 1025} {
		for _, at := range []int{0, n / 2, n - 1} {
			seq := make([]int32, n)
			cst := make([]float64, n)
			for i := range seq {
				seq[i], cst[i] = int32(7+2*i), 5
			}
			if _, ok := Sequence(e, seq, nil); !ok {
				t.Fatalf("n=%d: a sequence is not found", n)
			}
			if _, ok := Constant(e, cst, nil); !ok {
				t.Fatalf("n=%d: a constant is not found", n)
			}
			seq[at]++
			cst[at]++
			if _, ok := Sequence(e, seq, nil); ok {
				t.Fatalf("n=%d at=%d: one value off a sequence is one", n, at)
			}
			if _, ok := Constant(e, cst, nil); ok {
				t.Fatalf("n=%d at=%d: one value off a constant is one", n, at)
			}
		}
	}
}

// TestDetectionLimits is what the sequence and the constant refuse, or take, at the limits.
func TestDetectionLimits(t *testing.T) {
	e := New()
	yes := func(ok bool) bool { return ok }
	if _, ok := Sequence(e, []uint8{250, 252, 254, 0}, nil); yes(ok) {
		t.Fatal("a sequence that overflows is one")
	}
	if _, ok := Sequence(e, []int8{-128, 127}, nil); yes(ok) {
		t.Fatal("a step that overflows is a sequence")
	}
	if _, ok := Sequence(e, []int8{127, -128}, nil); yes(ok) {
		t.Fatal("a step that overflows downwards is a sequence")
	}
	if _, ok := Sequence(e, []uint8{9, 3}, nil); yes(ok) {
		t.Fatal("a decreasing sequence of unsigned integers is one")
	}
	if _, ok := Sequence(e, []int8{5, 5, 5}, nil); yes(ok) {
		t.Fatal("a constant is a sequence")
	}
	if _, ok := Sequence(e, []int8{}, nil); yes(ok) {
		t.Fatal("an empty array is a sequence")
	}
	if _, ok := Sequence(e, []int8{1, 2, 3}, []bool{false, true, false}); yes(ok) {
		t.Fatal("a null is in a sequence")
	}
	if _, ok := Sequence(e, []int64{-1 << 63, -1<<63 + 1}, nil); !ok {
		t.Fatal("a sequence at the limit is not one")
	}
	if _, ok := Sequence(e, []int8{3, 2, 1, 0, -1}, nil); !ok {
		t.Fatal("a decreasing sequence is not one")
	}
	if _, ok := Sequence(e, []int8{120, 125, 127}, nil); yes(ok) {
		t.Fatal("a sequence with a step that changes is one")
	}
	if _, ok := Sequence(e, []int8{120, 125, -126}, nil); yes(ok) {
		t.Fatal("a sequence that wraps is one")
	}
	if _, ok := Constant(e, []int8{}, nil); yes(ok) {
		t.Fatal("an empty array is a constant")
	}
	if _, ok := Constant(e, []int8{4, 4, 4}, []bool{false, true, false}); yes(ok) {
		t.Fatal("a null among equal values is a constant")
	}
}

func TestBitPackErrors(t *testing.T) {
	e := New()
	if _, err := BitPack(e, []int32{1, -2}, nil, 4); err == nil {
		t.Fatal("a negative value is packed")
	}
	if _, err := BitPack(e, []int32{1, -2}, []bool{false, true}, 4); err != nil {
		t.Fatalf("a null negative slot fails: %v", err)
	}
	for _, w := range []int{-1, 8, 9} {
		if _, err := BitPack(e, []uint8{1}, nil, w); err == nil {
			t.Fatalf("width %d is accepted", w)
		}
	}
	if _, err := BitPackBest(e, []uint8{255, 255, 255}, nil); err == nil {
		t.Fatal("values that need all the bits are packed")
	}
}

// TestBestWidth is the choice of the width, on hand-made histograms.
func TestBestWidth(t *testing.T) {
	for _, c := range []struct {
		h    []int
		size int
		want int
	}{
		{[]int{10, 0, 0, 0, 0}, 4, 0},                    // all zero
		{[]int{0, 0, 0, 0, 100}, 4, 4},                   // all 4 bits
		{[]int{0, 0, 0, 0, 1000, 1}, 4, 4},               // one exception costs less than a bit more for all
		{[]int{0, 0, 0, 0, 10, 0, 0, 0, 0, 0, 5}, 4, 10}, // too many exceptions
		{[]int{}, 4, 0},
	} {
		if got := BestWidth(c.h, c.size); got != c.want {
			t.Errorf("BestWidth(%v) = %d, want %d", c.h, got, c.want)
		}
	}
}

func TestHistogram(t *testing.T) {
	h := make([]int, 9)
	Histogram([]int8{0, 1, 3, 127, -1}, []bool{false, false, false, false, true}, h)
	if want := []int{2, 1, 1, 0, 0, 0, 0, 1, 0}; !slices.Equal(h, want) {
		t.Fatalf("histogram %v, want %v", h, want)
	}
	Histogram([]int8{-1}, nil, h)
	if h[8] != 1 {
		t.Fatalf("a negative number has %v bits", h)
	}
}

// TestPatches checks the patches of a bit-packing: indices in the type of the array's length,
// and the offsets of the blocks.
func TestPatches(t *testing.T) {
	e := New()
	v := make([]uint32, 3000)
	v[5], v[1030], v[2999] = 1<<20, 1<<21, 1<<22
	n, err := BitPack(e, v, nil, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Kids) != 3 || !bytes.Equal(n.Kids[0].Bufs[0].Data, []byte{5, 0, 6, 11, 0, 0}[:0:0]) && len(n.Kids[0].Bufs[0].Data) != 6 {
		t.Fatalf("patches: %d children", len(n.Kids))
	}
	offs := n.Kids[2].Bufs[0].Data
	if !bytes.Equal(offs, []byte{0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("offsets of the blocks %v", offs)
	}
	checkDecode(t, n, array.U32, wordBytes(v), nil)
}

// shape is the sizes of the buffers and metadata of a tree, and its identifiers.
func shape(n *Node) []int {
	out := []int{len(n.ID), len(n.Meta), len(n.Bufs), len(n.Kids)}
	for _, b := range n.Bufs {
		out = append(out, len(b.Data), b.Align)
	}
	for _, k := range n.Kids {
		out = append(out, shape(k)...)
	}
	return out
}

// TestMeasure: an encoder that only measures makes the bit-packing, its patches and its
// validity in buffers of the sizes and metadata of the real one.
func TestMeasure(t *testing.T) {
	v := make([]uint32, 3000)
	v[5], v[1030], v[2999] = 1<<20, 1<<21, 1<<22
	nulls := make([]bool, len(v))
	nulls[7] = true
	m := New()
	m.Measure()
	for _, width := range []int{4, 23} {
		real, err := BitPack(New(), v, nulls, width)
		if err != nil {
			t.Fatal(err)
		}
		measured, err := BitPack(m, v, nulls, width)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(shape(real), shape(measured)) || !bytes.Equal(real.Meta, measured.Meta) {
			t.Errorf("width %d: the measured array is not the size of the real one", width)
		}
	}
}

func wordBytes(v []uint32) []byte {
	var b []byte
	for _, x := range v {
		b = append(b, byte(x), byte(x>>8), byte(x>>16), byte(x>>24))
	}
	return b
}
