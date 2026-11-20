package variant

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// testDecimal is the decimal type of the tests: unscaled little-endian bytes and a scale.
type testDecimal struct {
	unscaled []byte
	scale    int
}

func decimalHook(v any) ([]byte, int, bool) {
	d, ok := v.(testDecimal)
	return d.unscaled, d.scale, ok
}

func buildDecimal(unscaled []byte, scale int) any { return testDecimal{unscaled, scale} }

// bigLE is n as width little-endian two's complement bytes.
func bigLE(n *big.Int, width int) []byte {
	if n.Sign() < 0 {
		n = new(big.Int).Add(n, new(big.Int).Lsh(big.NewInt(1), uint(8*width)))
	}
	b := n.FillBytes(make([]byte, width))
	slices.Reverse(b)
	return b
}

// roundTrip encodes v and decodes it back.
func roundTrip(t *testing.T, e *Encoder, v any) any {
	t.Helper()
	meta, value, err := e.Encode(v)
	if err != nil {
		t.Fatalf("Encode(%#v): %v", v, err)
	}
	got, err := Decode(meta, value, buildDecimal)
	if err != nil {
		t.Fatalf("Decode(%#v): %v (% x, % x)", v, err, meta, value)
	}
	return got
}

func TestEncodeRoundTrip(t *testing.T) {
	e := &Encoder{Decimal: decimalHook}
	utc := func(y int, ns int) time.Time { return time.Date(y, 3, 4, 5, 6, 7, ns, time.UTC) }
	nested := map[string]any{
		"b": []any{int64(1), map[string]any{"z": "x", "a": []any{true, nil, map[string]any{}}}, []any{}},
		"a": map[string]any{"b": map[string]any{"b": float64(1.5)}},
	}
	cases := []any{
		nil, true, false, int64(0), int64(-1), 1.25, math.Inf(1), "", "short", strings.Repeat("s", 63),
		strings.Repeat("l", 64), strings.Repeat("é", 100), []byte{}, []byte{1, 2, 3},
		utc(2026, 0), utc(2026, 123000), utc(2026, 123456789), utc(1900, 1), time.UnixMicro(-5).UTC(),
		[]any{}, map[string]any{}, []any{[]any{[]any{}}}, map[string]any{"a": map[string]any{"b": map[string]any{}}},
		nested, []any{nested, nested},
		[]any{map[string]any{"k": int64(1), "j": "a"}, map[string]any{"k": int64(2), "j": "b"}, map[string]any{"k": nil}},
		testDecimal{[]byte{1, 0, 0, 0}, 0},
		testDecimal{bigLE(big.NewInt(-999999999), 4), 3},
		testDecimal{bigLE(big.NewInt(1000000000), 8), 38},
		testDecimal{bigLE(big.NewInt(-999999999999999999), 8), 2},
		testDecimal{bigLE(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil), 16), 5},
		testDecimal{bigLE(new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil), big.NewInt(1)), 16), 5},
		testDecimal{bigLE(new(big.Int).Neg(new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil), big.NewInt(1))), 16), 0},
	}
	for _, v := range cases {
		if got := roundTrip(t, e, v); !reflect.DeepEqual(got, v) {
			t.Errorf("%#v came back as %#v", v, got)
		}
	}
}

func TestEncodeIntegerWidths(t *testing.T) {
	e := &Encoder{}
	edges := []struct {
		n    int64
		size int // the bytes of the value, the header included
	}{
		{0, 2}, {127, 2}, {-128, 2}, {128, 3}, {-129, 3}, {32767, 3}, {-32768, 3}, {32768, 5}, {-32769, 5},
		{math.MaxInt32, 5}, {math.MinInt32, 5}, {math.MaxInt32 + 1, 9}, {math.MinInt32 - 1, 9},
		{math.MaxInt64, 9}, {math.MinInt64, 9},
	}
	for _, c := range edges {
		_, value, err := e.Encode(c.n)
		if err != nil || len(value) != c.size {
			t.Errorf("%d: %d bytes, %v, want %d", c.n, len(value), err, c.size)
		}
		if got := roundTrip(t, e, c.n); got != any(c.n) {
			t.Errorf("%d came back as %v", c.n, got)
		}
	}
}

func TestEncodeGoNumbers(t *testing.T) {
	e := &Encoder{}
	cases := []struct {
		in   any
		want any
	}{
		{int(-5), int64(-5)}, {int8(-8), int64(-8)}, {int16(-16), int64(-16)}, {int32(-32), int64(-32)},
		{int64(-64), int64(-64)}, {uint(5), int64(5)}, {uint8(250), int64(250)}, {uint16(65535), int64(65535)},
		{uint32(math.MaxUint32), int64(math.MaxUint32)}, {uint64(math.MaxInt64), int64(math.MaxInt64)},
		{float32(0.5), float64(0.5)}, {float64(0.1), float64(0.1)},
	}
	for _, c := range cases {
		if got := roundTrip(t, e, c.in); got != c.want {
			t.Errorf("%T(%v) came back as %T(%v)", c.in, c.in, got, got)
		}
	}
	for _, v := range []any{uint(math.MaxInt64) + 1, uint64(math.MaxUint64)} {
		if _, _, err := e.Encode(v); err == nil || !strings.Contains(err.Error(), "does not fit an int64") {
			t.Errorf("%T(%v): %v", v, v, err)
		}
	}
}

