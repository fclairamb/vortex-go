package encode

import (
	"encoding/binary"
	"testing"
)

// tableOf reads the symbol table out of the node of the reference: the symbols by code, and
// their lengths.
func tableOf(t testing.TB, out *vnode) ([]uint64, []uint8) {
	t.Helper()
	symbols, lens := unhex(t, out.Buffers[0]), unhex(t, out.Buffers[1])
	syms := make([]uint64, len(lens))
	for i := range syms {
		syms[i] = binary.LittleEndian.Uint64(symbols[8*i:])
	}
	return syms, lens
}

// vectorFSST encodes the strings of a vector with the table of the reference, and checks that
// every byte is the reference's.
func vectorFSST(t *testing.T, e *Encoder, v vector) {
	vals := make([][]byte, len(v.Strings))
	strs := make([]string, len(v.Strings))
	for i, s := range v.Strings {
		vals[i] = unhex(t, s)
		strs[i] = string(vals[i])
	}
	syms, lens := tableOf(t, v.Output)
	got, ok := FSSTBytesWith(e, vals, nullsOf(t, v), syms, lens)
	if !ok {
		t.Fatal("the table of the reference is refused")
	}
	if d := diff("", got, v.Output); d != "" {
		t.Fatal("bytes: " + d)
	}
	e.Reset()
	got, _ = FSSTStringsWith(e, strs, nullsOf(t, v), syms, lens)
	if d := diff("", got, v.Output); d != "" {
		t.Fatal("strings: " + d)
	}
}


// TestTrainingMatchesReference checks more than the spec asks: the table our training makes
// is the reference's, symbol for symbol and in the order of the codes, so the whole node is.
func TestTrainingMatchesReference(t *testing.T) {
	same, total := 0, 0
	forEachVector(t, func(t *testing.T, e *Encoder, file vecFile, v vector) {
		if file.Encoding != "fsst" {
			return
		}
		vals := make([][]byte, len(v.Strings))
		for i, s := range v.Strings {
			vals[i] = unhex(t, s)
		}
		got := FSSTBytes(e, vals, nullsOf(t, v))
		total++
		if d := diff("", got, v.Output); d != "" {
			t.Error(d)
		} else {
			same++
		}
	})
	if same != total || total < 30 {
		t.Fatalf("%d of %d identical", same, total)
	}
}
