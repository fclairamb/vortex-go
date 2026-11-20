package encode

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// floatColumns makes the columns of floats of the spec: prices, measurements with noise,
// integers stored as floats, random bits, the values that never round trip, one value.
func floatColumns[F Float](rng *rand.Rand, n int) map[string][]F {
	out := map[string][]F{}
	mk := func(name string, f func(i int) F) {
		v := make([]F, n)
		for i := range v {
			v[i] = f(i)
		}
		out[name] = v
	}
	mk("prices", func(int) F { return F(rng.IntN(1_000_000)) / 100 })
	mk("noise", func(int) F { return F(20+rng.Float64()*0.001) + F(rng.IntN(1000))*1e-12 })
	mk("ints", func(int) F { return F(rng.IntN(100_000) - 500) })
	mk("bits", func(int) F {
		if unsafex.Size[F]() == 4 {
			return F(math.Float32frombits(rng.Uint32()))
		}
		return F(math.Float64frombits(rng.Uint64()))
	})
	special := []F{1.5, F(math.NaN()), F(math.Copysign(0, -1)), F(math.Inf(1)), 2.25, F(math.Inf(-1)), 0}
	mk("special", func(i int) F { return special[i%len(special)] })
	mk("one", func(int) F { return 3.25 })
	return out
}

// floatEncoders are the encoders of floats and zstd, on the numbers of v.
func floatEncoders[F Float]() map[string]func(e *Encoder, v []F, nulls []bool) *Node {
	return map[string]func(e *Encoder, v []F, nulls []bool) *Node{
		"alp": ALP[F],
		"alprd": func(e *Encoder, v []F, nulls []bool) *Node {
			n, _ := ALPRD(e, v, nulls)
			return n
		},
		"zstd": Zstd[F],
	}
}

// TestFloatsBitExact writes the columns of floats with every encoder, with and without nulls,
// and reads them back: every value valid has the bits it was written with.
func TestFloatsBitExact(t *testing.T) {
	rng := rand.New(rand.NewPCG(12, 13))
	e := New()
	for _, p := range []string{"f32", "f64"} {
		cols := map[string][]float64{}
		for name, v := range floatColumns[float64](rng, 2100) {
			cols[name] = v
		}
		for name, v := range cols {
			for _, nl := range []string{"none", "some", "all", "most"} {
				for _, n := range []int{len(v), 1} {
					nulls := nullPattern(nl, n)
					raw := floatRaw(p, v[:n])
					for enc, run := range floatEncoders[float64]() {
						t.Run(p+"/"+name+"/"+nl+"/"+enc, func(t *testing.T) {
							e.Reset()
							var node *Node
							if p == "f32" {
								node = floatEncoders[float32]()[enc](e, unsafex.Slice[float32](raw), nulls)
							} else {
								node = run(e, unsafex.Slice[float64](raw), nulls)
							}
							checkDecode(t, node, ptypes[p], raw, nulls)
						})
					}
				}
			}
		}
	}
}

// floatRaw is the bytes of the floats of v as the type p.
func floatRaw(p string, v []float64) []byte {
	if p == "f64" {
		return append([]byte(nil), unsafex.Bytes(v)...)
	}
	f := make([]float32, len(v))
	for i, x := range v {
		f[i] = float32(x)
	}
	return append([]byte(nil), unsafex.Bytes(f)...)
}

// TestALPPatches checks what goes to the patches: a NaN, an infinity and a -0 always do, a
// value with a short decimal form never does, and a null is never patched.
func TestALPPatches(t *testing.T) {
	e := New()
	v := []float64{1.5, math.NaN(), 2.25, math.Inf(1), math.Inf(-1), math.Copysign(0, -1), 0, 3.5}
	n := ALP(e, v, nil)
	if len(n.Kids) != 4 {
		t.Fatalf("%d children, want the integers and 3 of the patches", len(n.Kids))
	}
	if got := unsafex.Slice[uint64](n.Kids[1].Bufs[0].Data); len(got) != 4 || got[0] != 1 || got[1] != 3 || got[2] != 4 || got[3] != 5 {
		t.Fatalf("patches at %v, want NaN, +Inf, -Inf and -0", got)
	}
	// the integer of a patch is the one of the first value that is not: 1.5 is 15 at (1, 0)
	ints := unsafex.Slice[int64](n.Kids[0].Bufs[0].Data)
	for _, i := range []int{1, 3, 4, 5} {
		if ints[i] != ints[0] {
			t.Fatalf("patched row %d holds %d, want %d", i, ints[i], ints[0])
		}
	}
	e.Reset()
	// nulls hold values that do not round trip, and are not patches; the integer of a null
	// is the fill all the same
	w := []float64{1.5, math.NaN(), 2.5}
	n = ALP(e, w, []bool{false, true, false})
	if len(n.Kids) != 1 {
		t.Fatalf("a null is a patch: %d children", len(n.Kids))
	}
	e.Reset()
	// no value round trips: the integers are kept as they come
	n = ALP(e, []float64{math.NaN(), math.Inf(1)}, nil)
	if len(n.Kids) != 4 {
		t.Fatalf("%d children", len(n.Kids))
	}
	checkDecode(t, n, array.F64, unsafex.Bytes([]float64{math.NaN(), math.Inf(1)}), nil)
}