func TestEncodeSizes(t *testing.T) {
	e := &Encoder{Decimal: decimalHook}
	// 300 fields: a large object, 2 bytes of field ids, a dictionary of 2-byte offsets
	obj := map[string]any{}
	arr := make([]any, 300)
	for i := range arr {
		arr[i] = int64(i)
		obj[strings.Repeat("k", 1+i%250)+string(rune('a'+i%26))+string(rune('A'+i/26))] = int64(i)
	}
	// long keys: the offsets of the dictionary need 3 bytes
	obj[strings.Repeat("x", 70000)] = strings.Repeat("v", 300)
	big16 := strings.Repeat("b", 1<<24) // offsets of 4 bytes
	cases := []any{arr, obj, []any{obj, arr}, []any{big16, int64(1)}, map[string]any{"big": big16}}
	for _, v := range cases {
		if got := roundTrip(t, e, v); !reflect.DeepEqual(got, v) {
			t.Errorf("a value of %T did not round trip", v)
		}
	}
	meta, value, err := e.Encode(obj)
	if err != nil {
		t.Fatal(err)
	}
	if meta[0]>>6 != 2 { // offsets of 3 bytes
		t.Errorf("metadata header %#x", meta[0])
	}
	if value[0]>>2&1<<2 == 0 && value[0]>>6 == 0 { // the field ids of a large dictionary are not 1 byte
		t.Errorf("value header %#x", value[0])
	}
}

func TestEncodeMetadata(t *testing.T) {
	e := &Encoder{}
	meta, _, err := e.Encode(map[string]any{"b": nil, "a": map[string]any{"b": nil, "c": nil}})
	if err != nil {
		t.Fatal(err)
	}
	// version 1, sorted, offsets of one byte: 3 keys a, b, c, each of 1 byte
	if want := []byte{0x11, 3, 0, 1, 2, 3, 'a', 'b', 'c'}; !slices.Equal(meta, want) {
		t.Errorf("metadata % x, want % x", meta, want)
	}
	meta, value, err := e.Encode(int64(1))
	if err != nil || !slices.Equal(meta, []byte{0x11, 0, 0}) || !slices.Equal(value, []byte{pInt8 << 2, 1}) {
		t.Errorf("a scalar: % x, % x, %v", meta, value, err)
	}
	// the fields of an object are in the order of their keys
	_, value, err = e.Encode(map[string]any{"b": int64(2), "a": int64(1)})
	if want := []byte{basicObject | 0<<2, 2, 0, 1, 0, 2, 4, pInt8 << 2, 1, pInt8 << 2, 2}; err != nil || !slices.Equal(value, want) {
		t.Errorf("object % x, want % x, %v", value, want, err)
	}
}

func TestAppend(t *testing.T) {
	meta, value, err := Append([]byte("M"), []byte("V"), "x")
	if err != nil || string(meta[:1]) != "M" || string(value[:1]) != "V" {
		t.Fatalf("%q %q %v", meta, value, err)
	}
	got, err := Decode(meta[1:], value[1:], nil)
	if err != nil || got != "x" {
		t.Fatalf("decoded %v, %v", got, err)
	}
	meta, value, err = Append([]byte("M"), []byte("V"), struct{}{})
	if err == nil || string(meta) != "M" || string(value) != "V" {
		t.Fatalf("on an error: %q %q %v", meta, value, err)
	}
	if _, _, err = Append(nil, nil, testDecimal{[]byte{1}, 0}); err == nil {
		t.Fatal("a decimal needs an Encoder that knows it")
	}
}

func TestEncodeErrors(t *testing.T) {
	e := &Encoder{Decimal: decimalHook}
	deep := func(n int) any {
		var v any = int64(1)
		for range n {
			v = []any{v}
		}
		return v
	}
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"struct", struct{}{}, "struct {}"},
		{"chan", make(chan int), "chan int"},
		{"map of ints", map[int]any{1: 1}, "map[int]interface {}"},
		{"json.Number", json.Number("1"), "json.Number"},
		{"[]string", []string{"a"}, "[]string"},
		{"in an array", []any{int64(1), []any{struct{}{}}}, "element 1: element 0: a Go struct {}"},
		{"in an object", map[string]any{"k": map[string]any{"j": make(chan int)}}, `field "k": field "j": a Go chan int`},
		{"depth", deep(maxDepth + 1), "nested deeper than 128 levels"},
		{"time", time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC), "out of the range"},
		{"nanoseconds", time.Date(3000, 1, 1, 0, 0, 0, 1, time.UTC), "out of the range"},
		{"scale", testDecimal{[]byte{1}, 39}, "scale 39"},
		{"scale below", testDecimal{[]byte{1}, -1}, "scale -1"},
		{"no byte", testDecimal{nil, 0}, "0 bytes"},
		{"33 bytes", testDecimal{make([]byte, 33), 0}, "33 bytes"},
		{"39 digits", testDecimal{bigLE(new(big.Int).Exp(big.NewInt(10), big.NewInt(38), nil), 17), 0}, "more than 38 digits"},
	}
	for _, c := range cases {
		_, _, err := e.Encode(c.v)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		if err := e.Check(c.v); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Check: %v, want %q", c.name, err, c.want)
		}
	}
	// the deepest value that decodes
	if got := roundTrip(t, e, deep(maxDepth)); got == nil {
		t.Error("128 levels")
	}
}

func TestEncodeScalarsDoNotAllocate(t *testing.T) {
	e := &Encoder{}
	vals := []any{nil, true, int64(300), 1.5, "a string", []byte("bytes"), time.UnixMicro(7).UTC()}
	for _, v := range vals {
		_, _, _ = e.Encode(v) // grow the buffers
	}
	if n := testing.AllocsPerRun(100, func() {
		for _, v := range vals {
			_ = e.Check(v)
			_, _, _ = e.Encode(v)
		}
	}); n != 0 {
		t.Fatalf("%v allocations", n)
	}
}
