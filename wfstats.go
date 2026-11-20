package vortex

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"math"

	"github.com/fclairamb/vortex-go/internal/flatbuf"
	"github.com/fclairamb/vortex-go/internal/scalar"
)

// The statistics of an array (on the root node of each flat chunk) and of the file (in the
// segment the postscript points to).

// astat is the content of an ArrayStats table.
type astat struct {
	nans     *uint64
	sorted   *bool
	constant *bool
	min      []byte
	max      []byte
	nulls    uint64
	minExact bool
	maxExact bool
}

// table writes the ArrayStats flatbuffer.
func (s *astat) table(b *flatbuf.Builder) flatbuf.Off {
	var lo, hi flatbuf.Off
	if s.min != nil {
		lo = b.Bytes(s.min)
	}
	if s.max != nil {
		hi = b.Bytes(s.max)
	}
	b.StartTable(11)
	if lo != 0 {
		b.Ref(0, lo)
		b.Uint8(1, boolByte(s.minExact), 0)
	}
	if hi != 0 {
		b.Ref(2, hi)
		b.Uint8(3, boolByte(s.maxExact), 0)
	}
	optBool := func(id int, v *bool) {
		if v != nil {
			b.Uint8(id, boolByte(*v), 2)
		}
	}
	optBool(5, s.sorted)
	optBool(7, s.constant)
	b.Uint64(8, s.nulls, s.nulls+1)
	if s.nans != nil {
		b.Uint64(10, *s.nans, *s.nans+1)
	}
	return b.EndTable()
}

// rootID is the encoding of the root node of the array of a column of type t.
func rootID(t Type) string {
	switch t.kind {
	case Null:
		return "vortex.null"
	case Bool:
		return "vortex.bool"
	case Utf8, Binary:
		return "vortex.varbinview"
	case KindDecimal:
		return "vortex.decimal"
	case List:
		return "vortex.listview"
	case Map:
		return idMap
	case FixedSizeList:
		return idFixedSizeList
	case UUID, Date, Time, Timestamp:
		return "vortex.ext"
	case Variant:
		return idVariant
	}
	return "vortex.primitive"
}

// valueAt is the ScalarValue of the value whose width bytes are elem.
func (r *ranger) valueAt(elem []byte) *scalar.Value {
	switch r.class {
	case scInt:
		return &scalar.Value{Kind: scalar.Int, Int: rawInt(elem, r.width, 0)}
	case scUint:
		return &scalar.Value{Kind: scalar.Uint, Uint: rawUint(elem, r.width, 0)}
	case scFloat:
		switch r.width {
		case 2:
			return &scalar.Value{Kind: scalar.F16, Uint: rawUint(elem, 2, 0)}
		case 4:
			return &scalar.Value{Kind: scalar.F32, F32: math.Float32frombits(binary.LittleEndian.Uint32(elem))}
		}
		return &scalar.Value{Kind: scalar.F64, F64: math.Float64frombits(binary.LittleEndian.Uint64(elem))}
	case scBool:
		return &scalar.Value{Kind: scalar.Bool, Bool: elem[0] != 0}
	}
	return &scalar.Value{Kind: scalar.Bytes, Bytes: elem}
}

// uuidValue is the ScalarValue of a uuid: the list of its bytes, the storage of the extension.
func uuidValue(v []byte) *scalar.Value {
	out := &scalar.Value{Kind: scalar.List, List: make([]scalar.Value, len(v))}
	for i, c := range v {
		out.List[i] = scalar.Value{Kind: scalar.Uint, Uint: uint64(c)}
	}
	return out
}

// bytesValue is the ScalarValue of a string or of binary.
func (r *ranger) bytesValue(v []byte) *scalar.Value {
	if r.t.kind == Utf8 {
		return &scalar.Value{Kind: scalar.String, Str: string(v)}
	}
	return &scalar.Value{Kind: scalar.Bytes, Bytes: v}
}

