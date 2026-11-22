package vortex

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/vortex-go/internal/array"
)

// Rows reads the rows of the file that meet the query into values of T, a struct, batch by
// batch, and yields them one by one. The columns it reads come from the fields of T, so the
// query only selects rows: the scan is the one of File.Scan.
//
// A field takes the column named by its tag, vortex:"name", else its own name. The tag
// vortex:"-" skips the field, vortex:"name,optional" leaves the field at its zero value when
// the file has no such column. Unexported fields are skipped and embedded structs are
// flattened. The Go type of a field must be the one the column type calls for, with no
// widening:
//
//   - bool, the numbers (int and uint are 64 bits), and float32 for a f16 column;
//   - vortex.Decimal for a decimal, or int64 when its precision is 18 or less;
//   - string (a copy, see below) or []byte (a view, valid in the loop body) for utf8, []byte
//     for binary;
//   - time.Time for a date and a timestamp (UTC unless the type has a zone), time.Duration
//     since midnight for a time, or their storage integer;
//   - [16]byte for a uuid;
//   - a struct for a struct, []X for a list, [N]X or []X for a fixed-size list, map[K]V for a
//     map, and any for a variant (see Column.Variant).
//
// A nullable column needs sql.Null[X] or *X; a column that is not nullable takes those and
// the plain type. A column of type null has no Go type: leave its field out.
//
// A field whose type is a number, a bool, a decimal, a time, a uuid, a string or a []byte,
// plain or in sql.Null, is filled without allocating for the row. Pointers, slices, maps and
// variants allocate for each row.
//
// The strings of such a field share one block per batch, a copy of their bytes that the
// garbage collector owns, never the memory of the scan or of a mapped file: they stay valid
// after the loop. The block is at most the bytes of the strings of that field in the batch
// (8192 rows at most), and one string kept keeps its whole block alive: use strings.Clone to
// keep a few strings of many batches.
//
// A field that does not fit its column, a missing column, a T that is not a struct, and
// every error of Scan, come back as the error of the iteration, and the loop ends. A field
// problem is found before any data is read.
func Rows[T any](ctx context.Context, f *File, q Query) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		p, err := newRowPlan(f, reflect.TypeFor[T]())
		if err != nil {
			yield(zero, err)
			return
		}
		var rows []T
		for b, err := range f.Scan(ctx, q, p.columns...) {
			if err == nil {
				if cap(rows) < b.Len() {
					rows = make([]T, b.Len())
				}
				rows = rows[:b.Len()]
				err = p.fill(b, reflect.ValueOf(rows))
			}
			if err != nil {
				yield(zero, err)
				return
			}
			for _, r := range rows {
				if !yield(r, nil) {
					return
				}
			}
		}
	}
}

// decoder sets dst, an addressable value, to row i of a column.
type decoder func(c *Column, i int, dst reflect.Value) error

// fastFn fills a field of every row of a batch from a column.
type fastFn func(c *Column, rows reflect.Value) error

// rowCell is one field of T, or the nested struct it is in, read from one column.
type rowCell struct {
	fast fastFn
	dec  decoder
	sub  []int // the struct fields to go through from that column
	path []int // the field of T, for dec
	col  int   // the column of the scan
}

// rowPlan is how to turn the batches of a scan into rows of T. It is made once, before any
// read.
type rowPlan struct {
	rt      reflect.Type
	columns []string
	cells   []rowCell
}

// bindError is an error that already names its field.
type bindError struct{ msg string }

func (e *bindError) Error() string { return e.msg }

func newRowPlan(f *File, rt reflect.Type) (*rowPlan, error) {
	if rt.Kind() != reflect.Struct {
		return nil, fmt.Errorf("vortex: Rows: %s is not a struct", rt)
	}
	binds, err := structBinder(rt, f.schema.root.fields, "")
	if err != nil {
		return nil, err
	}
	p := &rowPlan{rt: rt}
	p.addBinds(binds, f.schema.root.fields, -1, nil, 0, nil)
	return p, nil
}

// column returns the index in the scan of the top-level column named name.
func (p *rowPlan) column(name string) int {
	i := slices.Index(p.columns, name)
	if i < 0 {
		p.columns = append(p.columns, name)
		i = len(p.columns) - 1
	}
	return i
}

