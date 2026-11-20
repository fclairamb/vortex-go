package array

import (
	"bytes"
	"encoding/binary"
	"math/rand/v2"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// lenField is the field num of a protobuf message, holding the message msg.
func lenField(num int, msg []byte) []byte {
	b := []byte{byte(num<<3 | 2)}
	b = binary.AppendUvarint(b, uint64(len(msg)))
	return append(b, msg...)
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed+1)) }

func primT(p PType) DType { return DType{Kind: Prim, PType: p} }

// leInts encodes the values as little-endian integers of w bytes.
func leInts(w int, vals []uint64) []byte {
	var b []byte
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint64(b, v)[:len(b)+w]
	}
	return b
}

// refPack packs the 1024 values, bit by bit, in the FastLanes block layout of words of bits
// bits: the value at position flOrder[r/8]*16 + (r%8)*128 + lane is row r of the lane, and
// the rows of a lane follow one another bit after bit, in the words of the lane.
func refPack(vals []uint64, bits, w int) []byte {
	lanes := 1024 / bits
	words := make([]uint64, lanes*w)
	for lane := range lanes {
		for row := range bits {
			v := vals[[8]int{0, 4, 2, 6, 1, 5, 3, 7}[row/8]*16+row%8*128+lane]
			for b := range w {
				if v>>b&1 == 1 {
					pos := row*w + b
					words[pos/bits*lanes+lane] |= 1 << (pos % bits)
				}
			}
		}
	}
	return leInts(bits/8, words)
}

func maskOf(w int) uint64 {
	if w == 64 {
		return ^uint64(0)
	}
	return 1<<w - 1
}

// bpCase builds the segment of a bit-packed array of the given type, w bits, whose first
// off values are filler, and whose rows are vals. Every 97th row is a patch.
type bpCase struct {
	t             DType
	w, off        int
	patches       bool
	patchesOffset int
	chunkOffsets  bool
	validity      []byte
	vals          []uint64
}

// want is the values the array decodes to: the packed low bits, but the patches.
func (c bpCase) want() []byte {
	out := make([]uint64, len(c.vals))
	for i, v := range c.vals {
		out[i] = v & maskOf(c.w)
		if c.patches && i%97 == 0 {
			out[i] = v
		}
	}
	return leInts(c.t.PType.Width(), out)
}

func (c bpCase) segment() []byte {
	bits := 8 * c.t.PType.Width()
	all := append(make([]uint64, c.off), c.vals...)
	for len(all)%1024 != 0 || len(all) == 0 {
		all = append(all, 0)
	}
	var packed []byte
	for b := 0; b < len(all); b += 1024 {
		blk := make([]uint64, 1024)
		for i := range blk {
			blk[i] = all[b+i] & maskOf(c.w)
		}
		packed = append(packed, refPack(blk, bits, c.w)...)
	}
	meta := pb(uint64(c.w), uint64(c.off))
	bufs := []buf{{data: packed}}
	var kids []fbtest.Tab
	if c.patches {
		var idx, pv []uint64
		for i := 0; i < len(c.vals); i += 97 {
			idx = append(idx, uint64(i+c.patchesOffset))
			pv = append(pv, c.vals[i])
		}
		pm := pb(uint64(len(idx)), uint64(c.patchesOffset), uint64(U32))
		bufs = append(bufs, buf{data: leInts(4, idx)}, buf{data: leInts(c.t.PType.Width(), pv)})
		kids = append(kids, pnode(1), pnode(2))
		if c.chunkOffsets {
			pm = pb(uint64(len(idx)), uint64(c.patchesOffset), uint64(U32), 1, uint64(U64), 0)
			bufs = append(bufs, buf{data: leInts(8, []uint64{0})})
			kids = append(kids, pnode(3))
		}
		meta = append(meta, lenField(3, pm)...)
	}
	if c.validity != nil {
		bufs = append(bufs, buf{data: c.validity})
		kids = append(kids, bnode(uint16(len(bufs)-1)))
	}
	return segment(node(eBitPacked, meta, []uint16{0}, kids...), bufs...)
}

