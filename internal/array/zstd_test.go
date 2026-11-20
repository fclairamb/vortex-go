package array

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"

	"github.com/klauspost/compress/dict"
	"github.com/klauspost/compress/zstd"
)

var (
	u16  = DType{Kind: Prim, PType: U16}
	u16n = DType{Kind: Prim, PType: U16, Nullable: true}
	strn = DType{Kind: Utf8, Nullable: true}
	bin  = DType{Kind: Binary}
)

// zmeta is the metadata of vortex.zstd: the dictionary size, then per frame its size and values.
func zmeta(dictSize int, frames ...[2]int) []byte {
	b := pb(uint64(dictSize))
	for _, f := range frames {
		m := pb(uint64(f[0]), uint64(f[1]))
		b = append(append(b, 0x12, byte(len(m))), m...)
	}
	return b
}

// framesMsg is a frame of the metadata, from the bytes of its message.
func framesMsg(body []byte) []byte { return append([]byte{0x12, byte(len(body))}, body...) }

func compress(t *testing.T, raw []byte, opts ...zstd.EOption) []byte {
	t.Helper()
	enc, err := zstd.NewWriter(nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	return enc.EncodeAll(raw, nil)
}

// zseg is a vortex.zstd array: the buffers (dictionary first, if any), and a validity child.
func zseg(meta []byte, validity []byte, bufs ...[]byte) []byte {
	idx := make([]uint16, len(bufs))
	var all []buf
	for i, b := range bufs {
		idx[i] = uint16(i)
		all = append(all, buf{data: b})
	}
	if validity == nil {
		return segment(node(eZstd, meta, idx), all...)
	}
	all = append(all, buf{data: validity})
	return segment(node(eZstd, meta, idx, bnode(uint16(len(bufs)))), all...)
}

// lenPrefixed is the values as the strings frames hold them: a u32 length, then the bytes.
func lenPrefixed(vals ...string) []byte {
	var b []byte
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint32(b, uint32(len(v)))
		b = append(b, v...)
	}
	return b
}

func u16s(vals ...uint16) []byte {
	var b []byte
	for _, v := range vals {
		b = binary.LittleEndian.AppendUint16(b, v)
	}
	return b
}

func TestZstdNumbers(t *testing.T) {
	f1, f2 := u16s(1, 2, 3), u16s(40000, 5)
	seg := zseg(zmeta(0, [2]int{len(f1), 3}, [2]int{len(f2), 2}), nil, compress(t, f1), compress(t, f2))
	a := mustDecode(t, seg, u16, 5)
	if got := string(a.Data); got != string(u16s(1, 2, 3, 40000, 5)) || a.Validity != nil {
		t.Fatalf("data = %v", a.Data)
	}
	// With nulls: rows 1 and 3 are null, the 3 valid values are in one frame.
	f := u16s(7, 8, 9)
	seg = zseg(zmeta(0, [2]int{len(f), 3}), []byte{0b10101}, compress(t, f))
	a = mustDecode(t, seg, u16n, 5)
	if got := string(a.Data); got != string(u16s(7, 0, 8, 0, 9)) || !a.Null(1) || a.Null(2) {
		t.Fatalf("data = %v", a.Data)
	}
}

func TestZstdStrings(t *testing.T) {
	f1, f2 := lenPrefixed("ab", "", "cde"), lenPrefixed("fghi")
	seg := zseg(zmeta(0, [2]int{len(f1), 3}, [2]int{len(f2), 1}), []byte{0b101011}, compress(t, f1), compress(t, f2))
	a := mustDecode(t, seg, strn, 6)
	var got []string
	for i := range a.Len {
		if a.Null(i) {
			got = append(got, "<null>")
		} else {
			got = append(got, string(a.Data[a.Starts[i]:a.Ends[i]]))
		}
	}
	if want := "[ab  <null> cde <null> fghi]"; fmt.Sprint(got) != want {
		t.Fatalf("got %v, want %s", got, want)
	}
	// Binary, no nulls.
	seg = zseg(zmeta(0, [2]int{len(f1), 3}), nil, compress(t, f1))
	a = mustDecode(t, seg, bin, 3)
	if string(a.Data[a.Starts[2]:a.Ends[2]]) != "cde" {
		t.Fatalf("starts %v ends %v", a.Starts, a.Ends)
	}
}

