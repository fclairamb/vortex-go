package array

import (
	"encoding/binary"
	"math"
	"strings"
	"testing"

	"github.com/fclairamb/vortex-go/internal/flatbuf/fbtest"
)

// The array specs of the hand-built segments, as a footer would list them.
var specs = []string{
	"vortex.null", "vortex.bool", "vortex.primitive", "vortex.struct", "vortex.chunked",
	"vortex.constant", "vortex.ext", "vortex.masked", "vortex.bytebool", "vortex.alp",
}

const (
	eNull = iota
	eBool
	ePrim
	eStruct
	eChunked
	eConst
	eExt
	eMasked
	eByteBool
	eAlp
)

var (
	i32  = DType{Kind: Prim, PType: I32}
	u64  = DType{Kind: Prim, PType: U64}
	bl   = DType{Kind: Bool}
	nul  = DType{Kind: Null, Nullable: true}
	i32n = DType{Kind: Prim, PType: I32, Nullable: true}
)

func structOf(nullable bool, fields ...DType) DType {
	t := DType{Kind: Struct, Nullable: nullable}
	for i, f := range fields {
		t.Fields = append(t.Fields, Field{Name: string(rune('a' + i)), Type: f})
	}
	return t
}

// buf is a buffer of a hand-built segment.
type buf struct {
	data []byte
	pad  int
	comp uint8
}

// node is an ArrayNode: encoding index, metadata, buffer indexes, children.
func node(enc int, meta []byte, bufs []uint16, kids ...fbtest.Tab) fbtest.Tab {
	t := fbtest.Tab{0: fbtest.U16(uint16(enc))}
	if meta != nil {
		t[1] = fbtest.Bytes(meta)
	}
	if kids != nil {
		t[2] = fbtest.Tables(kids...)
	}
	if bufs != nil {
		t[3] = fbtest.U16s(bufs...)
	}
	return t
}

// segment serializes an array: the buffers, the flatbuffer, its length.
func segment(root fbtest.Tab, bufs ...buf) []byte {
	var body []byte
	var descs [][]byte
	for _, b := range bufs {
		body = append(body, make([]byte, b.pad)...)
		body = append(body, b.data...)
		d := binary.LittleEndian.AppendUint16(nil, uint16(b.pad))
		d = append(d, 0, b.comp)
		descs = append(descs, binary.LittleEndian.AppendUint32(d, uint32(len(b.data))))
	}
	msg := fbtest.Tab{0: fbtest.T(root)}
	if descs != nil {
		msg[1] = fbtest.Structs(8, descs...)
	}
	fb := fbtest.Build(msg)
	out := append(body, fb...)
	return binary.LittleEndian.AppendUint32(out, uint32(len(fb)))
}

func le32(v ...int32) []byte {
	var b []byte
	for _, x := range v {
		b = binary.LittleEndian.AppendUint32(b, uint32(x))
	}
	return b
}

func le64(v ...uint64) []byte {
	var b []byte
	for _, x := range v {
		b = binary.LittleEndian.AppendUint64(b, x)
	}
	return b
}

// scalar messages: the field of the oneof, then its value.
func sNull() []byte       { return []byte{0x08, 0x00} }
func sBool(v bool) []byte { return []byte{0x10, map[bool]byte{false: 0, true: 1}[v]} }
func sInt(v int64) []byte {
	return append([]byte{0x18}, binary.AppendUvarint(nil, uint64(v<<1)^uint64(v>>63))...)
}
func sUint(v uint64) []byte { return append([]byte{0x20}, binary.AppendUvarint(nil, v)...) }
func sF16(bits uint16) []byte {
	return append([]byte{0x50}, binary.AppendUvarint(nil, uint64(bits))...)
}
func sF32(v float32) []byte {
	return binary.LittleEndian.AppendUint32([]byte{0x2d}, math.Float32bits(v))
}
func sF64(v float64) []byte {
	return binary.LittleEndian.AppendUint64([]byte{0x31}, math.Float64bits(v))
}

// primNode is a primitive array of i32 values, as a root node and its buffer.
func primSeg(vals ...int32) []byte {
	return segment(node(ePrim, nil, []uint16{0}), buf{data: le32(vals...)})
}

// constSeg is a constant array of the scalar message.
func constSeg(msg []byte) []byte {
	return segment(node(eConst, nil, []uint16{0}), buf{data: msg})
}

func mustDecode(t *testing.T, seg []byte, dt DType, rows int) *Array {
	t.Helper()
	a, err := Decode(seg, specs, dt, rows, Options{})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return a
}

func wantErr(t *testing.T, seg []byte, dt DType, rows int, sub string) {
	t.Helper()
	_, err := Decode(seg, specs, dt, rows, Options{})
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("Decode error = %v, want %q", err, sub)
	}
}

func i32s(t *testing.T, a *Array) []int32 {
	t.Helper()
	out := make([]int32, a.Len)
	for i := range out {
		out[i] = int32(binary.LittleEndian.Uint32(a.Data[4*i:]))
	}
	return out
}

var errors2 = errUnsupported("x")

type errUnsupported string

func (e errUnsupported) Error() string { return string(e) }
