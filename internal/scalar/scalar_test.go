package scalar

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestDecodeArms(t *testing.T) {
	tests := []struct {
		name  string
		in    []byte
		check func(Value) bool
	}{
		{"null", []byte{0x08, 0x00}, func(v Value) bool { return v.Kind == Null }},
		{"bool true", []byte{0x10, 0x01}, func(v Value) bool { return v.Kind == Bool && v.Bool }},
		{"bool false", []byte{0x10, 0x00}, func(v Value) bool { return v.Kind == Bool && !v.Bool }},
		{"int negative", []byte{0x18, 0x03}, func(v Value) bool { return v.Kind == Int && v.Int == -2 }},
		{"int min", append([]byte{0x18}, append(bytes.Repeat([]byte{0xff}, 9), 0x01)...),
			func(v Value) bool { return v.Kind == Int && v.Int == math.MinInt64 }},
		{"uint", []byte{0x20, 0xac, 0x02}, func(v Value) bool { return v.Kind == Uint && v.Uint == 300 }},
		{"f32", []byte{0x2d, 0x00, 0x00, 0xc0, 0x3f}, func(v Value) bool { return v.Kind == F32 && v.F32 == 1.5 }},
		{"f64", []byte{0x31, 0, 0, 0, 0, 0, 0, 0xf8, 0x3f}, func(v Value) bool { return v.Kind == F64 && v.F64 == 1.5 }},
		{"string", []byte{0x3a, 0x02, 'h', 'i'}, func(v Value) bool { return v.Kind == String && v.Str == "hi" }},
		{"empty string", []byte{0x3a, 0x00}, func(v Value) bool { return v.Kind == String && v.Str == "" }},
		{"bytes", []byte{0x42, 0x03, 1, 2, 3}, func(v Value) bool { return v.Kind == Bytes && bytes.Equal(v.Bytes, []byte{1, 2, 3}) }},
		{"f16", []byte{0x50, 0x80, 0x78}, func(v Value) bool { return v.Kind == F16 && v.Uint == 0x3c00 }},
		{"last arm wins", []byte{0x10, 0x01, 0x20, 0x07}, func(v Value) bool { return v.Kind == Uint && v.Uint == 7 }},
		{"unknown field skipped", []byte{0xf8, 0x01, 0x05, 0x10, 0x01}, func(v Value) bool { return v.Kind == Bool && v.Bool }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := Decode(tt.in)
			if err != nil || !tt.check(v) {
				t.Fatalf("%+v, %v", v, err)
			}
		})
	}
}

func TestDecodeList(t *testing.T) {
	// [true, [null]] with an unknown field in the ListValue, then an empty list.
	null := []byte{0x08, 0x00}
	tru := []byte{0x10, 0x01}
	nested := append([]byte{0x4a, byte(2 + len(null)), 0x0a, byte(len(null))}, null...)
	l := []byte{0x0a, byte(len(tru))}
	l = append(l, tru...)
	l = append(l, 0x0a, byte(len(nested)))
	l = append(l, nested...)
	l = append(l, 0x10, 0x09) // unknown field of the ListValue
	msg := append([]byte{0x4a, byte(len(l))}, l...)
	v, err := Decode(msg)
	if err != nil || v.Kind != List || len(v.List) != 2 || !v.List[0].Bool ||
		v.List[1].Kind != List || v.List[1].List[0].Kind != Null {
		t.Fatalf("%+v, %v", v, err)
	}
	v, err = Decode([]byte{0x4a, 0x00})
	if err != nil || v.Kind != List || len(v.List) != 0 {
		t.Fatalf("%+v, %v", v, err)
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := map[string][]byte{
		"empty":               {},
		"unknown fields only": {0xf8, 0x01, 0x05},
		"wrong wire type":     {0x09, 0, 0, 0, 0, 0, 0, 0, 0},
		"truncated":           {0x3a, 0x05, 'a'},
		"bad utf8":            {0x3a, 0x01, 0xff},
		"variant":             {0x5a, 0x00},
		"union":               {0x62, 0x00},
		"list item wire type": {0x4a, 0x02, 0x08, 0x00},
		"list item bad":       {0x4a, 0x02, 0x0a, 0x00},
		"list item truncated": {0x4a, 0x02, 0x0a, 0x05},
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			if v, err := Decode(in); err == nil {
				t.Fatalf("no error, got %+v", v)
			}
		})
	}
	if _, err := Decode(nil); !errors.Is(err, ErrNoKind) {
		t.Fatal(err)
	}
}

func TestDecodeTooDeep(t *testing.T) {
	// maxDepth+2 nested lists: each level wraps the previous one in a one-item ListValue.
	msg := []byte{0x08, 0x00}
	for range maxDepth + 2 {
		item := append([]byte{0x0a}, appendLen(msg)...)
		msg = append([]byte{0x4a}, appendLen(item)...)
	}
	if _, err := Decode(msg); err == nil {
		t.Fatal("no error")
	}
	// maxDepth levels is fine.
	msg = []byte{0x08, 0x00}
	for range maxDepth {
		item := append([]byte{0x0a}, appendLen(msg)...)
		msg = append([]byte{0x4a}, appendLen(item)...)
	}
	if _, err := Decode(msg); err != nil {
		t.Fatal(err)
	}
}

func appendLen(b []byte) []byte {
	n := len(b)
	var out []byte
	for n >= 0x80 {
		out = append(out, byte(n)|0x80)
		n >>= 7
	}
	out = append(out, byte(n))
	return append(out, b...)
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte{0x4a, 0x04, 0x0a, 0x02, 0x10, 0x01})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = Decode(b) })
}