// trainedDict trains a dictionary on the samples.
func trainedDict(t *testing.T, samples [][]byte) []byte {
	t.Helper()
	d, err := dict.BuildZstdDict(samples, dict.Options{MaxDictSize: 4096, HashBytes: 6})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestZstdDictionary(t *testing.T) {
	var samples [][]byte
	var vals []string
	for i := range 200 {
		v := fmt.Sprintf("hello world number %d, the quick brown fox jumps over the lazy dog %d times", i, i*7)
		samples = append(samples, []byte(v))
		vals = append(vals, v)
	}
	f := lenPrefixed(vals[:50]...)
	// A trained dictionary: the frames name it by its identifier.
	d := trainedDict(t, samples)
	meta := zmeta(len(d), [2]int{len(f), 50})
	seg := zseg(meta, nil, d, compress(t, f, zstd.WithEncoderDict(d)))
	a := mustDecode(t, seg, DType{Kind: Utf8}, 50)
	if string(a.Data[a.Starts[49]:a.Ends[49]]) != vals[49] {
		t.Fatalf("value 49 = %q", a.Data[a.Starts[49]:a.Ends[49]])
	}
	// A dictionary of raw content, which the writer of the reference uses when it has no
	// trained one: the frames carry no identifier.
	raw := []byte(strings.Repeat("hello world number, the quick brown fox ", 5))
	meta = zmeta(len(raw), [2]int{len(f), 50})
	seg = zseg(meta, nil, raw, compress(t, f, zstd.WithEncoderDictRaw(0, raw)))
	a = mustDecode(t, seg, DType{Kind: Utf8}, 50)
	if string(a.Data[a.Starts[3]:a.Ends[3]]) != vals[3] {
		t.Fatalf("value 3 = %q", a.Data[a.Starts[3]:a.Ends[3]])
	}
	// A dictionary that claims to be trained and is not.
	bad := []byte{0x37, 0xa4, 0x30, 0xec, 1, 2, 3}
	seg = zseg(zmeta(len(bad), [2]int{len(f), 50}), nil, bad, compress(t, f))
	wantErr(t, seg, DType{Kind: Utf8}, 50, "dictionary:")
	// A raw dictionary is not the one the frame was made with.
	seg = zseg(zmeta(len(raw), [2]int{len(f), 50}), nil, raw, compress(t, f, zstd.WithEncoderDictRaw(7, raw)))
	wantErr(t, seg, DType{Kind: Utf8}, 50, "frame 0")
}

func TestZstdErrors(t *testing.T) {
	f := u16s(1, 2, 3)
	frame := compress(t, f)
	good := zmeta(0, [2]int{len(f), 3})
	// A frame that says 4 bytes and holds 8, and one that says nothing of its size.
	lying := []byte{0x28, 0xB5, 0x2F, 0xFD, 0x20, 4, 0x41, 0, 0, 1, 2, 3, 4, 5, 6, 7, 8}
	// The encoder writes the size of a frame only when it is long: a short one has none.
	noSize := compress(t, f)
	var h zstd.Header
	if h.Decode(noSize) != nil || h.HasFCS {
		t.Fatal("the short frame has a size")
	}
	sized := compress(t, make([]byte, 5000))
	if h.Decode(sized) != nil || !h.HasFCS {
		t.Fatal("the long frame has no size")
	}
	long := []byte(strings.Repeat("x", 100))
	for _, c := range []struct {
		name string
		seg  []byte
		dt   DType
		rows int
		want string
	}{
		{"not a number or a string", zseg(good, nil, frame), bl, 3, "cannot be a"},
		{"two children", segment(node(eZstd, good, []uint16{0}, bnode(1), bnode(1)), buf{data: frame}, buf{data: []byte{7}}), u16, 3, "expected 0 or 1 child"},
		{"metadata", zseg([]byte{0x0a}, nil, frame), u16, 3, "protowire"},
		{"frame metadata", zseg([]byte{0x12, 0x02, 0x08, 0x80}, nil, frame), u16, 3, "protowire"},
		{"huge dictionary size", zseg(append([]byte{0x08}, binary.AppendUvarint(nil, 1<<63)...), nil, frame), u16, 3, "too large"},
		{"huge frame size", zseg(zmeta(0, [2]int{1 << 62, 1}), nil, frame), u16, 3, "decompress to more than"},
		{"frame size past the limit", zseg(zmeta(0, [2]int{maxZstdBytes + 1, 1}), nil, frame), u16, 1, "decompress to more than"},
		{"huge size", zseg(framesMsg(append([]byte{0x08}, append(binary.AppendUvarint(nil, 1<<63), 0x10, 0)...)), nil, frame), u16, 3, "too large"},
		{"huge values", zseg(framesMsg(append([]byte{0x08, 0x00, 0x10}, binary.AppendUvarint(nil, 1<<63)...)), nil, frame), u16, 3, "too large"},
		{"no dictionary buffer", zseg(zmeta(9, [2]int{len(f), 3}), nil), u16, 3, "buffer 0 is missing"},
		{"dictionary of another size", zseg(zmeta(9, [2]int{len(f), 3}), nil, []byte{1, 2}, frame), u16, 3, "dictionary buffer of 2 bytes"},
		{"missing dictionary", zseg(zmeta(9, [2]int{len(f), 3}), nil, frame), u16, 3, "dictionary buffer of"},
		{"frames and buffers", zseg(zmeta(0, [2]int{len(f), 3}, [2]int{len(f), 3}), nil, frame), u16, 6, "1 frame buffers, the metadata lists 2"},
		{"more values than valid rows", zseg(zmeta(0, [2]int{len(f), 3}), nil, frame), u16, 2, "more than the 2 valid"},
		{"fewer values than valid rows", zseg(zmeta(0, [2]int{len(f), 3}), nil, frame), u16, 4, "the frames hold 3 values, there are 4"},
		{"values of the wrong size", zseg(zmeta(0, [2]int{len(f) + 1, 3}), nil, frame), u16, 3, "7 bytes for 3 u16"},
		{"not a frame", zseg(good, nil, []byte("not a zstd frame")), u16, 3, "frame 0"},
		{"size of the header", zseg(zmeta(0, [2]int{4, 2}), nil, sized), u16, 2, "header declares 5000 bytes, the metadata 4"},
		{"frame bigger than its size", zseg(zmeta(0, [2]int{4, 2}), nil, lying), u16, 2, "frame 0"},
		{"frame without a size that is longer than the metadata says", zseg(zmeta(0, [2]int{4, 2}), nil, noSize), u16, 2, "decompresses to 6 bytes, the metadata says 4"},
		{"value past the frame", zseg(zmeta(0, [2]int{8, 1}), nil, compress(t, append(binary.LittleEndian.AppendUint32(nil, 200), 1, 2, 3, 4))), strn, 1, "a value of 200 bytes"},
		{"value without a length", zseg(zmeta(0, [2]int{2, 1}), nil, compress(t, []byte{1, 2})), strn, 1, "has no length"},
		{"bytes after the values", zseg(zmeta(0, [2]int{len(long) + 4, 1}), nil, compress(t, append(lenPrefixed("ab"), long[:len(long)-2]...))), strn, 1, "bytes after its 1 values"},
	} {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.seg, c.dt, c.rows, c.want) })
	}
}

func TestZstdDamage(t *testing.T) {
	f := lenPrefixed("ab", "cd")
	seg := zseg(zmeta(0, [2]int{len(f), 2}), []byte{0b11}, compress(t, f))
	for pos := range seg {
		for _, v := range []byte{0x00, 0x01, 0x7f, 0x80, 0xff} {
			damaged := append([]byte(nil), seg...)
			damaged[pos] = v
			_, _ = Decode(damaged, specs, DType{Kind: Utf8, Nullable: true}, 2, Options{})
		}
	}
}
