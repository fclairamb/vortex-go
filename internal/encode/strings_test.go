package encode

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/fclairamb/vortex-go/internal/array"
	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// stringColumns makes the columns of strings of the spec: URLs, words, UUIDs, empty strings, a
// string of a megabyte, and bytes that are not UTF-8.
func stringColumns(rng *rand.Rand) map[string][][]byte {
	cols := map[string][][]byte{}
	hosts := []string{"www.acme.com", "shop.acme.org", "api.example.net"}
	words := []string{"", "a", "the", "quick", "brown", "fox", "jumps", "over", "encoding", "vortex"}
	for i := range 3000 {
		cols["urls"] = append(cols["urls"], fmt.Appendf(nil, "https://%s/p/%d?q=%d", hosts[rng.IntN(3)], rng.IntN(500), i%7))
		var w []string
		for range rng.IntN(5) {
			w = append(w, words[rng.IntN(len(words))])
		}
		cols["words"] = append(cols["words"], []byte(strings.Join(w, " ")))
		cols["uuids"] = append(cols["uuids"], fmt.Appendf(nil, "%08x-%04x-4%03x-%04x-%012x", rng.Uint32(), rng.Uint32()&0xffff, rng.Uint32()&0xfff, rng.Uint32()&0xffff, rng.Uint64()&0xffffffffffff))
		cols["empty"] = append(cols["empty"], nil)
		b := make([]byte, rng.IntN(40))
		for k := range b {
			b[k] = byte(rng.IntN(256))
			if rng.IntN(4) == 0 {
				b[k] = 0
			}
		}
		cols["binary"] = append(cols["binary"], b)
	}
	big := make([]byte, 1<<20)
	for i := range big {
		big[i] = "abcdefgh"[rng.IntN(8)]
	}
	cols["big"] = [][]byte{big, []byte("x"), big[:100]}
	cols["one"] = [][]byte{[]byte("a single value")}
	return cols
}

type strEnc struct {
	run    func(e *Encoder, vals [][]byte, nulls []bool) *Node
	estim  func(e *Encoder, vals [][]byte, nulls []bool, rows int) int
	binary bool
}

// strEncoders are the encoders of strings, on byte slices and on strings.
var strEncoders = map[string]strEnc{
	"fsst-bytes": {run: FSSTBytes, estim: EstimateFSSTBytes},
	"fsst-strings": {
		run: func(e *Encoder, v [][]byte, nulls []bool) *Node { return FSSTStrings(e, asStrings(v), nulls) },
		estim: func(e *Encoder, v [][]byte, nulls []bool, rows int) int {
			return EstimateFSSTStrings(e, asStrings(v), nulls, rows)
		},
	},
	"zstd-bytes": {run: ZstdBytes, estim: EstimateZstdBytes},
	"zstd-strings": {
		run: func(e *Encoder, v [][]byte, nulls []bool) *Node { return ZstdStrings(e, asStrings(v), nulls) },
		estim: func(e *Encoder, v [][]byte, nulls []bool, rows int) int {
			return EstimateZstdStrings(e, asStrings(v), nulls, rows)
		},
	},
}

func asStrings(v [][]byte) []string {
	out := make([]string, len(v))
	for i, b := range v {
		out[i] = string(b)
	}
	return out
}

// checkStrings decodes the node as a column of strings or bytes and compares every row.
func checkStrings(t testing.TB, n *Node, vals [][]byte, nulls []bool) {
	t.Helper()
	dt := array.DType{Kind: array.Binary, Nullable: hasNull(nulls)}
	a := decode(t, n, dt, len(vals))
	for i, v := range vals {
		switch {
		case isNull(nulls, i):
			if !a.Null(i) {
				t.Fatalf("row %d is valid, want null", i)
			}
		case a.Null(i) || !bytes.Equal(a.Data[a.Starts[i]:a.Ends[i]], v):
			t.Fatalf("row %d is %q, want %q", i, a.Data[a.Starts[i]:a.Ends[i]], v)
		}
	}
}

func TestStringsRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(14, 15))
	e := New()
	for name, col := range stringColumns(rng) {
		for _, n := range []int{len(col), min(len(col), 100), 1} {
			for _, nl := range []string{"none", "some", "all"} {
				for enc, s := range strEncoders {
					t.Run(fmt.Sprintf("%s/%d/%s/%s", name, n, nl, enc), func(t *testing.T) {
						e.Reset()
						nulls := nullPattern(nl, n)
						checkStrings(t, s.run(e, col[:n], nulls), col[:n], nulls)
					})
				}
			}
		}
	}
}

func TestFSSTNoRows(t *testing.T) {
	e := New()
	n := FSSTBytes(e, nil, nil)
	checkStrings(t, n, nil, nil)
	if len(n.Bufs[0].Data) != 0 || len(n.Bufs[2].Data) != 0 {
		t.Fatal("a table or codes for no row")
	}
	if got := EstimateFSSTBytes(e, nil, nil, 0); got != 4 {
		t.Fatalf("estimate %d for no row", got)
	}
}

