package vortex

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/fclairamb/vortex-go/internal/protowire"
)

// The statistics of the zones of a column: the aggregates the reference writer picks
// (vortex-layout/src/layouts/zoned/writer.rs), computed a block of rows at a time.

// statBound is the longest bound of a string or binary statistic, in bytes.
const statBound = 64

// statClass is how the values of a column compare.
type statClass uint8

const (
	scNone  statClass = iota // no min and max: the null count only
	scInt                    // signed integers, and the storage of dates, times and timestamps
	scUint                   // unsigned integers
	scFloat                  // half, single and double floats
	scBool                   //
	scDec                    // decimals
	scBytes                  // strings and binary, bounded
)

// classOf returns how the values of a column of type t compare.
func classOf(t Type) statClass {
	switch t.kind {
	case Bool:
		return scBool
	case KindDecimal:
		return scDec
	case Utf8, Binary:
		return scBytes
	case Float16, Float32, Float64:
		return scFloat
	case Int8, Int16, Int32, Int64, Date, Time, Timestamp:
		return scInt
	case Uint8, Uint16, Uint32, Uint64:
		return scUint
	}
	return scNone
}

// statWidth is the bytes of a min or a max of a column of type t, 0 for a column of strings.
func statWidth(t Type, class statClass) int {
	switch class {
	case scBool:
		return 1
	case scDec:
		return decimalWidth(t.precision)
	case scBytes, scNone:
		return 0
	}
	return numberWidth(storageKind(t))
}

// zoneAcc gathers the statistics of the zones of a column, one entry per block of rows.
type zoneAcc struct {
	nulls []uint64
	nans  []uint64
	has   []bool
	unk   []bool
	minB  []byte
	maxB  []byte
	minV  [][]byte
	maxV  [][]byte
	arena []byte
	ranger
	n int
}

// ranger finds the extremes of the rows of a buffer of one type.
type ranger struct {
	t     Type
	class statClass
	width int
}

func newRanger(t Type) ranger {
	class := classOf(t)
	return ranger{t: t, class: class, width: statWidth(t, class)}
}

func newZoneAcc(t Type) *zoneAcc { return &zoneAcc{ranger: newRanger(t)} }

// add records the statistics of the rows lo to hi of the buffer.
func (z *zoneAcc) add(b *body, lo, hi int) {
	z.n++
	z.nulls = append(z.nulls, countNulls(b, z.t, lo, hi))
	if z.class == scNone {
		return
	}
	imin, imax, nans := z.scan(b, lo, hi)
	z.has = append(z.has, imin >= 0)
	if z.class == scFloat {
		z.nans = append(z.nans, uint64(nans))
	}
	if z.class == scBytes {
		z.addBounds(b, imin, imax)
		return
	}
	z.addFixed(b, imin, imax)
}

// countNulls counts the null rows lo to hi.
func countNulls(b *body, t Type, lo, hi int) uint64 {
	if t.kind == Null {
		return uint64(hi - lo)
	}
	n := uint64(0)
	if t.nullable {
		for _, null := range b.nulls[lo:hi] {
			if null {
				n++
			}
		}
	}
	return n
}

// scan finds the smallest and the largest value of the valid rows lo to hi, as row numbers
// (-1 when there is none), and counts the NaNs, which are neither.
func (r *ranger) scan(b *body, lo, hi int) (imin, imax, nans int) {
	imin, imax = -1, -1
	for i := lo; i < hi; i++ {
		if isNull(b.nulls, i) {
			continue
		}
		if r.class == scFloat && math.IsNaN(rawFloat(b.raw, r.width, i)) {
			nans++
			continue
		}
		if imin < 0 {
			imin, imax = i, i
			continue
		}
		if r.order(b, i, imin) < 0 {
			imin = i
		}
		if r.order(b, i, imax) > 0 {
			imax = i
		}
	}
	return imin, imax, nans
}

