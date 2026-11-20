package vortex

import (
	"encoding/binary"
	"math"

	"github.com/fclairamb/vortex-go/internal/array"
)

// zoneTable is the statistics of the zones of one zoned layout of a column. A missing
// statistic is nil. The bounds of a zone are the exact min and max when the file has them,
// otherwise the bounded ones, which only bound the values.
type zoneTable struct {
	min, max   *array.Array
	nulls      *array.Array
	nans       *array.Array
	start      int64 // the row of the file where the layout starts
	zoneLen    int64
	rows       int64
	exactBound bool // min and max are the exact values
}

// The statistics of the legacy vortex.stats layout, by bit of its bitset: the table has one
// field per bit set, in this order, and a flag field after Max and Min.
const (
	statIsConstant = iota
	statIsSorted
	statIsStrictSorted
	statMax
	statMin
	statSum
	statNullCount
	statUncompressedSize
	statNanCount
)

// zoneField is a field of a zones table: what it holds and its type.
type zoneField struct {
	stat string // "min", "max", "bounded_min", "bounded_max", "null_count", "nan_count" or ""
	t    array.DType
}

// nullable returns t with its nullability set.
func nullable(t array.DType) array.DType {
	t.Nullable = true
	if t.Storage != nil {
		s := *t.Storage
		s.Nullable = true
		t.Storage = &s
	}
	return t
}

// The statistics a zone table field can hold.
const (
	statMinName       = "min"
	statMaxName       = "max"
	statBoundedMin    = "bounded_min"
	statBoundedMax    = "bounded_max"
	statNullCountName = "null_count"
	statNanCountName  = "nan_count"
)

var counterType = array.DType{Kind: array.Prim, PType: array.U64, Nullable: true}

// zoneFields lists the fields of the zones table of a zone map for a column of type t. It
// returns false when the table cannot be typed, which means the column has no statistics.
func (z *zoneMap) zoneFields(t array.DType) ([]zoneField, bool) {
	if z.legacy {
		return z.legacyFields(t)
	}
	isFloat := t.Kind == array.Prim && t.PType.Float()
	var out []zoneField
	for _, a := range z.aggregates {
		switch a.id {
		case "vortex.min", "vortex.max", "vortex.bounded_min":
			out = append(out, zoneField{a.id[len("vortex."):], nullable(t)})
		case "vortex.bounded_max": // the partial state: the bound, and whether it is unknown
			st := array.DType{Kind: array.Struct, Nullable: true, Fields: []array.Field{
				{Name: "bound", Type: nullable(t)}, {Name: "unknown", Type: array.DType{Kind: array.Bool}},
			}}
			out = append(out, zoneField{statBoundedMax, st})
		case "vortex.null_count":
			out = append(out, zoneField{statNullCountName, counterType})
		case "vortex.nan_count":
			if isFloat {
				out = append(out, zoneField{statNanCountName, counterType})
			}
		default:
			return nil, false
		}
	}
	return out, true
}

// legacyFields is zoneFields for a vortex.stats layout: one field per bit of the bitset, in
// the order of the bits, with a flag field after the max and the min.
func (z *zoneMap) legacyFields(t array.DType) ([]zoneField, bool) {
	isFloat := t.Kind == array.Prim && t.PType.Float()
	flag := array.DType{Kind: array.Bool}
	var out []zoneField
	for bit := 0; bit < 8*len(z.stats); bit++ {
		if z.stats[bit/8]&(1<<(bit%8)) == 0 {
			continue
		}
		switch bit {
		case statIsConstant, statIsSorted, statIsStrictSorted:
			out = append(out, zoneField{"", nullable(flag)})
		case statMax:
			out = append(out, zoneField{statMaxName, nullable(t)}, zoneField{"", flag})
		case statMin:
			out = append(out, zoneField{statMinName, nullable(t)}, zoneField{"", flag})
		case statNullCount:
			out = append(out, zoneField{statNullCountName, counterType})
		case statUncompressedSize:
			out = append(out, zoneField{"", counterType})
		case statNanCount:
			if isFloat {
				out = append(out, zoneField{statNanCountName, counterType})
			}
		case statSum:
			if sum, ok := sumType(t); ok {
				out = append(out, zoneField{"", nullable(sum)})
			}
		default:
			return nil, false
		}
	}
	return out, true
}

// sumType is the type of the sum statistic of a column of type t, when it has one.
func sumType(t array.DType) (array.DType, bool) {
	if t.Kind != array.Prim {
		return t, false
	}
	switch {
	case t.PType.Float():
		t.PType = array.F64
	case t.PType.Signed():
		t.PType = array.I64
	default:
		t.PType = array.U64
	}
	return t, true
}

