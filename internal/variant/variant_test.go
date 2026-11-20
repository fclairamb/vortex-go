package variant

import (
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// le is v as size little-endian bytes.
func le(v uint64, size int) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return b[:size]
}

// metadata encodes a dictionary, with offsets of offSize bytes.
func metadata(offSize int, keys ...string) []byte {
	out := []byte{byte(1 | (offSize-1)<<6)}
	out = append(out, le(uint64(len(keys)), offSize)...)
	pos := 0
	var data []byte
	for _, k := range keys {
		out = append(out, le(uint64(pos), offSize)...)
		pos += len(k)
		data = append(data, k...)
	}
	out = append(out, le(uint64(pos), offSize)...)
	return append(out, data...)
}

func prim(id byte, payload ...byte) []byte { return append([]byte{id << 2}, payload...) }

func short(s string) []byte { return append([]byte{byte(len(s))<<2 | basicShortString}, s...) }

// join concatenates the values and returns them with the offsets of each, and the end.
func join(vals ...[]byte) (body []byte, offs []int) {
	for _, v := range vals {
		offs = append(offs, len(body))
		body = append(body, v...)
	}
	return body, append(offs, len(body))
}

func encodeOffsets(offs []int, size int) []byte {
	var out []byte
	for _, o := range offs {
		out = append(out, le(uint64(o), size)...)
	}
	return out
}

// object encodes an object of the key ids and values, with the given widths.
func object(ids []int, vals [][]byte, offSize, idSize int, large bool) []byte {
	header := byte(offSize-1) | byte(idSize-1)<<2
	numSize := 1
	if large {
		header |= 1 << 4
		numSize = 4
	}
	out := []byte{header<<2 | basicObject}
	out = append(out, le(uint64(len(ids)), numSize)...)
	for _, id := range ids {
		out = append(out, le(uint64(id), idSize)...)
	}
	body, offs := join(vals...)
	out = append(out, encodeOffsets(offs, offSize)...)
	return append(out, body...)
}

func array(vals [][]byte, offSize int, large bool) []byte {
	header := byte(offSize - 1)
	numSize := 1
	if large {
		header |= 1 << 2
		numSize = 4
	}
	out := []byte{header<<2 | basicArray}
	out = append(out, le(uint64(len(vals)), numSize)...)
	body, offs := join(vals...)
	out = append(out, encodeOffsets(offs, offSize)...)
	return append(out, body...)
}

var emptyMeta = metadata(1)

func dec(unscaled []byte, scale int) any { return fmt.Sprintf("dec(%x,%d)", unscaled, scale) }

func TestPrimitives(t *testing.T) {
	day := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	micros := day.UnixMicro() + 1_500_000
	cases := []struct {
		name string
		in   []byte
		want any
	}{
		{"null", prim(pNull), nil},
		{"true", prim(pTrue), true},
		{"false", prim(pFalse), false},
		{"int8", prim(pInt8, 0xfe), int64(-2)},
		{"int16", prim(pInt16, 0x00, 0x80), int64(-32768)},
		{"int32", prim(pInt32, le(math.MaxInt32, 4)...), int64(math.MaxInt32)},
		{"int64", prim(pInt64, le(1<<63, 8)...), int64(math.MinInt64)},
		{"double", prim(pDouble, le(math.Float64bits(1.5), 8)...), 1.5},
		{"float", prim(pFloat, le(uint64(math.Float32bits(-0.25)), 4)...), -0.25},
		{"decimal4", prim(pDecimal4, append([]byte{2}, le(12345, 4)...)...), "dec(39300000,2)"},
		{"decimal8", prim(pDecimal8, append([]byte{0}, le(7, 8)...)...), "dec(0700000000000000,0)"},
		{"decimal16", prim(pDecimal16, append([]byte{38}, make([]byte, 16)...)...), "dec(00000000000000000000000000000000,38)"},
		{"date", prim(pDate, le(uint64(day.Unix()/86400), 4)...), day},
		{"timestamp", prim(pTimestamp, le(uint64(micros), 8)...), time.UnixMicro(micros).UTC()},
		{"timestamp ntz", prim(pTimestampNTZ, le(uint64(micros), 8)...), time.UnixMicro(micros).UTC()},
		{"time", prim(pTimeNTZ, le(3_600_000_000, 8)...), time.Unix(3600, 0).UTC()},
		{"timestamp nanos", prim(pTimestampNanos, le(1_000_000_123, 8)...), time.Unix(1, 123).UTC()},
		{"timestamp nanos ntz", prim(pTimestampNanosNTZ, le(5, 8)...), time.Unix(0, 5).UTC()},
		{"binary", prim(pBinary, append(le(3, 4), 1, 2, 3)...), []byte{1, 2, 3}},
		{"empty binary", prim(pBinary, le(0, 4)...), []byte{}},
		{"string", prim(pString, append(le(2, 4), 'h', 'i')...), "hi"},
		{"short string", short("héllo"), "héllo"},
		{"empty short string", short(""), ""},
		{"uuid", prim(pUUID, []byte("0123456789abcdef")...), []byte("0123456789abcdef")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Decode(emptyMeta, c.in, dec)
			if err != nil || !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Decode = %#v, %v, want %#v", got, err, c.want)
			}
		})
	}
}