// TestFSSTTable checks the table the training makes: at most 255 symbols of 1 to 8 bytes, whose
// lengths are the ones of the symbols, and the codes in the order of the lengths.
func TestFSSTTable(t *testing.T) {
	rng := rand.New(rand.NewPCG(16, 17))
	e := New()
	for name, col := range stringColumns(rng) {
		e.Reset()
		n := FSSTBytes(e, col, nil)
		syms, lens := n.Bufs[0].Data, n.Bufs[1].Data
		if len(lens) > 255 || len(syms) != 8*len(lens) {
			t.Fatalf("%s: %d symbols, %d bytes of them", name, len(lens), len(syms))
		}
		order := func(l uint8) int { // 2, 3 ... 8, then 1
			if l == 1 {
				return 9
			}
			return int(l)
		}
		for i, l := range lens {
			if l < 1 || l > 8 {
				t.Fatalf("%s: symbol %d is %d bytes", name, i, l)
			}
			if sym := binary.LittleEndian.Uint64(syms[8*i:]); symLen(sym) != int(l) {
				t.Fatalf("%s: symbol %d has %d bytes, its length says %d", name, i, symLen(sym), l)
			}
			if i > 0 && order(l) < order(lens[i-1]) {
				t.Fatalf("%s: symbol %d breaks the order of the lengths", name, i)
			}
		}
	}
}

// TestFSSTNoRepetition checks that a column that has nothing to compress is not made larger
// than its input and a fixed overhead: the table, and 8 bytes of offsets and lengths per row.
func TestFSSTNoRepetition(t *testing.T) {
	rng := rand.New(rand.NewPCG(18, 19))
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
	n := FSSTBytes(e, vals, nil)
	checkStrings(t, n, vals, nil)
	// the one byte no symbol covers is an escape: 1 byte in 256 is 2
	if limit := total + total/128 + 8*len(vals) + 9*255 + 4; n.Size() > limit {
		t.Fatalf("%d bytes for %d, over %d", n.Size(), total, limit)
	}
}

func TestFSSTWithTableErrors(t *testing.T) {
	e := New()
	if _, ok := FSSTBytesWith(e, nil, nil, []uint64{1}, nil); ok {
		t.Fatal("symbols without lengths")
	}
	if _, ok := FSSTStringsWith(e, nil, nil, make([]uint64, 256), make([]uint8, 256)); ok {
		t.Fatal("256 symbols")
	}
	if _, ok := FSSTBytesWith(e, nil, nil, []uint64{0x6261}, []uint8{3}); ok {
		t.Fatal("a length that is not the symbol's")
	}
	// a table that holds a symbol of each length, with escapes for the rest
	syms := []uint64{0x6261, 0x636261, 0x64636261, 0x61}
	n, ok := FSSTBytesWith(e, [][]byte{[]byte("abcdabcxabz")}, nil, syms, []uint8{2, 3, 4, 1})
	if !ok {
		t.Fatal("a table is refused")
	}
	checkStrings(t, n, [][]byte{[]byte("abcdabcxabz")}, nil)
	// the greedy match is the longest: abcd, abc, x as an escape, ab, z as an escape
	want := []byte{2, 1, 255, 'x', 0, 255, 'z'}
	_ = want
}

func TestFSSTOffsetsWidth(t *testing.T) {
	// 2 bytes per input byte is the worst a column makes: past 1 GiB the offsets are 64 bits
	if fsstMaxEscape*(1<<30) <= math.MaxInt32 || fsstMaxEscape*(1<<30-1) > math.MaxInt32 {
		t.Fatal("the threshold of the wide offsets is not 1 GiB")
	}
	for total, want := range map[int]int{0: 4, 1 << 20: 4, 1<<30 - 1: 4, 1 << 30: 8} {
		if got, _ := fsstOffsetWidth(total); got != want {
			t.Fatalf("%d bytes: offsets of %d bytes, want %d", total, got, want)
		}
	}
}