// compare orders two stored values, the width bytes of a number, a bool or a decimal.
func (r *ranger) compare(a, b []byte) int {
	switch r.class {
	case scInt:
		return cmp.Compare(rawInt(a, r.width, 0), rawInt(b, r.width, 0))
	case scUint:
		return cmp.Compare(rawUint(a, r.width, 0), rawUint(b, r.width, 0))
	case scFloat:
		return cmp.Compare(rawFloat(a, r.width, 0), rawFloat(b, r.width, 0))
	case scDec:
		return decimalFromLE(a).Cmp(decimalFromLE(b))
	}
	return bytes.Compare(a, b)
}

func encodeScalar(v *scalar.Value) []byte {
	out, _ := scalar.Encode(v) // it fails on a string that is not UTF-8: the writer checked them
	return out
}

// arrayStats writes the ArrayStats of the rows b of the column, and returns it.
func (w *Writer) arrayStats(c *column, b *body) flatbuf.Off {
	n := b.n
	s := astat{nulls: countNulls(b, c.t, 0, n)}
	r := &c.ranger
	imin, imax, nans := -1, -1, 0
	if r.class != scNone {
		imin, imax, nans = r.scan(b, 0, n)
	}
	if r.class == scFloat {
		v := uint64(nans)
		s.nans = &v
	}
	if imin >= 0 && r.timeOK(r.putFixed(nil, b, imin), r.putFixed(nil, b, imax)) {
		r.minMax(&s, b, imin, imax)
	}
	if r.class != scNone && s.nulls == 0 && nans == 0 && n > 0 {
		constant, sorted := r.order(b, imin, imax) == 0, true
		for i := 1; i < n && sorted; i++ {
			sorted = r.order(b, i-1, i) <= 0
		}
		s.constant, s.sorted = &constant, &sorted
	}
	return s.table(w.fb)
}

// minMax sets the min and the max of the stats from the rows imin and imax of the buffer. A
// string is cut to the bounds of the zones, and marked inexact when it was.
func (r *ranger) minMax(s *astat, b *body, imin, imax int) {
	if r.class == scUUID {
		s.min, s.max = encodeScalar(uuidValue(b.bins[imin])), encodeScalar(uuidValue(b.bins[imax]))
		s.minExact, s.maxExact = true, true
		return
	}
	if r.class != scBytes {
		s.min = encodeScalar(r.valueAt(r.putFixed(nil, b, imin)))
		s.max = encodeScalar(r.valueAt(r.putFixed(nil, b, imax)))
		s.minExact, s.maxExact = true, true
		return
	}
	isUTF8 := r.t.kind == Utf8
	s.min = encodeScalar(r.bytesValue(lowerBound(b.bins[imin], isUTF8)))
	s.minExact = len(b.bins[imin]) <= statBound
	if upper, ok := upperBound(b.bins[imax], isUTF8); ok {
		s.max = encodeScalar(r.bytesValue(upper))
		s.maxExact = len(b.bins[imax]) <= statBound
	}
}

// colStat is what the zones of a column say of the whole column.
type colStat struct {
	min, max   *scalar.Value // nil: no value
	maxUnknown bool          // a string column with a zone that has no upper bound
	nulls      uint64
	nans       uint64
}

// reduce folds the zones of a column into the statistics of the column.
func (z *zoneAcc) reduce() colStat {
	var s colStat
	for _, n := range z.nulls {
		s.nulls += n
	}
	for _, n := range z.nans {
		s.nans += n
	}
	var lo, hi []byte
	for i, has := range z.has {
		if z.class == scBytes && z.unk[i] {
			s.maxUnknown = true
		}
		if !has {
			continue
		}
		minV, maxV := z.stored(z.minB, z.minV, i), z.stored(z.maxB, z.maxV, i)
		if lo == nil || z.compare(minV, lo) < 0 {
			lo = minV
		}
		if hi == nil || z.compare(maxV, hi) > 0 {
			hi = maxV
		}
	}
	if lo == nil || !z.timeOK(lo, hi) {
		return s
	}
	s.min = z.stat(lo)
	if !s.maxUnknown {
		s.max = z.stat(hi)
	}
	return s
}

