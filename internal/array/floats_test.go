package array

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

var (
	f64t  = primT(F64)
	f32t  = primT(F32)
	f64nt = DType{Kind: Prim, PType: F64, Nullable: true}
)

func leF64(v ...float64) []byte {
	var b []byte
	for _, x := range v {
		b = binary.LittleEndian.AppendUint64(b, math.Float64bits(x))
	}
	return b
}

func leU16(v ...uint16) []byte {
	var b []byte
	for _, x := range v {
		b = binary.LittleEndian.AppendUint16(b, x)
	}
	return b
}

func f64Bits(a *Array) []uint64 {
	out := make([]uint64, a.Len)
	for i := range out {
		out[i] = binary.LittleEndian.Uint64(a.Data[8*i:])
	}
	return out
}

// alpMeta is the metadata of vortex.alp, with the patches message when there are some.
func alpMeta(e, f uint64, patches []byte) []byte {
	m := pb(e, f)
	if patches != nil {
		m = append(m, lenField(3, patches)...)
	}
	return m
}

type alpVectors struct {
	F64 []alpVector `json:"f64"`
	F32 []alpVector `json:"f32"`
}

type alpVector struct {
	E   uint64   `json:"e"`
	F   uint64   `json:"f"`
	In  []int64  `json:"in"`
	Out []string `json:"out"`
}

// TestALPVectors decodes, for every pair of exponents, the integers of the reference vectors
// (tools/oracle/alpvectors.py) and compares the bits.
func TestALPVectors(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/vectors/alp.json")
	if err != nil {
		t.Fatal(err)
	}
	var v alpVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.F64) != 190 || len(v.F32) != 66 {
		t.Fatalf("%d f64 and %d f32 pairs of exponents, want 190 and 66", len(v.F64), len(v.F32))
	}
	for _, c := range v.F64 {
		in := make([]uint64, len(c.In))
		for i, x := range c.In {
			in[i] = uint64(x)
		}
		seg := segment(node(eAlp, alpMeta(c.E, c.F, nil), nil, pnode(0)), buf{data: le64(in...)})
		a := mustDecode(t, seg, f64t, len(in))
		for i, got := range f64Bits(a) {
			if want, _ := strconv.ParseUint(c.Out[i], 16, 64); got != want {
				t.Fatalf("f64 e=%d f=%d in=%d: bits %016x, want %016x", c.E, c.F, c.In[i], got, want)
			}
		}
	}
	for _, c := range v.F32 {
		in := make([]int32, len(c.In))
		for i, x := range c.In {
			in[i] = int32(x)
		}
		seg := segment(node(eAlp, alpMeta(c.E, c.F, nil), nil, node(ePrim, nil, []uint16{0})), buf{data: le32(in...)})
		a := mustDecode(t, seg, f32t, len(in))
		for i := range in {
			got := binary.LittleEndian.Uint32(a.Data[4*i:])
			if want, _ := strconv.ParseUint(c.Out[i], 16, 32); uint64(got) != want {
				t.Fatalf("f32 e=%d f=%d in=%d: bits %08x, want %08x", c.E, c.F, c.In[i], got, want)
			}
		}
	}
}

// patchMsg is a PatchesMetadata message with u32 indices.
func patchMsg(n, offset uint64) []byte { return pb(n, offset, uint64(U32)) }

