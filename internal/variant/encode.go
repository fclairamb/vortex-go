package variant

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"slices"
	"time"
)

// DecimalOf recognizes the decimal values of the caller: it returns the unscaled value as
// little-endian two's complement bytes (at most 32 of them) and the scale of v, and false for a
// value that is not a decimal.
type DecimalOf func(v any) (unscaled []byte, scale int, ok bool)

// Encoder turns Go values into the metadata and the value of a Variant row, the inverse of
// Decode. The zero Encoder takes no decimal. It reuses its buffers: after the first rows it
// allocates only for what a row holds besides its scalars. It is not safe for concurrent use,
// except for Check.
type Encoder struct {
	// Decimal recognizes the decimals; nil for none.
	Decimal DecimalOf

	meta, val, hdr []byte
	dict, keys     []string
	offs, ids      []int
}

// emptyMetadata is the metadata of a value that has no object: version 1, sorted, offsets of
// one byte, no key.
var emptyMetadata = []byte{1 | 1<<4, 0, 0}

// The ranges of the timestamps, in seconds: the ones whose microseconds, and whose
// nanoseconds, fit an int64.
const (
	maxMicroSec = math.MaxInt64 / 1_000_000
	maxNanoSec  = math.MaxInt64 / 1_000_000_000
)

// Append is Encoder.Encode, the metadata and the value appended to meta and value. It takes no
// decimal. On an error it returns meta and value as they were.
func Append(meta, value []byte, v any) (outMeta, outValue []byte, err error) {
	var e Encoder
	m, val, err := e.Encode(v)
	if err != nil {
		return meta, value, err
	}
	return append(meta, m...), append(value, val...), nil
}

// Encode returns the metadata and the value of v, which is nil, a bool, a Go integer or float
// of any width, a string, a []byte, a time.Time, a decimal the Encoder knows, a []any or a
// map[string]any of these. Integers take the narrowest width, the keys of the objects go in a
// sorted dictionary, and the fields of an object are in the order of their keys. The slices
// are valid until the next call.
func (e *Encoder) Encode(v any) (meta, value []byte, err error) {
	e.dict = e.dict[:0]
	if scanErr := e.scan(v, 0, true); scanErr != nil {
		return nil, nil, scanErr
	}
	slices.Sort(e.dict)
	e.dict = slices.Compact(e.dict)
	e.metadata()
	e.val = e.val[:0]
	e.value(v)
	return e.meta, e.val, nil
}

// Check returns the error that Encode would, without building anything. It may be called
// concurrently.
func (e *Encoder) Check(v any) error { return e.scan(v, 0, false) }

// scan checks the value, and gathers the keys of its objects into the dictionary when asked.
func (e *Encoder) scan(v any, depth int, keys bool) error {
	if depth > maxDepth {
		return fmt.Errorf("variant nested deeper than %d levels", maxDepth)
	}
	switch x := v.(type) {
	case nil, bool, int64, float64, float32, string, []byte:
		return nil
	case time.Time:
		if _, _, ok := timestamp(x); !ok {
			return fmt.Errorf("the time %s is out of the range of a timestamp", x.Format(time.RFC3339Nano))
		}
	case []any:
		for i, el := range x {
			if err := e.scan(el, depth+1, keys); err != nil {
				return fmt.Errorf("element %d: %w", i, err)
			}
		}
	case map[string]any:
		for k, el := range x {
			if keys {
				e.dict = append(e.dict, k)
			}
			if err := e.scan(el, depth+1, keys); err != nil {
				return fmt.Errorf("field %q: %w", k, err)
			}
		}
	default:
		return e.scanOther(v)
	}
	return nil
}

// scanOther checks a value that is not one of the common types: another integer, a decimal.
func (e *Encoder) scanOther(v any) error {
	if _, ok, err := integer(v); ok || err != nil {
		return err
	}
	if e.Decimal != nil {
		if raw, scale, ok := e.Decimal(v); ok {
			_, _, err := decimalOf(raw, scale)
			return err
		}
	}
	return fmt.Errorf("a Go %T cannot be a variant value", v)
}