// timeOK tells whether the bounds lo and hi, as stored, may be written as scalars. The
// reference reader rejects a time scalar that is not within its day ("adding duration to
// time overflowed") and then fails to open the file: so a time column whose values leave
// [0, 1 day) gets no min and no max in the array and file statistics (the zones table holds
// plain integers, and keeps its bounds). Reproduced with vortex-data 0.93.0 on type_time_*.
func (r *ranger) timeOK(lo, hi []byte) bool {
	if r.t.kind != Time {
		return true
	}
	day := int64(86400)
	switch r.t.unit {
	case Millisecond:
		day *= 1000
	case Microsecond:
		day *= 1000 * 1000
	case Nanosecond:
		day *= 1000 * 1000 * 1000
	}
	return rawInt(lo, r.width, 0) >= 0 && rawInt(hi, r.width, 0) < day
}

// stored is the min or the max of zone i: the bytes of a number, or the bound of a string.
func (z *zoneAcc) stored(fixed []byte, varlen [][]byte, i int) []byte {
	if z.class == scBytes || z.class == scUUID {
		return varlen[i]
	}
	return fixed[i*z.width : (i+1)*z.width]
}

func (r *ranger) stat(v []byte) *scalar.Value {
	if r.class == scUUID {
		return uuidValue(v)
	}
	if r.class == scBytes {
		return r.bytesValue(v)
	}
	return r.valueAt(v)
}

// fstat is an aggregate of the file statistics, and its value for one entry.
type fstat struct {
	aggSpec
	partial []byte
}

func uintValue(v uint64) []byte { return encodeScalar(&scalar.Value{Kind: scalar.Uint, Uint: v}) }

// aggregates lists the aggregates of the entry of a column: the bounds, then the counts.
func (s colStat) aggregates(r *ranger) []fstat {
	var out []fstat
	if r.class == scBytes {
		if s.max != nil || s.maxUnknown {
			bound := scalar.Value{Kind: scalar.Null}
			if s.max != nil {
				bound = *s.max
			}
			partial := &scalar.Value{Kind: scalar.List, List: []scalar.Value{
				bound, {Kind: scalar.Bool, Bool: s.maxUnknown}}}
			out = append(out, fstat{aggSpec{aggBoundedMax, optsBounded}, encodeScalar(partial)})
		}
		if s.min != nil {
			out = append(out, fstat{aggSpec{aggBoundedMin, optsBounded}, encodeScalar(s.min)})
		}
	} else if s.min != nil {
		out = append(out, fstat{aggSpec{aggMax, optsSkipNaNs}, encodeScalar(s.max)},
			fstat{aggSpec{aggMin, optsSkipNaNs}, encodeScalar(s.min)})
	}
	out = append(out, fstat{aggSpec{id: aggNullCount}, uintValue(s.nulls)})
	if r.class == scFloat {
		out = append(out, fstat{aggSpec{id: aggNanCount}, uintValue(s.nans)})
	}
	return out
}

// legacy is the ArrayStats of a top-level field of the file statistics.
func (s colStat) legacy(r *ranger) astat {
	a := astat{nulls: s.nulls}
	if s.min != nil {
		a.min, a.minExact = encodeScalar(s.min), r.class != scBytes
	}
	if s.max != nil {
		a.max, a.maxExact = encodeScalar(s.max), r.class != scBytes
	}
	if r.class == scFloat {
		a.nans = &s.nans
	}
	return a
}

// fileEntries walks the layout in post-order, as the reference does for the file statistics:
// an entry for each column, and one after the fields of a nullable struct for its own null
// count. A column under a nullable struct has no statistics.
func fileEntries(l *layoutBuild, out *[][]fstat) {
	if !l.isStruct {
		var set []fstat
		if c := l.col; c.zones != nil && c.rows > 0 {
			set = c.zones.reduce().aggregates(&c.ranger)
		}
		*out = append(*out, set)
		return
	}
	for _, k := range l.kids {
		fileEntries(k, out)
	}
	if l.valid != nil {
		*out = append(*out, []fstat{{aggSpec{id: aggNullCount}, uintValue(l.valid.nulls)}})
	}
}

// legacyStats are the ArrayStats of the top-level fields, for the readers that predate the
// aggregates.
func legacyStats(root *layoutBuild) []astat {
	out := make([]astat, len(root.kids))
	for i, k := range root.kids {
		switch {
		case k.isStruct && k.valid != nil:
			out[i] = astat{nulls: k.valid.nulls}
		case !k.isStruct && k.col.zones != nil && k.col.rows > 0:
			out[i] = k.col.zones.reduce().legacy(&k.col.ranger)
		}
	}
	return out
}

