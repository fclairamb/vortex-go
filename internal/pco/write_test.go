package pco

import (
	"math"
	"math/rand/v2"
	"testing"
)

var allTypes = []Type{U8, U16, U32, U64, I8, I16, I32, I64, F16, F32, F64}

// roundTrip codes vals, decodes them back with the reader and compares them bit for bit. It
// returns the coded chunks, and the chunk metadata of the first one for what it says.
func roundTrip(t *testing.T, c *Coder, typ Type, vals []uint64, pageN int) (chunks []Coded, first *Chunk) {
	t.Helper()
	coded := c.Compress(typ, vals, pageN)
	hdr, _, err := ReadHeader(WriteHeader(nil))
	if err != nil {
		t.Fatal(err)
	}
	w := typ.Bits() / 8
	pos, nChunks := 0, (len(vals)+ChunkN-1)/ChunkN
	if len(coded) != nChunks {
		t.Fatalf("%d chunks, want %d", len(coded), nChunks)
	}
	for i, ch := range coded {
		chunk, err := hdr.Chunk(typ, ch.Meta)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		if first == nil {
			first = chunk
		}
		if len(ch.Pages) != len(ch.N) {
			t.Fatalf("%d pages, %d counts", len(ch.Pages), len(ch.N))
		}
		for p, page := range ch.Pages {
			if ch.N[p] > pageN {
				t.Fatalf("a page of %d numbers, at most %d", ch.N[p], pageN)
			}
			dst := make([]byte, ch.N[p]*w)
			if err := chunk.Page(page, ch.N[p], dst); err != nil {
				t.Fatalf("chunk %d page %d: %v", i, p, err)
			}
			for k := 0; k < ch.N[p]; k++ {
				var got uint64
				for b := 0; b < w; b++ {
					got |= uint64(dst[k*w+b]) << (8 * b)
				}
				if got != vals[pos+k] {
					t.Fatalf("chunk %d page %d number %d = %#x, want %#x", i, p, k, got, vals[pos+k])
				}
			}
			pos += ch.N[p]
		}
	}
	if pos != len(vals) {
		t.Fatalf("%d numbers back, want %d", pos, len(vals))
	}
	return coded, first
}

func mask(t Type) uint64 { return t.mask() }

// gen makes n numbers of type typ, as bits.
func gen(kind string, typ Type, n int, rng *rand.Rand) []uint64 {
	m := mask(typ)
	out := make([]uint64, n)
	for i := range out {
		switch kind {
		case "random":
			out[i] = rng.Uint64() & m
		case "sorted":
			out[i] = uint64(i*3+rng.IntN(3)) & m
		case "step":
			out[i] = uint64(1_000_000+i*60) & m
		case "quad":
			out[i] = uint64(i*i/3) & m
		case "same":
			out[i] = 7
		case "small":
			out[i] = uint64(rng.IntN(5)) & m
		case "extremes":
			out[i] = [...]uint64{0, m, m >> 1, m>>1 + 1}[rng.IntN(4)]
		case "decimals":
			if typ.Float() {
				out[i] = fromFloat64(typ, float64(rng.IntN(20000))/100)
			} else {
				out[i] = uint64(rng.IntN(20000)) & m
			}
		case "special":
			if typ.Float() {
				neg := uint64(rng.IntN(2)) << (typ.Bits() - 1)
				special := [...]uint64{0, fromFloat64(typ, math.Inf(1)), fromFloat64(typ, math.NaN()) | 1, fromFloat64(typ, 1.5)}
				out[i] = special[rng.IntN(4)] | neg
			} else {
				out[i] = rng.Uint64() & m
			}
		}
	}
	return out
}

func TestWriteRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	c := new(Coder)
	kinds := []string{"random", "sorted", "step", "quad", "same", "small", "extremes", "decimals", "special"}
	for _, typ := range allTypes {
		for _, kind := range kinds {
			for _, n := range []int{1, 2, 3, 5, 100, 4097, 8192, 8193, 20000} {
				t.Run(typ.String()+"/"+kind, func(t *testing.T) {
					roundTrip(t, c, typ, gen(kind, typ, n, rng), 8192)
				})
			}
		}
	}
}