// integer returns v as an int64 when it is a Go integer. ok is false for another type, and
// err says that an unsigned integer is too large.
func integer(v any) (n int64, ok bool, err error) {
	var u uint64
	switch x := v.(type) {
	case int:
		return int64(x), true, nil
	case int8:
		return int64(x), true, nil
	case int16:
		return int64(x), true, nil
	case int32:
		return int64(x), true, nil
	case uint8:
		return int64(x), true, nil
	case uint16:
		return int64(x), true, nil
	case uint32:
		return int64(x), true, nil
	case uint:
		u = uint64(x)
	case uint64:
		u = x
	default:
		return 0, false, nil
	}
	if u > math.MaxInt64 {
		return 0, true, fmt.Errorf("the integer %d does not fit an int64", u)
	}
	return int64(u), true, nil
}

// timestamp is the primitive type, and the count of units, of a time: microseconds, or
// nanoseconds when the time has a finer part. It is not ok for a time out of the range.
func timestamp(t time.Time) (id byte, n int64, ok bool) {
	sec := t.Unix()
	if t.Nanosecond()%1000 == 0 {
		return pTimestamp, t.UnixMicro(), sec >= -maxMicroSec && sec <= maxMicroSec
	}
	return pTimestampNanos, t.UnixNano(), sec >= -maxNanoSec && sec <= maxNanoSec
}

// decimalOf checks a decimal and picks its primitive type: the narrowest of 4, 8 and 16 bytes
// whose precision (9, 18 and 38 digits) holds it. It returns the unscaled value.
func decimalOf(raw []byte, scale int) (id byte, v *big.Int, err error) {
	if scale < 0 || scale > 38 {
		return 0, nil, fmt.Errorf("a decimal of scale %d, want 0 to 38", scale)
	}
	if len(raw) == 0 || len(raw) > 32 {
		return 0, nil, fmt.Errorf("a decimal of %d bytes, want 1 to 32", len(raw))
	}
	be := slices.Clone(raw)
	slices.Reverse(be)
	v = new(big.Int).SetBytes(be)
	if raw[len(raw)-1]&0x80 != 0 {
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), uint(8*len(raw))))
	}
	abs := new(big.Int).Abs(v)
	for i, digits := range [...]int64{9, 18, 38} {
		if abs.Cmp(new(big.Int).Exp(big.NewInt(10), big.NewInt(digits), nil)) < 0 {
			return byte(pDecimal4 + i), v, nil
		}
	}
	return 0, nil, errors.New("a decimal of more than 38 digits")
}

// width is the bytes of the narrowest unsigned integer, 1 to 4, that holds v.
func width(v int) int {
	switch {
	case v < 1<<8:
		return 1
	case v < 1<<16:
		return 2
	case v < 1<<24:
		return 3
	}
	return 4
}

// putUint appends v as size little-endian bytes.
func putUint(b []byte, v uint64, size int) []byte {
	for range size {
		b = append(b, byte(v))
		v >>= 8
	}
	return b
}

// metadata builds the metadata of the dictionary.
func (e *Encoder) metadata() {
	if len(e.dict) == 0 {
		e.meta = append(e.meta[:0], emptyMetadata...)
		return
	}
	total := 0
	for _, k := range e.dict {
		total += len(k)
	}
	size := width(max(len(e.dict), total))
	e.meta = append(e.meta[:0], 1|1<<4|byte(size-1)<<6)
	e.meta = putUint(e.meta, uint64(len(e.dict)), size)
	pos := 0
	for _, k := range e.dict {
		e.meta = putUint(e.meta, uint64(pos), size)
		pos += len(k)
	}
	e.meta = putUint(e.meta, uint64(pos), size)
	for _, k := range e.dict {
		e.meta = append(e.meta, k...)
	}
}

// value appends the encoding of v, which scan checked.
func (e *Encoder) value(v any) {
	switch x := v.(type) {
	case nil:
		e.val = append(e.val, pNull<<2)
	case bool:
		id := byte(pFalse)
		if x {
			id = pTrue
		}
		e.val = append(e.val, id<<2)
	case int64:
		e.int(x)
	case float64:
		e.double(x)
	case float32:
		e.double(float64(x))
	case string:
		appendText(e, pString, x)
	case []byte:
		appendText(e, pBinary, x)
	case time.Time:
		id, n, _ := timestamp(x)
		e.val = putUint(append(e.val, id<<2), uint64(n), 8)
	case []any:
		e.array(x)
	case map[string]any:
		e.object(x)
	default:
		e.other(v)
	}
}

