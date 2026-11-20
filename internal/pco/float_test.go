package pco

import (
	"math"
	"math/rand/v2"
	"sort"
	"testing"
)

func isF16NaN(h uint16) bool { return h&0x7c00 == 0x7c00 && h&0x3ff != 0 }

func TestF16Widening(t *testing.T) {
	for i := range 1 << 16 {
		h := uint16(i)
		f := f16ToF32(h)
		if isF16NaN(h) {
			back := f32ToF16(f)
			if !math.IsNaN(float64(f)) || !isF16NaN(back) || back&0x8000 != h&0x8000 {
				t.Fatalf("NaN %#04x: %v, back %#04x", h, f, back)
			}
			continue
		}
		if back := f32ToF16(f); back != h {
			t.Fatalf("%#04x widens to %v, which narrows to %#04x", h, f, back)
		}
		// The value is the one the bits stand for.
		want := math.Ldexp(float64(h&0x3ff|0x400), int(h>>10&0x1f)-25)
		if h>>10&0x1f == 0 {
			want = math.Ldexp(float64(h&0x3ff), -24)
		}
		if h&0x7c00 == 0x7c00 {
			want = math.Inf(1)
		}
		if h&0x8000 != 0 {
			want = -want
		}
		if float64(f) != want {
			t.Fatalf("%#04x is %v, want %v", h, f, want)
		}
	}
}

// refHalf rounds x to a half float the slow way: the nearest of the two neighbours in the table
// of all non-negative halves, the one with the even bits when it is a tie.
func refHalf(vals []float64, x float64) uint16 {
	neg := math.Signbit(x)
	x = math.Abs(x)
	i := sort.SearchFloat64s(vals, x) // first value >= x
	var h uint16
	switch {
	case i == len(vals):
		h = 0x7c00
	case vals[i] == x:
		h = uint16(i)
	default:
		lo, hi := i-1, i
		dl, dh := x-vals[lo], vals[hi]-x
		switch {
		case dl < dh:
			h = uint16(lo)
		case dh < dl:
			h = uint16(hi)
		case lo%2 == 0:
			h = uint16(lo)
		default:
			h = uint16(hi)
		}
	}
	if neg {
		h |= 0x8000
	}
	return h
}

func TestF16Narrowing(t *testing.T) {
	// The values of the halves 0x0000 to 0x7bff, then 2^16: the next one would be infinity.
	vals := make([]float64, 0x7c01)
	for h := range 0x7c00 {
		vals[h] = float64(f16ToF32(uint16(h)))
	}
	vals[0x7c00] = 65536
	check := func(f float32) {
		t.Helper()
		got := f32ToF16(f)
		switch {
		case f != f:
			if !isF16NaN(got) {
				t.Fatalf("NaN narrows to %#04x", got)
			}
		case math.IsInf(float64(f), 0):
			if want := uint16(0x7c00) | uint16(math.Float32bits(f)>>16)&0x8000; got != want {
				t.Fatalf("%v narrows to %#04x", f, got)
			}
		default:
			if want := refHalf(vals, float64(f)); got != want {
				t.Fatalf("%v (%#08x) narrows to %#04x, want %#04x", f, math.Float32bits(f), got, want)
			}
		}
	}
	// Ties and their neighbours, around every power of two and on the subnormals.
	for e := -26; e <= 17; e++ {
		for _, m := range []float64{1, 1.0009765625, 1.00048828125, 1.000732421875, 1.0004882, 1.0004883, 1.999, 1.9995117, 1.99951171875} {
			x := float32(math.Ldexp(m, e))
			check(x)
			check(-x)
			check(math.Nextafter32(x, 0))
			check(math.Nextafter32(x, math.MaxFloat32))
		}
	}
	for _, f := range []float32{0, float32(math.Copysign(0, -1)), 65504, 65519.99, 65520, 65536, 1e10, float32(math.Inf(1)), float32(math.Inf(-1)), float32(math.NaN()), math.SmallestNonzeroFloat32} {
		check(f)
	}
	g := rand.New(rand.NewPCG(1, 2))
	for range 300000 {
		check(math.Float32frombits(g.Uint32()))
	}
	for range 300000 {
		check(float32(g.NormFloat64() * 1000))
	}
}

func TestIntFloatFromLatent(t *testing.T) {
	for _, c := range []struct {
		typ  Type
		bits func(float64) uint64
	}{
		{F16, func(v float64) uint64 { return uint64(f32ToF16(float32(v))) }},
		{F32, func(v float64) uint64 { return uint64(math.Float32bits(float32(v))) }},
		{F64, math.Float64bits},
	} {
		mid := c.typ.mid()
		gpi := uint64(1) << (mantissaBits(c.typ) + 1)
		gpiBits := c.bits(float64(gpi))
		for _, k := range []uint64{0, 1, 2, 7, 100, gpi - 1, gpi, gpi + 1, gpi + 5, mid - 1} {
			want := gpiBits + (k - gpi)
			if k <= gpi {
				want = c.bits(float64(k))
			}
			if got := intFloatFromLatent(c.typ, mid+k); got != want {
				t.Errorf("%s: latent middle + %d is %#x, want %#x", c.typ, k, got, want)
			}
			if got := intFloatFromLatent(c.typ, mid-1-k); got != want|mid {
				t.Errorf("%s: latent middle - 1 - %d is %#x, want %#x", c.typ, k, got, want|mid)
			}
		}
	}
}
