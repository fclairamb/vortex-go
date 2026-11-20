// Package variant decodes the values of the Parquet Variant encoding
// (apache/parquet-format, VariantEncoding.md): a metadata dictionary of the object keys and a
// value, both byte strings. It knows nothing of Vortex.
package variant

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// maxDepth bounds the nesting of objects and arrays.
const maxDepth = 128

// DecimalFunc builds the Go value of a decimal: its unscaled value as little-endian two's
// complement bytes (4, 8 or 16 of them), and its scale.
type DecimalFunc func(unscaled []byte, scale int) any

// The basic types, in the two low bits of the header of a value.
const (
	basicPrimitive = iota
	basicShortString
	basicObject
	basicArray
)

// The primitive types, in the six high bits of the header of a primitive value.
const (
	pNull = iota
	pTrue
	pFalse
	pInt8
	pInt16
	pInt32
	pInt64
	pDouble
	pDecimal4
	pDecimal8
	pDecimal16
	pDate
	pTimestamp
	pTimestampNTZ
	pFloat
	pBinary
	pString
	pTimeNTZ
	pTimestampNanos
	pTimestampNanosNTZ
	pUUID
)

// Keys is the dictionary of the keys of the objects of a value: the metadata.
type Keys struct {
	data    []byte
	offsets []byte
	size    int // bytes of an offset
	count   int
}

// ParseMetadata reads the header, the offsets and the strings of a metadata. The version
// must be 1.
func ParseMetadata(b []byte) (Keys, error) {
	if len(b) == 0 {
		return Keys{}, errors.New("variant metadata is empty")
	}
	if v := b[0] & 0x0f; v != 1 {
		return Keys{}, fmt.Errorf("variant metadata of version %d, want 1", v)
	}
	size := int(b[0]>>6) + 1
	if len(b) < 1+size {
		return Keys{}, errors.New("variant metadata ends before its dictionary size")
	}
	count := readUint(b[1:], size)
	// The offsets are count+1 of them: check before multiplying.
	if count > (len(b)-1-size)/size-1 {
		return Keys{}, fmt.Errorf("variant metadata of %d bytes holds no %d keys", len(b), count)
	}
	offsets := b[1+size : 1+size+(count+1)*size]
	data := b[1+size+(count+1)*size:]
	k := Keys{data: data, offsets: offsets, size: size, count: count}
	prev := 0
	for i := 0; i <= count; i++ {
		o := readUint(offsets[i*size:], size)
		if o < prev || o > len(data) {
			return Keys{}, fmt.Errorf("variant metadata offset %d is %d, after %d, in %d bytes of keys", i, o, prev, len(data))
		}
		prev = o
	}
	return k, nil
}

// Len is the number of keys.
func (k Keys) Len() int { return k.count }

// Key returns key i. It is an error for i to be out of the dictionary.
func (k Keys) Key(i int) (string, error) {
	if i < 0 || i >= k.count {
		return "", fmt.Errorf("variant key %d is out of the %d keys of the dictionary", i, k.count)
	}
	lo := readUint(k.offsets[i*k.size:], k.size)
	hi := readUint(k.offsets[(i+1)*k.size:], k.size)
	return string(k.data[lo:hi]), nil
}

// readUint reads a little-endian integer of 1 to 4 bytes. b holds at least size bytes.
func readUint(b []byte, size int) int {
	v := 0
	for i := size - 1; i >= 0; i-- {
		v = v<<8 | int(b[i])
	}
	return v
}

// Decode decodes one value, whose keys are in metadata. It returns nil, bool, int64,
// float64, string, []byte, time.Time, []any, map[string]any, or what dec returns for a
// decimal. A time of day is a time.Time on 1970-01-01, a UUID its 16 bytes.
func Decode(metadata, value []byte, dec DecimalFunc) (any, error) {
	keys, err := ParseMetadata(metadata)
	if err != nil {
		return nil, err
	}
	return DecodeWith(keys, value, dec)
}

// DecodeWith is Decode with metadata already parsed.
func DecodeWith(keys Keys, value []byte, dec DecimalFunc) (any, error) {
	d := decoder{keys: keys, dec: dec}
	return d.value(value, 0)
}

type decoder struct {
	dec  DecimalFunc
	keys Keys
}

var errTruncated = errors.New("variant value is cut short")

// value decodes the value that starts at b[0].
func (d *decoder) value(b []byte, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("variant nested deeper than %d levels", maxDepth)
	}
	if len(b) == 0 {
		return nil, errTruncated
	}
	header := b[0] >> 2
	switch b[0] & 3 {
	case basicPrimitive:
		return d.primitive(header, b[1:])
	case basicShortString:
		n := int(header)
		if len(b) < 1+n {
			return nil, errTruncated
		}
		return string(b[1 : 1+n]), nil
	case basicObject:
		return d.object(header, b[1:], depth)
	default:
		return d.array(header, b[1:], depth)
	}
}

// fixed returns the first n bytes of b.
func fixed(b []byte, n int) ([]byte, error) {
	if len(b) < n {
		return nil, errTruncated
	}
	return b[:n], nil
}

