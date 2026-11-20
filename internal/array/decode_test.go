package array

import (
	"errors"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

func TestDecodeSegmentErrors(t *testing.T) {
	garbage := append([]byte("not a flatbuffer"), 16, 0, 0, 0)
	noRoot := append(fbtest.Build(fbtest.Tab{}), 0, 0, 0, 0)
	noRoot[len(noRoot)-4] = byte(len(noRoot) - 4)
	cases := []struct {
		name string
		seg  []byte
		want string
	}{
		{"too short", []byte{1, 2}, "no flatbuffer length"},
		{"flatbuffer past the segment", []byte{1, 2, 3, 4, 200, 0, 0, 0}, "flatbuffer of 200 bytes"},
		{"not a flatbuffer", garbage, "flatbuf:"},
		{"no root", noRoot, "no root node"},
		{"compressed buffer", segment(node(ePrim, nil, []uint16{0}), buf{data: le32(1, 2), comp: 1}), "compressed"},
		{"buffer past the data", truncatedBuffer(t), "past the"},
		{"encoding out of range", segment(node(42, nil, nil)), "the footer has 17 array specs"},
		{"unsupported encoding", segment(node(eAlp, nil, nil)), `unsupported encoding "vortex.alp"`},
		{"missing buffer", segment(node(ePrim, nil, nil)), "buffer 0 is missing"},
		{"buffer index out of range", segment(node(ePrim, nil, []uint16{3}), buf{data: le32(1, 2)}), "which has 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { wantErr(t, c.seg, i32, 2, c.want) })
	}
}

// truncatedBuffer is a segment whose only buffer claims more bytes than the data holds.
func truncatedBuffer(t *testing.T) []byte {
	t.Helper()
	s := segment(node(ePrim, nil, []uint16{0}), buf{data: le32(1, 2)})
	// Drop the data: the flatbuffer then says 8 bytes, the body holds none.
	return s[8:]
}

func TestUnsupportedWrapsErrUnsupported(t *testing.T) {
	_, err := Decode(segment(node(eAlp, nil, nil)), specs, i32, 1, Options{})
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestPaddingBetweenBuffers(t *testing.T) {
	seg := segment(node(ePrim, nil, []uint16{1}), buf{data: []byte{9, 9}}, buf{data: le32(5, 6), pad: 6})
	a := mustDecode(t, seg, i32, 2)
	if got := i32s(t, a); got[0] != 5 || got[1] != 6 {
		t.Fatalf("values = %v", got)
	}
}

func TestTolerate(t *testing.T) {
	// struct{a: struct{a: i32 (alp), b: i32}, b: i32}, the alp field being unsupported.
	inner := structOf(false, i32, i32)
	root := structOf(false, inner, i32)
	seg := segment(
		node(eStruct, nil, nil,
			node(eStruct, nil, nil, node(eAlp, nil, nil), node(ePrim, nil, []uint16{0})),
			node(ePrim, nil, []uint16{0})),
		buf{data: le32(7)})
	var paths []string
	opt := Options{Tolerate: func(path string, err error) bool {
		paths = append(paths, path)
		return path == "a.a"
	}}
	a, err := Decode(seg, specs, root, 1, opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "a.a" {
		t.Fatalf("asked about %v", paths)
	}
	if skipped := a.Fields[0].Fields[0]; skipped.Skipped == nil || skipped.Len != 1 {
		t.Fatalf("a.a = %+v", skipped)
	}
	if got := i32s(t, a.Fields[0].Fields[1]); got[0] != 7 {
		t.Fatalf("a.b = %v", got)
	}
	// Not tolerated: the error names the field.
	opt.Tolerate = func(string, error) bool { return false }
	_, err = Decode(seg, specs, root, 1, opt)
	if err == nil || !strings.Contains(err.Error(), `field "a"`) || !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	// A corrupt field is never tolerated.
	bad := segment(node(eStruct, nil, nil, node(ePrim, nil, nil)))
	_, err = Decode(bad, specs, structOf(false, i32), 1, Options{Tolerate: func(string, error) bool { return true }})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v", err)
	}
}

func TestDTypeString(t *testing.T) {
	cases := map[string]DType{
		"null":                     nul,
		"bool":                     bl,
		"i32?":                     i32n,
		"struct{a: i32, b: bool?}": structOf(false, i32, DType{Kind: Bool, Nullable: true}),
		"ext(i64)":                 {Kind: Ext, Storage: &DType{Kind: Prim, PType: I64}},
		"utf8":                     {Kind: Other, Label: "utf8"},
	}
	for want, dt := range cases {
		if got := dt.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
	for p, w := range map[PType]int{U8: 1, I8: 1, U16: 2, I16: 2, F16: 2, U32: 4, I32: 4, F32: 4, U64: 8, I64: 8, F64: 8} {
		if p.Width() != w {
			t.Errorf("%s width %d", p, p.Width())
		}
	}
	if !I64.Signed() || U64.Signed() || F32.Signed() || !F16.Float() || I32.Float() {
		t.Fatal("Signed or Float is wrong")
	}
}

// TestDecodeDamage flips each byte of a serialized array in turn: whatever happens, it must
// be an error or an array, never a panic. It also reaches the errors of every field read.
func TestDecodeDamage(t *testing.T) {
	dt := structOf(false, DType{Kind: Prim, PType: I32, Nullable: true}, bl, i32)
	seg := segment(
		node(eStruct, nil, nil,
			node(ePrim, nil, []uint16{0}, node(eBool, []byte{0x08, 0x01}, []uint16{1})),
			node(eBool, []byte{0x08, 0x01}, []uint16{1}),
			node(eChunked, nil, nil, node(ePrim, nil, []uint16{2}), node(ePrim, nil, []uint16{3}))),
		buf{data: le32(1, 2)}, buf{data: []byte{3, 0}}, buf{data: le64(0, 2)}, buf{data: le32(5, 6)})
	for pos := range seg {
		for _, v := range []byte{0x00, 0x01, 0x7f, 0x80, 0xff} {
			damaged := append([]byte(nil), seg...)
			damaged[pos] = v
			_, _ = Decode(damaged, specs, dt, 2, Options{})
		}
	}
}
