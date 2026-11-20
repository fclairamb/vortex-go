package encode

import (
	"math/rand/v2"
	"testing"
)

// BenchmarkEncode is one encoder per encoding, on 64Ki values of a shape it likes, into the
// buffers of an Encoder that has already done the same work. MB/s is of the input.
func BenchmarkEncode(b *testing.B) {
	const n = 1 << 16
	rng := rand.New(rand.NewPCG(1, 2))
	small := make([]int64, n)
	runs := make([]int64, n)
	seq := make([]int64, n)
	same := make([]int64, n)
	floats := make([]float64, n)
	for i := range small {
		small[i] = int64(rng.IntN(1000))
		runs[i] = int64(i / 50)
		seq[i] = int64(3 + 2*i)
		same[i] = 7
		floats[i] = float64(rng.IntN(100000)) / 100
	}
	strs := make([]string, n)
	for i := range strs {
		strs[i] = "customer#" + string(rune('a'+rng.IntN(26))) + string(rune('a'+rng.IntN(26)))
	}
	bins := make([][]byte, n)
	for i, s := range strs {
		bins[i] = []byte(s)
	}
	e := New()
	cases := []struct {
		name  string
		bytes int
		run   func()
	}{
		{"bitpacked", 8 * n, func() { _, _ = BitPackBest(e, small, nil) }},
		{"for", 8 * n, func() { FoR(e, small, nil) }},
		{"zigzag", 8 * n, func() { ZigZag(e, small, nil) }},
		{"rle", 8 * n, func() { RLE(e, runs, nil) }},
		{"runend", 8 * n, func() { RunEnd(e, runs, nil) }},
		{"dict", 8 * n, func() { Dict(e, small, nil) }},
		{"sequence", 8 * n, func() { _, _ = Sequence(e, seq, nil) }},
		{"constant", 8 * n, func() { _, _ = Constant(e, same, nil) }},
		{"sparse", 8 * n, func() { _, _ = Sparse(e, same, nil) }},
		{"alp", 8 * n, func() { ALP(e, floats, nil) }},
		{"alprd", 8 * n, func() { _, _ = ALPRD(e, floats, nil) }},
		{"fsst", 14 * n, func() { FSSTStrings(e, strs, nil) }},
		{"onpair", 14 * n, func() { OnPairBytes(e, bins, nil) }},
		{"zstd", 8 * n, func() { Zstd(e, small, nil) }},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			e.Reset()
			c.run()
			b.SetBytes(int64(c.bytes))
			b.ReportAllocs()
			for b.Loop() {
				e.Reset()
				c.run()
			}
			b.ReportMetric(float64(n)*float64(b.N)/b.Elapsed().Seconds(), "values/s")
		})
	}
}