func (d *decoder) primitive(id byte, b []byte) (any, error) {
	switch id {
	case pNull:
		return nil, nil
	case pTrue:
		return true, nil
	case pFalse:
		return false, nil
	case pDecimal4, pDecimal8, pDecimal16:
		return d.decimal(id, b)
	case pBinary, pString:
		return lengthPrefixed(id, b)
	}
	size, ok := fixedSizes[id]
	if !ok {
		return nil, fmt.Errorf("variant primitive type %d is unknown", id)
	}
	raw, err := fixed(b, size)
	if err != nil {
		return nil, err
	}
	return fixedValue(id, raw), nil
}

// fixedSizes is the size of the value of each primitive type that has a fixed one.
var fixedSizes = map[byte]int{
	pInt8: 1, pInt16: 2, pInt32: 4, pInt64: 8, pDouble: 8, pDate: 4, pTimestamp: 8,
	pTimestampNTZ: 8, pFloat: 4, pTimeNTZ: 8, pTimestampNanos: 8, pTimestampNanosNTZ: 8,
	pUUID: 16,
}

// fixedValue converts the bytes of a value of a type of fixedSizes.
func fixedValue(id byte, raw []byte) any {
	var u uint64
	for i := len(raw) - 1; i >= 0 && len(raw) <= 8; i-- {
		u = u<<8 | uint64(raw[i])
	}
	switch id {
	case pInt8:
		return int64(int8(u))
	case pInt16:
		return int64(int16(u))
	case pInt32:
		return int64(int32(u))
	case pInt64:
		return int64(u)
	case pDouble:
		return math.Float64frombits(u)
	case pFloat:
		return float64(math.Float32frombits(uint32(u)))
	case pDate:
		return time.Unix(int64(int32(u))*86400, 0).UTC()
	case pTimestampNanos, pTimestampNanosNTZ:
		return time.Unix(0, int64(u)).UTC()
	case pUUID:
		return append([]byte(nil), raw...)
	}
	// The timestamps and the time of day, in microseconds.
	return time.UnixMicro(int64(u)).UTC()
}

func (d *decoder) decimal(id byte, b []byte) (any, error) {
	width := 4 << (id - pDecimal4)
	raw, err := fixed(b, 1+width)
	if err != nil {
		return nil, err
	}
	if raw[0] > 38 {
		return nil, fmt.Errorf("variant decimal of scale %d", raw[0])
	}
	if d.dec == nil {
		return nil, errors.New("variant decimal and no way to build one")
	}
	return d.dec(append([]byte(nil), raw[1:]...), int(raw[0])), nil
}

// lengthPrefixed reads a binary or a string: a uint32 length, then the bytes.
func lengthPrefixed(id byte, b []byte) (any, error) {
	raw, err := fixed(b, 4)
	if err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(raw)
	if uint64(len(b)-4) < uint64(n) {
		return nil, errTruncated
	}
	body := b[4 : 4+n]
	if id == pString {
		return string(body), nil
	}
	return slices.Clone(body), nil
}

// count reads the number of elements of an object or an array, and returns the rest.
func count(b []byte, large bool) (int, []byte, error) {
	size := 1
	if large {
		size = 4
	}
	if len(b) < size {
		return 0, nil, errTruncated
	}
	return readUint(b, size), b[size:], nil
}

// offsetsOf splits b into n offsets of size bytes and the values that follow, which end at
// the last offset.
func offsetsOf(b []byte, n, size int) (offs, values []byte, err error) {
	if n > len(b)/size {
		return nil, nil, errTruncated
	}
	offs, values = b[:n*size], b[n*size:]
	end := readUint(offs[(n-1)*size:], size)
	if end > len(values) {
		return nil, nil, fmt.Errorf("variant offset %d is past the %d bytes of values", end, len(values))
	}
	return offs, values[:end], nil
}

func (d *decoder) object(header byte, b []byte, depth int) (any, error) {
	offSize, idSize, large := int(header&3)+1, int(header>>2&3)+1, header>>4&1 == 1
	n, b, err := count(b, large)
	if err != nil {
		return nil, err
	}
	if n > len(b)/idSize {
		return nil, errTruncated
	}
	ids, b := b[:n*idSize], b[n*idSize:]
	offs, values, err := offsetsOf(b, n+1, offSize)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, n)
	for i := range n {
		key, err := d.keys.Key(readUint(ids[i*idSize:], idSize))
		if err != nil {
			return nil, err
		}
		v, err := d.element(values, readUint(offs[i*offSize:], offSize), depth)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		out[key] = v
	}
	return out, nil
}

func (d *decoder) array(header byte, b []byte, depth int) (any, error) {
	offSize, large := int(header&3)+1, header>>2&1 == 1
	n, b, err := count(b, large)
	if err != nil {
		return nil, err
	}
	offs, values, err := offsetsOf(b, n+1, offSize)
	if err != nil {
		return nil, err
	}
	out := make([]any, n)
	for i := range out {
		if out[i], err = d.element(values, readUint(offs[i*offSize:], offSize), depth); err != nil {
			return nil, fmt.Errorf("element %d: %w", i, err)
		}
	}
	return out, nil
}

// element decodes the value at offset off of the values of an object or an array.
func (d *decoder) element(values []byte, off, depth int) (any, error) {
	if off >= len(values) {
		return nil, fmt.Errorf("variant offset %d is past the %d bytes of values", off, len(values))
	}
	return d.value(values[off:], depth+1)
}