func TestBitPackedEveryWidth(t *testing.T) {
	rng := newRand(1)
	for _, p := range []PType{U8, U16, U32, U64, I8, I16, I32, I64} {
		bits := 8 * p.Width()
		vals := make([]uint64, 3000)
		for i := range vals {
			vals[i] = rng.Uint64() & maskOf(bits)
		}
		for w := 0; w <= bits; w++ {
			for _, off := range []int{0, 1, 1023} {
				for _, patches := range []bool{false, true} {
					c := bpCase{t: primT(p), w: w, off: off, patches: patches, vals: vals}
					a := mustDecode(t, c.segment(), c.t, len(vals))
					if !bytes.Equal(a.Data, c.want()) {
						t.Fatalf("%s, %d bits, offset %d, patches %v: wrong values", p, w, off, patches)
					}
				}
			}
		}
	}
}

func TestBitPackedPatchesAndValidity(t *testing.T) {
	rng := newRand(2)
	vals := make([]uint64, 1500)
	for i := range vals {
		vals[i] = rng.Uint64() & 0xffff
	}
	// Patches at an offset, with chunk offsets, and a validity after them.
	valid := bytes.Repeat([]byte{0b10101010}, 1500/8+1)
	c := bpCase{t: primT(U16), w: 5, off: 3, patches: true, patchesOffset: 7, chunkOffsets: true, validity: valid, vals: vals}
	dt := DType{Kind: Prim, PType: U16, Nullable: true}
	a := mustDecode(t, c.segment(), dt, len(vals))
	if !bytes.Equal(a.Data, c.want()) || a.Null(1) || !a.Null(0) {
		t.Fatal("patches, chunk offsets and validity are wrong")
	}
	// A validity without patches.
	c = bpCase{t: primT(U16), w: 16, validity: valid, vals: vals}
	if a = mustDecode(t, c.segment(), dt, len(vals)); a.Null(1) || !a.Null(0) {
		t.Fatal("validity is wrong")
	}
	// Metadata fields of other numbers or wire types are ignored.
	seg := segment(node(eBitPacked, []byte{0x08, 0x00, 0x10, 0x00, 0x1d, 0, 0, 0, 0, 0x28, 0x01, 0x48, 0x01}, []uint16{0}), buf{data: nil})
	mustDecode(t, seg, primT(U8), 0)
}

