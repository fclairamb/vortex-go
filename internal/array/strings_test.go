package array

import (
	"bytes"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// fsstSymbols are symbols of every length from 1 to 8.
var fsstSymbols = []string{"a", "bb", "ccc", "dddd", "eeeee", "ffffff", "ggggggg", "hhhhhhhh"}

// symbolBuffers makes the symbols buffer (little-endian u64) and the lengths one.
func symbolBuffers(syms []string) (symBuf, lenBuf []byte) {
	for _, s := range syms {
		var w [8]byte
		copy(w[:], s)
		symBuf = append(symBuf, w[:]...)
		lenBuf = append(lenBuf, byte(len(s)))
	}
	return symBuf, lenBuf
}

// fsstEncode is a naive FSST encoder: longest matching symbol, escape for the rest.
func fsstEncode(syms []string, s string) []byte {
	var out []byte
	for len(s) > 0 {
		best := -1
		for i, sym := range syms {
			if strings.HasPrefix(s, sym) && (best < 0 || len(sym) > len(syms[best])) {
				best = i
			}
		}
		if best < 0 {
			out = append(out, fsstEscape, s[0])
			s = s[1:]
			continue
		}
		out = append(out, byte(best))
		s = s[len(syms[best]):]
	}
	return out
}

// fsstIn is the parts of an FSST array in its current form.
type fsstIn struct {
	symBuf, lenBuf, codes []byte
	ulen, offs            []int64
	valid                 []byte
	meta                  []byte
	nullLens              bool // the lengths have a validity, which they must not
}

func newFSSTIn(syms []string, strs []string) fsstIn {
	in := fsstIn{meta: pb(uint64(U32), uint64(U32)), offs: []int64{0}}
	in.symBuf, in.lenBuf = symbolBuffers(syms)
	for _, s := range strs {
		in.codes = append(in.codes, fsstEncode(syms, s)...)
		in.ulen = append(in.ulen, int64(len(s)))
		in.offs = append(in.offs, int64(len(in.codes)))
	}
	return in
}

func (in fsstIn) bufs() []buf {
	return []buf{{data: in.symBuf}, {data: in.lenBuf}, {data: in.codes}, {data: ints(4, in.ulen...)}, {data: ints(4, in.offs...)}, {data: in.valid}}
}

func (in fsstIn) seg() []byte {
	kids := []fbtest.Tab{pnode(3), pnode(4)}
	if in.nullLens {
		kids[0] = node(ePrim, nil, []uint16{3}, bnode(1))
	}
	if in.valid != nil {
		kids = append(kids, bnode(5))
	}
	return segment(node(eFsst, in.meta, []uint16{0, 1, 2}, kids...), in.bufs()...)
}

// legacyBufs is bufs in the order of the older form: symbols, lengths, code bytes, code
// offsets, uncompressed lengths, validity.
func (in fsstIn) legacyBufs() []buf {
	b := in.bufs()
	b[3], b[4] = buf{data: ints(4, in.offs...)}, buf{data: ints(4, in.ulen...)}
	return b
}

// legacySeg is the older form: 2 buffers, the codes as a varbin child.
func (in fsstIn) legacySeg() []byte {
	codes := []fbtest.Tab{pnode(3)}
	if in.valid != nil {
		codes = append(codes, bnode(5))
	}
	b := in.legacyBufs()
	return segment(node(eFsst, in.meta, []uint16{0, 1},
		node(eVarBin, pb(uint64(U32)), []uint16{2}, codes...), pnode(4)), b...)
}

func checkStrings(t *testing.T, a *Array, want []string) {
	t.Helper()
	if a.Len != len(want) {
		t.Fatalf("len = %d, want %d", a.Len, len(want))
	}
	for i, w := range want {
		if got := str(a, i); got != w {
			t.Errorf("row %d = %q, want %q", i, got, w)
		}
	}
}

func TestFSST(t *testing.T) {
	long := strings.Repeat("hhhhhhhh", 3)
	strs := []string{
		"", "a", "bb", "ccc", "dddd", "eeeee", "ffffff", "ggggggg", "hhhhhhhh", // each length
		"\x00\xff\x01xyz", // escapes only
		"abbcccddddeeeeeffffffggggggghhhhhhhh" + long,
		"hhhhhhhh", // ends exactly at the end of the buffer: no room for the 8-byte write
	}
	in := newFSSTIn(fsstSymbols, strs)
	checkStrings(t, mustDecode(t, in.seg(), utf8t, len(strs)), strs)
	checkStrings(t, mustDecode(t, in.legacySeg(), utf8t, len(strs)), strs)
	checkStrings(t, mustDecode(t, in.seg(), bint, len(strs)), strs)
	// Only the last row is short of room: a lone escape at the end of the output.
	last := newFSSTIn(fsstSymbols, []string{"abc", "\xfe"})
	checkStrings(t, mustDecode(t, last.seg(), utf8t, 2), []string{"abc", "\xfe"})
	// No row at all, and no symbol at all.
	checkStrings(t, mustDecode(t, newFSSTIn(nil, nil).seg(), utf8t, 0), nil)
	checkStrings(t, mustDecode(t, newFSSTIn(nil, []string{"xy", ""}).seg(), utf8t, 2), []string{"xy", ""})
}

func TestFSSTValidity(t *testing.T) {
	strs := []string{"abc", "", "hhhhhhhh"}
	in := newFSSTIn(fsstSymbols, strs)
	in.valid = []byte{0b101}
	for name, seg := range map[string][]byte{"current": in.seg(), "legacy": in.legacySeg()} {
		a := mustDecode(t, seg, utf8n, 3)
		if a.Validity == nil || !a.Validity.Get(0) || a.Validity.Get(1) || !a.Validity.Get(2) {
			t.Errorf("%s: validity = %+v", name, a.Validity)
		}
		checkStrings(t, a, strs)
	}
}

func TestFSSTHostile(t *testing.T) {
	base := func() fsstIn { return newFSSTIn(fsstSymbols, []string{"abc", "hhhhhhhh"}) }
	sym := func(in fsstIn, s []string) fsstIn { in.symBuf, in.lenBuf = symbolBuffers(s); return in }
	cases := []struct {
		name string
		in   func() fsstIn
		want string
	}{
		{"symbol of 9 bytes", func() fsstIn { in := base(); in.lenBuf[2] = 9; return in }, "symbol 2 is 9 bytes"},
		{"symbol of 0 bytes", func() fsstIn { in := base(); in.lenBuf[0] = 0; return in }, "symbol 0 is 0 bytes"},
		{"symbols not a multiple of 8", func() fsstIn { in := base(); in.symBuf = in.symBuf[:5]; return in }, "not a multiple of 8"},
		{"too many symbols", func() fsstIn {
			in := base()
			in.symBuf, in.lenBuf = make([]byte, 8*256), bytes.Repeat([]byte{1}, 256)
			return in
		}, "256 symbols"},
		{"lengths of other count", func() fsstIn { in := base(); in.lenBuf = in.lenBuf[1:]; return in }, "7 symbol lengths"},
		{"code past the table", func() fsstIn { in := base(); in.codes[0] = 40; return in }, "has code 40, there are 8 symbols"},
		{"escape as the last byte", func() fsstIn {
			in := base()
			in.codes = []byte{0, fsstEscape}
			in.offs, in.ulen = []int64{0, 1, 2}, []int64{1, 1}
			return in
		}, "ends with an escape"},
		{"row of fewer bytes", func() fsstIn { in := base(); in.ulen[0] = 4; return in }, "row 0 decodes to 3 bytes, declared 4"},
		{"lengths with nulls", func() fsstIn { in := base(); in.nullLens = true; return in }, "uncompressed lengths: has nulls"},
		{"row of more bytes", func() fsstIn { in := base(); in.ulen[0] = 2; return in }, "more than its 2 bytes"},
		{"symbol past the row", func() fsstIn { in := base(); in.codes[2] = 3; in.ulen[1] = 3; return in }, "more than its 3 bytes"},
		{"row of more bytes, with room after it", func() fsstIn {
			in := newFSSTIn(fsstSymbols, []string{"hhhhhhhh", "hhhhhhhhhhhhhhhh"})
			in.ulen[0] = 7
			return in
		}, "row 0 decodes to more than its 7 bytes"},
		{"escape past the row", func() fsstIn {
			in := sym(base(), nil)
			in.codes = []byte{fsstEscape, 'a', fsstEscape, 'b'}
			in.offs, in.ulen = []int64{0, 4, 4}, []int64{1, 0}
			return in
		}, "more than its 1 bytes"},
		{"length no code can back", func() fsstIn { in := base(); in.ulen[1] = 1 << 30; return in }, "its codes hold at most"},
		{"offsets decrease", func() fsstIn { in := base(); in.offs[1] = 9; return in }, "offset 2 is"},
		{"offsets past the codes", func() fsstIn { in := base(); in.offs[2] = 99; return in }, "past the"},
		{"bad ptype", func() fsstIn { in := base(); in.meta = pb(99, 2); return in }, "not an integer type"},
		{"bad offsets ptype", func() fsstIn { in := base(); in.meta = pb(2, 99); return in }, "codes_offsets_ptype"},
		{"bad metadata", func() fsstIn { in := base(); in.meta = []byte{0x08}; return in }, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.in().seg(), utf8t, 2, c.want) })
	}
	t.Run("negative length", func(t *testing.T) {
		in := base()
		in.ulen[0] = -1
		in.meta = pb(uint64(I32), uint64(U32))
		wantErr(t, in.seg(), utf8t, 2, "negative")
	})
}