func TestNested(t *testing.T) {
	meta := metadata(1, "a", "bc", "d")
	inner := array([][]byte{prim(pTrue), prim(pNull), short("x")}, 1, false)
	// Field ids point at the dictionary: sorted by key name, offsets in any order.
	obj := object([]int{0, 1, 2}, [][]byte{prim(pInt8, 7), inner, object(nil, nil, 1, 1, false)}, 1, 1, false)
	want := map[string]any{
		"a":  int64(7),
		"bc": []any{true, nil, "x"},
		"d":  map[string]any{},
	}
	got, err := Decode(meta, obj, nil)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode = %#v, %v, want %#v", got, err, want)
	}
	// The same, with the widest offsets, ids and counts.
	obj = object([]int{0, 1, 2}, [][]byte{prim(pInt8, 7), inner, object(nil, nil, 4, 4, true)}, 4, 4, true)
	if got, err = Decode(meta, obj, nil); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("wide Decode = %#v, %v, want %#v", got, err, want)
	}
	// An array with large counts and a null inside, an empty array.
	arr := array([][]byte{prim(pNull), array(nil, 2, true), short("")}, 2, true)
	if got, err = Decode(emptyMeta, arr, nil); err != nil || !reflect.DeepEqual(got, []any{nil, []any{}, ""}) {
		t.Fatalf("array Decode = %#v, %v", got, err)
	}
}

func TestObjectOf300Keys(t *testing.T) {
	const n = 300
	keys := make([]string, n)
	ids := make([]int, n)
	vals := make([][]byte, n)
	want := map[string]any{}
	for i := range keys {
		keys[i] = fmt.Sprintf("key%03d", i)
		ids[i] = i
		vals[i] = prim(pInt16, le(uint64(i), 2)...)
		want[keys[i]] = int64(i)
	}
	// 300 keys need ids of 2 bytes, a count of 4 bytes, and the offsets are over 255.
	got, err := Decode(metadata(2, keys...), object(ids, vals, 2, 2, true), nil)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode = %v, %v", got, err)
	}
	k, err := ParseMetadata(metadata(2, keys...))
	if err != nil || k.Len() != n {
		t.Fatalf("ParseMetadata = %v, %v", k, err)
	}
}

func TestDamage(t *testing.T) {
	meta := metadata(1, "a", "b")
	value := object([]int{0, 1}, [][]byte{
		array([][]byte{prim(pInt32, le(1, 4)...), short("abc")}, 1, false),
		prim(pString, append(le(3, 4), 'x', 'y', 'z')...),
	}, 1, 1, false)
	if _, err := Decode(meta, value, nil); err != nil {
		t.Fatalf("the whole value: %v", err)
	}
	for n := range len(value) {
		if _, err := Decode(meta, value[:n], nil); err == nil {
			t.Errorf("value cut at %d bytes decoded", n)
		}
	}
	for n := range len(meta) {
		if _, err := Decode(meta[:n], value, nil); err == nil {
			t.Errorf("metadata cut at %d bytes decoded", n)
		}
	}
}