// order compares the rows i and j of the buffer.
func (r *ranger) order(b *body, i, j int) int {
	w := r.width
	switch r.class {
	case scInt:
		return cmp.Compare(rawInt(b.raw, w, i), rawInt(b.raw, w, j))
	case scUint:
		return cmp.Compare(rawUint(b.raw, w, i), rawUint(b.raw, w, j))
	case scFloat:
		return cmp.Compare(rawFloat(b.raw, w, i), rawFloat(b.raw, w, j))
	case scBool:
		return cmp.Compare(boolInt(b.bools[i]), boolInt(b.bools[j]))
	case scDec:
		return b.decs[i].Cmp(b.decs[j])
	}
	return bytes.Compare(b.bins[i], b.bins[j])
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// rawInt reads the i-th w-byte signed integer of raw.
func rawInt(raw []byte, w, i int) int64 {
	switch w {
	case 1:
		return int64(int8(raw[i]))
	case 2:
		return int64(int16(binary.LittleEndian.Uint16(raw[2*i:])))
	case 4:
		return int64(int32(binary.LittleEndian.Uint32(raw[4*i:])))
	}
	return int64(binary.LittleEndian.Uint64(raw[8*i:]))
}

// rawUint reads the i-th w-byte unsigned integer of raw.
func rawUint(raw []byte, w, i int) uint64 {
	switch w {
	case 1:
		return uint64(raw[i])
	case 2:
		return uint64(binary.LittleEndian.Uint16(raw[2*i:]))
	case 4:
		return uint64(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	return binary.LittleEndian.Uint64(raw[8*i:])
}

// rawFloat reads the i-th w-byte float of raw: a half float, a float or a double.
func rawFloat(raw []byte, w, i int) float64 {
	switch w {
	case 2:
		return halfToFloat(binary.LittleEndian.Uint16(raw[2*i:]))
	case 4:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:])))
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(raw[8*i:]))
}

// halfToFloat converts the bits of an IEEE 754 half float.
func halfToFloat(h uint16) float64 {
	sign := 1.0
	if h&0x8000 != 0 {
		sign = -1
	}
	exp, frac := int(h>>10)&0x1f, float64(h&0x3ff)
	switch exp {
	case 0:
		return sign * math.Ldexp(frac, -24)
	case 0x1f:
		if frac != 0 {
			return math.NaN()
		}
		return math.Inf(int(sign))
	}
	return sign * math.Ldexp(1+frac/1024, exp-15)
}

// addFixed stores the min and the max of a zone, rows imin and imax of the buffer.
func (z *zoneAcc) addFixed(b *body, imin, imax int) {
	z.minB, z.maxB = z.putFixed(z.minB, b, imin), z.putFixed(z.maxB, b, imax)
}

func (r *ranger) putFixed(dst []byte, b *body, i int) []byte {
	w := r.width
	switch {
	case i < 0:
		return append(dst, zeros[:w]...)
	case r.class == scBool:
		return append(dst, byte(boolInt(b.bools[i])))
	case r.class == scDec:
		le := b.decs[i].le()
		return append(dst, le[:w]...)
	}
	return append(dst, b.raw[i*w:(i+1)*w]...)
}

// addBounds stores the bounded min and max of a zone of strings.
func (z *zoneAcc) addBounds(b *body, imin, imax int) {
	if imin < 0 {
		z.minV, z.maxV, z.unk = append(z.minV, nil), append(z.maxV, nil), append(z.unk, false)
		return
	}
	isUTF8 := z.t.kind == Utf8
	upper, ok := upperBound(b.bins[imax], isUTF8)
	z.minV = append(z.minV, copyBytes(&z.arena, lowerBound(b.bins[imin], isUTF8)))
	z.maxV = append(z.maxV, copyBytes(&z.arena, upper))
	z.unk = append(z.unk, !ok)
}

// cut is the length a value longer than statBound is cut to: a character boundary for utf8.
func cut(v []byte, isUTF8 bool) int {
	p := statBound
	for isUTF8 && p > statBound-3 && !utf8.RuneStart(v[p]) {
		p--
	}
	return p
}

// lowerBound is a value at most statBound bytes long that is not greater than v.
func lowerBound(v []byte, isUTF8 bool) []byte {
	if len(v) <= statBound {
		return v
	}
	return v[:cut(v, isUTF8)]
}

// upperBound is a value at most statBound bytes long that is not less than v. It is false
// when there is none: every byte of the cut value is 0xff (or every character is the last).
func upperBound(v []byte, isUTF8 bool) ([]byte, bool) {
	if len(v) <= statBound {
		return v, true
	}
	s := slices.Clone(v[:cut(v, isUTF8)])
	if isUTF8 {
		return incrementUTF8(s)
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i]++; s[i] != 0 {
			return s, true
		}
	}
	return nil, false
}

// incrementUTF8 returns the string that follows s: its last character, increased, and the
// characters before it. A character that cannot be increased is dropped.
func incrementUTF8(s []byte) ([]byte, bool) {
	for len(s) > 0 {
		r, size := utf8.DecodeLastRune(s)
		s = s[:len(s)-size]
		switch r {
		case 0xD7FF:
			r = 0xE000
		case utf8.MaxRune:
			continue
		default:
			r++
		}
		return utf8.AppendRune(s, r), true
	}
	return nil, false
}

