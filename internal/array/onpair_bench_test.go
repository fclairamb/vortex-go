package array

import (
	"math/rand/v2"
	"testing"
)

// benchOnPairDict is a dictionary of n tokens of 1 to 16 bytes, padded as the reference pads
// it, and codes of it.
func benchOnPairDict(n, codes int) (*onPairDict, []uint16, int) {
	rng := rand.New(rand.NewPCG(1, 2))
	d := &onPairDict{offs: []int{0}, fast: true, maxTok: 16}
	for range n {
		l := 1 + rng.IntN(16)
		if rng.IntN(4) > 0 {
			l = 1 + rng.IntN(4) // most tokens are short
		}
		for range l {
			d.bytes = append(d.bytes, byte('a'+rng.IntN(26)))
		}
		d.offs = append(d.offs, len(d.bytes))
	}
	d.bytes = append(d.bytes, make([]byte, onPairMaxToken)...)
	d.tab = make([]uint32, n)
	for i := range d.tab {
		d.tab[i] = uint32(d.offs[i])<<onPairLenBits | uint32(d.offs[i+1]-d.offs[i])
	}
	out := make([]uint16, codes)
	total := 0
	for i := range out {
		out[i] = uint16(rng.IntN(n))
		total += d.offs[int(out[i])+1] - d.offs[out[i]]
	}
	return d, out, total
}

// BenchmarkOnPairBulk is the kernel of the OnPair decode, per token, for a dictionary that
// fits the first cache and for a full one.
func BenchmarkOnPairBulk(b *testing.B) {
	for _, c := range []struct {
		name string
		toks int
	}{{"dict256", 256}, {"dict4096", 4096}, {"dict65536", 65536}} {
		b.Run(c.name, func(b *testing.B) {
			d, codes, total := benchOnPairDict(c.toks, 1<<16)
			dst := make([]byte, total+onPairMaxToken)
			b.SetBytes(int64(total))
			for b.Loop() {
				if got := onPairBulk(d, dst, codes); got != total {
					b.Fatal(got, total)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(len(codes)), "ns/token")
		})
	}
}