func TestDamagedValues(t *testing.T) {
	meta := metadata(1, "a")
	deep := prim(pNull)
	for range maxDepth + 2 {
		deep = array([][]byte{deep}, 2, false)
	}
	fine := prim(pNull)
	for range maxDepth - 1 {
		fine = array([][]byte{fine}, 2, false)
	}
	cases := []struct {
		name  string
		meta  []byte
		value []byte
		want  string
	}{
		{"key id past the dictionary", meta, object([]int{1}, [][]byte{prim(pTrue)}, 1, 1, false), "variant key 1 is out of the 1 keys"},
		{"offset past the values", meta, []byte{basicObject | 0, 1, 0, 0, 9, 1}, "past the 1 bytes of values"},
		{"element offset past the values", meta, []byte{basicArray, 1, 0, 1}, "variant offset 1 is past"},
		{"element offset of an object past the values", meta, []byte{basicObject, 1, 0, 3, 1, 5}, "variant offset 3 is past"},
		{"count past the data", meta, []byte{basicArray, 200, 0}, "cut short"},
		{"object count past the data", meta, []byte{basicObject, 200, 0}, "cut short"},
		{"nested too deep", meta, deep, "nested deeper than"},
		{"unknown primitive", meta, []byte{31 << 2}, "primitive type 31 is unknown"},
		{"cut short string", meta, []byte{5<<2 | basicShortString, 'a'}, "cut short"},
		{"cut decimal", meta, prim(pDecimal8, 0, 1), "cut short"},
		{"cut string length", meta, prim(pString, 1, 0), "cut short"},
		{"array without a count", meta, []byte{basicArray}, "cut short"},
		{"array offset past the values", meta, []byte{basicArray, 1, 0, 9}, "past the 0 bytes of values"},
		{"no value", meta, nil, "cut short"},
		{"cut object field", meta, object([]int{0}, [][]byte{{pInt8 << 2}}, 1, 1, false), `field "a": variant value is cut short`},
		{"cut array element", meta, array([][]byte{{pInt8 << 2}}, 1, false), "element 0"},
		{"decimal scale", meta, prim(pDecimal4, 39, 0, 0, 0, 0), "decimal of scale 39"},
		{"decimal without a builder", meta, prim(pDecimal4, 0, 0, 0, 0, 0), "no way to build one"},
		{"string length", meta, prim(pString, le(1<<31, 4)...), "cut short"},
		{"no metadata", nil, prim(pNull), "metadata is empty"},
		{"metadata version", []byte{2, 0, 0}, prim(pNull), "version 2"},
		{"metadata without size", []byte{1}, prim(pNull), "dictionary size"},
		{"metadata keys past the data", []byte{1, 5, 0, 0}, prim(pNull), "holds no 5 keys"},
		{"metadata offsets going back", []byte{1, 2, 0, 2, 1, 'a', 'b'}, prim(pNull), "after"},
		{"metadata offset past the keys", []byte{1, 1, 0, 9}, prim(pNull), "in 0 bytes of keys"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			build := dec
			if c.name == "decimal without a builder" {
				build = nil
			}
			_, err := Decode(c.meta, c.value, build)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Decode error = %v, want %q", err, c.want)
			}
		})
	}
	if _, err := Decode(meta, fine, nil); err != nil {
		t.Fatalf("a value at the depth limit: %v", err)
	}
}

func TestKeyOutOfRange(t *testing.T) {
	k, err := ParseMetadata(metadata(1, "a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Key(-1); err == nil {
		t.Fatal("Key(-1) succeeded")
	}
	if s, err := k.Key(0); err != nil || s != "a" {
		t.Fatalf("Key(0) = %q, %v", s, err)
	}
}