func TestALPPatches(t *testing.T) {
	nanPayload := math.Float64frombits(0x7ff8_0000_dead_beef)
	snan := math.Float64frombits(0x7ff0_0000_0000_0001)
	sub := math.Float64frombits(1)
	negZero := math.Copysign(0, -1)
	patchVals := []float64{nanPayload, math.Inf(1), math.Inf(-1), negZero, sub, snan}
	idx := []uint64{0, 2, 3, 5, 6, 8}
	// Rows 1, 4 and 7 are 1.5, 2.5 and 3.5 at exponents (1, 0): encoded 15, 25 and 35.
	enc := []uint64{0, 15, 0, 0, 25, 0, 0, 35, 0}
	seg := segment(node(eAlp, alpMeta(1, 0, patchMsg(uint64(len(idx)), 0)), nil, pnode(0), pnode(1), pnode(2)),
		buf{data: le64(enc...)}, buf{data: leInts(4, idx)}, buf{data: leF64(patchVals...)})
	got := f64Bits(mustDecode(t, seg, f64t, 9))
	want := []uint64{
		math.Float64bits(nanPayload), math.Float64bits(1.5), math.Float64bits(math.Inf(1)), math.Float64bits(math.Inf(-1)),
		math.Float64bits(2.5), math.Float64bits(negZero), math.Float64bits(sub), math.Float64bits(3.5), math.Float64bits(snan),
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: bits %016x, want %016x", i, got[i], want[i])
		}
	}
	// A patch offset with chunk offsets (a fourth child, not read), and an f32.
	chunked := pb(1, 10, uint64(U32), 1, uint64(U32))
	seg = segment(node(eAlp, alpMeta(0, 0, chunked), nil, pnode(0), pnode(1), pnode(2), pnode(3)),
		buf{data: le64(4, 5)}, buf{data: leInts(4, []uint64{11})}, buf{data: leF64(-7.25)}, buf{data: leInts(4, []uint64{0})})
	if got := f64Bits(mustDecode(t, seg, f64t, 2)); got[0] != math.Float64bits(4) || got[1] != math.Float64bits(-7.25) {
		t.Fatalf("offset patch: %x", got)
	}
	seg = segment(node(eAlp, alpMeta(0, 0, patchMsg(1, 0)), nil, pnode(0), pnode(1), pnode(2)),
		buf{data: le32(1, 2)}, buf{data: leInts(4, []uint64{1})}, buf{data: binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(math.Inf(-1))))})
	a := mustDecode(t, seg, f32t, 2)
	if binary.LittleEndian.Uint32(a.Data) != math.Float32bits(1) || binary.LittleEndian.Uint32(a.Data[4:]) != math.Float32bits(float32(math.Inf(-1))) {
		t.Fatalf("f32 patch: %x", a.Data)
	}
}

func TestALPNulls(t *testing.T) {
	seg := segment(node(eAlp, alpMeta(1, 0, nil), nil, node(ePrim, nil, []uint16{0}, bnode(1))),
		buf{data: le64(15, 99, 25)}, buf{data: []byte{0b101}})
	a := mustDecode(t, seg, f64nt, 3)
	if a.Null(0) || !a.Null(1) || a.Null(2) || math.Float64frombits(f64Bits(a)[2]) != 2.5 {
		t.Fatalf("a = %+v", a)
	}
}

func TestALPErrors(t *testing.T) {
	one := func(meta []byte, kid fbtest.Tab) []byte {
		return segment(node(eAlp, meta, nil, kid), buf{data: le64(1)})
	}
	wantErr(t, one(alpMeta(19, 0, nil), pnode(0)), f64t, 1, "exponents e=19")
	wantErr(t, one(alpMeta(0, 19, nil), pnode(0)), f64t, 1, "f=19")
	wantErr(t, segment(node(eAlp, alpMeta(11, 0, nil), nil, pnode(0)), buf{data: le32(1)}), f32t, 1, "at most 10 for f32")
	wantErr(t, one(alpMeta(0, 0, nil), pnode(0)), i32, 1, "cannot be a i32")
	wantErr(t, segment(node(eAlp, alpMeta(0, 0, nil), nil), buf{}), f64t, 1, "no child")
	// A patch past the end of the array, and a null patch.
	seg := segment(node(eAlp, alpMeta(0, 0, patchMsg(1, 0)), nil, pnode(0), pnode(1), pnode(2)),
		buf{data: le64(1)}, buf{data: leInts(4, []uint64{1})}, buf{data: leF64(1)})
	wantErr(t, seg, f64t, 1, "patch 0 is at index 1")
	seg = segment(node(eAlp, alpMeta(0, 0, patchMsg(1, 0)), nil, pnode(0), pnode(1)), buf{data: le64(1)}, buf{data: leInts(4, []uint64{0})})
	wantErr(t, seg, f64t, 1, "patches need 2 children")
	seg = segment(node(eAlp, alpMeta(0, 0, patchMsg(1, 0)), nil, pnode(0), pnode(1), node(ePrim, nil, []uint16{2}, bnode(3))),
		buf{data: le64(1)}, buf{data: leInts(4, []uint64{0})}, buf{data: leF64(1)}, buf{data: []byte{0}})
	wantErr(t, seg, f64nt, 1, "patch 0 is null")
}

