package encode

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// est is the estimate of the size of an encoder, from a sample of rows rows.
type est func(e *Encoder, sample []byte, nulls []bool, rows int, width int) int

func intEsts[T Int]() map[string]est {
	return map[string]est{
		"bitpacked": func(_ *Encoder, s []byte, nulls []bool, rows, width int) int {
			return EstimateBitPack(unsafex.Slice[T](s), nulls, rows, width)
		},
		"for":      func(_ *Encoder, _ []byte, nulls []bool, rows, _ int) int { return EstimateFoR[T](nulls, rows) },
		"sequence": func(*Encoder, []byte, []bool, int, int) int { return EstimateSequence() },
	}
}

func numberEsts[T Number]() map[string]est {
	m := map[string]est{
		"rle": func(_ *Encoder, s []byte, nulls []bool, rows, _ int) int {
			return EstimateRLE(unsafex.Slice[T](s), nulls, rows)
		},
		"runend": func(_ *Encoder, s []byte, nulls []bool, rows, _ int) int {
			return EstimateRunEnd(unsafex.Slice[T](s), nulls, rows)
		},
		"dict": func(e *Encoder, s []byte, nulls []bool, rows, _ int) int {
			return EstimateDict(e, unsafex.Slice[T](s), nulls, rows)
		},
		"sparse": func(e *Encoder, s []byte, nulls []bool, rows, _ int) int {
			return EstimateSparse(e, unsafex.Slice[T](s), nulls, rows)
		},
		"constant": func(_ *Encoder, s []byte, nulls []bool, _, _ int) int {
			return EstimateConstant(unsafex.Slice[T](s), nulls)
		},
		"zstd": func(e *Encoder, s []byte, nulls []bool, rows, _ int) int {
			return EstimateZstd(e, unsafex.Slice[T](s), nulls, rows)
		},
	}
	m["pco"] = func(e *Encoder, s []byte, nulls []bool, rows, _ int) int {
		return EstimatePco(e, unsafex.Slice[T](s), nulls, rows)
	}
	return m
}

func floatEsts[T Float]() map[string]est {
	return map[string]est{
		"alp": func(e *Encoder, s []byte, nulls []bool, rows, _ int) int {
			return EstimateALP(e, unsafex.Slice[T](s), nulls, rows)
		},
		"alprd": func(e *Encoder, s []byte, nulls []bool, rows, _ int) int {
			return EstimateALPRD(e, unsafex.Slice[T](s), nulls, rows)
		},
	}
}

func signedEsts[T Signed]() map[string]est {
	return map[string]est{
		"zigzag": func(_ *Encoder, _ []byte, nulls []bool, rows, _ int) int { return EstimateZigZag[T](nulls, rows) },
	}
}

var ests = map[string]map[string]est{
	"i8":  merge2(intEsts[int8](), numberEsts[int8](), signedEsts[int8]()),
	"i16": merge2(intEsts[int16](), numberEsts[int16](), signedEsts[int16]()),
	"i32": merge2(intEsts[int32](), numberEsts[int32](), signedEsts[int32]()),
	"i64": merge2(intEsts[int64](), numberEsts[int64](), signedEsts[int64]()),
	"u8":  merge2(intEsts[uint8](), numberEsts[uint8]()),
	"u16": merge2(intEsts[uint16](), numberEsts[uint16]()),
	"u32": merge2(intEsts[uint32](), numberEsts[uint32]()),
	"u64": merge2(intEsts[uint64](), numberEsts[uint64]()),
	"f32": merge2(numberEsts[float32](), floatEsts[float32]()),
	"f64": merge2(numberEsts[float64](), floatEsts[float64]()),
}