// other appends an integer of another type, or a decimal.
func (e *Encoder) other(v any) {
	if n, ok, _ := integer(v); ok {
		e.int(n)
		return
	}
	raw, scale, _ := e.Decimal(v)
	id, n, _ := decimalOf(raw, scale)
	size := 4 << (id - pDecimal4)
	if n.Sign() < 0 {
		n.Add(n, new(big.Int).Lsh(big.NewInt(1), uint(8*size)))
	}
	le := n.FillBytes(make([]byte, size))
	slices.Reverse(le)
	e.val = append(append(e.val, id<<2, byte(scale)), le...)
}

// int appends an integer in the narrowest primitive type.
func (e *Encoder) int(n int64) {
	switch {
	case n >= math.MinInt8 && n <= math.MaxInt8:
		e.val = putUint(append(e.val, pInt8<<2), uint64(n), 1)
	case n >= math.MinInt16 && n <= math.MaxInt16:
		e.val = putUint(append(e.val, pInt16<<2), uint64(n), 2)
	case n >= math.MinInt32 && n <= math.MaxInt32:
		e.val = putUint(append(e.val, pInt32<<2), uint64(n), 4)
	default:
		e.val = putUint(append(e.val, pInt64<<2), uint64(n), 8)
	}
}

func (e *Encoder) double(f float64) {
	e.val = putUint(append(e.val, pDouble<<2), math.Float64bits(f), 8)
}

// text appends a string or a binary: a string of up to 63 bytes is a short string.
func appendText[S string | []byte](e *Encoder, id byte, s S) {
	if id == pString && len(s) < 64 {
		e.val = append(append(e.val, byte(len(s))<<2|basicShortString), s...)
		return
	}
	e.val = append(putUint(append(e.val, id<<2), uint64(len(s)), 4), s...)
}

// container puts the header of an object or an array before its n elements, which are the
// bytes of the value from start, whose offsets (relative to start) are offs and, for an
// object, whose key ids are ids.
func (e *Encoder) container(start, n int, offs, ids []int, isObject bool) {
	body := len(e.val) - start
	offSize := width(body)
	large := n > math.MaxUint8
	size := 1
	if large {
		size = 4
	}
	idSize := width(len(e.dict) - 1)
	h := e.hdr[:0]
	if isObject {
		h = append(h, (byte(offSize-1)|byte(idSize-1)<<2|b2b(large)<<4)<<2|basicObject)
	} else {
		h = append(h, (byte(offSize-1)|b2b(large)<<2)<<2|basicArray)
	}
	h = putUint(h, uint64(n), size)
	for _, id := range ids {
		h = putUint(h, uint64(id), idSize)
	}
	for _, o := range offs {
		h = putUint(h, uint64(o), offSize)
	}
	h = putUint(h, uint64(body), offSize)
	e.hdr = h
	e.val = append(e.val, h...)
	copy(e.val[start+len(h):], e.val[start:start+body])
	copy(e.val[start:], h)
}

func b2b(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func (e *Encoder) array(a []any) {
	start, base := len(e.val), len(e.offs)
	for _, el := range a {
		e.offs = append(e.offs, len(e.val)-start)
		e.value(el)
	}
	e.container(start, len(a), e.offs[base:], nil, false)
	e.offs = e.offs[:base]
}

func (e *Encoder) object(m map[string]any) {
	start, base, kbase, ibase := len(e.val), len(e.offs), len(e.keys), len(e.ids)
	for k := range m {
		e.keys = append(e.keys, k)
	}
	keys := e.keys[kbase:]
	slices.Sort(keys)
	for _, k := range keys {
		id, _ := slices.BinarySearch(e.dict, k)
		e.ids = append(e.ids, id)
		e.offs = append(e.offs, len(e.val)-start)
		e.value(m[k])
	}
	e.container(start, len(keys), e.offs[base:], e.ids[ibase:], true)
	e.offs, e.keys, e.ids = e.offs[:base], e.keys[:kbase], e.ids[:ibase]
}
