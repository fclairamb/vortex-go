package pco

import (
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
)

// bw writes a little-endian bit stream, to build the metadata and the pages the oracle never
// produces: the corrupt ones, and the ones for formats a vector cannot reach.
type bw struct {
	b []byte
	n int
}

func (w *bw) put(v uint64, nbits int) *bw {
	for i := range nbits {
		for len(w.b)*8 <= w.n {
			w.b = append(w.b, 0)
		}
		if v>>i&1 == 1 {
			w.b[w.n>>3] |= 1 << (w.n & 7)
		}
		w.n++
	}
	return w
}

func (w *bw) align() *bw {
	w.n = (w.n + 7) &^ 7
	for len(w.b)*8 < w.n {
		w.b = append(w.b, 0)
	}
	return w
}

// bin is a bin of a latent variable: its weight, lower bound and offset bits.
type bin struct {
	weight uint32
	lower  uint64
	off    int
}

// lv is a latent variable of a chunk.
type lv struct {
	ansLog int
	bins   []bin
}

// single is a variable with one bin, so with no tANS bits.
func single(lower uint64, off int) lv { return lv{0, []bin{{1, lower, off}}} }

func (w *bw) latentVar(nb int, v lv) {
	w.put(uint64(v.ansLog), 4).put(uint64(len(v.bins)), 15)
	for _, b := range v.bins {
		w.put(uint64(b.weight-1), v.ansLog).put(b.lower, nb).put(uint64(b.off), bitLen(nb))
	}
}

func (w *bw) consecutive(order int, secondary bool) {
	w.put(deltaConsecutive, 4).put(uint64(order), 3)
	w.put(map[bool]uint64{false: 0, true: 1}[secondary], 1)
}

func (w *bw) lookback(windowLog, stateLog int) {
	w.put(deltaLookback, 4).put(uint64(windowLog-1), 5).put(uint64(stateLog), 4).put(0, 1)
}

func (w *bw) conv1(quant int, bias int64, weights ...int32) {
	w.put(deltaConv1, 4).put(uint64(quant), 5).put(uint64(bias)^1<<63, 64).put(uint64(len(weights)-1), 5)
	for _, x := range weights {
		w.put(uint64(uint32(x)^1<<31), 32)
	}
}

// meta builds a chunk metadata: the bits of the mode and of the delta encoding, then the latent
// variables, which are all nb bits wide but the delta ones.
func meta(nb int, head func(w *bw), vars ...lv) []byte {
	w := &bw{}
	head(w)
	for _, v := range vars {
		w.latentVar(nb, v)
	}
	return w.align().b
}

// classicHead is the mode and delta encoding of a classic chunk with no delta.
func classicHead(w *bw) { w.put(modeClassic, 4).put(deltaNone, 4) }

// pvar is a latent variable of a page of a chunk whose variables have one bin: its delta state,
// and the offset of each of its latents.
type pvar struct {
	nb      int
	state   []uint64
	offsets []uint64
	offBits int
	ansLog  int
}

func pageOf(vars ...pvar) []byte {
	w := &bw{}
	for _, v := range vars {
		for _, s := range v.state {
			w.put(s, v.nb)
		}
		w.put(0, 4*v.ansLog)
	}
	w.align()
	for _, v := range vars {
		for _, o := range v.offsets {
			w.put(o, v.offBits)
		}
	}
	return w.align().b
}

func chunk(t *testing.T, typ Type, m []byte) *Chunk {
	t.Helper()
	h, _, err := ReadHeader([]byte{4, 1})
	if err != nil {
		t.Fatal(err)
	}
	c, err := h.Chunk(typ, m)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func page(c *Chunk, p []byte, n int) ([]byte, error) {
	dst := make([]byte, n*c.typ.Bits()/8)
	return dst, c.Page(p, n, dst)
}

func wantCorrupt(t *testing.T, err error, sub string) {
	t.Helper()
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), sub) {
		t.Fatalf("err = %v, want a corruption with %q", err, sub)
	}
}