// zoneTable reads the statistics of the zones of the zoned layout l, whose column has type t
// and whose first row is start. It returns false when there are none to use: they are only a
// hint, so a table that cannot be read or typed is the same as no table.
func (f *File) zoneTable(l *layoutNode, z *zoneMap, t array.DType, start int64) (zoneTable, bool) {
	tbl := zoneTable{start: start, zoneLen: int64(z.zoneLen), rows: int64(l.rowCount)}
	fields, ok := z.zoneFields(t)
	if !ok || z.zoneLen == 0 {
		return tbl, false
	}
	st := array.DType{Kind: array.Struct}
	for i, fld := range fields {
		st.Fields = append(st.Fields, array.Field{Name: string(rune('a' + i)), Type: fld.t})
	}
	rd, err := f.newReader(z.zones, st, array.Options{}, "", 0, int(z.zones.rowCount))
	if err != nil {
		return tbl, false
	}
	arr, err := readAll(rd, st)
	if err != nil || int64(arr.Len) != (tbl.rows+tbl.zoneLen-1)/tbl.zoneLen || len(arr.Fields) != len(fields) {
		return tbl, false
	}
	tbl.fill(fields, arr)
	return tbl, true
}

// fill sets the statistics of the table from the fields of the zones table.
func (tbl *zoneTable) fill(fields []zoneField, arr *array.Array) {
	var exactMin, exactMax bool
	for i, fld := range fields {
		a := arr.Fields[i]
		switch fld.stat {
		case statMinName:
			tbl.min, exactMin = a, true
		case statMaxName:
			tbl.max, exactMax = a, true
		case statBoundedMin:
			if tbl.min == nil {
				tbl.min = a
			}
		case statBoundedMax:
			if tbl.max == nil {
				tbl.max = boundOf(a)
			}
		case statNullCountName:
			tbl.nulls = a
		case statNanCountName:
			tbl.nans = a
		}
	}
	tbl.exactBound = exactMin && exactMax
}

// boundOf is the bound of a bounded_max statistic, a struct of the bound and a flag. A row
// where the struct is null (no value) has no bound.
func boundOf(s *array.Array) *array.Array {
	b := *s.Fields[0]
	if s.Validity != nil {
		w := make([]byte, (s.Len+7)/8)
		for i := range s.Len {
			if !s.Null(i) && !b.Null(i) {
				w[i/8] |= 1 << (i % 8)
			}
		}
		b.Validity = &array.Bitmap{Bytes: w}
	}
	return &b
}

// leafZones finds the zone tables of the column whose layout is l, starting at row start.
func (f *File) leafZones(l *layoutNode, t array.DType, start int64, rows [2]int64, out *[]zoneTable) {
	if start+int64(l.rowCount) <= rows[0] || start >= rows[1] {
		return // wholly outside the rows of the scan: its statistics are never needed
	}
	switch f.layoutEncodings[l.encoding] {
	case layoutChunked:
		for _, k := range l.children {
			f.leafZones(k, t, start, rows, out)
			start += int64(k.rowCount)
		}
	case layoutZoned, layoutStats:
		z, err := parseZoneLayout(l, f.layoutEncodings[l.encoding] == layoutStats)
		if err != nil {
			return
		}
		if tbl, ok := f.zoneTable(l, &z, t, start); ok {
			*out = append(*out, tbl)
		}
	}
}

// loadZones finds the statistics of the column of the leaf in the layout tree: the layout of
// the column is a child of the root struct, then of structs for a nested field.
func (f *File) loadZones(root *layoutNode, rootType Type, lf *leaf, rows [2]int64) {
	if lf.op == opIsNull && len(lf.path) > 0 {
		return // a null parent makes a row null, which the statistics of the field do not say
	}
	l, t := root, rootType
	idx := append([]int{lf.field}, lf.path...)
	for _, i := range idx {
		if f.layoutEncodings[l.encoding] != layoutStruct {
			return
		}
		first := 0
		if t.nullable {
			first = 1
		}
		l, t = l.children[first+i], t.fields[i].Type
	}
	f.leafZones(l, arrayType(t), 0, rows, &lf.tables)
}

// uintAt reads the unsigned statistic of zone k, when it is there.
func uintAt(a *array.Array, k int) (uint64, bool) {
	if a == nil || a.Null(k) || a.Type.Kind != array.Prim || a.Type.PType != array.U64 {
		return 0, false
	}
	return binary.LittleEndian.Uint64(a.Data[8*k:]), true
}

// bounds says what the statistics of a zone leave possible for the rows that meet a leaf.
// A row is true or false for the leaf, or unknown when it is null.
type bounds struct {
	canTrue, canFalse bool
}

