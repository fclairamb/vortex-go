package encode

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
)

// TestNoAllocation checks that a running Encoder allocates nothing: every encoder, into the
// buffers of an Encoder that has already done the same work.
func TestNoAllocation(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	e := New()
	for _, p := range slices.Sorted(mapsKeys(runs)) {
		for _, name := range slices.Sorted(mapsKeys(runs[p])) {
			for _, nl := range []string{"none", "some"} {
				raw := gens[p]("small", 3000, rng)
				if name == "bitpacked" {
					raw = nonNegative(raw, ptypes[p])
				}
				nulls := nullPattern(nl, 3000)
				run, par := runs[p][name], vparams{BitWidth: 5}
				t.Run(fmt.Sprintf("%s/%s/%s", name, p, nl), func(t *testing.T) {
					call := func() {
						e.Reset()
						run(e, raw, nulls, par)
					}
					call()
					if got := allocsPerRun(5, call); got != 0 {
						t.Fatalf("%v allocations", got)
					}
				})
			}
		}
	}
}

func TestNoAllocationStrings(t *testing.T) {
	e := New()
	strs := make([]string, 3000)
	bins := make([][]byte, 3000)
	nulls := nullPattern("some", 3000)
	for i := range strs {
		strs[i] = fmt.Sprint("a string that is long ", i%40)
		bins[i] = []byte(strs[i])
	}
	for name, f := range map[string]func(){
		"strings":  func() { e.Reset(); DictStrings(e, strs, nulls) },
		"bytes":    func() { e.Reset(); DictBytes(e, bins, nulls) },
		"estimate": func() { e.Reset(); EstimateDictStrings(e, strs, nulls, len(strs)) },
	} {
		f()
		if got := allocsPerRun(5, f); got != 0 {
			t.Errorf("%s: %v allocations", name, got)
		}
	}
}

// TestNodesSurviveGrowth checks that the nodes already made stay valid when the Encoder grows
// its memory for a later call.
func TestNodesSurviveGrowth(t *testing.T) {
	e := New()
	small := FoR(e, []int32{5, 6, 7}, nil)
	big := make([]int64, 100_000)
	FoR(e, big, nil)
	checkDecode(t, small, ptypes["i32"], []byte{5, 0, 0, 0, 6, 0, 0, 0, 7, 0, 0, 0}, nil)
}

// allocsPerRun is testing.AllocsPerRun, and 0 under the race detector: its sync.Pool drops
// what it is given at random, and the memory of the trainings and of the tables of a
// dictionary comes from pools.
func allocsPerRun(runs int, f func()) float64 {
	if raceEnabled {
		return 0
	}
	return testing.AllocsPerRun(runs, f)
}