// TestALPChunkOffsets checks the number of patches before each chunk of 1024 rows, nulls
// taken off.
func TestALPChunkOffsets(t *testing.T) {
	e := New()
	v := make([]float64, 3000)
	nulls := make([]bool, len(v))
	for i := range v {
		v[i] = float64(i) / 4
	}
	for _, i := range []int{3, 1500, 1501, 2500} {
		v[i] = math.NaN()
	}
	nulls[3], nulls[1501] = true, true // patches of the first and the second chunk that are nulls
	n := ALP(e, v, nulls)
	offs := unsafex.Slice[uint64](n.Kids[3].Bufs[0].Data)
	if len(offs) != 3 || offs[0] != 0 || offs[1] != 0 || offs[2] != 1 {
		t.Fatalf("offsets %v, want 0 0 1", offs)
	}
	if idx := unsafex.Slice[uint64](n.Kids[1].Bufs[0].Data); len(idx) != 2 || idx[0] != 1500 || idx[1] != 2500 {
		t.Fatalf("patches at %v", idx)
	}
}

func TestALPExponents(t *testing.T) {
	e := New()
	if got := BestExponents(e, []float64{1.234, 1.234, 1.234}); got != (Exponents{9, 6}) && got.E-got.F != 3 {
		t.Fatalf("exponents %v for 3 digits", got)
	}
	if got := BestExponents(e, []float32{}); got != (Exponents{}) {
		t.Fatalf("exponents %v for no value", got)
	}
	// a long column: the sample is runs of 8 rows spread over it
	v := make([]float32, 100_000)
	for i := range v {
		v[i] = float32(i%1000) / 100
	}
	if got := BestExponents(e, v); got.E-got.F != 2 {
		t.Fatalf("exponents %v for 2 digits", got)
	}
	if ALPWith(e, []float64{1}, nil, Exponents{19, 0}) != nil || ALPWith(e, []float32{1}, nil, Exponents{0, 11}) != nil {
		t.Fatal("exponents past the limit are accepted")
	}
	if n := ALPWith(e, []float32{1.5, 2.5}, nil, Exponents{1, 0}); n == nil {
		t.Fatal("fixed exponents are refused")
	}
	// a range of integers that does not fit the type has all the bits
	if got := BestExponents(e, []float32{3e9, -3e9}); got.E != 0 && got.F != 0 {
		t.Logf("exponents %v", got)
	}
	// saturation: NaN is 0, out of range is the limit
	if saturate[float64, int64](math.NaN()) != 0 || saturate[float64, int64](1e30) != math.MaxInt64 ||
		saturate[float64, int64](-1e30) != math.MinInt64 || saturate[float32, int32](float32(3e9)) != math.MaxInt32 {
		t.Fatal("saturate")
	}
}

func TestALPRD(t *testing.T) {
	e := New()
	if _, ok := ALPRD(e, []float64{}, nil); ok {
		t.Fatal("no row is encoded")
	}
	v := []float64{math.Pi, math.E, 1e300, 5e-324, -1}
	if _, ok := ALPRDWith(e, v, nil, 0, []uint16{1}); ok {
		t.Fatal("a cut of 0 bits")
	}
	if _, ok := ALPRDWith(e, v, nil, 64, []uint16{1}); ok {
		t.Fatal("a cut of 64 bits")
	}
	if _, ok := ALPRDWith(e, v, nil, 48, nil); ok {
		t.Fatal("no dictionary")
	}
	if _, ok := ALPRDWith(e, v, nil, 48, make([]uint16, 9)); ok {
		t.Fatal("a dictionary of 9")
	}
	// the dictionary of the first two left parts: the others are patches
	d := []uint16{uint16(math.Float64bits(math.Pi) >> 48)}
	n, ok := ALPRDWith(e, v, nil, 48, d)
	if !ok || len(n.Kids) != 4 {
		t.Fatalf("ok=%v, %d children, want the parts and the patches", ok, len(n.Kids))
	}
	checkDecode(t, n, array.F64, unsafex.Bytes(v), nil)
	// every left part is in the dictionary: no patch
	e.Reset()
	n, _ = ALPRD(e, []float64{1.5, 1.25, 1.75}, nil)
	if len(n.Kids) != 2 {
		t.Fatalf("%d children, want no patch", len(n.Kids))
	}
	// the search picks a cut of 16 bits or less at the front
	w := make([]float64, 5000)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range w {
		w[i] = 1 + rng.Float64()
	}
	e.Reset()
	n, _ = ALPRD(e, w, nil)
	checkDecode(t, n, array.F64, unsafex.Bytes(w), nil)
	if n.Size() >= 8*len(w) {
		t.Fatalf("%d bytes for %d doubles", n.Size(), len(w))
	}
}

func TestSelectRDDict(t *testing.T) {
	groups := []rdGroup{{1, 5}, {2, 9}, {3, 9}, {4, 1}, {5, 20}, {6, 3}, {7, 3}, {8, 7}, {9, 2}, {10, 30}}
	d := selectRDDict(48, groups)
	// the 8 most frequent, a tie going after the one held
	want := []uint16{10, 5, 2, 3, 8, 1, 6, 7}
	if d.len != 8 || d.leftBits != 3 {
		t.Fatalf("dictionary %v", d)
	}
	for i, p := range want {
		if d.patterns[i] != p {
			t.Fatalf("dictionary %v, want %v", d.patterns, want)
		}
	}
	if d.encodable != 30+20+9+9+7+5+3+3 {
		t.Fatalf("encodable %d", d.encodable)
	}
	if got := selectRDDict(48, nil); got.len != 0 || got.leftBits != 1 {
		t.Fatalf("empty dictionary %v", got)
	}
}

func TestEstimateEmpty(t *testing.T) {
	e := New()
	if EstimateALPRD(e, []float64{}, nil, 0) != 0 {
		t.Fatal("alprd")
	}
	if EstimateALP(e, []float32{}, nil, 0) != 0 {
		t.Fatal("alp")
	}
}