func TestBitPackedErrors(t *testing.T) {
	good := bpCase{t: primT(U16), w: 3, vals: make([]uint64, 10)}
	packed := buf{data: make([]byte, 128*3)}
	idx := func(v ...uint64) buf { return buf{data: leInts(4, v)} }
	val := buf{data: leInts(2, []uint64{9})}
	withMeta := func(pm []byte, kids ...fbtest.Tab) []byte {
		meta := append(pb(3, 0), lenField(3, pm)...)
		return segment(node(eBitPacked, meta, []uint16{0}, kids...), packed, idx(5), val)
	}
	both := []fbtest.Tab{pnode(1), pnode(2)}
	runErrCases(t, []errCase{
		{"float", good.segment(), primT(F32), 10, "cannot be a f32"},
		{"bool", good.segment(), bl, 10, "cannot be a bool"},
		{"bit width past the type", segment(node(eBitPacked, pb(17, 0), []uint16{0}), buf{data: make([]byte, 128*17)}), primT(U16), 10, "bit width 17"},
		{"offset 1024", segment(node(eBitPacked, pb(3, 1024), []uint16{0}), packed), primT(U16), 10, "offset 1024"},
		{"packed buffer too short", segment(node(eBitPacked, pb(3, 0), []uint16{0}), buf{data: make([]byte, 128*3-1)}), primT(U16), 10, "buffer of 383 bytes, want 384"},
		{"packed buffer too short for the offset", segment(node(eBitPacked, pb(3, 1023), []uint16{0}), packed), primT(U16), 10, "want 768"},
		{"packed buffer too long", segment(node(eBitPacked, pb(3, 0), []uint16{0}), buf{data: make([]byte, 128*3+1)}), primT(U16), 10, "want 384"},
		{"no buffer", segment(node(eBitPacked, pb(0, 0), nil)), primT(U16), 0, "expected 1 buffer"},
		{"bad metadata", segment(node(eBitPacked, []byte{0x08}, []uint16{0}), packed), primT(U16), 10, "truncated"},
		{"too many children", segment(node(eBitPacked, pb(3, 0), []uint16{0}, bnode(1), bnode(1)), packed, buf{data: []byte{0xff, 0xff}}), primT(U16), 10, "0 or 1 children"},
		{"validity of the wrong length", segment(node(eBitPacked, pb(3, 0), []uint16{0}, bnode(1)), packed, buf{data: []byte{1}}), primT(U16), 10, "validity"},
		{"patch index past the end", withMeta(pb(1, 0, uint64(U32)), both...), primT(U16), 5, "patch 0 is at index 5"},
		{"patch index under the offset", withMeta(pb(1, 6, uint64(U32)), both...), primT(U16), 10, "patch 0 is at index 5, offset 6"},
		{"patches without children", withMeta(pb(1, 0, uint64(U32))), primT(U16), 10, "patches need 2 children"},
		{"patches without chunk offsets", withMeta(pb(1, 0, uint64(U32), 1, uint64(U64)), both...), primT(U16), 10, "need 3 children"},
		{"signed patch indices", withMeta(pb(1, 0, uint64(I32)), both...), primT(U16), 10, "not unsigned"},
		{"float patch indices", withMeta(pb(1, 0, uint64(F32)), both...), primT(U16), 10, "not an integer type"},
		{"float chunk offsets", withMeta(pb(1, 0, uint64(U32), 1, uint64(F32)), pnode(1), pnode(2), pnode(1)), primT(U16), 10, "chunk_offsets_ptype"},
		{"patches len too large", withMeta(pb(^uint64(0), 0, 0), both...), primT(U16), 10, "patches len"},
		{"patches offset too large", withMeta(pb(1, ^uint64(0), 0), both...), primT(U16), 10, "patches offset"},
		{"bad patches metadata", withMeta([]byte{0x08}, both...), primT(U16), 10, "truncated"},
		{"patch values of the wrong length", withMeta(pb(2, 0, uint64(U32)), both...), primT(U16), 10, "patch"},
		{"patch values of the wrong size", segment(node(eBitPacked, append(pb(3, 0), lenField(3, pb(1, 0, uint64(U32)))...), []uint16{0}, pnode(1), pnode(2)), packed, idx(5), buf{data: le32(9)}), primT(U16), 10, "patch values"},
		{"buffer out of range", segment(node(eBitPacked, pb(3, 0), []uint16{7}), packed), primT(U16), 10, "buffer 0 is buffer 7"},
		{"patch indices with a huge value", segment(node(eBitPacked, append(pb(3, 0), lenField(3, pb(1, 0, uint64(U64)))...), []uint16{0}, pnode(1), pnode(2)), packed, buf{data: leInts(8, []uint64{1 << 63})}, val), primT(U16), 10, "too large"},
	})
	// A field of the patches of another wire type is skipped.
	seg := segment(node(eBitPacked, append(pb(3, 0), lenField(3, append(pb(1, 0, uint64(U32)), 0x32, 0))...), []uint16{0}, pnode(1), pnode(2)), packed, idx(5), val)
	mustDecode(t, seg, primT(U16), 10)
	// A null patch value.
	nullVal := segment(node(eBitPacked, append(pb(3, 0), lenField(3, pb(1, 0, uint64(U32)))...), []uint16{0}, pnode(1), node(ePrim, nil, []uint16{2}, bnode(3))), packed, idx(5), val, buf{data: []byte{0}})
	wantErr(t, nullVal, DType{Kind: Prim, PType: U16, Nullable: true}, 10, "patch 0 is null")
}

func TestUnpackBlockAllocs(t *testing.T) {
	var out [blockSize]uint32
	packed := make([]byte, 128*11)
	if n := testing.AllocsPerRun(100, func() { unpackBlock(&out, packed, 11) }); n != 0 {
		t.Fatalf("unpackBlock allocates %v times", n)
	}
	if n := testing.AllocsPerRun(100, func() { unpackBlock(&out, packed, 0) }); n != 0 {
		t.Fatalf("unpackBlock of width 0 allocates %v times", n)
	}
}
