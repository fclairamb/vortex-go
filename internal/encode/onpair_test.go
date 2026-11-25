package encode

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"
)

// dictOf reads the dictionary out of the node of the reference: the bytes of the tokens and
// their offsets.
func dictOf(t testing.TB, out *vnode) ([]byte, []uint32) {
	t.Helper()
	raw := unhex(t, out.Children[0].Buffers[0])
	offs := make([]uint32, len(raw)/4)
	for i := range offs {
		offs[i] = binary.LittleEndian.Uint32(raw[4*i:])
	}
	return unhex(t, out.Buffers[0]), offs
}

// vectorOnPair encodes the strings of a vector with the dictionary of the reference, and checks
// that every byte is the reference's.
func vectorOnPair(t *testing.T, e *Encoder, v vector) {
	vals := make([][]byte, len(v.Strings))
	for i, s := range v.Strings {
		vals[i] = unhex(t, s)
	}
	dict, offs := dictOf(t, v.Output)
	got, ok := OnPairBytesWith(e, vals, nullsOf(t, v), dict, offs)
	if !ok {
		t.Fatal("the dictionary of the reference is refused")
	}
	if d := diff("", got, v.Output); d != "" {
		t.Fatal("bytes: " + d)
	}
}

// TestOnPairTraining says how many of the vectors our training gives the reference's node for,
// which is not asked (the sample is not the crate's), and that they all decode.
func TestOnPairTraining(t *testing.T) {
	same, total := 0, 0
	forEachVector(t, func(t *testing.T, e *Encoder, file vecFile, v vector) {
		if file.Encoding != "onpair" {
			return
		}
		vals := make([][]byte, len(v.Strings))
		for i, s := range v.Strings {
			vals[i] = unhex(t, s)
		}
		nulls := nullsOf(t, v)
		got := OnPairBytes(e, vals, nulls)
		checkStrings(t, got, vals, nulls)
		total++
		if diff("", got, v.Output) == "" {
			same++
		}
	})
	t.Logf("%d of %d vectors: our training makes the reference's node", same, total)
	if total < 30 {
		t.Fatalf("only %d vectors", total)
	}
}

func TestOnPairWithErrors(t *testing.T) {
	e := New()
	var dict []byte
	offs := []uint32{0}
	for b := range 256 {
		dict = append(dict, byte(b))
		offs = append(offs, uint32(len(dict)))
	}
	if _, ok := OnPairBytesWith(e, [][]byte{[]byte("abc")}, nil, dict, offs); !ok {
		t.Fatal("the 256 bytes are a dictionary")
	}
	mut := func(f func(d []byte, o []uint32) ([]byte, []uint32)) bool {
		d, o := f(bytes.Clone(dict), append([]uint32(nil), offs...))
		_, ok := OnPairBytesWith(e, nil, nil, d, o)
		return ok
	}
	for name, f := range map[string]func(d []byte, o []uint32) ([]byte, []uint32){
		"no byte": func(d []byte, o []uint32) ([]byte, []uint32) { return d[:100], o[:101] },
		"too many tokens": func(d []byte, o []uint32) ([]byte, []uint32) {
			for range onPairMaxTokens {
				d = append(d, 0xff, 0xff)
				o = append(o, uint32(len(d)))
			}
			return d, o
		},
		"first offset":  func(d []byte, o []uint32) ([]byte, []uint32) { o[0] = 1; return d, o },
		"past the data": func(d []byte, o []uint32) ([]byte, []uint32) { return d[:200], o },
		"empty token":   func(d []byte, o []uint32) ([]byte, []uint32) { o[5] = o[4]; return d, o },
		"long token": func(d []byte, o []uint32) ([]byte, []uint32) {
			d = append(d, bytes.Repeat([]byte{0xff}, 17)...)
			return d, append(o, uint32(len(d)))
		},
		"not sorted": func(d []byte, o []uint32) ([]byte, []uint32) { d[10], d[11] = d[11], d[10]; return d, o },
		"a byte twice": func(d []byte, o []uint32) ([]byte, []uint32) {
			d = append(d, 'a', 'b')
			o = append(o, uint32(len(d)-1))
			return d, append(o, uint32(len(d)))
		},
	} {
		if mut(f) {
			t.Errorf("%s: accepted", name)
		}
	}
	// 256 sorted tokens that are not the bytes: one byte is missing
	d2 := bytes.Clone(dict[:255])
	o2 := append([]uint32(nil), offs[:256]...)
	d2 = append(d2, 0xff, 0xff)
	o2 = append(o2, uint32(len(d2)))
	if _, ok := OnPairBytesWith(e, nil, nil, d2, o2); ok {
		t.Error("a dictionary without the byte 0xfe")
	}
}

