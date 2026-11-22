package vortex

import (
	"strconv"
	"testing"
)

// TestCodeTable gives codes to numbers and to strings, through the growth of the table and a
// null in the middle, and finds each one back after a reset.
func TestCodeTable(t *testing.T) {
	for _, strs := range []bool{false, true} {
		tab := newCodeTable(strs)
		var vals [][]byte
		find := func(i int) (code, slot int, id uint64) {
			if strs {
				return tab.str(vals, []byte("v"+strconv.Itoa(i)))
			}
			code, slot = tab.number(uint64(i))
			return code, slot, uint64(i)
		}
		for round := range 2 {
			const n = 1000
			for i := range n {
				if i == 10 {
					tab.skip() // code 10 is null, which the table does not hold
					vals = append(vals, nil)
				}
				code, slot, id := find(i)
				if code >= 0 {
					t.Fatalf("strs %v, round %d: %d has code %d before it is put", strs, round, i, code)
				}
				tab.put(slot, id)
				vals = append(vals, []byte("v"+strconv.Itoa(i)))
			}
			for i := range n {
				want := i + boolInt(i >= 10)
				if code, _, _ := find(i); code != want {
					t.Fatalf("strs %v, round %d: %d has code %d, want %d", strs, round, i, code, want)
				}
			}
			if len(tab.slots) < 2*len(tab.ids) {
				t.Fatalf("strs %v: %d slots for %d codes", strs, len(tab.slots), len(tab.ids))
			}
			tab.reset()
			vals = vals[:0]
		}
	}
}