// fileAggs is the aggregates of the file statistics: the dictionary of aggregates, the
// dictionary of sets, the set of each entry and the partial states, each behind its length.
type fileAggs struct {
	specs     []aggSpec
	sets      [][]uint16
	entrySets []uint16
	partials  []byte
}

func newFileAggs(entries [][]fstat) *fileAggs {
	a := &fileAggs{entrySets: make([]uint16, len(entries))}
	for i, set := range entries {
		ids := make([]uint16, len(set))
		for j, f := range set {
			ids[j] = aggIndex(&a.specs, f.aggSpec)
			a.partials = binary.AppendUvarint(a.partials, uint64(len(f.partial)))
			a.partials = append(a.partials, f.partial...)
		}
		a.entrySets[i] = setIndex(&a.sets, ids)
	}
	return a
}

// tables writes the AggregateSpec and the AggregateSet tables.
func (a *fileAggs) tables(b *flatbuf.Builder) (specs, sets flatbuf.Off) {
	specOffs := make([]flatbuf.Off, len(a.specs))
	for i, s := range a.specs {
		id := b.String(s.id)
		var opts flatbuf.Off
		if len(s.options) > 0 {
			opts = b.Bytes(s.options)
		}
		b.StartTable(2)
		b.Ref(0, id)
		if opts != 0 {
			b.Ref(1, opts)
		}
		specOffs[i] = b.EndTable()
	}
	setOffs := make([]flatbuf.Off, len(a.sets))
	for i, ids := range a.sets {
		v := b.Uint16s(ids)
		b.StartTable(1)
		b.Ref(0, v)
		setOffs[i] = b.EndTable()
	}
	return b.Offsets(specOffs), b.Offsets(setOffs)
}

// fileStatsTable writes the FileStatistics: the legacy field statistics, then the aggregates
// of every entry.
func (w *Writer) fileStatsTable() flatbuf.Off {
	b := w.fb
	legacy := legacyStats(w.root)
	legacyOffs := make([]flatbuf.Off, len(legacy))
	for i := range legacy {
		legacyOffs[i] = legacy[i].table(b)
	}
	if w.legacyFile { // an edition before the aggregates: the legacy statistics only
		fieldStats := b.Offsets(legacyOffs)
		b.StartTable(5)
		b.Ref(0, fieldStats)
		return b.EndTable()
	}
	var entries [][]fstat
	fileEntries(w.root, &entries)
	aggs := newFileAggs(entries)
	specs, sets := aggs.tables(b)
	fieldStats := b.Offsets(legacyOffs)
	entryVec, partialVec := b.Uint16s(aggs.entrySets), b.Bytes(aggs.partials)
	b.StartTable(5)
	b.Ref(0, fieldStats)
	b.Ref(1, specs)
	b.Ref(2, sets)
	b.Ref(3, entryVec)
	b.Ref(4, partialVec)
	return b.EndTable()
}

// aggIndex returns the index of the aggregate in the dictionary, adding it when it is not there.
func aggIndex(specs *[]aggSpec, s aggSpec) uint16 {
	for i, o := range *specs {
		if o.id == s.id && bytes.Equal(o.options, s.options) {
			return uint16(i)
		}
	}
	*specs = append(*specs, s)
	return uint16(len(*specs) - 1)
}

// setIndex returns the index of the list of aggregates in the dictionary of sets.
func setIndex(sets *[][]uint16, ids []uint16) uint16 {
	for i, o := range *sets {
		if len(o) == len(ids) && (len(ids) == 0 || bytes.Equal(u16Bytes(o), u16Bytes(ids))) {
			return uint16(i)
		}
	}
	*sets = append(*sets, ids)
	return uint16(len(*sets) - 1)
}

func u16Bytes(v []uint16) []byte {
	out := make([]byte, 0, 2*len(v))
	for _, x := range v {
		out = binary.LittleEndian.AppendUint16(out, x)
	}
	return out
}
