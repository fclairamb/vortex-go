package compress

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/encode"
)

// onPairColumns are a column that OnPair makes smaller than FSST (urls) and one that it does not
// (keys of 32 hex digits), as the reference writer 0.94.0 picks them.
func onPairColumns() (urls, keys [][]byte) {
	r := rand.New(rand.NewPCG(1, 2))
	hosts := []string{"www.acme.com", "shop.acme.org", "api.example.net"}
	for i := range 20000 {
		urls = append(urls, fmt.Appendf(nil, "https://%s/p/%d?q=%d&ref=%s", hosts[r.IntN(3)], r.IntN(5000), i%7, strings.Repeat("x", r.IntN(10))))
		keys = append(keys, fmt.Appendf(nil, "%016x%016x", r.Uint64(), r.Uint64()))
	}
	return urls, keys
}

// TestOnPairPicked: the compressor keeps the smaller of FSST and OnPair, and each is met.
func TestOnPairPicked(t *testing.T) {
	urls, keys := onPairColumns()
	if got := ids(New(Default).Strings(urls, nil, true)); !slices.Contains(got, encode.IDOnPair) {
		t.Fatalf("urls: %v, want OnPair", got)
	}
	if got := ids(New(Default).Strings(keys, nil, true)); !slices.Contains(got, encode.IDFSST) || slices.Contains(got, encode.IDOnPair) {
		t.Fatalf("keys: %v, want FSST", got)
	}
	// nulls: the validity is the one of the column
	nulls := make([]bool, len(urls))
	for i := range nulls {
		nulls[i] = i%5 == 0
	}
	if got := ids(New(Default).Strings(urls, nulls, true)); !slices.Contains(got, encode.IDOnPair) {
		t.Fatalf("urls with nulls: %v, want OnPair", got)
	}
}

// TestOnPairNotForBinary: the reference has the scheme for strings only.
func TestOnPairNotForBinary(t *testing.T) {
	urls, _ := onPairColumns()
	for _, m := range []Mode{Default, Compact} {
		if got := ids(New(m).Strings(urls, nil, false)); slices.Contains(got, encode.IDOnPair) {
			t.Fatalf("mode %d: %v", m, got)
		}
	}
}

// TestOnPairLimit: an edition that does not have vortex.onpair gets none.
func TestOnPairLimit(t *testing.T) {
	urls, _ := onPairColumns()
	if got := ids(NewLimited(Default, Limits{NoOnPair: true}).Strings(urls, nil, true)); slices.Contains(got, encode.IDOnPair) {
		t.Fatalf("limited: %v", got)
	}
}

// BenchmarkOnPairCandidate is the cost of trying OnPair next to FSST on a column that ends as
// FSST (the keys): the same chunk with and without the candidate.
func BenchmarkOnPairCandidate(b *testing.B) {
	_, keys := onPairColumns()
	for _, tc := range []struct {
		name  string
		limit Limits
	}{{"with", Limits{}}, {"without", Limits{NoOnPair: true}}} {
		b.Run(tc.name, func(b *testing.B) {
			c := NewLimited(Default, tc.limit)
			c.Strings(keys, nil, true)
			b.ReportAllocs()
			for b.Loop() {
				c.Reset()
				c.Strings(keys, nil, true)
			}
		})
	}
}