// alpRD is a naive ALP-RD encoder: the left rightBits-complement bits of every value, the
// four most common ones in the dictionary, the others as exceptions.
type alpRD struct {
	dict  []uint16
	left  []uint16
	right []uint64
	excIx []uint64
	excV  []uint16
}

func splitRD(vals []uint64, width, rightBits int) alpRD {
	count := map[uint16]int{}
	for _, v := range vals {
		count[uint16(v>>rightBits)]++
	}
	var r alpRD
	for len(r.dict) < 4 && len(count) > 0 {
		var best uint16
		bestN := -1
		for k, n := range count {
			if n > bestN || (n == bestN && k < best) {
				best, bestN = k, n
			}
		}
		r.dict = append(r.dict, best)
		delete(count, best)
	}
	for i, v := range vals {
		l := uint16(v >> rightBits)
		code := -1
		for c, d := range r.dict {
			if d == l {
				code = c
			}
		}
		if code < 0 {
			code = 0
			r.excIx = append(r.excIx, uint64(i))
			r.excV = append(r.excV, l)
		}
		r.left = append(r.left, uint16(code))
		r.right = append(r.right, v&(1<<rightBits-1))
	}
	return r
}

func (r alpRD) meta(rightBits uint64, withPatches bool) []byte {
	var dict []byte
	for _, d := range r.dict {
		dict = binary.AppendUvarint(dict, uint64(d))
	}
	m := pb(rightBits, uint64(len(r.dict)))
	m = append(m, lenField(3, dict)...)
	m = append(m, 4<<3, byte(U16))
	if withPatches {
		m = append(m, lenField(5, patchMsg(uint64(len(r.excIx)), 0))...)
	}
	return m
}

func TestALPRD(t *testing.T) {
	rng := newRand(42)
	const rows = 3000
	// Doubles of a narrow range of exponents, plus a few anywhere: the latter are exceptions.
	vals := make([]uint64, rows)
	for i := range vals {
		v := 1 + rng.Float64()*3
		if i%97 == 0 {
			v = math.Float64frombits(rng.Uint64())
		}
		vals[i] = math.Float64bits(v)
	}
	vals[5], vals[6], vals[7] = 0x7ff8_0000_0000_1234, 1<<63, 0x7ff0_0000_0000_0000
	r := splitRD(vals, 64, 48)
	if len(r.excIx) == 0 {
		t.Fatal("no exception")
	}
	seg := segment(node(eAlpRD, r.meta(48, true), nil, node(ePrim, nil, []uint16{0}), node(ePrim, nil, []uint16{1}), pnode(2), node(ePrim, nil, []uint16{3})),
		buf{data: leU16(r.left...)}, buf{data: le64(r.right...)}, buf{data: leInts(4, r.excIx)}, buf{data: leU16(r.excV...)})
	// The left parts child has the type left_parts_ptype: u16.
	a := mustDecode(t, seg, f64t, rows)
	for i, got := range f64Bits(a) {
		if got != vals[i] {
			t.Fatalf("row %d: bits %016x, want %016x", i, got, vals[i])
		}
	}
	// No exception, narrower values, f32.
	v32 := make([]uint64, 500)
	for i := range v32 {
		v32[i] = uint64(math.Float32bits(float32(1 + rng.Float64()*3)))
	}
	r = splitRD(v32, 32, 22)
	r32 := make([]byte, 0, len(r.right)*4)
	for _, x := range r.right {
		r32 = binary.LittleEndian.AppendUint32(r32, uint32(x))
	}
	seg = segment(node(eAlpRD, r.meta(22, false), nil, node(ePrim, nil, []uint16{0}), node(ePrim, nil, []uint16{1})),
		buf{data: leU16(r.left...)}, buf{data: r32})
	a = mustDecode(t, seg, f32t, len(v32))
	for i := range v32 {
		if got := binary.LittleEndian.Uint32(a.Data[4*i:]); uint64(got) != v32[i] {
			t.Fatalf("f32 row %d: bits %08x, want %08x", i, got, v32[i])
		}
	}
}

