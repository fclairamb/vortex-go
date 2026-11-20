package array

import (
	"bytes"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// scalarOf is the ScalarValue message of an integer of type p, given as its low bits.
func scalarOf(p PType, v uint64) []byte {
	if p.Signed() {
		bits := uint(8 * p.Width())
		return sInt(int64(v<<(64-bits)) >> (64 - bits))
	}
	return sUint(v)
}

// oneBuf is a node over a primitive buffer, then the segment of the root node.
func prim(p PType, vals []uint64) (fbtest.Tab, buf) {
	return pnode(0), buf{data: leInts(p.Width(), vals)}
}

func TestFrameOfReference(t *testing.T) {
	rng := newRand(3)
	for _, p := range []PType{U8, U16, U32, U64, I8, I16, I32, I64} {
		bits := 8 * p.Width()
		enc := make([]uint64, 2500) // more than one run of 1024
		want := make([]uint64, len(enc))
		ref := rng.Uint64() & maskOf(bits)
		for i := range enc {
			enc[i] = rng.Uint64() & maskOf(bits)
			want[i] = (enc[i] + ref) & maskOf(bits)
		}
		kid, b := prim(p, enc)
		a := mustDecode(t, segment(node(eFoR, scalarOf(p, ref), nil, kid), b), primT(p), len(enc))
		if !bytes.Equal(a.Data, leInts(p.Width(), want)) {
			t.Fatalf("%s: wrong values", p)
		}
	}
	// A wrap, and a validity carried by the child.
	seg := segment(node(eFoR, sUint(250), nil, node(ePrim, nil, []uint16{0}, bnode(1))), buf{data: []byte{10, 1}}, buf{data: []byte{0b01}})
	a := mustDecode(t, seg, DType{Kind: Prim, PType: U8, Nullable: true}, 2)
	if a.Data[0] != 4 || a.Data[1] != 251 || a.Null(0) || !a.Null(1) {
		t.Fatalf("a = %+v", a)
	}
	kid, b := prim(U8, []uint64{1})
	good := func(meta []byte) []byte { return segment(node(eFoR, meta, nil, kid), b) }
	runErrCases(t, []errCase{
		{"string type", good(sUint(1)), utf8t, 1, "cannot be a utf8"},
		{"float type", good(sUint(1)), primT(F32), 1, "cannot be a f32"},
		{"a buffer", segment(node(eFoR, sUint(1), []uint16{0}, kid), b), primT(U8), 1, "no buffer"},
		{"no child", segment(node(eFoR, sUint(1), nil)), primT(U8), 1, "1 child"},
		{"reference of the wrong type", good(sStr("a")), primT(U8), 1, "reference"},
		{"reference too large", good(sUint(256)), primT(U8), 1, "does not fit"},
		{"reference negative for an unsigned type", good(sInt(-1)), primT(U8), 1, "does not fit"},
		{"null reference", good(sNull()), primT(U8), 1, "reference"},
		{"bad reference", good([]byte{0x18}), primT(U8), 1, "reference"},
		{"child of the wrong length", good(sUint(1)), primT(U8), 2, "buffer"},
	})
}

func TestZigZag(t *testing.T) {
	rng := newRand(4)
	for _, p := range []PType{I8, I16, I32, I64} {
		bits := 8 * p.Width()
		enc := make([]uint64, 2100)
		want := make([]uint64, len(enc))
		for i := range enc {
			enc[i] = rng.Uint64() & maskOf(bits)
			want[i] = uint64(int64(enc[i]>>1)^-int64(enc[i]&1)) & maskOf(bits)
		}
		kid, b := prim(unsignedOf(p), enc)
		a := mustDecode(t, segment(node(eZigZag, nil, nil, kid), b), primT(p), len(enc))
		if !bytes.Equal(a.Data, leInts(p.Width(), want)) || a.Type.PType != p {
			t.Fatalf("%s: wrong values", p)
		}
	}
	// The classic values: 0, -1, 1, -2, 2 are 0, 1, 2, 3, 4.
	seg := segment(node(eZigZag, nil, nil, pnode(0)), buf{data: []byte{0, 1, 2, 3, 4}})
	if got := mustDecode(t, seg, primT(I8), 5).Data; !bytes.Equal(got, []byte{0, 0xff, 1, 0xfe, 2}) {
		t.Fatalf("got %v", got)
	}
	// A validity of the child is carried.
	seg = segment(node(eZigZag, nil, nil, node(ePrim, nil, []uint16{0}, bnode(1))), buf{data: []byte{2, 2}}, buf{data: []byte{0b10}})
	if a := mustDecode(t, seg, DType{Kind: Prim, PType: I8, Nullable: true}, 2); !a.Null(0) || a.Null(1) {
		t.Fatal("validity is wrong")
	}
	kid, b := prim(U8, []uint64{1})
	runErrCases(t, []errCase{
		{"unsigned type", segment(node(eZigZag, nil, nil, kid), b), primT(U8), 1, "cannot be a u8"},
		{"float type", segment(node(eZigZag, nil, nil, kid), b), primT(F32), 1, "cannot be a f32"},
		{"metadata", segment(node(eZigZag, []byte{1}, nil, kid), b), primT(I8), 1, "no metadata"},
		{"a buffer", segment(node(eZigZag, nil, []uint16{0}, kid), b), primT(I8), 1, "no buffer"},
		{"two children", segment(node(eZigZag, nil, nil, kid, kid), b), primT(I8), 1, "1 child"},
		{"child of the wrong length", segment(node(eZigZag, nil, nil, kid), b), primT(I8), 2, "buffer"},
	})
}

func TestSequence(t *testing.T) {
	seq := func(base, step []byte) []byte {
		meta := append(lenField(1, base), lenField(2, step)...)
		return segment(node(eSequence, meta, nil))
	}
	vals := func(p PType, a *Array, n int) []int64 {
		out := make([]int64, n)
		w := p.Width()
		for i := range out {
			var u uint64
			for k := range w {
				u |= uint64(a.Data[i*w+k]) << (8 * k)
			}
			if p.Signed() {
				bits := uint(8 * w)
				out[i] = int64(u<<(64-bits)) >> (64 - bits)
			} else {
				out[i] = int64(u)
			}
		}
		return out
	}
	// A negative multiplier.
	got := vals(I32, mustDecode(t, seq(sInt(10), sInt(-3)), primT(I32), 4), 4)
	if got[0] != 10 || got[1] != 7 || got[3] != 1 {
		t.Fatalf("got %v", got)
	}
	// Overflow wraps at the width of the type.
	got = vals(U8, mustDecode(t, seq(sUint(250), sUint(10)), primT(U8), 3), 3)
	if got[0] != 250 || got[1] != 4 || got[2] != 14 {
		t.Fatalf("got %v", got)
	}
	got = vals(I8, mustDecode(t, seq(sInt(120), sInt(10)), primT(I8), 2), 2)
	if got[1] != -126 {
		t.Fatalf("got %v", got)
	}
	// A 64-bit multiplier above the signed range wraps too.
	got = vals(U64, mustDecode(t, seq(sUint(1), sUint(1<<63)), primT(U64), 3), 3)
	if got[0] != 1 || uint64(got[1]) != 1<<63+1 || got[2] != 1 {
		t.Fatalf("got %v", got)
	}
	// A nullable sequence has no null.
	a := mustDecode(t, seq(sInt(5), sInt(1)), DType{Kind: Prim, PType: I16, Nullable: true}, 3)
	if a.Null(0) || a.Null(2) || vals(I16, a, 3)[2] != 7 {
		t.Fatalf("a = %+v", a)
	}
	// No row.
	mustDecode(t, seq(sInt(5), sInt(1)), primT(I16), 0)
	// Fields of other wire types are skipped.
	mustDecode(t, segment(node(eSequence, append([]byte{0x18, 1}, append(lenField(1, sInt(1)), lenField(2, sInt(1))...)...), nil)), primT(I8), 1)
	runErrCases(t, []errCase{
		{"float type", seq(sInt(1), sInt(1)), primT(F64), 1, "cannot be a f64"},
		{"string type", seq(sInt(1), sInt(1)), utf8t, 1, "cannot be a utf8"},
		{"a buffer", segment(node(eSequence, nil, []uint16{0}), buf{}), primT(I8), 1, "no buffer"},
		{"a child", segment(node(eSequence, nil, nil, pnode(0)), buf{}), primT(I8), 1, "no child"},
		{"no metadata", segment(node(eSequence, nil, nil)), primT(I8), 1, "needs a base and a multiplier"},
		{"no multiplier", segment(node(eSequence, lenField(1, sInt(1)), nil)), primT(I8), 1, "needs a base"},
		{"bad metadata", segment(node(eSequence, []byte{0x0a}, nil)), primT(I8), 1, "truncated"},
		{"base out of range", seq(sInt(300), sInt(1)), primT(U8), 1, "base"},
		{"base of the wrong type", seq(sStr("a"), sInt(1)), primT(U8), 1, "base"},
		{"bad base", seq([]byte{0x18}, sInt(1)), primT(U8), 1, "base"},
		{"float multiplier", seq(sInt(1), sF32(1)), primT(U8), 1, "multiplier of kind"},
		{"bad multiplier", seq(sInt(1), []byte{0x18}), primT(U8), 1, "multiplier"},
	})
}