func TestReadHeader(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []byte
		want Header
		n    int
		err  string
	}{
		{"4.1", []byte{4, 1, 9}, Header{4, 1}, 2, ""},
		{"4.7 is read, in the hope it is compatible", []byte{4, 7}, Header{4, 7}, 2, ""},
		{"3", []byte{3, 9}, Header{3, 0}, 1, ""},
		{"empty", nil, Header{}, 0, "empty header"},
		{"4 without a minor", []byte{4}, Header{}, 0, "has 2"},
		{"5", []byte{5, 0}, Header{}, 0, "newer"},
		{"2", []byte{2}, Header{}, 0, "older than 3"},
	} {
		h, n, err := ReadHeader(c.in)
		if c.err != "" {
			wantCorrupt(t, err, c.err)
			continue
		}
		if err != nil || h != c.want || n != c.n {
			t.Errorf("%s: %v, %d, %v", c.name, h, n, err)
		}
	}
}

func TestTypes(t *testing.T) {
	for i, name := range typeNames {
		typ := Type(i)
		if typ.String() != name || typ.Float() != strings.HasPrefix(name, "f") || typ.Signed() != strings.HasPrefix(name, "i") {
			t.Errorf("%s: String %q Float %t Signed %t", name, typ, typ.Float(), typ.Signed())
		}
	}
}