func TestALPRDErrors(t *testing.T) {
	r := alpRD{dict: []uint16{7, 9}, left: []uint16{0, 1}, right: []uint64{1, 2}}
	kids := func() []fbtest.Tab {
		return []fbtest.Tab{node(ePrim, nil, []uint16{0}), node(ePrim, nil, []uint16{1})}
	}
	bufs := []buf{{data: leU16(r.left...)}, {data: le64(r.right...)}}
	ok := segment(node(eAlpRD, r.meta(8, false), nil, kids()...), bufs...)
	mustDecode(t, ok, f64t, 2)
	wantErr(t, segment(node(eAlpRD, r.meta(65, false), nil, kids()...), bufs...), f64t, 2, "right_bit_width 65 is above the 64 bits of a f64")
	wantErr(t, segment(node(eAlpRD, r.meta(33, false), nil, node(ePrim, nil, []uint16{0}), node(ePrim, nil, []uint16{1})), buf{data: leU16(0, 1)}, buf{data: le32(1, 2)}), f32t, 2, "above the 32 bits of a f32")
	r.left = []uint16{0, 2}
	wantErr(t, segment(node(eAlpRD, r.meta(8, false), nil, kids()...), buf{data: leU16(r.left...)}, bufs[1]), f64t, 2, "left code 2 at row 1 is past the dictionary of 2 entries")
	// A dictionary shorter than its length, a left part type that is not an integer, a missing child.
	m := pb(8, 5)
	m = append(m, lenField(3, []byte{1, 2})...)
	wantErr(t, segment(node(eAlpRD, m, nil, kids()...), bufs...), f64t, 2, "dictionary length 5 exceeds the 2 stored")
	m = pb(8, 0, 0, uint64(F32))
	wantErr(t, segment(node(eAlpRD, m, nil, kids()...), bufs...), f64t, 2, "not an integer type")
	wantErr(t, segment(node(eAlpRD, r.meta(8, false), nil, kids()[0]), bufs[0]), f64t, 2, "at least 2 children")
	wantErr(t, segment(node(eAlpRD, r.meta(8, false), nil, kids()...), bufs...), i32, 2, "cannot be a i32")
}

func TestDecimalByteParts(t *testing.T) {
	d := DType{Kind: Decimal}
	for _, c := range []struct {
		p    PType
		data []byte
		w    int
	}{
		{I8, []byte{0x01, 0xff, 0x80}, 1},
		{I32, le32(1, -1, math.MinInt32), 4},
		{I64, le64(1, ^uint64(0), 1<<63), 8},
	} {
		seg := segment(node(eDecParts, pb(uint64(c.p), 0), nil, pnode(0)), buf{data: c.data})
		a := mustDecode(t, seg, d, 3)
		if a.Width != c.w || a.Type.Kind != Decimal || string(a.Data) != string(c.data) {
			t.Fatalf("%s: width %d data %x", c.p, a.Width, a.Data)
		}
	}
	// Nulls come from the most significant part.
	seg := segment(node(eDecParts, pb(uint64(I32), 0), nil, node(ePrim, nil, []uint16{0}, bnode(1))), buf{data: le32(5, 6)}, buf{data: []byte{0b10}})
	a := mustDecode(t, seg, DType{Kind: Decimal, Nullable: true}, 2)
	if !a.Null(0) || a.Null(1) {
		t.Fatal("wrong nulls")
	}
	// Lower parts are not written under this identifier, and not supported.
	seg = segment(node(eDecParts, pb(uint64(I32), 1), nil, pnode(0), pnode(1)), buf{data: le32(1)}, buf{data: le64(1)})
	if _, err := Decode(seg, specs, d, 1, Options{}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("lower parts: err = %v, want unsupported", err)
	}
	wantErr(t, segment(node(eDecParts, pb(uint64(F64), 0), nil, pnode(0)), buf{data: le64(1)}), d, 1, "not an integer type")
	wantErr(t, segment(node(eDecParts, pb(uint64(U32), 0), nil, pnode(0)), buf{data: le32(1)}), d, 1, "not a signed integer")
	wantErr(t, segment(node(eDecParts, pb(uint64(I32), 0), nil, pnode(0), pnode(1)), buf{data: le32(1)}, buf{data: le32(1)}), d, 1, "expected 1 child")
	wantErr(t, segment(node(eDecParts, pb(uint64(I32), 0), nil, pnode(0)), buf{data: le32(1)}), i32, 1, "cannot be a i32")
}

