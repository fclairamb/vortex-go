package compress

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/encode"
)

// onPairColumns are a column that OnPair makes smaller than FSST (urls, whose values go to a
// dictionary and the dictionary to OnPair) and one that it does not (log lines).
func onPairColumns() (urls, logs [][]byte) {
	r := rand.New(rand.NewPCG(1, 2))
	hosts := []string{"www.acme.com", "shop.acme.org", "api.example.net"}
	for i := range 20000 {
		urls = append(urls, fmt.Appendf(nil, "https://%s/p/%d?q=%d&ref=%s", hosts[r.IntN(3)], r.IntN(5000), i%7, strings.Repeat("x", r.IntN(10))))
		logs = append(logs, fmt.Appendf(nil, "2026-11-%02d 12:%02d:%02d INFO request id=%d user=u%d took %dms", 1+r.IntN(28), r.IntN(60), r.IntN(60), r.IntN(100000), r.IntN(300), r.IntN(900)))
	}
	return urls, logs
}

// TestOnPairPicked: the compressor keeps the smaller of FSST and OnPair, and each is met.
func TestOnPairPicked(t *testing.T) {
	urls, logs := onPairColumns()
	if got := ids(New(Default).Strings(urls, nil, true)); !slices.Contains(got, encode.IDOnPair) {
		t.Fatalf("urls: %v, want OnPair", got)
	}
	if got := ids(New(Default).Strings(logs, nil, true)); !slices.Contains(got, encode.IDFSST) || slices.Contains(got, encode.IDOnPair) {
		t.Fatalf("logs: %v, want FSST", got)
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