// at gives the bounds of the zone of row pos, and the row where the zone ends (the next
// zone starts, when pos is not in a zone). Without statistics anything can happen.
func (l *leaf) at(pos int64) (bounds, int64) {
	for l.cur < len(l.tables) && l.tables[l.cur].start+l.tables[l.cur].rows <= pos {
		l.cur++
	}
	if l.cur == len(l.tables) {
		return bounds{true, true}, math.MaxInt64
	}
	tbl := &l.tables[l.cur]
	if pos < tbl.start {
		return bounds{true, true}, tbl.start
	}
	k := (pos - tbl.start) / tbl.zoneLen
	end := min(tbl.start+(k+1)*tbl.zoneLen, tbl.start+tbl.rows)
	return l.zoneBounds(tbl, int(k), end-tbl.start-k*tbl.zoneLen), end
}

// zoneBounds is the verdict of the statistics of zone k, which has n rows.
func (l *leaf) zoneBounds(tbl *zoneTable, k int, n int64) bounds {
	nulls, haveNulls := uintAt(tbl.nulls, k)
	if l.op == opIsNull {
		return bounds{!haveNulls || nulls > 0, !haveNulls || int64(nulls) < n}
	}
	if haveNulls && int64(nulls) == n { // every comparison is unknown
		return bounds{}
	}
	return l.valueBounds(tbl, k)
}

// valueBounds is the verdict of the min, the max and the NaN count of zone k.
func (l *leaf) valueBounds(tbl *zoneTable, k int) bounds {
	if tbl.min == nil || tbl.max == nil || tbl.min.Null(k) || tbl.max.Null(k) {
		return bounds{true, true}
	}
	nans, haveNans := uintAt(tbl.nans, k)
	nan := l.float && (!haveNans || nans > 0)
	inside, constant, lo, hi, ok := l.against(tbl, k)
	switch {
	case !ok:
		return bounds{true, true}
	case l.op == opEq || l.op == opIn:
		return bounds{inside, nan || !constant}
	case l.op == opNe:
		return bounds{nan || !constant, inside}
	}
	return orderBounds(l.op, lo, hi, nan)
}

// orderBounds is the verdict of an ordering comparison, given the order of the min (lo) and
// of the max (hi) against the literal, and whether a NaN can be in the zone: it is neither
// less nor greater than anything.
func orderBounds(op exprOp, lo, hi int, nan bool) bounds {
	switch op {
	case opLt:
		return bounds{lo < 0, hi >= 0 || nan}
	case opLe:
		return bounds{lo <= 0, hi > 0 || nan}
	case opGt:
		return bounds{hi > 0, lo <= 0 || nan}
	}
	return bounds{hi >= 0, lo < 0 || nan}
}

// against compares the bounds of zone k with the literals: whether one of them can be in
// the zone, whether the zone can only hold one of them, and the order of the bounds against
// the first literal (lo for the min, hi for the max). It is not ok when a comparison is
// unordered.
func (l *leaf) against(tbl *zoneTable, k int) (inside, constant bool, lo, hi int, ok bool) {
	for i := range l.lits {
		c1, ok1 := l.compare(tbl.min, k, &l.lits[i])
		c2, ok2 := l.compare(tbl.max, k, &l.lits[i])
		if !ok1 || !ok2 {
			return false, false, 0, 0, false
		}
		if i == 0 {
			lo, hi = c1, c2
		}
		inside = inside || c1 <= 0 && c2 >= 0
		constant = constant || tbl.exactBound && c1 == 0 && c2 == 0
	}
	return inside, constant, lo, hi, true
}

// bounds says what the zones leave possible for the node over the rows from pos, and the
// row where that stops being valid.
func (n *node) bounds(pos int64) (bounds, int64) {
	switch n.op {
	case opKeep:
		return bounds{true, false}, math.MaxInt64
	case opNot:
		b, limit := n.kids[0].bounds(pos)
		return bounds{b.canFalse, b.canTrue}, limit
	case opAnd, opOr:
		// And can be true when every kid can, false when one can. Or is the other way round.
		out, limit := bounds{n.op == opAnd, n.op == opOr}, int64(math.MaxInt64)
		for _, k := range n.kids {
			b, lim := k.bounds(pos)
			limit = min(limit, lim)
			if n.op == opAnd {
				out = bounds{out.canTrue && b.canTrue, out.canFalse || b.canFalse}
			} else {
				out = bounds{out.canTrue || b.canTrue, out.canFalse && b.canFalse}
			}
		}
		return out, limit
	}
	return n.leaf.at(pos)
}

// loadZones reads the statistics of the columns of the leaves of the node.
func (n *node) loadZones(f *File, root *layoutNode, rows [2]int64) {
	if n.leaf != nil {
		f.loadZones(root, f.schema.root, n.leaf, rows)
	}
	for _, k := range n.kids {
		k.loadZones(f, root, rows)
	}
}
