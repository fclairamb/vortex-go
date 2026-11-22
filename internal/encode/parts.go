package encode

import (
	"math"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// DecimalBytes is the decimal byte parts encoding of decimals: raw holds the unscaled values,
// width bytes each (1, 2, 4, 8, 16 or 32), two's complement, little-endian. A decimal whose
// valid values all fit an i64 is written as the narrowest signed integer that holds them: the
// most significant part, with no lower part, and it carries the nulls. It returns false for a
// value that does not fit an i64, which would need lower parts, or a width that is not one.
func DecimalBytes(e *Encoder, raw []byte, width int, nulls []bool) (*Node, bool) {
	if width < 1 || width > 32 || width&(width-1) != 0 || len(raw)%width != 0 {
		return nil, false
	}
	rows := len(raw) / width
	lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
	for i := range rows {
		if isNull(nulls, i) {
			continue
		}
		v, ok := decimalInt64(raw[i*width:(i+1)*width], width)
		if !ok {
			return nil, false
		}
		lo, hi = min(lo, v), max(hi, v)
	}
	ptype, w := narrowSigned(lo, hi)
	out := e.allocDirty(rows * w)
	for i := range rows {
		v := int64(0)
		if !isNull(nulls, i) {
			v, _ = decimalInt64(raw[i*width:(i+1)*width], width)
		}
		putWidth(out[i*w:], w, uint64(v))
	}
	n := e.node(IDDecimalBytes)
	n.kid(e.leaf(out, w, nulls, rows))
	e.tmp = protowire.AppendUint(e.tmp[:0], 1, uint64(ptype))
	n.Meta = e.keep(e.tmp)
	return n, true
}

// decimalInt64 is the value of a decimal of width bytes, when it fits an i64.
func decimalInt64(b []byte, width int) (int64, bool) {
	v := int64(getWidth(b, min(width, 8)))
	if width < 8 {
		shift := 64 - 8*width
		return v << shift >> shift, true
	}
	ext := byte(0)
	if v < 0 {
		ext = 0xff
	}
	for _, x := range b[8:] {
		if x != ext {
			return 0, false
		}
	}
	return v, true
}

// The ticks of a second of the units of a timestamp.
const (
	TicksSecond = 1
	TicksMilli  = 1_000
	TicksMicro  = 1_000_000
	TicksNano   = 1_000_000_000
)

const secondsPerDay = 86_400

// DateTimeParts is the datetime parts encoding of the timestamps of v, counts of ticks (one of
// the Ticks constants) of a second since the epoch: the days, the seconds of the day and the
// subseconds, each the remainder of a division towards zero, so that those of a time before the
// epoch are negative. The days are the narrowest signed integer that holds the valid ones, and
// carry the nulls; the seconds and the subseconds are i32, the subseconds a constant 0 for a
// timestamp in seconds. It returns false for other ticks.
func DateTimeParts(e *Encoder, v []int64, nulls []bool, ticks int64) (*Node, bool) {
	if ticks != TicksSecond && ticks != TicksMilli && ticks != TicksMicro && ticks != TicksNano {
		return nil, false
	}
	n := len(v)
	lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
	for i, ts := range v {
		if !isNull(nulls, i) {
			d := ts / (secondsPerDay * ticks)
			lo, hi = min(lo, d), max(hi, d)
		}
	}
	ptype, dw := narrowSigned(lo, hi)
	days, secs, subs := e.allocDirty(n*dw), e.allocDirty(4*n), e.allocDirty(4*n)
	for i, ts := range v {
		d := ts / (secondsPerDay * ticks)
		total := ts / ticks
		putWidth(days[i*dw:], dw, uint64(d))
		putWidth(secs[4*i:], 4, uint64(total-d*secondsPerDay))
		putWidth(subs[4*i:], 4, uint64(ts-total*ticks))
	}
	node := e.node(IDDateTimeParts)
	node.kid(e.leaf(days, dw, nulls, n))
	node.kid(e.leaf(secs, 4, nil, n))
	if ticks == TicksSecond {
		zero := e.node(IDConstant)
		e.tmp = appendScalar(e.tmp[:0], int32(0))
		zero.add(e.keep(e.tmp), 1)
		node.kid(zero)
	} else {
		node.kid(e.leaf(subs, 4, nil, n))
	}
	e.tmp = protowire.AppendUint(e.tmp[:0], 1, uint64(ptype))
	e.tmp = protowire.AppendUint(e.tmp, 2, pI32)
	e.tmp = protowire.AppendUint(e.tmp, 3, pI32)
	node.Meta = e.keep(e.tmp)
	return node, true
}

// EstimateDecimalBytes is the size of DecimalBytes: the width of the narrowest integer that
// holds the sample.
func EstimateDecimalBytes(raw []byte, width int, nulls []bool, rows int) int {
	if width < 1 || width&(width-1) != 0 {
		return 0
	}
	lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
	for i := range len(raw) / width {
		if v, ok := decimalInt64(raw[i*width:(i+1)*width], width); ok && !isNull(nulls, i) {
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	_, w := narrowSigned(lo, hi)
	return rows*w + validBytes(nulls, rows)
}

// EstimateDateTimeParts is the size of DateTimeParts.
func EstimateDateTimeParts(v []int64, nulls []bool, rows int, ticks int64) int {
	lo, hi := int64(math.MaxInt64), int64(math.MinInt64)
	for i, ts := range v {
		if !isNull(nulls, i) {
			d := ts / (secondsPerDay * ticks)
			lo, hi = min(lo, d), max(hi, d)
		}
	}
	_, dw := narrowSigned(lo, hi)
	size := rows*dw + 4*rows + validBytes(nulls, rows)
	if ticks == TicksSecond {
		return size + 2 // the constant 0
	}
	return size + 4*rows
}

// narrowSigned is the PType number and the width of the narrowest signed integer that holds
// lo to hi, the i8 when there is no value (lo above hi).
func narrowSigned(lo, hi int64) (ptype, width int) {
	ptype, width = pI8, 1
	for width < 8 && lo <= hi && (lo < -1<<(8*width-1) || hi > 1<<(8*width-1)-1) {
		ptype, width = ptype+1, 2*width
	}
	return ptype, width
}