// The identifiers of the aggregates of the statistics.
const (
	aggBoundedMax = "vortex.bounded_max"
	aggBoundedMin = "vortex.bounded_min"
	aggMax        = "vortex.max"
	aggMin        = "vortex.min"
	aggNullCount  = "vortex.null_count"
	aggNanCount   = "vortex.nan_count"
)

// aggSpec is an aggregate of the zone map: its identifier and its serialized options.
type aggSpec struct {
	id      string
	options []byte
}

var (
	optsSkipNaNs = []byte{0x08, 0x01}                        // NumericalAggregateOpts{skip_nans: true}
	optsBounded  = binary.LittleEndian.AppendUint64(nil, 64) // max_bytes
)

// table returns the zones table: a field and the values of every zone for each statistic the
// zones have, sorted by the name of the aggregate as the reference writer does, and the
// aggregates. An aggregate with no value in any zone is left out.
func (z *zoneAcc) table() ([]Field, []Data, []aggSpec) {
	var fields []Field
	var cols []Data
	var specs []aggSpec
	add := func(id string, options []byte, t Type, d Data) {
		fields = append(fields, Field{Name: id, Type: t})
		cols = append(cols, d)
		specs = append(specs, aggSpec{id, options})
	}
	nullable := z.t.AsNullable()
	var missing []bool
	if slices.Contains(z.has, false) {
		missing = make([]bool, z.n)
		for i, h := range z.has {
			missing[i] = !h
		}
	}
	anyHas := slices.Contains(z.has, true)
	switch {
	case z.class == scBytes:
		if anyHas || slices.Contains(z.unk, true) {
			add(aggBoundedMax, optsBounded, z.boundedMaxType(), z.boundedMax(missing))
		}
		if anyHas {
			add(aggBoundedMin, optsBounded, nullable, OfBytes(z.minV).Nulls(missing))
		}
	case z.class != scNone && anyHas:
		add(aggMax, optsSkipNaNs, nullable, z.fixed(z.maxB).Nulls(missing))
		add(aggMin, optsSkipNaNs, nullable, z.fixed(z.minB).Nulls(missing))
	}
	if z.class == scFloat {
		add(aggNanCount, nil, Uint64Type().AsNullable(), Of(z.nans))
	}
	add(aggNullCount, nil, Uint64Type().AsNullable(), Of(z.nulls))
	return fields, cols, specs
}

// fixed is the Data of the fixed-width values raw, one per zone.
func (z *zoneAcc) fixed(raw []byte) Data {
	w := z.width
	switch z.class {
	case scBool:
		v := make([]bool, z.n)
		for i := range v {
			v[i] = raw[i] != 0
		}
		return OfBools(v)
	case scDec:
		v := make([]Decimal, z.n)
		for i := range v {
			v[i] = decimalFromLE(raw[i*w : (i+1)*w])
		}
		return OfDecimals(v)
	}
	return Data{&body{kind: dPrim, pk: storageKind(z.t), n: z.n, raw: raw}}
}

// boundedMaxType is the type of the partial of a bounded max: a struct of the bound and a
// flag that says the bound is unknown, null for a zone with no value.
func (z *zoneAcc) boundedMaxType() Type {
	return StructOf(Field{"bound", z.t.AsNullable()}, Field{"unknown", BoolType()}).AsNullable()
}

func (z *zoneAcc) boundedMax(missing []bool) Data {
	var empty []bool
	if missing != nil || slices.Contains(z.unk, true) {
		empty = make([]bool, z.n)
		for i := range empty {
			empty[i] = !z.has[i] && !z.unk[i]
		}
	}
	bound := make([]bool, z.n)
	for i := range bound {
		bound[i] = !z.has[i] || z.unk[i]
	}
	return OfStruct(OfBytes(z.maxV).Nulls(bound), OfBools(z.unk)).Nulls(empty)
}

// zonedMetadata is the metadata of a zoned layout: a version byte, then the protobuf of the
// zone length and the aggregates.
func zonedMetadata(zoneLen int, specs []aggSpec) []byte {
	meta := protowire.AppendUint([]byte{zonedMetadataVersion}, 1, uint64(zoneLen))
	for _, s := range specs {
		spec := protowire.AppendBytes(nil, 1, []byte(s.id))
		if len(s.options) > 0 {
			spec = protowire.AppendBytes(spec, 2, s.options)
		}
		meta = protowire.AppendBytes(meta, 2, spec)
	}
	return meta
}