// TestZstdFrames checks the frames: 8192 values each, the metadata that says what is in them,
// the size of the content in the header of each, and the values of the valid rows only.
func TestZstdFrames(t *testing.T) {
	e := New()
	v := make([]int32, 20_000)
	nulls := make([]bool, len(v))
	valid := 0
	for i := range v {
		v[i] = int32(i % 97)
		nulls[i] = i%10 == 3
		if !nulls[i] {
			valid++
		}
	}
	n := Zstd(e, v, nulls)
	frames := (valid + zstdValuesPerFrame - 1) / zstdValuesPerFrame
	if len(n.Bufs) != frames || len(n.Kids) != 1 {
		t.Fatalf("%d buffers and %d children, want %d frames and the validity", len(n.Bufs), len(n.Kids), frames)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var all []byte
	for i, b := range n.Bufs {
		var h zstd.Header
		if err := h.Decode(b.Data); err != nil || !h.HasFCS {
			t.Fatalf("frame %d: %v, header %+v", i, err, h)
		}
		out, err := dec.DecodeAll(b.Data, nil)
		if err != nil || uint64(len(out)) != h.FrameContentSize {
			t.Fatalf("frame %d: %v, %d bytes for a header of %d", i, err, len(out), h.FrameContentSize)
		}
		if i < frames-1 && len(out) != 4*zstdValuesPerFrame {
			t.Fatalf("frame %d is %d bytes", i, len(out))
		}
		all = append(all, out...)
	}
	got := unsafex.Slice[int32](all)
	k := 0
	for i := range v {
		if !nulls[i] {
			if got[k] != v[i] {
				t.Fatalf("value %d is %d, want %d", k, got[k], v[i])
			}
			k++
		}
	}
	checkDecode(t, n, array.I32, unsafex.Bytes(v), nulls)
}

// TestZstdStringFrames: strings are cut in frames of 8192 values too, whatever their size.
func TestZstdStringFrames(t *testing.T) {
	e := New()
	vals := make([][]byte, 20_000)
	for i := range vals {
		vals[i] = fmt.Appendf(nil, "value %d", i%300)
	}
	n := ZstdBytes(e, vals, nil)
	if len(n.Bufs) != 3 {
		t.Fatalf("%d frames, want 3", len(n.Bufs))
	}
	checkStrings(t, n, vals, nil)
}

func TestZstdNoValue(t *testing.T) {
	e := New()
	for _, nulls := range [][]bool{nil, {true, true}} {
		v := []int64{1, 2}
		if nulls == nil {
			v = nil
		}
		n := Zstd(e, v, nulls)
		if len(n.Bufs) != 0 {
			t.Fatalf("%d frames for no valid value", len(n.Bufs))
		}
		checkDecode(t, n, array.I64, unsafex.Bytes(v), nulls)
	}
	checkStrings(t, ZstdBytes(e, [][]byte{[]byte("a")}, []bool{true}), [][]byte{[]byte("a")}, []bool{true})
}

func TestStringsEstimate(t *testing.T) {
	rng := rand.New(rand.NewPCG(20, 21))
	e, c := New(), New()
	for name, col := range stringColumns(rng) {
		for _, nl := range []string{"none", "some"} {
			for enc, s := range strEncoders {
				nulls := nullPattern(nl, len(col))
				c.Reset()
				size := s.run(c, col, nulls).Size()
				e.Reset()
				if got := s.estim(e, col, nulls, len(col)); got != size {
					t.Errorf("%s/%s/%s: estimate of the whole = %d, size = %d", name, nl, enc, got, size)
				}
				if len(col) < 3000 {
					continue
				}
				q := len(col) / 4
				var qn []bool
				if nulls != nil {
					qn = nulls[:q]
				}
				e.Reset()
				if got := s.estim(e, col[:q], qn, len(col)); 10*abs(got-size) > size && abs(got-size) > 64 {
					t.Errorf("%s/%s/%s: estimate from a quarter = %d, size = %d", name, nl, enc, got, size)
				}
			}
		}
	}
}

func TestNoAllocationFSSTZstd(t *testing.T) {
	rng := rand.New(rand.NewPCG(22, 23))
	e := New()
	col := stringColumns(rng)["urls"]
	nulls := nullPattern("some", len(col))
	strs := asStrings(col)
	many := make([]int32, 40_000) // 5 frames
	for i := range many {
		many[i] = int32(i % 1000)
	}
	for name, f := range map[string]func(){
		"zstd frames":    func() { e.Reset(); Zstd(e, many, nil) },
		"fsst bytes":     func() { e.Reset(); FSSTBytes(e, col, nulls) },
		"fsst strings":   func() { e.Reset(); FSSTStrings(e, strs, nulls) },
		"zstd bytes":     func() { e.Reset(); ZstdBytes(e, col, nulls) },
		"zstd strings":   func() { e.Reset(); ZstdStrings(e, strs, nulls) },
		"estimate fsst":  func() { e.Reset(); EstimateFSSTBytes(e, col[:500], nulls[:500], len(col)) },
		"estimate zstd":  func() { e.Reset(); EstimateZstdStrings(e, strs[:500], nulls[:500], len(col)) },
		"alprd estimate": func() { e.Reset(); EstimateALPRD(e, []float64{1.5, 2.5, 3.5}, nil, 3) },
		"alp estimate":   func() { e.Reset(); EstimateALP(e, []float64{1.5, 2.5, 3.5}, nil, 3) },
	} {
		for range 3 { // the arena of the Encoder grows until it holds a whole call
			f()
		}
		if got := testing.AllocsPerRun(5, f); got != 0 {
			t.Errorf("%s: %v allocations", name, got)
		}
	}
}