func TestClassicPage(t *testing.T) {
	c := chunk(t, U32, meta(32, classicHead, single(5, 3)))
	got, err := page(c, pageOf(pvar{nb: 32, offsets: []uint64{1, 2, 7}, offBits: 3}), 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{6, 0, 0, 0, 7, 0, 0, 0, 12, 0, 0, 0}
	if string(got) != string(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPageArguments(t *testing.T) {
	c := chunk(t, U32, meta(32, classicHead, single(5, 3)))
	p := pageOf(pvar{nb: 32, offsets: []uint64{1, 2, 7}, offBits: 3})
	_, err := page(c, p, 0)
	wantCorrupt(t, err, "a page of 0 numbers")
	_, err = page(c, p, maxN+1)
	wantCorrupt(t, err, "a page of")
	wantCorrupt(t, c.Page(p, 3, make([]byte, 5)), "a destination of 5 bytes")
}

func TestChunkErrors(t *testing.T) {
	f32Mid := func(bits uint32) uint64 { return uint64(bits) ^ 1<<31 } // a float32 as a latent, when positive
	for _, c := range []struct {
		name string
		typ  Type
		head func(w *bw)
		vars []lv
		cut  []byte // the metadata itself, when it is not built
		want string
	}{
		{"unknown mode", U32, func(w *bw) { w.put(5, 4).put(0, 4) }, []lv{single(0, 0)}, nil, "unknown mode 5"},
		{"dictionary past the input", U32, func(w *bw) { w.put(modeDict, 4).put(1000, 25) }, nil, nil, "a dictionary of 1000 values"},
		{"dictionary padding", U32, func(w *bw) { w.put(modeDict, 4).put(0, 25).put(4, 3) }, nil, nil, "non-zero bits"},
		{"no input", U32, nil, nil, []byte{}, "the input ends"},
		{"dictionary length cut", U32, nil, nil, []byte{modeDict}, "the input ends"},
		{"consecutive of order 0", U32, func(w *bw) { w.put(modeClassic, 4).put(deltaConsecutive, 4).put(0, 4) }, []lv{single(0, 0)}, nil, "order 0"},
		{"lookback window too large", U32, func(w *bw) { w.put(modeClassic, 4).put(deltaLookback, 4).put(24, 5).put(0, 5) }, nil, nil, "2^25"},
		{"lookback state past the window", U32, func(w *bw) { w.put(modeClassic, 4).put(deltaLookback, 4).put(0, 5).put(2, 4).put(0, 1) }, nil, nil, "larger than its window"},
		{"lookback variable cut", U32, func(w *bw) { w.put(modeClassic, 4); w.lookback(4, 0) }, nil, nil, "the input ends"},
		{"unknown delta", U32, func(w *bw) { w.put(modeClassic, 4).put(9, 4) }, nil, nil, "unknown delta encoding 9"},
		{"tANS too small for the bins", U32, classicHead, []lv{{0, []bin{{1, 0, 0}, {1, 1, 0}}}}, nil, "too small for 2 bins"},
		{"one bin and a tANS", U32, classicHead, []lv{{3, []bin{{8, 0, 0}}}}, nil, "1 bin but"},
		{"tANS too large", U32, classicHead, []lv{{15, []bin{{1, 0, 0}, {1, 1, 0}}}}, nil, "at most 2^14"},
		{"weights", U32, classicHead, []lv{{1, []bin{{1, 0, 0}, {2, 1, 0}}}}, nil, "add up to 3"},
		{"no bin and a tANS", U32, classicHead, []lv{{1, nil}}, nil, "add up to 1"},
		{"offset bits", U32, classicHead, []lv{single(0, 33)}, nil, "33 offset bits"},
		{"int mult by 0", U32, func(w *bw) { w.put(modeIntMult, 4).put(0, 32).put(deltaNone, 4) }, []lv{single(0, 0), single(0, 0)}, nil, "int mult"},
		{"int mult floats", F32, func(w *bw) { w.put(modeIntMult, 4).put(3, 32).put(deltaNone, 4) }, []lv{single(0, 0), single(0, 0)}, nil, "int mult"},
		{"float mult ints", U32, func(w *bw) { w.put(modeFloatMult, 4).put(3, 32).put(deltaNone, 4) }, []lv{single(0, 0), single(0, 0)}, nil, "float mult"},
		{"float mult by infinity", F32, func(w *bw) { w.put(modeFloatMult, 4).put(f32Mid(0x7f800000), 32).put(deltaNone, 4) }, []lv{single(0, 0), single(0, 0)}, nil, "float mult"},
		{"float mult by zero", F32, func(w *bw) { w.put(modeFloatMult, 4).put(f32Mid(0), 32).put(deltaNone, 4) }, []lv{single(0, 0), single(0, 0)}, nil, "float mult"},
		{"float quant ints", U32, func(w *bw) { w.put(modeFloatQuant, 4).put(3, 8).put(deltaNone, 4) }, []lv{single(0, 0), single(0, 0)}, nil, "float quant"},
		{"float quant of 0", F32, func(w *bw) { w.put(modeFloatQuant, 4).put(0, 8).put(deltaNone, 4) }, []lv{single(0, 0), single(0, 0)}, nil, "float quant"},
		{"float quant too wide", F32, func(w *bw) { w.put(modeFloatQuant, 4).put(24, 8).put(deltaNone, 4) }, []lv{single(0, 0), single(0, 0)}, nil, "float quant"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := c.cut
			if c.cut == nil {
				w := &bw{}
				c.head(w)
				for _, v := range c.vars {
					w.latentVar(c.typ.Bits(), v)
				}
				m = w.align().b
			}
			h, _, _ := ReadHeader([]byte{4, 1})
			_, err := h.Chunk(c.typ, m)
			wantCorrupt(t, err, c.want)
		})
	}
}

// vars builds the variables of a lookback chunk: the lookbacks (32 bits), then the primary.
func lookbackChunk(t *testing.T, windowLog, stateLog int, lookbacks, primary lv) (*Chunk, error) {
	t.Helper()
	w := &bw{}
	w.put(modeClassic, 4)
	w.lookback(windowLog, stateLog)
	w.latentVar(32, lookbacks)
	w.latentVar(32, primary)
	h, _, _ := ReadHeader([]byte{4, 1})
	return h.Chunk(U32, w.align().b)
}

func TestLookbackChunkBins(t *testing.T) {
	_, err := lookbackChunk(t, 2, 0, single(0, 1), single(0, 0))
	wantCorrupt(t, err, "lookback bin with a lower bound of 0")
	_, err = lookbackChunk(t, 2, 0, single(5, 0), single(0, 0))
	wantCorrupt(t, err, "lower bound of 5 outside [1, 4]")
}

func TestLookbackPage(t *testing.T) {
	// A window of 2, a state of one latent, 4 numbers: 3 lookbacks and 3 deltas. The lookbacks
	// are 1 + their offset, and so is the delta's latent.
	c, err := lookbackChunk(t, 1, 0, single(1, 2), single(0, 8))
	if err != nil {
		t.Fatal(err)
	}
	p := pageOf(
		pvar{nb: 32, offsets: []uint64{0, 0, 1}, offBits: 2},
		pvar{nb: 32, state: []uint64{7}, offsets: []uint64{0, 0, 0}, offBits: 8},
	)
	// The lookbacks 1, 1 and 2 are within the window of 2.
	if _, err = page(c, p, 4); err != nil {
		t.Fatal(err)
	}
	// A lookback of 3 is outside a window of 2.
	p = pageOf(
		pvar{nb: 32, offsets: []uint64{0, 0, 2}, offBits: 2},
		pvar{nb: 32, state: []uint64{7}, offsets: []uint64{0, 0, 0}, offBits: 8},
	)
	_, err = page(c, p, 4)
	wantCorrupt(t, err, "lookback of 3, the window is 2")
}

func TestLookbackWrapsToZero(t *testing.T) {
	// 32 offset bits on a lower bound of 1: the latent 0xFFFFFFFF + 1 wraps to 0.
	c, err := lookbackChunk(t, 1, 0, single(1, 32), single(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	p := pageOf(
		pvar{nb: 32, offsets: []uint64{0xFFFFFFFF}, offBits: 32},
		pvar{nb: 32, state: []uint64{7}},
	)
	_, err = page(c, p, 2)
	wantCorrupt(t, err, "lookback of 0")
}

func TestPageErrors(t *testing.T) {
	t.Run("delta state past the input", func(t *testing.T) {
		w := &bw{}
		w.put(modeClassic, 4).consecutive(7, false)
		w.latentVar(64, single(0, 0))
		c := chunk(t, U64, w.align().b)
		_, err := page(c, []byte{1}, 20)
		wantCorrupt(t, err, "a delta state of 7 latents")
	})
	t.Run("padding of the page metadata", func(t *testing.T) {
		two := lv{1, []bin{{1, 0, 0}, {1, 1, 0}}}
		c := chunk(t, U32, meta(32, classicHead, two))
		w := (&bw{}).put(0, 4).put(1, 4)
		_, err := page(c, w.b, 1)
		wantCorrupt(t, err, "non-zero bits at the end of the page metadata")
	})
	t.Run("padding of the page", func(t *testing.T) {
		c := chunk(t, U32, meta(32, classicHead, single(0, 3)))
		w := (&bw{}).put(1, 3).put(1, 5)
		_, err := page(c, w.b, 1)
		wantCorrupt(t, err, "non-zero bits at the end of the page")
	})
	t.Run("no bins", func(t *testing.T) {
		c := chunk(t, U32, meta(32, classicHead, lv{}))
		_, err := page(c, nil, 1)
		wantCorrupt(t, err, "1 numbers and no bins")
	})
	t.Run("the offsets are cut", func(t *testing.T) {
		c := chunk(t, U32, meta(32, classicHead, single(0, 20)))
		_, err := page(c, []byte{1, 2}, 1)
		wantCorrupt(t, err, "the input ends")
	})
}

func TestDictIndexOutOfRange(t *testing.T) {
	w := &bw{}
	w.put(modeDict, 4).put(2, 25).align().put(10, 32).put(20, 32).put(deltaNone, 4)
	w.latentVar(32, single(0, 2))
	c := chunk(t, U32, w.align().b)
	got, err := page(c, pageOf(pvar{nb: 32, offsets: []uint64{0, 1}, offBits: 2}), 2)
	if err != nil || binary.LittleEndian.Uint32(got) != 10 || binary.LittleEndian.Uint32(got[4:]) != 20 {
		t.Fatalf("got %v, %v", got, err)
	}
	_, err = page(c, pageOf(pvar{nb: 32, offsets: []uint64{0, 2}, offBits: 2}), 2)
	wantCorrupt(t, err, "dictionary index 2")
}

func TestConvChunkErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		typ  Type
		head func(w *bw)
		want string
	}{
		{"64 bit latents", U64, func(w *bw) { w.put(modeClassic, 4).conv1(1, 0, 1) }, "64 bit latents"},
		{"quantization", U32, func(w *bw) { w.put(modeClassic, 4).conv1(31, 0, 1) }, ""},
		{"overflow", U32, func(w *bw) { w.put(modeClassic, 4).conv1(1, 0, math.MaxInt32, math.MaxInt32) }, "overflowing"},
		{"u8 quantization", U8, func(w *bw) { w.put(modeClassic, 4).conv1(16, 0, 1) }, "quantization of 16"},
	} {
		w := &bw{}
		c.head(w)
		w.latentVar(c.typ.Bits(), single(0, 0))
		h, _, _ := ReadHeader([]byte{4, 1})
		_, err := h.Chunk(c.typ, w.align().b)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		wantCorrupt(t, err, c.want)
	}
}

func TestConvShorterThanItsState(t *testing.T) {
	// Order 2, one number: it is the first latent of the state.
	w := &bw{}
	w.put(modeClassic, 4).conv1(1, 0, 1, 1)
	w.latentVar(32, single(0, 32))
	c := chunk(t, U32, w.align().b)
	got, err := page(c, pageOf(pvar{nb: 32, state: []uint64{9, 5}}), 1)
	if err != nil || binary.LittleEndian.Uint32(got) != 9 {
		t.Fatalf("got %v, %v", got, err)
	}
	// Three numbers: the state, then one latent predicted from them: (9 + 5) >> 1 = 7, plus a
	// delta of 0, which is stored as 2^31.
	got, err = page(c, pageOf(pvar{nb: 32, state: []uint64{9, 5}, offsets: []uint64{1 << 31}, offBits: 32}), 3)
	if err != nil || binary.LittleEndian.Uint32(got[8:]) != 7 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestConsecutiveWithoutBins(t *testing.T) {
	w := &bw{}
	w.put(modeClassic, 4).consecutive(1, false)
	w.latentVar(32, lv{})
	c := chunk(t, U32, w.align().b)
	got, err := page(c, pageOf(pvar{nb: 32, state: []uint64{77}}), 1)
	if err != nil || binary.LittleEndian.Uint32(got) != 77 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestFloatBases(t *testing.T) {
	for _, c := range []struct {
		typ  Type
		bits uint64
		ok   bool
	}{
		{F16, 0x3c00, true}, {F16, 0x7c00, false}, {F16, 0x0000, false}, {F16, 0x8000, false}, {F16, 0x0001, true},
		{F32, 0x3f800000, true}, {F32, 0x7fc00000, false}, {F32, 0xff800000, false},
		{F64, 0x4000000000000000, true}, {F64, 0x7ff0000000000000, false},
	} {
		if got := validFloatBase(c.typ, c.typ.floatToOrdered(c.bits)); got != c.ok {
			t.Errorf("%s %#x: %t", c.typ, c.bits, got)
		}
	}
}

func TestChunkPadding(t *testing.T) {
	m := meta(32, classicHead, single(0, 0))
	m[len(m)-1] |= 0x80
	h, _, _ := ReadHeader([]byte{4, 1})
	_, err := h.Chunk(U32, m)
	wantCorrupt(t, err, "non-zero bits at the end of the chunk metadata")
}
