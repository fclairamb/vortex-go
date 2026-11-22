package compress

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/encode"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// spreadCase counts the values of v with a map, and checks what spread says of them: the
// dictionary and the sparse array are tried as the reference tries them.
func spreadCase[T encode.Int](t *testing.T, name string, v []T, nulls []bool) {
	t.Helper()
	counts := map[T]int{}
	top := 0
	for i, x := range v {
		if !isNull(nulls, i) {
			counts[x]++
			top = max(top, counts[x])
		}
	}
	st := statsOfInts(v, nulls)
	if st.valid == 0 {
		return
	}
	spread(v, nulls, &st)
	if !st.counted {
		t.Fatalf("%s: not counted", name)
	}
	if want := len(counts) <= st.valid/2; st.dict() != want {
		t.Errorf("%s: %d distinct of %d, dict %v", name, len(counts), st.valid, st.dict())
	}
	want := st.nullDominated(len(v)) || 10*top >= 9*st.valid && top < st.valid
	if st.sparse(len(v)) != want {
		t.Errorf("%s: the most frequent is %d of %d, sparse %v", name, top, st.valid, st.sparse(len(v)))
	}
	if st.dict() && st.distinct != len(counts) {
		t.Errorf("%s: %d distinct, counted %d", name, len(counts), st.distinct)
	}
}

// TestSpread: the distinct values and the most frequent one, on values that fill the first
// table and make it grow, that stop the count early, and on the 256 values of a byte.
func TestSpread(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	for _, n := range []int{1, 2, 3, 9, 10, 100, 5000, 20_000} {
		for _, distinct := range []int{1, 2, 10, 300, 3000, 1 << 20} {
			for kind := range 3 {
				nulls := nullsOf(r, n, kind)
				v := make([]int64, n)
				w := make([]uint8, n)
				for i := range v {
					v[i] = int64(r.IntN(distinct)) - int64(distinct/2)
					w[i] = uint8(r.IntN(min(distinct, 256)))
				}
				// nine tenths and more of one value
				s := make([]uint16, n)
				for i := range s {
					if r.IntN(20) == 0 {
						s[i] = uint16(r.IntN(distinct))
					}
				}
				spreadCase(t, "i64", v, nulls)
				spreadCase(t, "u8", w, nulls)
				spreadCase(t, "u16", s, nulls)
			}
		}
	}
	all := make([]uint8, 512)
	for i := range all {
		all[i] = uint8(i)
	}
	spreadCase(t, "every byte twice", all, nil)
}

// TestTrialGates: in a trial, an array of integers with too many distinct values is not
// tried as a dictionary, nor as a sparse array when its most frequent value is under nine
// tenths of its rows, and the floats are not tried as a dictionary either, as the reference
// does. At the top of a chunk, both are tried on the sample.
func TestTrialGates(t *testing.T) {
	c := New(Default)
	r := rand.New(rand.NewPCG(5, 6))
	distinct := make([]int32, 1024)
	for i := range distinct {
		distinct[i] = int32(r.Uint32())
	}
	has := func(x at, s scheme) bool {
		st := statsOfInts(distinct, nil)
		if x.trial {
			spread(distinct, nil, &st)
		}
		l := intCands(c, st, x, I32, len(distinct), true)
		return slices.Contains(l.s[:l.n], s)
	}
	if !has(c.top(), sDict) || !has(c.top(), sSparse) {
		t.Error("at the top, the dictionary and the sparse array are tried on the sample")
	}
	trial := at{e: c.trial, trial: true}
	if has(trial, sDict) || has(trial, sSparse) {
		t.Error("in a trial, distinct values are neither a dictionary nor a sparse array")
	}
	floats := make([]float64, 1024)
	for i := range floats {
		floats[i] = r.Float64()
	}
	if floatsDict(floats, nil, len(floats)) || floatsDict(slices.Repeat([]float32{1.5, 2}, 512), nil, 1024) != true {
		t.Error("the floats are a dictionary when at most half of them are distinct")
	}
}

