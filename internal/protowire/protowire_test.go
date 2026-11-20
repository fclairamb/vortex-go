package protowire

import (
	"bytes"
	"errors"
	"math"
	"testing"
)

func TestReadVarint(t *testing.T) {
	eleven := append(bytes.Repeat([]byte{0x80}, 10), 0x01)
	tests := []struct {
		name string
		in   []byte
		v    uint64
		n    int
		err  error
	}{
		{"zero", []byte{0}, 0, 1, nil},
		{"one byte", []byte{0x7f, 0xff}, 127, 1, nil},
		{"two bytes", []byte{0xac, 0x02}, 300, 2, nil},
		{"max", append(bytes.Repeat([]byte{0xff}, 9), 0x01), math.MaxUint64, 10, nil},
		{"empty", nil, 0, 0, ErrTruncated},
		{"continuation at end", []byte{0x80, 0x80}, 0, 0, ErrTruncated},
		{"11 bytes", eleven, 0, 0, ErrVarint},
		{"10th byte overflows", append(bytes.Repeat([]byte{0xff}, 9), 0x02), 0, 0, ErrVarint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, n, err := ReadVarint(tt.in)
			if v != tt.v || n != tt.n || !errors.Is(err, tt.err) {
				t.Fatalf("got %d, %d, %v", v, n, err)
			}
		})
	}
}

func TestZigzag(t *testing.T) {
	for in, want := range map[uint64]int64{0: 0, 1: -1, 2: 1, 3: -2, 4294967294: 2147483647, math.MaxUint64: math.MinInt64} {
		if got := Zigzag(in); got != want {
			t.Errorf("Zigzag(%d) = %d, want %d", in, got, want)
		}
	}
}

// msg is every wire type, plus an unknown field in the middle.
var msg = []byte{
	0x08, 0xac, 0x02, // field 1 varint 300
	0x15, 0x00, 0x00, 0x80, 0x3f, // field 2 fixed32 1.0
	0x19, 0, 0, 0, 0, 0, 0, 0xf0, 0x3f, // field 3 fixed64 1.0
	0x22, 0x03, 'a', 'b', 'c', // field 4 bytes "abc"
	0xf8, 0x01, 0x07, // field 31 varint, unknown to everybody
}

func TestIter(t *testing.T) {
	it := NewIter(msg)
	type got struct {
		num int
		typ Type
	}
	var seen []got
	for it.Next() {
		seen = append(seen, got{it.Num(), it.Type()})
		switch it.Num() {
		case 1:
			if it.Uint() != 300 {
				t.Errorf("varint %d", it.Uint())
			}
		case 2:
			if it.Float32() != 1 || it.Uint() != 0x3f800000 {
				t.Errorf("fixed32 %v", it.Float32())
			}
		case 3:
			if it.Float64() != 1 {
				t.Errorf("fixed64 %v", it.Float64())
			}
		case 4:
			if string(it.Bytes()) != "abc" {
				t.Errorf("bytes %q", it.Bytes())
			}
		}
	}
	want := []got{{1, Varint}, {2, Fixed32}, {3, Fixed64}, {4, Bytes}, {31, Varint}}
	if it.Err() != nil || len(seen) != len(want) {
		t.Fatalf("err %v, seen %v", it.Err(), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("field %d: %v, want %v", i, seen[i], want[i])
		}
	}
	if it.Next() {
		t.Error("Next after the end")
	}
}

func TestIterTruncatedAtEveryByte(t *testing.T) {
	// Cutting the message inside a field is an error; cutting between two fields is not.
	boundaries := map[int]bool{0: true, 3: true, 8: true, 17: true, 22: true, 25: true}
	for n := range len(msg) + 1 {
		it := NewIter(msg[:n])
		for it.Next() {
		}
		if (it.Err() == nil) != boundaries[n] {
			t.Errorf("cut at %d: err %v", n, it.Err())
		}
	}
}

