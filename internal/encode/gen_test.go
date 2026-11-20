package encode

import (
	"math/rand/v2"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// gens makes the data of a type: the values, as the bytes of a slice.
type gen func(kind string, n int, rng *rand.Rand) []byte

var kinds = []string{"random", "small", "sorted", "constant", "limits", "runs", "steps"}

// limits is the smallest and the largest value of T.
func limits[T Number]() (lo, hi T) {
	w := unsafex.Size[T]()
	loB, hiB := make([]byte, w), make([]byte, w)
	for i := range hiB {
		hiB[i] = 0xff
	}
	switch {
	case isFloat[T]():
		big := 1e30
		return T(-big), T(big)
	case signed[T]():
		loB[w-1], hiB[w-1] = 0x80, 0x7f
	}
	return unsafex.Slice[T](append(loB, 0, 0, 0, 0, 0, 0, 0, 0)[:w])[0], unsafex.Slice[T](hiB)[0]
}

func makeGen[T Number]() gen {
	return func(kind string, n int, rng *rand.Rand) []byte {
		lo, hi := limits[T]()
		v := make([]T, n)
		pick := func() T {
			if isFloat[T]() {
				return T(rng.NormFloat64() * 1e6)
			}
			return T(rng.Uint64())
		}
		for i := range v {
			switch kind {
			case "random":
				v[i] = pick()
			case "small":
				v[i] = T(rng.IntN(100))
				if rng.IntN(50) == 0 {
					v[i] = pick()
					if v[i] < 0 {
						v[i] = -v[i]
					}
					if v[i] < 0 { // the smallest integer
						v[i] = 0
					}
				}
			case "sorted":
				v[i] = T(i / 3)
			case "constant":
				v[i] = hi
			case "limits":
				v[i] = [4]T{lo, hi, 0, 1}[i%4]
			case "runs":
				v[i] = T(i / (1 + rng.IntN(5)) % 7)
			default: // an arithmetic sequence, going down when T can
				v[i] = T(i)
				if unsafex.Size[T]() > 1 {
					v[i] = T(2*i + 3)
					if signed[T]() {
						v[i] = T(1000 - 3*i)
					}
				}
			}
		}
		return append([]byte(nil), unsafex.Bytes(v)...)
	}
}

var gens = map[string]gen{
	"i8": makeGen[int8](), "i16": makeGen[int16](), "i32": makeGen[int32](), "i64": makeGen[int64](),
	"u8": makeGen[uint8](), "u16": makeGen[uint16](), "u32": makeGen[uint32](), "u64": makeGen[uint64](),
	"f32": makeGen[float32](), "f64": makeGen[float64](),
}

// patterns of nulls of n rows.
func nullPattern(name string, n int) []bool {
	switch name {
	case "some":
		out := make([]bool, n)
		for i := 0; i < n; i += 7 {
			out[i] = true
		}
		return out
	case "all":
		out := make([]bool, n)
		for i := range out {
			out[i] = true
		}
		return out
	case "most":
		out := make([]bool, n)
		for i := range out {
			out[i] = i%25 != 3
		}
		return out
	}
	return nil
}

var nullNames = []string{"none", "some", "all", "most"}

var sizes = []int{1, 2, 100, 1023, 1024, 1025, 2049}

// nonNegative makes the values of a signed type fit a bit-packing: the numbers of raw, as a
// type of the width, with the top bit cleared.
func nonNegative(raw []byte, p array.PType) []byte {
	out := append([]byte(nil), raw...)
	w := p.Width()
	if p.Signed() {
		for i := w - 1; i < len(out); i += w {
			out[i] &= 0x7f
		}
	}
	return out
}