func TestWriteManyChunks(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	c := new(Coder)
	for _, typ := range []Type{I64, F32, U16} {
		coded, _ := roundTrip(t, c, typ, gen("sorted", typ, 300_000, rng), 8192)
		if len(coded) != 2 || len(coded[0].Pages) != 32 {
			t.Fatalf("%s: %d chunks, %d pages in the first", typ, len(coded), len(coded[0].Pages))
		}
	}
}

// TestWriteModes checks that each mode and each delta order is made by some input.
func TestWriteModes(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	c := new(Coder)
	for _, tc := range []struct {
		kind  string
		typ   Type
		mode  int
		delta int
		order int
	}{
		{"random", I64, modeClassic, deltaNone, 0},
		{"step", I64, modeClassic, deltaConsecutive, 1},
		{"quad", I32, modeClassic, deltaConsecutive, 2},
		{"decimals", F64, modeFloatMult, deltaNone, 0},
		{"decimals", F32, modeFloatMult, deltaNone, 0},
		{"random", F64, modeClassic, deltaNone, 0},
	} {
		_, ch := roundTrip(t, c, tc.typ, gen(tc.kind, tc.typ, 20000, rng), 8192)
		if ch.mode != tc.mode || ch.delta.kind != tc.delta || ch.delta.order != tc.order {
			t.Errorf("%s %s: mode %d delta %d order %d, want %d %d %d", tc.typ, tc.kind, ch.mode, ch.delta.kind, ch.delta.order, tc.mode, tc.delta, tc.order)
		}
	}
	// a sorted column of floats and prices that climb: a multiplier and a delta together
	prices := make([]uint64, 20000)
	for i := range prices {
		prices[i] = math.Float64bits(float64(100000+i*5) / 100)
	}
	_, ch := roundTrip(t, c, F64, prices, 8192)
	if ch.mode != modeFloatMult || ch.delta.kind != deltaConsecutive {
		t.Errorf("climbing prices: mode %d delta %d", ch.mode, ch.delta.kind)
	}
}

// TestWriteNoAllocation: a Coder that has coded something codes the same again with no
// allocation, whatever the mode.
func TestWriteNoAllocation(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	for _, tc := range []struct {
		typ  Type
		kind string
	}{{I64, "step"}, {U32, "random"}, {F64, "decimals"}, {F32, "special"}} {
		c := new(Coder)
		vals := gen(tc.kind, tc.typ, 20000, rng)
		call := func() { c.Compress(tc.typ, vals, 8192) }
		call()
		if got := testing.AllocsPerRun(5, call); got != 0 {
			t.Errorf("%s %s: %v allocations", tc.typ, tc.kind, got)
		}
	}
}

// TestWriteSize: the bits of a column that Pco is made for are far fewer than its plain bytes.
func TestWriteSize(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	c := new(Coder)
	for kind, limit := range map[string]int{"step": 100, "sorted": 6000, "same": 100, "decimals": 38000} {
		vals := gen(kind, F64, 20000, rng)
		if kind != "decimals" {
			vals = gen(kind, I64, 20000, rng)
		}
		size := 0
		for _, ch := range c.Compress(map[bool]Type{true: F64, false: I64}[kind == "decimals"], vals, 8192) {
			size += len(ch.Meta)
			for _, p := range ch.Pages {
				size += len(p)
			}
		}
		if size > limit {
			t.Errorf("%s: %d bytes for 20000 numbers, want at most %d", kind, size, limit)
		}
	}
}

// FuzzWriteRoundTrip codes numbers of any type, in pages of any size, and reads them back.
func FuzzWriteRoundTrip(f *testing.F) {
	f.Add(uint8(3), []byte{1, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0})
	f.Add(uint8(0x3a), []byte{0, 0, 0, 0, 0, 0, 0xf8, 0x7f, 0, 0, 0, 0, 0, 0, 0, 0x80})
	f.Fuzz(func(t *testing.T, sel uint8, data []byte) {
		typ := allTypes[int(sel&15)%len(allTypes)]
		w := typ.Bits() / 8
		vals := make([]uint64, len(data)/w)
		for i := range vals {
			for b := 0; b < w; b++ {
				vals[i] |= uint64(data[i*w+b]) << (8 * b)
			}
		}
		if len(vals) == 0 {
			return
		}
		roundTrip(t, new(Coder), typ, vals, []int{1, 2, 7, 8192}[sel>>6])
	})
}