func TestFSSTShapes(t *testing.T) {
	in := newFSSTIn(fsstSymbols, []string{"abc", "hhhhhhhh"})
	bufs := in.bufs()
	plain := func(meta []byte, idx []uint16, kids ...fbtest.Tab) []byte {
		b := bufs
		if len(idx) == 2 {
			b = in.legacyBufs()
		}
		return segment(node(eFsst, meta, idx, kids...), b...)
	}
	cases := []struct {
		name string
		seg  []byte
		dt   DType
		want string
	}{
		{"not bytes", in.seg(), i32, "cannot be a"},
		{"one buffer", plain(in.meta, []uint16{0}, pnode(3), pnode(4)), utf8t, "expected 2 or 3 buffers"},
		{"missing symbols", plain(in.meta, []uint16{9, 1, 2}, pnode(3), pnode(4)), utf8t, "buffer 0 is buffer 9"},
		{"missing lengths", plain(in.meta, []uint16{0, 9, 2}, pnode(3), pnode(4)), utf8t, "buffer 1 is buffer 9"},
		{"missing codes", plain(in.meta, []uint16{0, 1, 9}, pnode(3), pnode(4)), utf8t, "buffer 2 is buffer 9"},
		{"one child", plain(in.meta, []uint16{0, 1, 2}, pnode(3)), utf8t, "expected 2 or 3 children"},
		{"bad lengths child", plain(in.meta, []uint16{0, 1, 2}, pnode(9), pnode(4)), utf8t, "uncompressed lengths"},
		{"bad offsets child", plain(in.meta, []uint16{0, 1, 2}, pnode(3), pnode(9)), utf8t, "codes offsets"},
		{"bad validity child", plain(in.meta, []uint16{0, 1, 2}, pnode(3), pnode(4), bnode(9)), utf8n, "validity"},
		{"legacy: one child", plain(in.meta, []uint16{0, 1}, pnode(3)), utf8t, "expected 2 children"},
		{"legacy: bad codes", plain(in.meta, []uint16{0, 1}, pnode(9), pnode(4)), utf8t, "codes:"},
		{"legacy: bad lengths", plain(in.meta, []uint16{0, 1}, node(eVarBin, pb(uint64(U32)), []uint16{2}, pnode(3)), pnode(9)), utf8t, "uncompressed lengths"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.seg, c.dt, 2, c.want) })
	}
}