// TestExclusions: the schemes a chain of parents leaves out, as the rules of the reference do.
func TestExclusions(t *testing.T) {
	c := New(Default)
	st := intStats{runs: 10, valid: 8192, packW: 20, forW: 10, negative: true}
	for _, tc := range []struct {
		name string
		excl uint32
		out  []scheme
		in   []scheme
	}{
		{"zigzag", exclZigZag, []scheme{sZigZag, sDict, sRunEnd, sSparse}, []scheme{sFoR, sRLE}},
		{"ends of a run-end", exclEnds, []scheme{sRunEnd, sDict, sRLE, sSparse}, []scheme{sFoR}},
		{"codes", exclCodes, []scheme{sDict, sFoR, sZigZag}, []scheme{sRunEnd, sRLE, sSparse}},
		{"values", exclValues, []scheme{sDict, sRunEnd, sRLE}, []scheme{sFoR, sSparse}},
	} {
		x := at{e: c.e, depth: 1, parent: sALP, excl: tc.excl}
		l := intCands(c, st, x, I64, 8192, true)
		for _, s := range tc.out {
			if slices.Contains(l.s[:l.n], s) {
				t.Errorf("%s: scheme %d is a candidate", tc.name, s)
			}
		}
		for _, s := range tc.in {
			if !slices.Contains(l.s[:l.n], s) {
				t.Errorf("%s: scheme %d is not a candidate", tc.name, s)
			}
		}
	}
	// a sequence is not tried under the codes of a dictionary
	seq := make([]int64, 4096)
	for i := range seq {
		seq[i] = int64(i)
	}
	if n := ints(c, at{e: c.e, depth: 1, parent: sDict, excl: exclCodes}, I64, seq, nil, encode.ZigZag[int64]); n != nil && n.ID == encode.IDSequence {
		t.Error("the codes of a dictionary are a sequence")
	}
}

// TestChosen: the chunk that follows the probe of its rows takes the scheme the probe chose,
// and makes the tree a fresh compressor makes. Other rows of the same length do not take it.
func TestChosen(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	const rows = 5000
	fresh := func(p PType, raw []byte, nulls []bool) string { return tree(New(Default).Numbers(p, raw, nulls)) }
	for _, p := range []PType{U16, I32, F64} {
		cols := columns(r, p, rows)
		for _, name := range sortedKeys(cols) {
			nulls := nullsOf(r, rows, 1)
			c := New(Default)
			if c.PicksDict(p, cols[name], nulls) {
				continue
			}
			if c.chosen.n != rows {
				t.Fatalf("%d %s: the probe keeps no choice", p, name)
			}
			c.Reset()
			if got, want := tree(c.Numbers(p, cols[name], nulls)), fresh(p, cols[name], nulls); got != want {
				t.Errorf("%d %s: %s after the probe, %s", p, name, got, want)
			}
			if c.chosen.n != 0 {
				t.Errorf("%d %s: the choice serves twice", p, name)
			}
			other := slices.Clone(cols[name])
			for i := range other {
				other[i] = byte(r.Uint32())
			}
			c.PicksDict(p, cols[name], nulls)
			c.Reset()
			if got, want := tree(c.Numbers(p, other, nulls)), fresh(p, other, nulls); got != want {
				t.Errorf("%d %s: other rows are %s, %s", p, name, got, want)
			}
		}
	}
	// strings, and their nulls
	v := make([][]byte, rows)
	for i := range v {
		v[i] = []byte{'a' + byte(r.IntN(26)), 'b', byte(i), byte(i >> 8)}
	}
	nulls := nullsOf(r, rows, 1)
	c := New(Default)
	if c.PicksDictStrings(v, nulls, true) {
		t.Fatal("the strings are a dictionary")
	}
	c.Reset()
	if got, want := tree(c.Strings(v, nulls, true)), tree(New(Default).Strings(v, nulls, true)); got != want {
		t.Errorf("strings: %s after the probe, %s", got, want)
	}
	if digestBins(v, nulls) == digestBins(v, nil) || digestNumbers(unsafex.Slice[uint16](make([]byte, 64)), nil) == digestNumbers(unsafex.Slice[uint16](make([]byte, 66)), nil) {
		t.Error("the digests do not tell the nulls or the rows apart")
	}
}