func tsType(ticks int32, nullable bool) DType {
	st := DType{Kind: Prim, PType: I64, Ticks: ticks, Nullable: nullable}
	return DType{Kind: Ext, Storage: &st, Nullable: nullable}
}

func datePartsSeg(days []int32, secs, subs []int32, validity []byte) []byte {
	dn := node(ePrim, nil, []uint16{0})
	bufs := []buf{{data: le32(days...)}, {data: le32(secs...)}, {data: le32(subs...)}}
	kids := []fbtest.Tab{dn, pnode(1), pnode(2)}
	if validity != nil {
		kids[0] = node(ePrim, nil, []uint16{0}, bnode(3))
		bufs = append(bufs, buf{data: validity})
	}
	p := uint64(I32)
	return segment(node(eDateParts, pb(p, p, p), nil, kids...), bufs...)
}

func TestDateTimeParts(t *testing.T) {
	times := []time.Time{
		time.Date(2024, 2, 29, 13, 45, 7, 123456789, time.UTC),
		time.Date(1969, 12, 31, 23, 59, 58, 500_000_000, time.UTC), // before 1970
		time.Date(1900, 1, 1, 0, 0, 0, 1000, time.UTC),
		time.Unix(0, 0).UTC(),
	}
	for _, u := range []struct {
		ticks int32
		name  string
	}{{1_000_000_000, "ns"}, {1_000_000, "us"}, {1_000, "ms"}, {1, "s"}} {
		var days, secs, subs []int32
		var want []int64
		for _, tm := range times {
			// Truncate to the unit: the parts hold the value in the unit.
			tk := int64(u.ticks)
			tick := tm.Unix()*tk + int64(tm.Nanosecond())/(1_000_000_000/tk)
			sec := tick / tk
			sub := tick % tk
			if sub < 0 {
				sub, sec = sub+tk, sec-1
			}
			day := sec / secondsPerDay
			rem := sec % secondsPerDay
			if rem < 0 {
				day, rem = day-1, rem+secondsPerDay
			}
			days, secs, subs = append(days, int32(day)), append(secs, int32(rem)), append(subs, int32(sub))
			want = append(want, tick)
		}
		a := mustDecode(t, datePartsSeg(days, secs, subs, nil), tsType(u.ticks, false), len(times))
		for i, got := range f64Bits(a) {
			if int64(got) != want[i] {
				t.Errorf("%s row %d: %d, want %d", u.name, i, int64(got), want[i])
			}
		}
		if a.Type.Kind != Prim || a.Type.PType != I64 {
			t.Fatalf("type %s", a.Type)
		}
	}
	// A null day makes a null row; the storage type is also accepted.
	a := mustDecode(t, datePartsSeg([]int32{1, 2}, []int32{3, 4}, []int32{5, 6}, []byte{0b01}), tsType(1000, true), 2)
	if a.Null(0) || !a.Null(1) || int64(f64Bits(a)[0]) != 86_400_000+3_000+5 {
		t.Fatalf("nullable: %+v", a)
	}
	st := *tsType(1000, false).Storage
	mustDecode(t, datePartsSeg([]int32{1}, []int32{0}, []int32{0}, nil), st, 1)
}