func TestFSSTDecodeNoAllocs(t *testing.T) {
	strs := []string{"abc", "", "hhhhhhhh", "\x00\x01", "ffffff"}
	in := newFSSTIn(fsstSymbols, strs)
	tbl, err := newSymbolTable(in.symBuf, in.lenBuf)
	if err != nil {
		t.Fatal(err)
	}
	n := len(strs)
	ulen, offs := make([]int, n), make([]int, n+1)
	total := 0
	for i := range n {
		ulen[i], offs[i+1] = int(in.ulen[i]), int(in.offs[i+1])
		total += ulen[i]
	}
	dst, outS, outE := make([]byte, total), make([]int, n), make([]int, n)
	allocs := testing.AllocsPerRun(20, func() {
		if err := tbl.decode(dst, outS, outE, in.codes, offs[:n], offs[1:], ulen); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocations, want 0", allocs)
	}
	for i, s := range strs {
		if got := string(dst[outS[i]:outE[i]]); got != s {
			t.Errorf("row %d = %q, want %q", i, got, s)
		}
	}
}

// onPairIn is the parts of an OnPair array.
type onPairIn struct {
	dict                 []byte
	dictOffs             []int64
	codes                []int64
	codeOffs, ulen       []int64
	valid                []byte
	meta                 []byte
	dictPT, codesPT, off uint64
}

// onPairTokens is a dictionary with single bytes and longer tokens, one of them over 8 bytes.
var onPairTokens = []string{"a", "b", "c", "ab", "abc", "the quick brown fox", "cc"}

func newOnPairIn(tokens []string, strs []string) onPairIn {
	in := onPairIn{dictOffs: []int64{0}, codeOffs: []int64{0}}
	for _, tk := range tokens {
		in.dict = append(in.dict, tk...)
		in.dictOffs = append(in.dictOffs, int64(len(in.dict)))
	}
	for _, s := range strs {
		for rest := s; rest != ""; {
			best := -1
			for i, tk := range tokens {
				if strings.HasPrefix(rest, tk) && (best < 0 || len(tk) > len(tokens[best])) {
					best = i
				}
			}
			in.codes = append(in.codes, int64(best))
			rest = rest[len(tokens[best]):]
		}
		in.codeOffs = append(in.codeOffs, int64(len(in.codes)))
		in.ulen = append(in.ulen, int64(len(s)))
	}
	in.meta = pb(uint64(U32), 0, uint64(len(tokens)), uint64(len(in.codes)), uint64(U32), uint64(U16), uint64(U32))
	return in
}

func (in onPairIn) seg() []byte {
	kids := []fbtest.Tab{pnode(1), pnode(2), pnode(3), pnode(4)}
	if in.valid != nil {
		kids = append(kids, bnode(5))
	}
	return segment(node(eOnPair, in.meta, []uint16{0}, kids...),
		buf{data: in.dict}, buf{data: ints(4, in.dictOffs...)}, buf{data: ints(2, in.codes...)},
		buf{data: ints(4, in.codeOffs...)}, buf{data: ints(4, in.ulen...)}, buf{data: in.valid})
}

func TestOnPair(t *testing.T) {
	strs := []string{"", "a", "abcabc", "the quick brown fox", "ccc", "ab" + "the quick brown fox" + "c", "bbbb"}
	in := newOnPairIn(onPairTokens, strs)
	checkStrings(t, mustDecode(t, in.seg(), utf8t, len(strs)), strs)
	checkStrings(t, mustDecode(t, in.seg(), bint, len(strs)), strs)
	checkStrings(t, mustDecode(t, newOnPairIn(nil, nil).seg(), utf8t, 0), nil)

	in.valid = []byte{0b1111101}
	a := mustDecode(t, in.seg(), utf8n, len(strs))
	if a.Validity == nil || a.Validity.Get(1) || !a.Validity.Get(0) {
		t.Errorf("validity = %+v", a.Validity)
	}
	checkStrings(t, a, strs)
}

func TestOnPairHostile(t *testing.T) {
	base := func() onPairIn { return newOnPairIn(onPairTokens, []string{"abc", "ccb"}) }
	cases := []struct {
		name string
		in   func() onPairIn
		want string
	}{
		{"code past the dictionary", func() onPairIn { in := base(); in.codes[0] = 40; return in }, "has code 40, there are 7 tokens"},
		{"dictionary offset past the dictionary", func() onPairIn { in := base(); in.dictOffs[7] = 999; return in }, "dictionary offsets: offset 999 is past"},
		{"dictionary offsets decrease", func() onPairIn { in := base(); in.dictOffs[2] = 0; return in }, "dictionary offsets: offset 2 is"},
		{"row of fewer bytes", func() onPairIn { in := base(); in.ulen[0] = 5; return in }, "row 0 decodes to 3 bytes, declared 5"},
		{"row of more bytes", func() onPairIn { in := base(); in.ulen[0] = 2; return in }, "more than its 2 bytes"},
		{"length no code can back", func() onPairIn { in := base(); in.ulen[0] = 1 << 30; return in }, "its codes hold at most"},
		{"more tokens than bytes", func() onPairIn { in := base(); in.meta = pb(2, 0, 9999, 3, 2, 1, 2); return in }, "dict_size is 9999"},
		{"code offsets decrease", func() onPairIn { in := base(); in.codeOffs[1] = 9; return in }, "codes offsets: offset 2 is"},
		{"bad lengths ptype", func() onPairIn { in := base(); in.meta = pb(99, 0, 7, 4, 2, 1, 2); return in }, "uncompressed_lengths_ptype"},
		{"bad dict offsets ptype", func() onPairIn { in := base(); in.meta = pb(2, 0, 7, 4, 99, 1, 2); return in }, "dict_offsets_ptype"},
		{"bad codes ptype", func() onPairIn { in := base(); in.meta = pb(2, 0, 7, 4, 2, 99, 2); return in }, "codes_ptype"},
		{"bad offsets ptype", func() onPairIn { in := base(); in.meta = pb(2, 0, 7, 4, 2, 1, 99); return in }, "codes_offsets_ptype"},
		{"huge dict_size", func() onPairIn { in := base(); in.meta = pb(2, 0, 1<<63, 4, 2, 1, 2); return in }, "dict_size"},
		{"huge codes_len", func() onPairIn { in := base(); in.meta = pb(2, 0, 7, 1<<63, 2, 1, 2); return in }, "codes_len"},
		{"bad metadata", func() onPairIn { in := base(); in.meta = []byte{0x08}; return in }, ""},
		{"negative code", func() onPairIn {
			in := base()
			in.meta = pb(2, 0, 7, 3, 2, uint64(I16), 2)
			in.codes[0] = -1
			return in
		}, "negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.in().seg(), utf8t, 2, c.want) })
	}
}

func TestOnPairShapes(t *testing.T) {
	in := newOnPairIn(onPairTokens, []string{"abc", "ccb"})
	bufs := []buf{{data: in.dict}, {data: ints(4, in.dictOffs...)}, {data: ints(2, in.codes...)},
		{data: ints(4, in.codeOffs...)}, {data: ints(4, in.ulen...)}, {data: nil}}
	mk := func(idx []uint16, kids ...fbtest.Tab) []byte {
		return segment(node(eOnPair, in.meta, idx, kids...), bufs...)
	}
	good := []fbtest.Tab{pnode(1), pnode(2), pnode(3), pnode(4)}
	with := func(i int, k fbtest.Tab) []fbtest.Tab {
		out := append([]fbtest.Tab(nil), good...)
		out[i] = k
		return out
	}
	cases := []struct {
		name string
		seg  []byte
		dt   DType
		want string
	}{
		{"not bytes", mk([]uint16{0}, good...), i32, "cannot be a"},
		{"no buffer", mk(nil, good...), utf8t, "expected 1 buffer"},
		{"three children", mk([]uint16{0}, good[:3]...), utf8t, "expected 4 or 5 children"},
		{"missing dictionary", mk([]uint16{9}, good...), utf8t, "buffer 0 is buffer 9"},
		{"bad dict offsets child", mk([]uint16{0}, with(0, pnode(9))...), utf8t, "dictionary offsets"},
		{"bad codes child", mk([]uint16{0}, with(1, pnode(9))...), utf8t, "codes"},
		{"bad code offsets child", mk([]uint16{0}, with(2, pnode(9))...), utf8t, "codes offsets"},
		{"bad lengths child", mk([]uint16{0}, with(3, pnode(9))...), utf8t, "uncompressed lengths"},
		{"bad validity child", mk([]uint16{0}, append(append([]fbtest.Tab(nil), good...), bnode(9))...), utf8n, "validity"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.seg, c.dt, 2, c.want) })
	}
}

func TestOnPairDecodeNoAllocs(t *testing.T) {
	strs := []string{"abc", "", "the quick brown fox", "ccb"}
	in := newOnPairIn(onPairTokens, strs)
	toInts := func(v []int64) []int {
		out := make([]int, len(v))
		for i, x := range v {
			out[i] = int(x)
		}
		return out
	}
	d := &onPairDict{bytes: in.dict, offs: toInts(in.dictOffs)}
	n := len(strs)
	ulen, offs, codes := toInts(in.ulen), toInts(in.codeOffs), toInts(in.codes)
	dst, outS, outE := make([]byte, 3+19+3), make([]int, n), make([]int, n)
	allocs := testing.AllocsPerRun(20, func() {
		if err := d.decode(dst, outS, outE, codes, offs, ulen); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("%v allocations, want 0", allocs)
	}
	for i, s := range strs {
		if got := string(dst[outS[i]:outE[i]]); got != s {
			t.Errorf("row %d = %q, want %q", i, got, s)
		}
	}
}