func TestIterErrors(t *testing.T) {
	tests := map[string][]byte{
		"field number 0":          {0x00, 0x01},
		"tag past 32 bits":        {0x88, 0x80, 0x80, 0x80, 0x10, 0x00},
		"start group":             {0x0b},
		"end group":               {0x0c},
		"wire type 6":             {0x0e},
		"wire type 7":             {0x0f},
		"length past the end":     {0x0a, 0x05, 'a'},
		"length of 2^63":          {0x0a, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01},
		"length varint truncated": {0x0a, 0x80},
		"varint of 11 bytes":      append([]byte{0x08}, append(bytes.Repeat([]byte{0x80}, 10), 1)...),
		"tag varint of 11 bytes":  append(bytes.Repeat([]byte{0x80}, 10), 1),
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			it := NewIter(in)
			for it.Next() {
			}
			if it.Err() == nil {
				t.Fatal("no error")
			}
			if it.Next() {
				t.Error("Next after an error")
			}
		})
	}
}

func TestLengthPastEndIsTruncated(t *testing.T) {
	it := NewIter([]byte{0x0a, 0x05, 'a'})
	it.Next()
	if !errors.Is(it.Err(), ErrTruncated) {
		t.Fatal(it.Err())
	}
}

func TestPacked(t *testing.T) {
	v, err := PackedVarints([]byte{0x01, 0xac, 0x02})
	if err != nil || len(v) != 2 || v[0] != 1 || v[1] != 300 {
		t.Fatal(v, err)
	}
	if v, err := PackedVarints(nil); err != nil || len(v) != 0 {
		t.Fatal(v, err)
	}
	if _, err := PackedVarints([]byte{0x01, 0x80}); !errors.Is(err, ErrTruncated) {
		t.Fatal(err)
	}
	f32, err := PackedFixed32([]byte{1, 0, 0, 0, 2, 0, 0, 0})
	if err != nil || len(f32) != 2 || f32[0] != 1 || f32[1] != 2 {
		t.Fatal(f32, err)
	}
	if _, err := PackedFixed32([]byte{1, 0, 0}); !errors.Is(err, ErrTruncated) {
		t.Fatal(err)
	}
	f64, err := PackedFixed64([]byte{1, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0})
	if err != nil || len(f64) != 2 || f64[0] != 1 || f64[1] != 2 {
		t.Fatal(f64, err)
	}
	if _, err := PackedFixed64(make([]byte, 9)); !errors.Is(err, ErrTruncated) {
		t.Fatal(err)
	}
}

func FuzzIter(f *testing.F) {
	f.Add(msg)
	f.Fuzz(func(t *testing.T, b []byte) {
		it := NewIter(b)
		for it.Next() {
			_ = it.Bytes()
			_ = it.Uint()
		}
		_, _ = PackedVarints(b)
		_, _ = PackedFixed32(b)
		_, _ = PackedFixed64(b)
	})
}

func TestAppendRoundTrip(t *testing.T) {
	var b []byte
	b = AppendUint(b, 1, 300)
	b = AppendSint(b, 2, -3)
	b = AppendFixed32(b, 3, 0xdeadbeef)
	b = AppendFixed64(b, 4, 1<<40)
	b = AppendBytes(b, 5, []byte("hi"))
	b = AppendUint(b, 6, 0)
	it := NewIter(b)
	want := []struct {
		num int
		typ Type
	}{{1, Varint}, {2, Varint}, {3, Fixed32}, {4, Fixed64}, {5, Bytes}, {6, Varint}}
	for i, w := range want {
		if !it.Next() || it.Num() != w.num || it.Type() != w.typ {
			t.Fatalf("field %d: %v", i, it.Err())
		}
		switch w.num {
		case 1:
			if it.Uint() != 300 {
				t.Fatal(it.Uint())
			}
		case 2:
			if Zigzag(it.Uint()) != -3 {
				t.Fatal(it.Uint())
			}
		case 3, 4:
			if it.Uint() != map[int]uint64{3: 0xdeadbeef, 4: 1 << 40}[w.num] {
				t.Fatal(it.Uint())
			}
		case 5:
			if string(it.Bytes()) != "hi" {
				t.Fatal(it.Bytes())
			}
		}
	}
	if it.Next() || it.Err() != nil {
		t.Fatal(it.Err())
	}
	for _, v := range []uint64{0, 1, 127, 128, 1<<63 + 5, math.MaxUint64} {
		got, n, err := ReadVarint(AppendVarint(nil, v))
		if err != nil || got != v || n == 0 {
			t.Fatal(v, got, err)
		}
	}
	for _, v := range []int64{0, -1, 1, math.MinInt64, math.MaxInt64} {
		got, _, _ := ReadVarint(AppendZigzag(nil, v))
		if Zigzag(got) != v {
			t.Fatal(v)
		}
	}
}