func TestDateTimePartsErrors(t *testing.T) {
	ok := datePartsSeg([]int32{1}, []int32{0}, []int32{0}, nil)
	wantErr(t, ok, i32, 1, "not a timestamp")
	wantErr(t, ok, primT(I64), 1, "not a timestamp")
	wantErr(t, ok, tsType(0, false), 1, "not a timestamp") // a date has no ticks
	f := uint64(F64)
	wantErr(t, segment(node(eDateParts, pb(f, uint64(I32), uint64(I32)), nil, pnode(0), pnode(1), pnode(2)), buf{data: le64(1)}, buf{data: le32(1)}, buf{data: le32(1)}), tsType(1, false), 1, "days_ptype is 10, not an integer type")
	i := uint64(I32)
	wantErr(t, segment(node(eDateParts, pb(i, i, i), nil, pnode(0), pnode(1)), buf{data: le32(1)}, buf{data: le32(1)}), tsType(1, false), 1, "expected 3 children")
	// Seconds that do not fit the i32 the reference casts them to.
	seg := segment(node(eDateParts, pb(i, uint64(I64), i), nil, pnode(0), pnode(1), pnode(2)), buf{data: le32(1)}, buf{data: le64(1 << 40)}, buf{data: le32(1)})
	wantErr(t, seg, tsType(1, false), 1, "does not fit in an i32")
}