// addBinds adds the cells of the fields of a struct type, which sits off bytes into the row
// type, at the field path goPath of it. They read vfields, the fields of a struct column: the
// root when col is negative, else the column col of the scan and the fields sub below it.
func (p *rowPlan) addBinds(binds []*fieldBind, vfields []Field, col int, sub []int, off uintptr, goPath []int) {
	for _, b := range binds {
		gt, at, path := b.gf.sf.Type, off+b.gf.off, append(slices.Clone(goPath), b.gf.path...)
		c, s := col, append(slices.Clone(sub), b.idx)
		if col < 0 {
			c, s = p.column(vfields[b.idx].Name), nil
		}
		if b.nested != nil {
			p.addBinds(b.nested, b.vt.fields, c, s, at, path)
			continue
		}
		p.cells = append(p.cells, rowCell{col: c, sub: s, dec: b.dec, path: path, fast: fastLeaf(p.rt, gt, b.vt, at)})
	}
}

// fill reads the rows of a batch into rows, which has as many.
func (p *rowPlan) fill(b *Batch, rows reflect.Value) error {
	for _, cell := range p.cells {
		col := b.Col(cell.col)
		for _, k := range cell.sub {
			col = col.Field(k)
		}
		var err error
		if cell.fast != nil {
			err = cell.fast(col, rows)
		} else {
			err = cell.fillSlow(col, rows)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// fillSlow reads the rows of a column into a field with reflection.
func (c *rowCell) fillSlow(col *Column, rows reflect.Value) error {
	var err error
	for i := range rows.Len() {
		err = errors.Join(err, c.dec(col, i, rows.Index(i).FieldByIndex(c.path)))
	}
	return err
}

// goField is a field of a row struct that is read from a column.
type goField struct {
	label    string
	name     string // the column
	opts     string // the tag after the name: the options, comma separated
	path     []int  // in the struct that was asked for
	sf       reflect.StructField
	off      uintptr // in the struct that was asked for
	optional bool
}

// goFields lists the fields of rt that are read, the embedded structs flattened.
func goFields(rt reflect.Type, path []int, off uintptr, label string) []*goField {
	var out []*goField
	for i := range rt.NumField() {
		sf := rt.Field(i)
		tag, tagged := sf.Tag.Lookup("vortex")
		name, opts, _ := strings.Cut(tag, ",")
		fpath := append(slices.Clone(path), i)
		switch {
		case tag == "-":
		case sf.Anonymous && !tagged && sf.Type.Kind() == reflect.Struct:
			out = append(out, goFields(sf.Type, fpath, off+sf.Offset, label)...)
		case sf.IsExported():
			if name == "" {
				name = sf.Name
			}
			out = append(out, &goField{sf: sf, path: fpath, off: off + sf.Offset, label: label + sf.Name,
				name: name, opts: opts, optional: slices.Contains(strings.Split(opts, ","), "optional")})
		}
	}
	return out
}

// fieldBind is a field of a row struct, and the column it reads.
type fieldBind struct {
	gf  *goField
	dec decoder
	// nested is the fields of a struct that a struct column fills in place: the struct is not
	// nullable, and the field is a plain struct.
	nested []*fieldBind
	vt     Type
	idx    int // in the fields of the struct column
}

// structBinder matches the fields of the struct type gt with vfields and checks their types.
func structBinder(gt reflect.Type, vfields []Field, label string) ([]*fieldBind, error) {
	var binds []*fieldBind
	for _, gf := range goFields(gt, nil, 0, label) {
		idx := slices.IndexFunc(vfields, func(f Field) bool { return f.Name == gf.name })
		if idx < 0 {
			if gf.optional {
				continue
			}
			return nil, &bindError{fmt.Sprintf("vortex: Rows: field %s (%s): column %q not found",
				gf.label, gf.sf.Type, gf.name)}
		}
		vt := vfields[idx].Type
		b := &fieldBind{gf: gf, idx: idx, vt: vt}
		var err error
		if vt.kind == Struct && !vt.nullable && gf.sf.Type.Kind() == reflect.Struct && !isSQLNull(gf.sf.Type) {
			b.nested, err = structBinder(gf.sf.Type, vt.fields, gf.label+".")
			b.dec = structDecoder(b.nested)
		} else {
			b.dec, err = decoderFor(gf.sf.Type, vt, gf.label)
		}
		if be := new(bindError); err != nil && !errors.As(err, &be) {
			err = &bindError{fmt.Sprintf("vortex: Rows: field %s (%s): column %q of type %s: %v",
				gf.label, gf.sf.Type, gf.name, vt, err)}
		}
		if err != nil {
			return nil, err
		}
		binds = append(binds, b)
	}
	if len(binds) == 0 {
		return nil, &bindError{fmt.Sprintf("vortex: Rows: no field of %s is read from a column", gt)}
	}
	return binds, nil
}

var (
	decimalType  = reflect.TypeFor[Decimal]()
	timeType     = reflect.TypeFor[time.Time]()
	durationType = reflect.TypeFor[time.Duration]()
)

// isSQLNull reports whether t is an instance of sql.Null.
func isSQLNull(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && t.PkgPath() == reflect.TypeFor[sql.NullBool]().PkgPath() &&
		strings.HasPrefix(t.Name(), "Null[")
}

// isBytes reports whether t is a slice of bytes.
func isBytes(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8
}

// wanted says which Go types hold a column, by kind.
var wanted = map[Kind]string{
	Bool: "bool", Int8: "int8", Int16: "int16", Int32: "int32", Int64: "int64 or int", Uint8: "uint8",
	Uint16: "uint16", Uint32: "uint32", Uint64: "uint64 or uint", Float16: "float32", Float32: "float32",
	Float64: "float64", Utf8: "string or []byte", Binary: "[]byte", UUID: "[16]byte", Struct: "a struct",
	List: "a slice", FixedSizeList: "a slice, or an array of its size", Map: "a map", Variant: "any",
	Date: "time.Time or its storage integer",
	Time: "time.Duration or its storage integer", Timestamp: "time.Time or int64",
	KindDecimal: "vortex.Decimal, or int64 up to a precision of 18",
}

// numberKinds are the Go kinds of each numeric column kind.
var numberKinds = map[Kind][]reflect.Kind{
	Int8: {reflect.Int8}, Int16: {reflect.Int16}, Int32: {reflect.Int32}, Int64: {reflect.Int64, reflect.Int},
	Uint8: {reflect.Uint8}, Uint16: {reflect.Uint16}, Uint32: {reflect.Uint32}, Uint64: {reflect.Uint64, reflect.Uint},
	Float16: {reflect.Float32}, Float32: {reflect.Float32}, Float64: {reflect.Float64},
}

// storageKinds are the Go kinds of the integer that stores a date, a time or a timestamp.
func storageKinds(t Type) []reflect.Kind {
	if storage(t) == array.I32 {
		return []reflect.Kind{reflect.Int32}
	}
	return numberKinds[Int64]
}

// decoderFor checks that a field of Go type gt can hold a column of type vt, and returns how
// to read a row of it. The error says why not.
func decoderFor(gt reflect.Type, vt Type, label string) (decoder, error) {
	if vt.kind == Null {
		return nil, errors.New("no Go type holds a column of type null, leave the field out")
	}
	var inner decoder
	var err error
	switch {
	case isSQLNull(gt):
		if inner, err = decoderFor(gt.Field(0).Type, nonNull(vt), label); err != nil {
			return nil, err
		}
		return func(c *Column, i int, dst reflect.Value) error {
			if c.Null(i) {
				dst.SetZero()
				return nil
			}
			dst.Field(1).SetBool(true)
			return inner(c, i, dst.Field(0))
		}, nil
	case gt.Kind() == reflect.Pointer:
		if inner, err = decoderFor(gt.Elem(), nonNull(vt), label); err != nil {
			return nil, err
		}
		return func(c *Column, i int, dst reflect.Value) error {
			if c.Null(i) {
				dst.SetZero()
				return nil
			}
			p := reflect.New(gt.Elem())
			dst.Set(p)
			return inner(c, i, p.Elem())
		}, nil
	case vt.nullable && vt.kind != Variant:
		return nil, errors.New("the column is nullable: the field must be a sql.Null[X] or a pointer")
	}
	return baseDecoder(gt, vt, label)
}

// nonNull is t, as a type that cannot hold a null.
func nonNull(t Type) Type {
	t.nullable = false
	return t
}

// baseDecoder is decoderFor for a column that has no null.
func baseDecoder(gt reflect.Type, vt Type, label string) (decoder, error) {
	mismatch := fmt.Errorf("want %s", wanted[vt.kind])
	switch vt.kind {
	case Variant:
		if gt.Kind() == reflect.Interface && gt.NumMethod() == 0 {
			return variantDecode, nil
		}
	case Struct:
		if gt.Kind() == reflect.Struct {
			binds, err := structBinder(gt, vt.fields, label+".")
			return structDecoder(binds), err
		}
	case List, FixedSizeList:
		return listDecoder(gt, vt, label, mismatch)
	case Map:
		if gt.Kind() == reflect.Map {
			return mapDecoder(gt, vt, label)
		}
	default:
		return scalarDecoder(gt, vt, mismatch)
	}
	return nil, mismatch
}

// scalarDecoder is baseDecoder for the columns of one value per row.
func scalarDecoder(gt reflect.Type, vt Type, mismatch error) (decoder, error) {
	switch k := vt.kind; {
	case k == Bool:
		if gt.Kind() == reflect.Bool {
			return func(c *Column, i int, dst reflect.Value) error { dst.SetBool(c.Bool(i)); return nil }, nil
		}
	case numberKinds[k] != nil:
		if slices.Contains(numberKinds[k], gt.Kind()) {
			return setNumber, nil
		}
	case k == KindDecimal:
		return decimalDecoder(gt, vt, mismatch)
	case k == Utf8 || k == Binary:
		switch {
		case gt.Kind() == reflect.String && k == Utf8:
			return func(c *Column, i int, dst reflect.Value) error { dst.SetString(string(c.Bytes(i))); return nil }, nil
		case isBytes(gt):
			return func(c *Column, i int, dst reflect.Value) error { dst.SetBytes(c.Bytes(i)); return nil }, nil
		}
	case k == UUID:
		if gt.Kind() == reflect.Array && gt.Len() == uuidSize && gt.Elem().Kind() == reflect.Uint8 {
			return func(c *Column, i int, dst reflect.Value) error {
				reflect.Copy(dst, reflect.ValueOf(c.Bytes(i)))
				return nil
			}, nil
		}
	default: // Date, Time, Timestamp
		return timeDecoder(gt, vt, mismatch)
	}
	return nil, mismatch
}

// rawAt returns the bytes of row i of a numeric column, as an unsigned number, and their
// count.
func rawAt(c *Column, i int) (uint64, int) {
	w := c.arr.Type.PType.Width()
	var buf [8]byte
	copy(buf[:], c.arr.Data[i*w:(i+1)*w])
	return binary.LittleEndian.Uint64(buf[:]), w
}

// intAt returns row i of a numeric column of integers.
func intAt(c *Column, i int) int64 {
	raw, w := rawAt(c, i)
	return int64(raw<<(64-8*w)) >> (64 - 8*w)
}

// setNumber sets dst to row i of a numeric column.
func setNumber(c *Column, i int, dst reflect.Value) error {
	raw, _ := rawAt(c, i)
	switch {
	case c.arr.Type.PType == array.F16:
		dst.SetFloat(float64(math.Float32frombits(half(uint32(raw)))))
	case c.arr.Type.PType == array.F32:
		dst.SetFloat(float64(math.Float32frombits(uint32(raw))))
	case c.arr.Type.PType == array.F64:
		dst.SetFloat(math.Float64frombits(raw))
	case dst.CanInt():
		dst.SetInt(intAt(c, i))
	default:
		dst.SetUint(raw)
	}
	return nil
}

func decimalDecoder(gt reflect.Type, vt Type, mismatch error) (decoder, error) {
	switch {
	case gt == decimalType:
		return func(c *Column, i int, dst reflect.Value) error { dst.Set(reflect.ValueOf(c.Decimal(i))); return nil }, nil
	case slices.Contains(numberKinds[Int64], gt.Kind()) && vt.Precision() <= maxInt64Digits:
		return func(c *Column, i int, dst reflect.Value) error {
			v, _ := c.Decimal(i).Int64()
			dst.SetInt(v)
			return nil
		}, nil
	case slices.Contains(numberKinds[Int64], gt.Kind()):
		return nil, fmt.Errorf("a decimal of precision %d does not fit in %s: want vortex.Decimal", vt.Precision(), gt)
	}
	return nil, mismatch
}

// locationOf is the time zone of a timestamp type.
func locationOf(zone string) (*time.Location, error) {
	if zone == "" {
		return time.UTC, nil
	}
	return time.LoadLocation(zone)
}

// timeOf is the time of a date, or of a timestamp, that counts v units from the epoch.
func timeOf(v int64, u Unit, loc *time.Location) time.Time {
	if u == Day {
		return time.Unix(v*86400, 0).In(loc)
	}
	ticks := int64(ticksPerSecond[u])
	sec := v / ticks
	if v%ticks < 0 {
		sec--
	}
	return time.Unix(sec, (v-sec*ticks)*(1e9/ticks)).In(loc)
}

// durationOf is a time of day that counts v units from midnight.
func durationOf(v int64, u Unit) time.Duration {
	return time.Duration(v) * (time.Second / time.Duration(ticksPerSecond[u]))
}

func timeDecoder(gt reflect.Type, vt Type, mismatch error) (decoder, error) {
	loc, err := locationOf(vt.zone)
	switch {
	case gt == timeType && vt.kind != Time:
		return func(c *Column, i int, dst reflect.Value) error {
			dst.Set(reflect.ValueOf(timeOf(intAt(c, i), vt.unit, loc)))
			return nil
		}, err
	case gt == durationType && vt.kind == Time:
		return func(c *Column, i int, dst reflect.Value) error {
			dst.SetInt(int64(durationOf(intAt(c, i), vt.unit)))
			return nil
		}, err
	case slices.Contains(storageKinds(vt), gt.Kind()) && gt != durationType:
		return func(c *Column, i int, dst reflect.Value) error { dst.SetInt(intAt(c, i)); return nil }, err
	}
	return nil, mismatch
}

func variantDecode(c *Column, i int, dst reflect.Value) error {
	v, err := c.Variant(i)
	if err != nil {
		return err
	}
	if v == nil {
		dst.SetZero()
		return nil
	}
	dst.Set(reflect.ValueOf(v))
	return nil
}

// structDecoder reads a struct column into a struct of Go.
func structDecoder(binds []*fieldBind) decoder {
	return func(c *Column, i int, dst reflect.Value) error {
		var err error
		for _, b := range binds {
			err = errors.Join(err, b.dec(c.Field(b.idx), i, dst.FieldByIndex(b.gf.path)))
		}
		return err
	}
}

func listDecoder(gt reflect.Type, vt Type, label string, mismatch error) (decoder, error) {
	isArray := gt.Kind() == reflect.Array
	switch {
	case gt.Kind() == reflect.Slice:
	case isArray && vt.kind == FixedSizeList && gt.Len() == vt.Size():
	case isArray && vt.kind == FixedSizeList:
		return nil, fmt.Errorf("a fixed-size list of %d does not fit %s: want [%d]X or []X", vt.Size(), gt, vt.Size())
	default:
		return nil, mismatch
	}
	elem, err := decoderFor(gt.Elem(), *vt.elem, label)
	if err != nil {
		return nil, fmt.Errorf("elements: %w", err)
	}
	return func(c *Column, i int, dst reflect.Value) error {
		start, end := c.List(i)
		elems, out := c.Elems(), dst
		if !isArray {
			out = reflect.MakeSlice(gt, end-start, end-start)
		}
		var err error
		for j := start; j < end; j++ {
			err = errors.Join(err, elem(elems, j, out.Index(j-start)))
		}
		dst.Set(out)
		return err
	}, nil
}

func mapDecoder(gt reflect.Type, vt Type, label string) (decoder, error) {
	key, err := decoderFor(gt.Key(), *vt.elem, label)
	if err != nil {
		return nil, fmt.Errorf("keys: %w", err)
	}
	val, err := decoderFor(gt.Elem(), *vt.value, label)
	if err != nil {
		return nil, fmt.Errorf("values: %w", err)
	}
	return func(c *Column, i int, dst reflect.Value) error {
		start, end := c.List(i)
		entries := c.Elems()
		keys, vals := entries.Field(0), entries.Field(1)
		m := reflect.MakeMapWithSize(gt, end-start)
		var err error
		for j := start; j < end; j++ {
			k, v := reflect.New(gt.Key()).Elem(), reflect.New(gt.Elem()).Elem()
			err = errors.Join(err, key(keys, j, k), val(vals, j, v))
			m.SetMapIndex(k, v)
		}
		dst.Set(m)
		return err
	}, nil
}

// checkLayout reports whether a field of type leaf, at off in a row of type row, is inside
// the row and aligned, and as big as a V that is written there.
func checkLayout(row, leaf reflect.Type, off, vsize uintptr) bool {
	return leaf.Size() == vsize && off+vsize <= row.Size() && off%uintptr(leaf.Align()) == 0
}
