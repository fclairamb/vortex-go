package compress

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/fclairamb/vortex-go/internal/encode"
)

func isDict(n *encode.Node) bool { return n != nil && n.ID == encode.IDDict }

// TestProbe checks that the probe of the dictionary layout, which stops as soon as it knows,
// says what the compression of the whole chunk says.
func TestProbe(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	const rows = 3000
	words := []string{"alpha", "beta", "gamma", "a-much-longer-value-than-twelve-bytes", ""}
	strs := map[string]func(i int) []byte{
		"few":  func(i int) []byte { return []byte(words[i*7%len(words)]) },
		"long": func(i int) []byte { return bytes.Repeat([]byte{'a' + byte(i%3)}, 40) },
		"text": func(i int) []byte { return fmt.Appendf(nil, "row %d of %d", i, r.IntN(1000)) },
		// fewer distinct values than half of the rows, which share one head
		"heads": func(i int) []byte { return fmt.Appendf(nil, "same-head-%d", i%(rows/3)) },
	}
	for _, mode := range []Mode{Default, Compact} {
		c := New(mode)
		for _, p := range []PType{U8, U16, I32, I64, F64} {
			for _, n := range []int{50, rows} {
				cols := columns(r, p, n)
				for _, name := range sortedKeys(cols) {
					for kind := range 3 {
						nulls := nullsOf(r, n, kind)
						c.Reset()
						want := isDict(c.Numbers(p, cols[name], nulls))
						c.Reset()
						if got := c.PicksDict(p, cols[name], nulls); got != want {
							t.Errorf("%d %d %s %d %d: the probe says %v, the compression %v", mode, p, name, n, kind, got, want)
						}
					}
				}
			}
		}
		for _, name := range []string{"few", "heads", "long", "text"} {
			for kind := range 3 {
				v := make([][]byte, rows)
				for i := range v {
					v[i] = strs[name](i)
				}
				nulls := nullsOf(r, rows, kind)
				c.Reset()
				want := isDict(c.Strings(v, nulls, true))
				c.Reset()
				if got := c.PicksDictStrings(v, nulls, true); got != want {
					t.Errorf("%d %s %d: the probe says %v, the compression %v", mode, name, kind, got, want)
				}
			}
		}
	}
}

// TestDictHeads checks that a dictionary is not tried on strings with more distinct heads (the
// length and the first 4 bytes) than half of the valid rows, as the reference does.
func TestDictHeads(t *testing.T) {
	var h headSet
	v := [][]byte{[]byte("abcd1"), []byte("abcd2"), []byte("ab"), []byte(""), nil, []byte("abcd1")}
	some := []bool{false, false, true, true, false, false}
	for _, c := range []struct {
		nulls []bool
		limit int
		over  bool
	}{
		{nil, 3, false}, // abcd* of 5 bytes, ab, and the empty ones
		{nil, 2, true},
		{some, 2, false}, // abcd* of 5 bytes, and the nulls and the empty one
		{some, 1, true},
	} {
		if got := h.over(v, c.nulls, c.limit); got != c.over {
			t.Errorf("%v %d: over is %v", c.nulls, c.limit, got)
		}
	}
	// 2000 distinct values with as many heads, then 400 of them in 2000 rows
	c := New(Default)
	many := make([][]byte, 2000)
	for i := range many {
		many[i] = fmt.Appendf(nil, "%04d-the-same-suffix-of-a-long-value", i)
	}
	if n := c.Strings(many, nil, true); n == nil || isDict(n) {
		t.Errorf("distinct strings are %s", tree(n))
	}
	c.Reset()
	few := slices.Concat(many[:400], many[:400], many[:400], many[:400], many[:400])
	if n := c.Strings(few, nil, true); !isDict(n) {
		t.Errorf("400 values in 2000 rows are %s", tree(n))
	}
}