func merge2(ms ...map[string]est) map[string]est {
	out := map[string]est{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// within says whether got is at most 5% away from want.
func within(got, want int) bool { return 20*abs(got-want) <= want }

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// widthFor is the width an encoder is given: the one of the parameters.
func widthFor(name string, p string, par vparams) int {
	if name != "bitpacked" {
		return 0
	}
	if par.Best {
		return -1
	}
	return par.BitWidth
}

// TestEstimate checks that the estimate of every encoder is within 5% of the size it makes, on
// the whole data and on a quarter of it.
func TestEstimate(t *testing.T) {
	estimateEncoders(t, rand.New(rand.NewPCG(8, 9)), func(name string) bool { return !floatOnly(name) && name != "zstd" && name != "pco" }, within)
}

// TestEstimateFloatsAndZstd is the same for the encoders of floats and zstd, which have a
// tolerance of 10%.
func TestEstimateFloatsAndZstd(t *testing.T) {
	estimateEncoders(t, rand.New(rand.NewPCG(10, 11)), func(name string) bool { return floatOnly(name) || name == "zstd" || name == "pco" },
		// 10%, or a frame header when the whole is a few bytes
		func(got, want int) bool { return 10*abs(got-want) <= want || abs(got-want) <= 32 })
}

func countTrue(b []bool) int {
	n := 0
	for _, x := range b {
		if x {
			n++
		}
	}
	return n
}

func floatOnly(name string) bool { return name == "alp" || name == "alprd" }

func estimateEncoders(t *testing.T, rng *rand.Rand, take func(name string) bool, close func(got, want int) bool) {
	e, c := New(), New()
	for _, p := range slices.Sorted(mapsKeys(ests)) {
		w := ptypes[p].Width()
		for _, name := range slices.Sorted(mapsKeys(ests[p])) {
			if !take(name) {
				continue
			}
			for _, kind := range kinds {
				for _, n := range []int{1, 100, 1025, 20480} {
					for _, nl := range nullNames {
						if n > 1025 && nl != "none" && nl != "most" || n == 1 && nl == "some" {
							continue
						}
						raw := gens[p](kind, n, rng)
						if name == "bitpacked" {
							raw = nonNegative(raw, ptypes[p])
						}
						nulls := nullPattern(nl, n)
						for _, par := range []vparams{{BitWidth: 3}, {BitWidth: 8*w - 1}} {
							t.Run(fmt.Sprintf("%s/%s/%s/%d/%s/%d", name, p, kind, n, nl, par.BitWidth), func(t *testing.T) {
								checkEstimate(t, e, c, p, name, kind, raw, nulls, par, close)
							})
							if name != "bitpacked" {
								break
							}
						}
					}
				}
			}
		}
	}
}

func checkEstimate(t *testing.T, e, c *Encoder, p, name, kind string, raw []byte, nulls []bool, par vparams, close func(got, want int) bool) {
	w := ptypes[p].Width()
	n := len(raw) / w
	c.Reset()
	node, ok := runs[p][name](c, raw, nulls, par)
	if !ok {
		return
	}
	size := node.Size()
	e.Reset()
	if got := ests[p][name](e, raw, nulls, n, par.BitWidth); got != size {
		t.Fatalf("estimate of the whole data = %d, size = %d", got, size)
	}
	// a quarter of the rows, in whole blocks, of data that is the same all along: the number of
	// distinct values of sorted data, or of random 16-bit values, grows with the rows in a way that a
	// sample can hardly tell
	if n < 8192 || kind == "sorted" || kind == "steps" || name == "dict" && kind == "random" || floatOnly(name) && kind == "random" {
		return
	}
	quarter := n / 4 / blockSize * blockSize
	var qn []bool
	if nulls != nil {
		qn = nulls[:quarter]
	}
	if (name == "zstd" || name == "pco") && quarter-countTrue(qn) < 1024 {
		return // a sample too small for a compressor to learn from
	}
	e.Reset()
	if got := ests[p][name](e, raw[:quarter*w], qn, n, par.BitWidth); !close(got, size) {
		t.Fatalf("estimate from a quarter = %d, size = %d", got, size)
	}
}

func TestEstimateStrings(t *testing.T) {
	e, c := New(), New()
	words := []string{"", "a", "abcdefghijkl", "abcdefghijklm", "a longer string that is not inlined", "zz"}
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{1, 100, 4000} {
		for _, nl := range []string{"none", "some"} {
			strs := make([]string, n)
			bins := make([][]byte, n)
			for i := range strs {
				strs[i] = words[rng.IntN(len(words))]
				if n == 4000 {
					strs[i] += fmt.Sprint(rng.IntN(30))
				}
				bins[i] = []byte(strs[i])
			}
			nulls := nullPattern(nl, n)
			c.Reset()
			size := DictStrings(c, strs, nulls).Size()
			if got := EstimateDictStrings(e, strs, nulls, n); got != size {
				t.Fatalf("n=%d %s: estimate = %d, size = %d", n, nl, got, size)
			}
			if got := EstimateDictBytes(e, bins, nulls, n); got != size {
				t.Fatalf("n=%d %s: bytes estimate = %d, size = %d", n, nl, got, size)
			}
			if n == 4000 {
				q := n / 4
				if got := EstimateDictStrings(e, strs[:q], nulls[:min(q, len(nulls))], n); !within(got, size) {
					t.Fatalf("n=%d %s: estimate from a quarter = %d, size = %d", n, nl, got, size)
				}
			}
		}
	}
}
