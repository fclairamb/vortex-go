package compress

import (
	"bytes"
	"fmt"
	"testing"
)

// TestDictProbe: the probe gives up on runs that hold too many bytes or no valid row, and the
// cost model leaves to the sample a dictionary over the bounds of the trial.
func TestDictProbe(t *testing.T) {
	c := New(Default)
	long := make([][]byte, 20000)
	for i := range long {
		long[i] = bytes.Repeat([]byte{byte(i)}, 1024)
	}
	if _, ok := c.probeOf(long, nil, probeRun); ok {
		t.Error("a probe of 8 MiB is made")
	}
	nulls := make([]bool, len(long))
	for i := range nulls {
		nulls[i] = true
	}
	if _, ok := c.probeOf(long, nulls, probeRun); ok {
		t.Error("a probe of null rows is made")
	}
	if got := (probe{}).runStarts(); got != 1 {
		t.Errorf("a probe of no pair starts %v runs a row", got)
	}
	// 2^24 rows of a hundred values: their codes are over the bounds of the trial
	pr := profile{rows: 1 << 24, valid: 1 << 24, valueBytes: 1 << 24, plain: 1 << 30}
	p := probe{rows: 8192, distinct: 100, doubletons: 100, pairs: 8000, changes: 8000}
	if got := admit(pr, p, 1); got != admitTooLarge {
		t.Errorf("admission %d", got)
	}
}

// TestDictTooLarge: a dictionary that the probe under-counts, 70000 values each twice in a row,
// is over the bounds of the trial once built, and is measured on the sample.
func TestDictTooLarge(t *testing.T) {
	v := make([][]byte, 140000)
	for i := range v {
		v[i] = fmt.Appendf(nil, "v%06d", i/2)
	}
	c := New(Default)
	if n := c.Strings(v, nil, true); n == nil {
		t.Fatal("the strings are not compressed")
	}
	x := c.top()
	x.full = true
	if n := c.applyStrings(x, sDict, v, nil, true); n != nil {
		t.Errorf("a dictionary of 70000 values is %s", tree(n))
	}
}