// TestFloatEncodingsMalformed covers the metadata and the children the other tests do not
// reach.
func TestFloatEncodingsMalformed(t *testing.T) {
	bad := []byte{0x08} // a varint field with no value
	fsst := node(eFsst, nil, nil)
	rdMeta := func(extra ...[]byte) []byte {
		m := pb(8, 1)
		m = append(m, lenField(3, []byte{1})...)
		for _, e := range extra {
			m = append(m, e...)
		}
		return m
	}
	left := []byte{4 << 3, byte(U16)}
	twoKids := []fbtest.Tab{node(ePrim, nil, []uint16{0}), node(ePrim, nil, []uint16{1})}
	rdBufs := []buf{{data: leU16(0)}, {data: le64(1)}}
	patched := func(n int) []byte { return lenField(5, patchMsg(uint64(n), 0)) }
	dateMeta := func(d, s, u PType) []byte { return pb(uint64(d), uint64(s), uint64(u)) }
	cases := []struct {
		name string
		seg  []byte
		dt   DType
		want string
	}{
		{"alp metadata", segment(node(eAlp, bad, nil, pnode(0)), buf{data: le64(1)}), f64t, "truncated"},
		{"alp buffer", segment(node(eAlp, alpMeta(0, 0, nil), []uint16{1}, pnode(0)), buf{data: le64(1)}, buf{data: le64(1)}), f64t, "expected no buffer"},
		{"alp child", segment(node(eAlp, alpMeta(0, 0, nil), nil, fsst)), f64t, "unsupported encoding"},
		{"alp extra child", segment(node(eAlp, alpMeta(0, 0, nil), nil, pnode(0), pnode(1)), buf{data: le64(1)}, buf{data: le64(1)}), f64t, "expected 1 child without patches"},
		{"alp patches", segment(node(eAlp, alpMeta(0, 0, bad), nil, pnode(0)), buf{data: le64(1)}), f64t, "truncated"},
		{"alprd metadata", segment(node(eAlpRD, bad, nil, twoKids...), rdBufs...), f64t, "truncated"},
		{"alprd dictionary", segment(node(eAlpRD, rdMeta(lenField(3, []byte{0x80})), nil, twoKids...), rdBufs...), f64t, "dictionary"},
		{"alprd code", segment(node(eAlpRD, append(append(pb(8, 1), lenField(3, []byte{0x80, 0x80, 0x08})...), left...), nil, twoKids...), rdBufs...), f64t, "does not fit in 16 bits"},
		{"alprd width", segment(node(eAlpRD, pb(256), nil, twoKids...), rdBufs...), f64t, "right_bit_width 256 is out of range"},
		{"alprd signed", segment(node(eAlpRD, append(pb(8, 0), 4<<3, byte(I32)), nil, twoKids...), rdBufs...), f64t, "not unsigned"},
		{"alprd left child", segment(node(eAlpRD, rdMeta(left), nil, fsst, twoKids[1]), rdBufs...), f64t, "left parts: "},
		{"alprd right child", segment(node(eAlpRD, rdMeta(left), nil, twoKids[0], fsst), rdBufs...), f64t, "right parts: "},
		{"alprd left too large", segment(node(eAlpRD, rdMeta([]byte{4 << 3, byte(U64)}), nil, twoKids...), buf{data: le64(1 << 63)}, rdBufs[1]), f64t, "too large"},
		{"alprd patches", segment(node(eAlpRD, rdMeta(left, lenField(5, bad)), nil, twoKids...), rdBufs...), f64t, "truncated"},
		{"alprd patch children", segment(node(eAlpRD, rdMeta(left, patched(1)), nil, twoKids...), rdBufs...), f64t, "patches need 2 children"},
		{"alprd patch values", segment(node(eAlpRD, rdMeta([]byte{4 << 3, byte(U64)}, patched(1)), nil, twoKids[0], twoKids[1], pnode(2), pnode(3)),
			buf{data: le64(0)}, rdBufs[1], buf{data: leInts(4, []uint64{0})}, buf{data: le64(1 << 63)}), f64t, "too large"},
		{"alprd extra child", segment(node(eAlpRD, rdMeta(left), nil, twoKids[0], twoKids[1], pnode(0)), rdBufs...), f64t, "expected 2 children without patches"},
		{"decimal metadata", segment(node(eDecParts, bad, nil, pnode(0)), buf{data: le32(1)}), dec, "truncated"},
		{"decimal buffer", segment(node(eDecParts, pb(uint64(I32), 0), []uint16{1}, pnode(0)), buf{data: le32(1)}, buf{data: le32(1)}), dec, "expected no buffer"},
		{"decimal child", segment(node(eDecParts, pb(uint64(I32), 0), nil, fsst)), dec, "most significant part: "},
		{"datetime metadata", segment(node(eDateParts, bad, nil, pnode(0), pnode(1), pnode(2))), tsType(1, false), "truncated"},
		{"datetime days", segment(node(eDateParts, dateMeta(I32, I32, I32), nil, fsst, pnode(1), pnode(2)), buf{}, buf{data: le32(1)}, buf{data: le32(1)}), tsType(1, false), "days: "},
		{"datetime seconds", segment(node(eDateParts, dateMeta(I32, I32, I32), nil, pnode(0), fsst, pnode(2)), buf{data: le32(1)}), tsType(1, false), "seconds: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.seg, c.dt, 1, c.want) })
	}
	// An unpacked dictionary entry, as a writer that does not pack would send it.
	seg := segment(node(eAlpRD, append(pb(8, 1, 7), left...), nil, twoKids...), rdBufs...)
	if got := f64Bits(mustDecode(t, seg, f64t, 1))[0]; got != 7<<8|1 {
		t.Fatalf("unpacked dictionary: %x", got)
	}
	// Parts of one and two bytes, signed and unsigned.
	seg = segment(node(eDateParts, dateMeta(U8, I16, I8), nil, pnode(0), pnode(1), pnode(2)),
		buf{data: []byte{200}}, buf{data: []byte{0xff, 0xff}}, buf{data: []byte{0xfe}})
	a := mustDecode(t, seg, tsType(1, false), 1)
	if got := int64(f64Bits(a)[0]); got != 200*86400-1-2 {
		t.Fatalf("narrow parts: %d", got)
	}
}
