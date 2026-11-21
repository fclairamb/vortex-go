package array

import (
	"bytes"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// rleEncode is the naive RLE encoder of FastLanes: per block of 1024 values, the values of
// its runs, and for each value the number of its run in the block.
func rleEncode(logical []uint64) (values, indices, offsets []uint64) {
	for b := 0; b < len(logical); b += 1024 {
		offsets = append(offsets, uint64(len(values)))
		run := -1
		for i := b; i < b+1024; i++ {
			if i == b || logical[i] != logical[i-1] {
				values = append(values, logical[i])
				run++
			}
			indices = append(indices, uint64(run))
		}
	}
	return values, indices, offsets
}

func rleSegment(values, indices, offsets []uint64, ip PType, offset int, validity []byte) []byte {
	meta := pb(uint64(len(values)), uint64(len(indices)), uint64(ip), uint64(len(offsets)), uint64(U64), uint64(offset))
	bufs := []buf{{data: leInts(4, values)}, {data: leInts(ip.Width(), indices)}, {data: leInts(8, offsets)}}
	idxNode := pnode(1)
	if validity != nil {
		bufs = append(bufs, buf{data: validity})
		idxNode = node(ePrim, nil, []uint16{1}, bnode(3))
	}
	return segment(node(eRLE, meta, nil, pnode(0), idxNode, pnode(2)), bufs...)
}

func TestRLE(t *testing.T) {
	rng := newRand(5)
	logical := make([]uint64, 3*1024)
	for i := 0; i < len(logical); {
		v := rng.Uint64() & 0xffffffff
		for n := 1 + rng.IntN(30); n > 0 && i < len(logical); n-- {
			logical[i] = v
			i++
		}
	}
	values, indices, offsets := rleEncode(logical)
	for _, ip := range []PType{U8, U16} {
		for _, c := range [][2]int{{0, 3072}, {5, 2000}, {1030, 1500}, {2048, 1024}, {100, 0}} {
			off, rows := c[0], c[1]
			a := mustDecode(t, rleSegment(values, indices, offsets, ip, off, nil), i32, rows)
			if !bytes.Equal(a.Data, leInts(4, logical[off:off+rows])) || a.Validity != nil {
				t.Fatalf("%s, offset %d, %d rows: wrong values", ip, off, rows)
			}
		}
	}
	// The indices have the validity. A null row has any index.
	valid := make([]byte, 3072/8)
	for i := range valid {
		valid[i] = byte(rng.Uint32())
	}
	badIdx := append([]uint64(nil), indices...)
	badIdx[7] = 200
	valid[0] &^= 1 << 7
	a := mustDecode(t, rleSegment(values, badIdx, offsets, U8, 3, valid), i32n, 2000)
	for i := range 2000 {
		valid := valid[(i+3)/8]>>((i+3)%8)&1 == 1
		if a.Null(i) == valid || valid && !bytes.Equal(a.Data[4*i:4*i+4], leInts(4, logical[i+3:i+4])) {
			t.Fatalf("row %d is wrong", i)
		}
	}
	// A block of one value, and an array of a single block.
	one := rleSegment([]uint64{9}, make([]uint64, 1024), []uint64{0}, U8, 0, nil)
	if a = mustDecode(t, one, i32, 1024); a.Data[4*1023] != 9 {
		t.Fatal("block of one value is wrong")
	}

	good := func(meta []byte) []byte {
		return segment(node(eRLE, meta, nil, pnode(0), pnode(1), pnode(2)), buf{data: le32(7)}, buf{data: make([]byte, 1024)}, buf{data: le64(0)})
	}
	rep := func(vals ...uint64) []byte { return pb(vals...) }
	runErrCases(t, []errCase{
		{"wrong type", good(rep(1, 1024, 0, 1, 3, 0)), utf8t, 1, "cannot be a utf8"},
		{"bad metadata", good([]byte{0x08}), i32, 1, "truncated"},
		{"values_len too large", good(rep(^uint64(0), 1024, 0, 1, 3, 0)), i32, 1, "values_len"},
		{"indices_len too large", good(rep(1, ^uint64(0), 0, 1, 3, 0)), i32, 1, "indices_len"},
		{"indices_ptype float", good(rep(1, 1024, uint64(F32), 1, 3, 0)), i32, 1, "indices_ptype"},
		{"indices_ptype i32", good(rep(1, 1024, uint64(I32), 1, 3, 0)), i32, 1, "not u8 or u16"},
		{"values_idx_offsets_len too large", good(rep(1, 1024, 0, ^uint64(0), 3, 0)), i32, 1, "values_idx_offsets_len"},
		{"values_idx_offsets_ptype float", good(rep(1, 1024, 0, 1, uint64(F32), 0)), i32, 1, "values_idx_offsets_ptype"},
		{"offset too large", good(rep(1, 1024, 0, 1, 3, ^uint64(0))), i32, 1, "offset"},
		{"a buffer", segment(node(eRLE, rep(1, 1024, 0, 1, 3, 0), []uint16{0}, pnode(0), pnode(1), pnode(2)), buf{data: le32(7)}, buf{data: make([]byte, 1024)}, buf{data: le64(0)}), i32, 1, "no buffer and 3 children"},
		{"two children", segment(node(eRLE, rep(1, 1024, 0, 1, 3, 0), nil, pnode(0), pnode(1)), buf{data: le32(7)}, buf{data: make([]byte, 1024)}), i32, 1, "no buffer and 3 children"},
		{"indices not in blocks", good(rep(1, 1000, 0, 1, 3, 0)), i32, 1, "blocks of 1024"},
		{"offset past the indices", good(rep(1, 1024, 0, 1, 3, 2000)), i32, 1, "blocks of 1024"},
		{"too many rows", good(rep(1, 1024, 0, 1, 3, 10)), i32, 1020, "blocks of 1024"},
		{"offsets for another number of blocks", good(rep(1, 1024, 0, 2, 3, 0)), i32, 1, "2 offsets"},
		{"more values than indices", good(rep(2000, 1024, 0, 1, 3, 0)), i32, 1, "2000 values"},
		{"values of the wrong length", good(rep(2, 1024, 0, 1, 3, 0)), i32, 1, "values"},
		{"indices of the wrong length", good(rep(1, 2048, 0, 2, 3, 0)), i32, 1, "indices"},
		{"offsets of the wrong length", good(rep(1, 1024, 0, 1, 2, 0)), i32, 1, "values index offsets"},
	})
	two := func(offs []uint64, nvals int) []byte {
		meta := rep(uint64(nvals), 2048, 0, 2, 3, 0)
		return segment(node(eRLE, meta, nil, pnode(0), pnode(1), pnode(2)), buf{data: leInts(4, make([]uint64, nvals))}, buf{data: make([]byte, 2048)}, buf{data: leInts(8, offs)})
	}
	runErrCases(t, []errCase{
		{"offsets decrease", two([]uint64{3, 1}, 5), i32, 2048, "decrease"},
		{"offsets span too many values", two([]uint64{0, 9}, 5), i32, 2048, "span 9 values"},
		{"a block without value", two([]uint64{0, 0}, 0), i32, 2048, "block 0 references no values"},
		{"index past the values", segment(node(eRLE, rep(2, 1024, 0, 1, 3, 0), nil, pnode(0), pnode(1), pnode(2)), buf{data: le32(1, 2)}, buf{data: append([]byte{5}, make([]byte, 1023)...)}, buf{data: le64(0)}), i32, 1024, "past the 2 values"},
	})
}

// runs is random run lengths and values: the ends of the runs, their values, and the values
// of the rows they make.
func runs(rng interface{ IntN(int) int }, n int) (ends, vals, rows []uint64) {
	end := 0
	for range n {
		end += 1 + rng.IntN(5)
		v := uint64(rng.IntN(1000))
		for len(rows) < end {
			rows = append(rows, v)
		}
		ends, vals = append(ends, uint64(end)), append(vals, v)
	}
	return ends, vals, rows
}

func runEndSegment(ends, vals []uint64, ep PType, offset int) []byte {
	return segment(node(eRunEnd, pb(uint64(ep), uint64(len(ends)), uint64(offset)), nil, pnode(0), pnode(1)),
		buf{data: leInts(ep.Width(), ends)}, buf{data: leInts(4, vals)})
}

func TestRunEnd(t *testing.T) {
	rng := newRand(6)
	ends, vals, rows := runs(rng, 300)
	for _, ep := range []PType{U16, U32, U64, I32} {
		for _, c := range [][2]int{{0, len(rows)}, {1, 100}, {7, len(rows) - 7}, {len(rows) - 1, 1}} {
			a := mustDecode(t, runEndSegment(ends, vals, ep, c[0]), i32, c[1])
			if !bytes.Equal(a.Data, leInts(4, rows[c[0]:c[0]+c[1]])) {
				t.Fatalf("%s, offset %d, %d rows: wrong values", ep, c[0], c[1])
			}
		}
	}
	// No row, with or without run.
	mustDecode(t, runEndSegment(ends, vals, U32, 3), i32, 0)
	mustDecode(t, runEndSegment(nil, nil, U32, 0), i32, 0)
	// Booleans, with a null run.
	seg := segment(node(eRunEnd, pb(uint64(U8), 3, 0), nil, pnode(0), node(eBool, nil, []uint16{1}, bnode(2))),
		buf{data: []byte{2, 3, 6}}, buf{data: []byte{0b101}}, buf{data: []byte{0b011}})
	a := mustDecode(t, seg, DType{Kind: Bool, Nullable: true}, 6)
	for i, want := range []struct{ null, v bool }{{false, true}, {false, true}, {false, false}, {true, false}, {true, false}, {true, false}} {
		if a.Null(i) != want.null || (!want.null && a.Bits.Get(i) != want.v) {
			t.Fatalf("row %d is wrong", i)
		}
	}
	// A leading run that ends before the offset is skipped, and zero-length runs are allowed
	// only first.
	mustDecode(t, runEndSegment([]uint64{0, 4}, []uint64{1, 2}, U32, 0), i32, 4)
	good := func(ends []uint64) []byte { return runEndSegment(ends, make([]uint64, len(ends)), U32, 0) }
	runErrCases(t, []errCase{
		{"bad metadata", segment(node(eRunEnd, []byte{0x08}, nil)), i32, 1, "truncated"},
		{"ends_ptype float", segment(node(eRunEnd, pb(uint64(F32), 1, 0), nil)), i32, 1, "ends_ptype"},
		{"num_runs too large", segment(node(eRunEnd, pb(0, ^uint64(0), 0), nil)), i32, 1, "num_runs"},
		{"offset too large", segment(node(eRunEnd, pb(0, 1, ^uint64(0)), nil)), i32, 1, "offset"},
		{"a buffer", segment(node(eRunEnd, pb(uint64(U32), 1, 0), []uint16{0}, pnode(0), pnode(0)), buf{data: le32(1)}), i32, 1, "no buffer and 2 children"},
		{"one child", segment(node(eRunEnd, pb(uint64(U32), 1, 0), nil, pnode(0)), buf{data: le32(1)}), i32, 1, "no buffer and 2 children"},
		{"ends of the wrong length", segment(node(eRunEnd, pb(uint64(U32), 2, 0), nil, pnode(0), pnode(1)), buf{data: le32(1)}, buf{data: le32(1, 2)}), i32, 1, "run ends"},
		{"values of the wrong length", segment(node(eRunEnd, pb(uint64(U32), 2, 0), nil, pnode(0), pnode(1)), buf{data: le32(1, 2)}, buf{data: le32(1)}), i32, 1, "values"},
		{"decreasing ends", good([]uint64{3, 2}), i32, 2, "run end 1 is 2, after 3"},
		{"repeated ends", good([]uint64{3, 3}), i32, 2, "run end 1 is 3, after 3"},
		{"negative ends", segment(node(eRunEnd, pb(uint64(I32), 1, 0), nil, pnode(0), pnode(1)), buf{data: le32(-1)}, buf{data: le32(1)}), i32, 1, "negative"},
		{"last end below the length", good([]uint64{1, 2}), i32, 3, "end before row 3"},
		{"last end below the length with an offset", runEndSegment([]uint64{1, 2}, []uint64{1, 2}, U32, 1), i32, 2, "end before row 3"},
		{"no run", good(nil), i32, 1, "end before row 1"},
	})
}

func TestDict(t *testing.T) {
	rng := newRand(7)
	pool := make([]uint64, 50)
	for i := range pool {
		pool[i] = rng.Uint64() & 0xffffffff
	}
	codes := make([]uint64, 3000)
	want := make([]uint64, len(codes))
	for i := range codes {
		codes[i] = uint64(rng.IntN(len(pool)))
		want[i] = pool[codes[i]]
	}
	for _, cp := range []PType{U8, U16, U32, U64, I16} {
		seg := segment(node(eDict, pb(uint64(len(pool)), uint64(cp)), nil, pnode(0), pnode(1)), buf{data: leInts(cp.Width(), codes)}, buf{data: leInts(4, pool)})
		if a := mustDecode(t, seg, i32, len(codes)); !bytes.Equal(a.Data, leInts(4, want)) {
			t.Fatalf("%s: wrong values", cp)
		}
	}
	// Nullable codes: a null code is a null row, whatever it holds, even with no value; the
	// codes are nullable because the flag says so, not the type.
	valid := []byte{0b0101}
	seg := segment(node(eDict, pb(2, uint64(U8), 1), nil, node(ePrim, nil, []uint16{0}, bnode(2)), pnode(1)), buf{data: []byte{1, 255, 0, 255}}, buf{data: le32(10, 20)}, buf{data: valid})
	a := mustDecode(t, seg, i32n, 4)
	if a.Null(0) || !a.Null(1) || a.Null(2) || !a.Null(3) || i32s(t, a)[0] != 20 || i32s(t, a)[2] != 10 {
		t.Fatalf("a = %+v", a)
	}
	seg = segment(node(eDict, pb(0, uint64(U8), 1), nil, node(ePrim, nil, []uint16{0}, bnode(1)), pnode(2)), buf{data: []byte{7, 7}}, buf{data: []byte{0}}, buf{})
	if a = mustDecode(t, seg, i32n, 2); !a.Null(0) || !a.Null(1) {
		t.Fatal("codes of an empty dictionary are all null")
	}
	// Nullable values, with non-null codes: the nullability is the type's.
	seg = segment(node(eDict, pb(2, uint64(U8)), nil, pnode(0), node(ePrim, nil, []uint16{1}, bnode(2))), buf{data: []byte{1, 0, 1}}, buf{data: le32(10, 20)}, buf{data: []byte{0b10}})
	if a = mustDecode(t, seg, i32n, 3); a.Null(0) || !a.Null(1) || a.Null(2) {
		t.Fatalf("a = %+v", a)
	}
	// Strings: the values are decoded once, the rows share their bytes.
	words := []string{"alpha", "", "gamma"}
	data := []byte("alphagamma")
	seg = segment(node(eDict, pb(3, uint64(U8)), nil, pnode(0), node(eVarBin, pb(uint64(U32)), []uint16{1}, pnode(2))),
		buf{data: []byte{2, 0, 1, 2}}, buf{data: data}, buf{data: le32(0, 5, 5, 10)})
	a = mustDecode(t, seg, utf8t, 4)
	for i, c := range []int{2, 0, 1, 2} {
		if str(a, i) != words[c] {
			t.Fatalf("row %d is %q", i, str(a, i))
		}
	}
	// Extension types are decoded as their storage.
	ext := DType{Kind: Ext, Storage: &i32}
	seg = segment(node(eDict, pb(1, uint64(U8)), nil, pnode(0), node(eExt, nil, nil, pnode(1))), buf{data: []byte{0, 0}}, buf{data: le32(5)})
	if a = mustDecode(t, seg, ext, 2); i32s(t, a)[1] != 5 {
		t.Fatalf("a = %+v", a)
	}
	good := func(meta []byte, codes []byte) []byte {
		return segment(node(eDict, meta, nil, pnode(0), pnode(1)), buf{data: codes}, buf{data: le32(10, 20)})
	}
	runErrCases(t, []errCase{
		{"bad metadata", good([]byte{0x08}, []byte{0}), i32, 1, "truncated"},
		{"values_len too large", good(pb(^uint64(0), 0), []byte{0}), i32, 1, "values_len"},
		{"values_len with a field of another wire type", good(append([]byte{0x22, 0}, pb(2, 0)...), []byte{0}), i32, 2, "codes"},
		{"codes_ptype float", good(pb(2, uint64(F32)), []byte{0}), i32, 1, "codes_ptype"},
		{"a buffer", segment(node(eDict, pb(2, 0), []uint16{0}, pnode(0), pnode(1)), buf{data: []byte{0}}, buf{data: le32(10, 20)}), i32, 1, "no buffer and 2 children"},
		{"one child", segment(node(eDict, pb(2, 0), nil, pnode(0)), buf{data: []byte{0}}), i32, 1, "no buffer and 2 children"},
		{"codes of the wrong length", good(pb(2, 0), []byte{0}), i32, 2, "codes"},
		{"values of the wrong length", good(pb(3, 0), []byte{0}), i32, 1, "values"},
		{"code past the dictionary", good(pb(2, 0), []byte{0, 2}), i32, 2, "code 2 at row 1 is past the 2 values"},
		{"negative code", good(pb(2, uint64(I8)), []byte{0xff}), i32, 1, "negative"},
	})
}

// sparseSegment is a sparse array: the fill scalar is buffer 0, the indices buffer 1, the
// values child over the buffers that follow.
func sparseSegment(fill []byte, offset uint64, idx []uint64, values fbtest.Tab, bufs ...buf) []byte {
	meta := lenField(1, pb(uint64(len(idx)), offset, uint64(U32)))
	return segment(node(eSparse, meta, []uint16{0}, pnode(1), values), append([]buf{{data: fill}, {data: leInts(4, idx)}}, bufs...)...)
}

func TestSparse(t *testing.T) {
	rng := newRand(8)
	for _, offset := range []uint64{0, 10} {
		rows := 1000
		fill := rng.Uint64() & 0xffffffff
		want := make([]uint64, rows)
		for i := range want {
			want[i] = fill
		}
		var idx, vals []uint64
		for i := rng.IntN(20); i < rows; i += 1 + rng.IntN(40) {
			idx = append(idx, uint64(i)+offset)
			vals = append(vals, rng.Uint64()&0xffffffff)
			want[i] = vals[len(vals)-1]
		}
		seg := sparseSegment(sUint(fill), offset, idx, pnode(2), buf{data: leInts(4, vals)})
		if a := mustDecode(t, seg, primT(U32), rows); !bytes.Equal(a.Data, leInts(4, want)) {
			t.Fatalf("offset %d: wrong values", offset)
		}
	}
	// A patch listed twice: the last one wins. No patch at all.
	seg := sparseSegment(sInt(1), 0, []uint64{2, 2}, pnode(2), buf{data: le32(5, 6)})
	if got := i32s(t, mustDecode(t, seg, i32, 4)); got[0] != 1 || got[2] != 6 {
		t.Fatalf("got %v", got)
	}
	seg = sparseSegment(sInt(1), 0, nil, pnode(2), buf{})
	if got := i32s(t, mustDecode(t, seg, i32, 2)); got[0] != 1 || got[1] != 1 {
		t.Fatalf("got %v", got)
	}
	// A null fill, in a nullable type, with a null patch.
	seg = sparseSegment(sNull(), 0, []uint64{1, 3}, node(ePrim, nil, []uint16{2}, bnode(3)), buf{data: le32(5, 6)}, buf{data: []byte{0b01}})
	a := mustDecode(t, seg, i32n, 4)
	if !a.Null(0) || a.Null(1) || !a.Null(2) || !a.Null(3) || i32s(t, a)[1] != 5 {
		t.Fatalf("a = %+v", a)
	}
	// A fill in a nullable type, patched with values.
	seg = sparseSegment(sInt(9), 0, []uint64{1}, pnode(2), buf{data: le32(5)})
	if a = mustDecode(t, seg, i32n, 2); a.Null(0) || a.Null(1) {
		t.Fatal("no row is null")
	}
	// Strings and booleans.
	seg = sparseSegment(sStr("x"), 0, []uint64{1}, node(eVarBin, pb(uint64(U32)), []uint16{2}, pnode(3)), buf{data: []byte("yz")}, buf{data: le32(0, 2)})
	if a = mustDecode(t, seg, utf8t, 3); str(a, 0) != "x" || str(a, 1) != "yz" || str(a, 2) != "x" {
		t.Fatalf("strings = %q %q %q", str(a, 0), str(a, 1), str(a, 2))
	}
	seg = sparseSegment(sBool(true), 0, []uint64{0}, bnode(2), buf{data: []byte{0}})
	if a = mustDecode(t, seg, bl, 2); a.Bits.Get(0) || !a.Bits.Get(1) {
		t.Fatal("booleans are wrong")
	}
	// Metadata fields of other numbers or wire types are skipped, and chunk offsets are not read.
	meta := append([]byte{0x10, 1}, lenField(1, pb(1, 0, uint64(U32), 1, uint64(U64)))...)
	seg = segment(node(eSparse, meta, []uint16{0}, pnode(1), pnode(2)), buf{data: sInt(1)}, buf{data: le32(0)}, buf{data: le32(5)})
	mustDecode(t, seg, i32, 1)

	good := func(meta []byte, fill []byte) []byte {
		return segment(node(eSparse, meta, []uint16{0}, pnode(1), pnode(2)), buf{data: fill}, buf{data: le32(1)}, buf{data: le32(5)})
	}
	pm := lenField(1, pb(1, 0, uint64(U32)))
	runErrCases(t, []errCase{
		{"bad metadata", good([]byte{0x0a}, sInt(1)), i32, 2, "truncated"},
		{"no patches", good(nil, sInt(1)), i32, 2, "no patches"},
		{"bad patches", good(lenField(1, []byte{0x08}), sInt(1)), i32, 2, "truncated"},
		{"no buffer", segment(node(eSparse, pm, nil, pnode(0), pnode(0)), buf{data: le32(1)}), i32, 2, "1 buffer and 2 children"},
		{"one child", segment(node(eSparse, pm, []uint16{0}, pnode(1)), buf{data: sInt(1)}, buf{data: le32(1)}), i32, 2, "1 buffer and 2 children"},
		{"buffer out of range", segment(node(eSparse, pm, []uint16{9}, pnode(1), pnode(2)), buf{data: sInt(1)}, buf{data: le32(1)}, buf{data: le32(5)}), i32, 2, "buffer 0 is buffer 9"},
		{"bad fill", good(pm, []byte{0x18}), i32, 2, "fill value"},
		{"fill of the wrong kind", good(pm, sStr("a")), i32, 2, "fill value"},
		{"null fill in a type with no null", good(pm, sNull()), i32, 2, "fill value"},
		{"unsupported fill type", good(pm, sInt(1)), structOf(false, i32), 2, "fill value"},
		{"patch past the end", good(pm, sInt(1)), i32, 1, "patch 0 is at index 1"},
		{"patch values of the wrong length", good(lenField(1, pb(2, 0, uint64(U32))), sInt(1)), i32, 4, "patch"},
	})
}

func TestTake(t *testing.T) {
	cnst := func(dt DType, msg []byte, rows int) *Array { return mustDecode(t, constSeg(msg), dt, rows) }
	// Booleans, numbers and decimals; -1 is a null row.
	b := take(nil, cnst(bl, sBool(true), 3), []int{0, -1, 2})
	if !b.Bits.Get(0) || b.Bits.Get(1) || !b.Null(1) || b.Null(0) {
		t.Fatalf("bools = %+v", b)
	}
	if n := take(nil, cnst(i32, sInt(7), 2), []int{1, 0}); n.Validity != nil || i32s(t, n)[1] != 7 {
		t.Fatalf("numbers = %+v", n)
	}
	n := take(nil, cnst(i32n, sInt(7), 2), []int{-1, 1})
	if got := i32s(t, n); got[0] != 0 || got[1] != 7 || !n.Null(0) {
		t.Fatalf("numbers = %+v", n)
	}
	d := take(nil, cnst(dec, sBytes([]byte{5, 0}), 2), []int{1, -1})
	if d.Width != 2 || d.Data[0] != 5 || d.Data[2] != 0 {
		t.Fatalf("decimals = %+v", d)
	}
	// Strings and lists share the bytes and the elements.
	s := take(nil, cnst(utf8n, sStr("ab"), 2), []int{1, -1, 0})
	if str(s, 0) != "ab" || str(s, 1) != "" || !s.Null(1) || str(s, 2) != "ab" {
		t.Fatalf("strings = %+v", s)
	}
	l := take(nil, cnst(i32l, sList(sInt(1), sInt(2)), 2), []int{1, 0})
	if l.Starts[0] != 2 || l.Ends[1] != 2 || l.Elems.Len != 4 {
		t.Fatalf("lists = %+v", l)
	}
	// Fixed size lists take their elements.
	f := take(nil, cnst(i32fs2, sList(sInt(1), sInt(2)), 2), []int{1, 0, 1})
	if f.Len != 3 || f.Elems.Len != 6 || i32s(t, f.Elems)[5] != 2 {
		t.Fatalf("fixed size lists = %+v", f)
	}
	f = take(nil, cnst(DType{Kind: FixedSizeList, Elem: &i32, Size: 2, Nullable: true}, sList(sInt(1), sInt(2)), 1), []int{-1})
	if f.Elems.Len != 2 || !f.Null(0) {
		t.Fatalf("a null fixed size list = %+v", f)
	}
	// Structs take every field.
	st := &Array{Type: structOf(false, i32, utf8t), Len: 2, Fields: []*Array{cnst(i32, sInt(3), 2), cnst(utf8t, sStr("q"), 2)}}
	if g := take(nil, st, []int{1}); g.Len != 1 || i32s(t, g.Fields[0])[0] != 3 || str(g.Fields[1], 0) != "q" {
		t.Fatalf("struct = %+v", g)
	}
	// A skipped array stays skipped.
	if sk := take(nil, &Array{Type: i32, Len: 2, Skipped: errors2}, []int{0}); sk.Skipped == nil || sk.Len != 1 {
		t.Fatalf("skipped = %+v", sk)
	}
	// Nulls are nulls.
	if nl := take(nil, &Array{Type: nul, Len: 2}, []int{0, 1, -1}); nl.Len != 3 || !nl.Null(2) {
		t.Fatalf("nulls = %+v", nl)
	}
}