func TestOnPairNoRows(t *testing.T) {
	e := New()
	n := OnPairBytes(e, nil, nil)
	checkStrings(t, n, nil, nil)
	if got, size := EstimateOnPairBytes(e, nil, nil, 0), n.Size(); got != size {
		t.Fatalf("estimate %d, size %d", got, size)
	}
	// every row null is a constant, as the reference
	all := []bool{true, true, true}
	n = OnPairBytes(e, make([][]byte, 3), all)
	if n.ID != IDConstant {
		t.Fatalf("%s for null rows", n.ID)
	}
	checkStrings(t, n, make([][]byte, 3), all)
}

// TestOnPairDictionary checks the dictionary the training makes: at most 4096 tokens of 1 to
// 16 bytes, the 256 bytes among them, sorted, and the padding that lets a token be read whole.
func TestOnPairDictionary(t *testing.T) {
	rng := rand.New(rand.NewPCG(24, 25))
	e := New()
	for name, col := range stringColumns(rng) {
		e.Reset()
		n := OnPairBytes(e, col, nil)
		dict, offs := n.Bufs[0].Data, n.Kids[0].Bufs[0].Data
		tokens := len(offs)/4 - 1
		if tokens < 256 || tokens > 1<<onPairMaxBits {
			t.Fatalf("%s: %d tokens", name, tokens)
		}
		off := func(i int) int { return int(binary.LittleEndian.Uint32(offs[4*i:])) }
		singles := 0
		for i := range tokens {
			tok := dict[off(i):off(i+1)]
			if len(tok) < 1 || len(tok) > onPairMaxToken {
				t.Fatalf("%s: token %d is %d bytes", name, i, len(tok))
			}
			if i > 0 && bytes.Compare(dict[off(i-1):off(i)], tok) >= 0 {
				t.Fatalf("%s: token %d is not after the one before", name, i)
			}
			if len(tok) == 1 {
				singles++
			}
		}
		if singles != 256 {
			t.Fatalf("%s: %d single bytes", name, singles)
		}
		if len(dict) < off(tokens-1)+onPairMaxToken {
			t.Fatalf("%s: the last token is not padded: %d bytes", name, len(dict))
		}
		if name == "urls" && tokens < 300 {
			t.Fatalf("urls: only %d tokens", tokens)
		}
	}
}

// TestOnPairNoRepetition checks that a column that has nothing to compress is not made larger
// than its input and a fixed overhead: the dictionary of the bytes, and 12 bytes of offsets and
// lengths per row, and 2 bytes of code per byte at the worst.
func TestOnPairNoRepetition(t *testing.T) {
	rng := rand.New(rand.NewPCG(26, 27))
	e := New()
	vals := make([][]byte, 2000)
	total := 0
	for i := range vals {
		vals[i] = make([]byte, 50)
		for k := range vals[i] {
			vals[i][k] = byte(rng.IntN(256))
		}
		total += len(vals[i])
	}
	n := OnPairBytes(e, vals, nil)
	checkStrings(t, n, vals, nil)
	// a code is 2 bytes, and a byte that no token joins is a code: twice the input
	limit := 2*total + 12*len(vals) + 256 + onPairMaxToken + 4*257 + 4*onPairMaxTokens + 4
	if n.Size() > limit {
		t.Fatalf("%d bytes for %d, over %d", n.Size(), total, limit)
	}
}

// TestOnPairSkewedRows: a few long rows among many short ones make the sample take more rows.
func TestOnPairSkewedRows(t *testing.T) {
	e := New()
	vals := make([][]byte, 5000)
	for i := range vals {
		vals[i] = []byte("ab")
	}
	for i := 0; i < len(vals); i += 1000 {
		vals[i] = bytes.Repeat([]byte("the quick brown fox "), 5000)
	}
	checkStrings(t, OnPairBytes(e, vals, nil), vals, nil)
}

// TestOnPairMeta: the first field of the metadata is the type of the lengths, u32 (2).
func TestOnPairMeta(t *testing.T) {
	e := New()
	n := OnPairBytes(e, [][]byte{[]byte("abcabcabc")}, nil)
	if !bytes.Equal(n.Meta[:2], []byte{0x08, 0x02}) {
		t.Fatalf("metadata %x", n.Meta)
	}
}

// TestNarrowOnPair: the children of a narrowed array are the narrowest unsigned integers that
// hold their values, the metadata says so, and the strings read back the same.
func TestNarrowOnPair(t *testing.T) {
	e := New()
	vals := make([][]byte, 300)
	for i := range vals {
		vals[i] = fmt.Appendf(nil, "row %d of the narrow test", i%40)
	}
	nulls := make([]bool, len(vals))
	nulls[7] = true
	n := OnPairBytes(e, vals, nulls)
	NarrowOnPair(e, n)
	checkStrings(t, n, vals, nulls)
	for i, want := range []int{2, 1, 2, 1} { // dictionary offsets, codes, codes offsets, lengths
		if got := n.Kids[i].Bufs[0].Align; got != want {
			t.Errorf("child %d: %d bytes, want %d", i, got, want)
		}
	}
	// every row null is a constant, which stays as it is
	all := []bool{true, true}
	n = OnPairBytes(e, make([][]byte, 2), all)
	NarrowOnPair(e, n)
	checkStrings(t, n, make([][]byte, 2), all)
}
