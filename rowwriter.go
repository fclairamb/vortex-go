package vortex

import (
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/vortex-go/internal/unsafex"
)

// rowBatch is the most rows that a RowWriter turns into columns at once.
const rowBatch = 8192

// RowWriter writes rows, values of the struct type T, to a Vortex file. It is the other half
// of Rows: the schema comes from the fields of T, with the same tags, and the Go types are
// the ones that Rows reads. It drives a Writer, which sets the chunks, the statistics and the
// encodings.
//
// A field is the column named by its tag, vortex:"name", else its own name. The tag
// vortex:"-" skips the field, unexported fields are skipped and embedded structs are
// flattened. The Vortex type of a field follows from its Go type:
//
//   - bool, the numbers (int and uint are 64 bits), string (utf8), []byte (binary), [16]byte
//     (uuid) and vortex.Decimal (a decimal, with the tag option decimal(p,s), for precision p
//     and scale s, which int64 takes as well, p up to 18);
//   - time.Time is a timestamp in microseconds, UTC, with no zone in the type; the options
//     timestamp(unit) and timestamp(unit,zone) pick another unit (s, ms, us or ns) and a zone
//     name, and date a date in days;
//   - time.Duration is a time of day in nanoseconds; the option time(unit) picks another unit;
//   - sql.Null[X] and *X are nullable, anything else is not;
//   - a struct is a struct, []X a list, [N]X a fixed-size list and map[K]V a map, whose keys
//     are not nullable and whose order of entries is the one of Go's iteration. The options
//     of a list field apply to its elements.
//
// A T that cannot be written is the error of the first Write, and of Close if Write was never
// called: it names the field and its Go type.
//
// A Write call copies what it needs, so the caller can reuse the rows as soon as it returns.
// A field that is a number, a bool, a decimal, a time, a uuid, a string or a []byte, plain or
// in sql.Null, is read without allocating. Pointers, structs, slices and maps allocate.
//
// Write and Close are not safe for concurrent use.
type RowWriter[T any] struct {
	w      *Writer
	err    error
	cols   []rwCol[T]
	data   []Data
	closed bool
}

// rwCol is the column of a field of T, and how to fill it.
type rwCol[T any] struct {
	node *rwNode
	fast func(rows []T) // nil when the node reads the rows with reflection
	path []int
}

// NewRowWriter returns a RowWriter that writes to w. It does not close w.
func NewRowWriter[T any](w io.Writer, o WriteOptions) *RowWriter[T] {
	rw := &RowWriter[T]{}
	rt := reflect.TypeFor[T]()
	if rt.Kind() != reflect.Struct {
		rw.err = fmt.Errorf("vortex: RowWriter: %s is not a struct", rt)
		return rw
	}
	root, err := newRwStruct(rt, "")
	if err != nil {
		var be *bindError
		if !errors.As(err, &be) {
			err = fmt.Errorf("vortex: RowWriter: %w", err)
		}
		rw.err = err
		return rw
	}
	fields := make([]Field, len(root.kids))
	for i, k := range root.kids {
		fields[i] = Field{Name: root.names[i], Type: k.t}
		c := rwCol[T]{node: k, path: root.paths[i], fast: fastFor[T](k, rt, root.offs[i])}
		rw.cols = append(rw.cols, c)
		rw.data = append(rw.data, k.d)
	}
	rw.w = NewWriter(w, NewSchema(fields...), o)
	return rw
}

// Write adds rows to the file. It returns the number of rows it took, which is less than
// len(rows) when it returns an error. An error closes the writer for writing, and every later
// call returns it.
func (w *RowWriter[T]) Write(rows []T) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	done := 0
	for {
		part := rows[done:min(len(rows), done+rowBatch)]
		w.fill(part)
		if err := w.w.Write(w.data...); err != nil {
			return done, err
		}
		done += len(part)
		if done == len(rows) {
			return done, nil
		}
	}
}

// fill turns rows into the columns.
func (w *RowWriter[T]) fill(rows []T) {
	var rv reflect.Value
	for _, c := range w.cols {
		if c.fast != nil {
			c.fast(rows)
			continue
		}
		if !rv.IsValid() {
			rv = reflect.ValueOf(rows)
		}
		c.node.reset()
		for i := range rows {
			c.node.add(rv.Index(i).FieldByIndex(c.path))
		}
		c.node.seal()
	}
}

// Close writes the footer. A second Close returns nil, and Close does not close the
// io.Writer.
func (w *RowWriter[T]) Close() error {
	if w.w == nil {
		if w.closed {
			return nil
		}
		w.closed = true
		return w.err
	}
	return w.w.Close()
}

// rwKind is how the values of a node are read.
type rwKind uint8

const (
	rkNum      rwKind = iota // the numbers
	rkBool                   // bool
	rkString                 // string
	rkBytes                  // []byte
	rkUUID                   // [16]byte
	rkDecimal                // vortex.Decimal
	rkDecInt                 // an int64 that is the unscaled value of a decimal
	rkTime                   // time.Time, as a date or a timestamp
	rkDuration               // time.Duration, as a time
	rkStruct
	rkList
	rkFixed // a fixed-size list
	rkMap
)

// rwNode is a column of the file, or a part of one, and the buffers its values go to.
type rwNode struct {
	full  reflect.Type // the Go type, with its sql.Null or pointer
	gt    reflect.Type // the Go type of the values
	b     *body        // the buffers, and what the Writer reads
	d     Data
	kids  []*rwNode // the fields of a struct, the element of a list, the key and the value of a map
	names []string  // the names of the fields of a struct
	paths [][]int   // where the fields of a struct are in it
	offs  []uintptr // the offsets of the fields of a struct, in it
	uuids [][16]byte
	i32   []int32
	t     Type
	n     int
	w     int // the bytes of a number, a time, a date
	size  int // of a fixed-size list
	null  uint8
	kind  rwKind
}

const (
	notNull   = iota
	nullSQL   // sql.Null[X]
	nullPoint // *X
)

// The names of the options of a tag.
const (
	optDecimal   = "decimal"
	optDate      = "date"
	optTimestamp = "timestamp"
	optTime      = "time"
	optOptional  = "optional"
)

// tagOpt is an option of a tag, after the name.
type tagOpt struct {
	name, raw, zone string
	p, s            int
	unit            Unit
}

// parseTagOpts parses the options of a tag: names, some with arguments in parentheses.
func parseTagOpts(s string) ([]tagOpt, error) {
	var parts []string
	depth, start := 0, 0
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts, start = append(parts, s[start:i]), i+1
			}
		}
	}
	parts = append(parts, s[start:])
	var out []tagOpt
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		o, err := parseTagOpt(part)
		if err != nil {
			return nil, err
		}
		if o.name != optOptional {
			out = append(out, o)
		}
	}
	return out, nil
}

func parseTagOpt(raw string) (tagOpt, error) {
	name, rest, hasArgs := strings.Cut(raw, "(")
	o := tagOpt{name: name, raw: raw}
	var args []string
	if hasArgs {
		inner, ok := strings.CutSuffix(rest, ")")
		if !ok || strings.ContainsAny(inner, "()") {
			return tagOpt{}, o.bad("unbalanced parentheses")
		}
		for a := range strings.SplitSeq(inner, ",") {
			args = append(args, strings.TrimSpace(a))
		}
	}
	var err error
	switch name {
	case optOptional:
	case optDate:
		if len(args) != 0 {
			err = o.bad("date takes no argument")
		}
	case optDecimal:
		err = o.decimal(args)
	case optTimestamp, optTime:
		err = o.units(args)
	default:
		err = o.bad("unknown option")
	}
	return o, err
}

func (o tagOpt) bad(why string) error {
	return fmt.Errorf("bad tag option %q: %s", o.raw, why)
}

// decimal reads the arguments of decimal(p,s).
func (o *tagOpt) decimal(args []string) error {
	if len(args) != 2 {
		return o.bad("want decimal(precision,scale)")
	}
	var err1, err2 error
	o.p, err1 = strconv.Atoi(args[0])
	o.s, err2 = strconv.Atoi(args[1])
	if err1 != nil || err2 != nil || o.p < 1 || o.p > 76 || o.s < 0 || o.s > o.p {
		return o.bad("the precision is 1 to 76 and the scale 0 to the precision")
	}
	return nil
}

// units reads the arguments of timestamp(unit), timestamp(unit,zone) and time(unit).
func (o *tagOpt) units(args []string) error {
	if len(args) != 1 && (len(args) != 2 || o.name == optTime) {
		return o.bad("want " + o.name + "(unit), and timestamp(unit,zone) takes a zone")
	}
	u, ok := map[string]Unit{"s": Second, "ms": Millisecond, "us": Microsecond, "ns": Nanosecond}[args[0]]
	if !ok {
		return o.bad("the unit is s, ms, us or ns")
	}
	o.unit = u
	if len(args) == 2 {
		o.zone = args[1]
	}
	return nil
}

// takeOpt removes the first option with this name from opts and returns it.
func takeOpt(opts *[]tagOpt, name string) (tagOpt, bool) {
	i := slices.IndexFunc(*opts, func(o tagOpt) bool { return o.name == name })
	if i < 0 {
		return tagOpt{}, false
	}
	o := (*opts)[i]
	*opts = slices.Delete(*opts, i, i+1)
	return o, true
}

// rwNumbers are the Vortex types of the numbers of Go, and their sizes.
var rwNumbers = map[reflect.Kind]struct {
	t Type
	w int
}{
	reflect.Int8: {Int8Type(), 1}, reflect.Int16: {Int16Type(), 2}, reflect.Int32: {Int32Type(), 4},
	reflect.Int64: {Int64Type(), 8}, reflect.Int: {Int64Type(), 8},
	reflect.Uint8: {Uint8Type(), 1}, reflect.Uint16: {Uint16Type(), 2}, reflect.Uint32: {Uint32Type(), 4},
	reflect.Uint64: {Uint64Type(), 8}, reflect.Uint: {Uint64Type(), 8},
	reflect.Float32: {Float32Type(), 4}, reflect.Float64: {Float64Type(), 8},
}

// newRwStruct is the node of the struct type gt, whose fields are labeled with the prefix
// label.
func newRwStruct(gt reflect.Type, label string) (*rwNode, error) {
	n := &rwNode{gt: gt, full: gt, kind: rkStruct}
	seen := map[string]bool{}
	var fields []Field
	for _, gf := range goFields(gt, nil, 0, label) {
		fail := func(err error) error {
			if be := new(bindError); errors.As(err, &be) {
				return err
			}
			return &bindError{fmt.Sprintf("vortex: RowWriter: field %s (%s): %v", gf.label, gf.sf.Type, err)}
		}
		opts, err := parseTagOpts(gf.opts)
		if err != nil {
			return nil, fail(err)
		}
		if seen[gf.name] {
			return nil, fail(fmt.Errorf("the column %q is written twice", gf.name))
		}
		seen[gf.name] = true
		k, err := newRwNode(gf.sf.Type, opts, gf.label+".")
		if err != nil {
			return nil, fail(err)
		}
		n.kids, n.names = append(n.kids, k), append(n.names, gf.name)
		n.paths, n.offs = append(n.paths, gf.path), append(n.offs, gf.off)
		fields = append(fields, Field{Name: gf.name, Type: k.t})
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("no field of %s is written", gt)
	}
	n.t = StructOf(fields...)
	return n, nil
}

// newRwNode is the node of a field of Go type gt, with the options of its tag. label is the
// prefix of the fields of a struct.
func newRwNode(gt reflect.Type, opts []tagOpt, label string) (*rwNode, error) {
	full := gt
	var null uint8
	switch {
	case isSQLNull(gt):
		null, gt = nullSQL, gt.Field(0).Type
	case gt.Kind() == reflect.Pointer:
		null, gt = nullPoint, gt.Elem()
	}
	var n *rwNode
	var err error
	if gt.Kind() == reflect.Struct && gt != decimalType && gt != timeType && !isSQLNull(gt) {
		n, err = newRwStruct(gt, label)
	} else {
		n = &rwNode{gt: gt}
		n.t, err = n.build(&opts, label)
	}
	if err != nil {
		return nil, err
	}
	if len(opts) > 0 {
		return nil, fmt.Errorf("the option %q does not apply to %s", opts[0].raw, gt)
	}
	n.full, n.null = full, null
	if null != notNull {
		n.t = n.t.AsNullable()
	}
	n.init()
	return n, nil
}

// build sets the kind of a node that is not a struct, and returns its Vortex type.
func (n *rwNode) build(opts *[]tagOpt, label string) (Type, error) {
	gt := n.gt
	switch gt {
	case decimalType:
		o, ok := takeOpt(opts, optDecimal)
		if !ok {
			return Type{}, errors.New(`a vortex.Decimal needs its precision and scale in the tag: vortex:"name,decimal(p,s)"`)
		}
		n.kind = rkDecimal
		return DecimalOf(o.p, o.s), nil
	case timeType:
		n.kind, n.w = rkTime, 8
		if _, ok := takeOpt(opts, optDate); ok {
			n.w = 4
			return DateType(Day), nil
		}
		if o, ok := takeOpt(opts, optTimestamp); ok {
			return TimestampType(o.unit, o.zone), nil
		}
		return TimestampType(Microsecond, ""), nil
	case durationType:
		n.kind, n.w = rkDuration, 8
		t := TimeType(Nanosecond)
		if o, ok := takeOpt(opts, optTime); ok {
			t = TimeType(o.unit)
			if o.unit == Second || o.unit == Millisecond {
				n.w = 4
			}
		}
		return t, nil
	}
	if num, ok := rwNumbers[gt.Kind()]; ok {
		n.kind, n.w = rkNum, num.w
		if o, ok := takeOpt(opts, optDecimal); ok {
			if gt.Kind() != reflect.Int64 && gt.Kind() != reflect.Int {
				return Type{}, fmt.Errorf("the option %q needs an int64 or a vortex.Decimal", o.raw)
			}
			if o.p > maxInt64Digits {
				return Type{}, fmt.Errorf("the option %q: an int64 holds a precision of %d at most", o.raw, maxInt64Digits)
			}
			n.kind = rkDecInt
			return DecimalOf(o.p, o.s), nil
		}
		return num.t, nil
	}
	return n.buildOther(opts, label)
}

// buildOther is build for the types that are not numbers, times and decimals.
func (n *rwNode) buildOther(opts *[]tagOpt, label string) (Type, error) {
	gt := n.gt
	switch gt.Kind() {
	case reflect.Bool:
		n.kind = rkBool
		return BoolType(), nil
	case reflect.String:
		n.kind = rkString
		return Utf8Type(), nil
	case reflect.Slice:
		if isBytes(gt) {
			n.kind = rkBytes
			return BinaryType(), nil
		}
		n.kind = rkList
		e, err := elemNode(gt.Elem(), opts, label)
		n.kids = []*rwNode{e}
		return ListOf(e.t), err
	case reflect.Array:
		if gt.Elem().Kind() == reflect.Uint8 && gt.Len() == uuidSize {
			n.kind = rkUUID
			return UUIDType(), nil
		}
		if gt.Len() == 0 {
			return Type{}, errors.New("an array of no element has no Vortex type")
		}
		n.kind, n.size = rkFixed, gt.Len()
		e, err := elemNode(gt.Elem(), opts, label)
		n.kids = []*rwNode{e}
		return FixedSizeListOf(e.t, gt.Len()), err
	case reflect.Map:
		n.kind = rkMap
		k, err := newRwNode(gt.Key(), nil, label)
		if err != nil {
			return Type{}, fmt.Errorf("map key: %w", err)
		}
		if k.null != notNull {
			return Type{}, fmt.Errorf("map key: %s is nullable, a key cannot be", gt.Key())
		}
		v, err := newRwNode(gt.Elem(), nil, label)
		if err != nil {
			return Type{}, fmt.Errorf("map value: %w", err)
		}
		n.kids = []*rwNode{k, v}
		return MapOf(k.t, v.t), nil
	}
	return Type{}, fmt.Errorf("no Vortex type holds a Go %s", gt.Kind())
}

// elemNode is the node of the elements of a list: they take the options of the list.
func elemNode(gt reflect.Type, opts *[]tagOpt, label string) (*rwNode, error) {
	e, err := newRwNode(gt, *opts, label)
	*opts = nil
	if err != nil {
		return &rwNode{}, fmt.Errorf("elements: %w", err)
	}
	return e, nil
}

// init makes the buffers of the node, and the Data that holds them.
func (n *rwNode) init() {
	b := &body{}
	switch n.kind {
	case rkNum:
		b.kind, b.pk = dPrim, n.t.kind
	case rkTime, rkDuration:
		b.kind, b.pk = dPrim, storageKind(n.t)
	case rkDecimal, rkDecInt:
		b.kind = dDec
	case rkBool:
		b.kind = dBool
	case rkString:
		b.kind = dStr
	case rkBytes, rkUUID:
		b.kind = dBytes
	case rkStruct:
		b.kind = dStruct
	case rkList, rkFixed:
		b.kind, b.kids = dList, []Data{n.kids[0].d}
	case rkMap:
		b.kind = dList
		entries := OfStruct(n.kids[0].d, n.kids[1].d)
		b.kids = []Data{entries}
	}
	if n.kind == rkStruct {
		for _, k := range n.kids {
			b.kids = append(b.kids, k.d)
		}
	}
	n.b, n.d = b, Data{b}
}

// reset empties the buffers of the node and of its children, and keeps their memory.
func (n *rwNode) reset() {
	b := n.b
	n.n = 0
	b.raw, b.bools, b.nulls = b.raw[:0], b.bools[:0], b.nulls[:0]
	b.strs, b.bins, b.decs = b.strs[:0], b.bins[:0], b.decs[:0]
	n.uuids = n.uuids[:0]
	b.offs = b.offs[:0]
	if n.kind == rkList || n.kind == rkFixed || n.kind == rkMap {
		b.offs = append(b.offs, 0)
	}
	for _, k := range n.kids {
		k.reset()
	}
}

// seal sets the lengths of the Data of the node and of its children, after its values were
// added.
func (n *rwNode) seal() {
	b := n.b
	b.n = n.n
	if n.null == notNull {
		b.nulls = nil
	}
	switch n.kind {
	case rkUUID:
		b.bins = slices.Grow(b.bins[:0], len(n.uuids))[:len(n.uuids)]
		for i := range n.uuids {
			b.bins[i] = n.uuids[i][:]
		}
	case rkMap:
		b.kids[0].n = n.kids[0].n
	}
	for _, k := range n.kids {
		k.seal()
	}
}

// add adds a value of the Go type of the node: a row of a field, an element of a list...
func (n *rwNode) add(v reflect.Value) {
	if n.null != notNull {
		v = n.unwrap(v)
	}
	n.n++
	b := n.b
	switch n.kind {
	case rkNum:
		n.put(n.bits(v))
	case rkDecInt:
		b.decs = append(b.decs, DecimalFromInt64(v.Int()))
	case rkBool:
		b.bools = append(b.bools, v.Bool())
	case rkString:
		b.strs = append(b.strs, v.String())
	case rkBytes:
		b.bins = append(b.bins, v.Bytes())
	case rkUUID:
		var u [uuidSize]byte
		reflect.Copy(reflect.ValueOf(u[:]), v)
		n.uuids = append(n.uuids, u)
	case rkDecimal:
		b.decs = append(b.decs, v.Interface().(Decimal))
	case rkTime:
		n.put(uint64(n.instant(v.Interface().(time.Time))))
	case rkDuration:
		n.put(uint64(n.ticks(time.Duration(v.Int()))))
	default:
		n.addNested(v)
	}
}

// unwrap records whether v, a sql.Null or a pointer, is null, and returns the value it holds:
// the zero value for a null.
func (n *rwNode) unwrap(v reflect.Value) reflect.Value {
	var null bool
	switch {
	case n.null == nullSQL:
		null, v = !v.Field(1).Bool(), v.Field(0)
	case v.IsNil():
		null = true
	default:
		v = v.Elem()
	}
	n.b.nulls = append(n.b.nulls, null)
	if null {
		return reflect.Zero(n.gt)
	}
	return v
}

// bits is the number v, in the bits that are stored.
func (n *rwNode) bits(v reflect.Value) uint64 {
	switch {
	case v.CanInt():
		return uint64(v.Int())
	case v.CanUint():
		return v.Uint()
	case n.w == 4:
		return uint64(math.Float32bits(float32(v.Float())))
	}
	return math.Float64bits(v.Float())
}

// put adds the low bytes of a number to the raw bytes, as many as the node says.
func (n *rwNode) put(bits uint64) {
	for k := range n.w {
		n.b.raw = append(n.b.raw, byte(bits>>(8*k)))
	}
}

// addNested is add for a struct, a list, a fixed-size list and a map.
func (n *rwNode) addNested(v reflect.Value) {
	switch n.kind {
	case rkStruct:
		for i, k := range n.kids {
			k.add(v.FieldByIndex(n.paths[i]))
		}
	case rkMap:
		for it := v.MapRange(); it.Next(); {
			n.kids[0].add(it.Key())
			n.kids[1].add(it.Value())
		}
		n.b.offs = append(n.b.offs, n.kids[0].n)
	default:
		for i := range v.Len() {
			n.kids[0].add(v.Index(i))
		}
		n.b.offs = append(n.b.offs, n.kids[0].n)
	}
}

// instant is the number of units of the node since the epoch that t is, rounded down.
func (n *rwNode) instant(t time.Time) int64 {
	if n.t.unit == Day {
		return floorDiv(t.Unix(), 86400)
	}
	tps := int64(ticksPerSecond[n.t.unit])
	return t.Unix()*tps + int64(t.Nanosecond())/(1e9/tps)
}

// ticks is the number of units of the node that d is, rounded toward zero.
func (n *rwNode) ticks(d time.Duration) int64 {
	return int64(d) / (1e9 / int64(ticksPerSecond[n.t.unit]))
}

// store makes ints the raw bytes of the node: as they are, or narrowed to 32 bits.
func (n *rwNode) store(ints []int64) {
	if n.w == 8 {
		n.b.raw = unsafex.Bytes(ints)
		return
	}
	n.i32 = slices.Grow(n.i32[:0], len(ints))[:len(ints)]
	for i, v := range ints {
		n.i32[i] = int32(v)
	}
	n.b.raw = unsafex.Bytes(n.i32)
}

// fastFor returns how to fill a node from the rows without reflection, for a field of T at
// off, or nil when the node is not a plain or sql.Null number, bool, string, []byte, uuid,
// decimal, time or duration.
func fastFor[T any](n *rwNode, row reflect.Type, off uintptr) func([]T) {
	var valid uintptr
	switch n.null {
	case nullPoint:
		return nil
	case nullSQL:
		v, ok := n.full.Field(0), n.full.Field(1)
		off, valid = off+v.Offset, off+ok.Offset
		if !checkLayout(row, reflect.TypeFor[bool](), valid, 1) {
			return nil
		}
	}
	switch n.kind {
	case rkNum:
		return fastNum[T](n, row, off, valid)
	case rkBool:
		return gather[T](n, row, off, valid, func(v []bool) { n.b.bools = v })
	case rkString:
		return gather[T](n, row, off, valid, func(v []string) { n.b.strs = v })
	case rkBytes:
		return gather[T](n, row, off, valid, func(v [][]byte) { n.b.bins = v })
	case rkDecimal:
		return gather[T](n, row, off, valid, func(v []Decimal) { n.b.decs = v })
	case rkUUID:
		return gather[T](n, row, off, valid, func(v [][uuidSize]byte) {
			n.b.bins = slices.Grow(n.b.bins[:0], len(v))[:len(v)]
			for i := range v {
				n.b.bins[i] = v[i][:]
			}
		})
	}
	return fastTimes[T](n, row, off, valid)
}

// fastTimes is fastFor for the times and the durations.
func fastTimes[T any](n *rwNode, row reflect.Type, off, valid uintptr) func([]T) {
	var ints []int64
	switch n.kind {
	case rkTime:
		return gather[T](n, row, off, valid, func(v []time.Time) {
			ints = slices.Grow(ints[:0], len(v))[:len(v)]
			for i, t := range v {
				ints[i] = 0
				if n.null != nullSQL || !n.b.nulls[i] {
					ints[i] = n.instant(t)
				}
			}
			n.store(ints)
		})
	case rkDuration:
		return gather[T](n, row, off, valid, func(v []time.Duration) {
			ints = slices.Grow(ints[:0], len(v))[:len(v)]
			for i, d := range v {
				ints[i] = n.ticks(d)
			}
			n.store(ints)
		})
	}
	return nil
}

// fastNum is fastFor for the numbers.
func fastNum[T any](n *rwNode, row reflect.Type, off, valid uintptr) func([]T) {
	switch n.gt.Kind() {
	case reflect.Int8:
		return gatherNum[T, int8](n, row, off, valid)
	case reflect.Int16:
		return gatherNum[T, int16](n, row, off, valid)
	case reflect.Int32:
		return gatherNum[T, int32](n, row, off, valid)
	case reflect.Int64, reflect.Int:
		return gatherNum[T, int64](n, row, off, valid)
	case reflect.Uint8:
		return gatherNum[T, uint8](n, row, off, valid)
	case reflect.Uint16:
		return gatherNum[T, uint16](n, row, off, valid)
	case reflect.Uint32:
		return gatherNum[T, uint32](n, row, off, valid)
	case reflect.Uint64, reflect.Uint:
		return gatherNum[T, uint64](n, row, off, valid)
	case reflect.Float32:
		return gatherNum[T, float32](n, row, off, valid)
	}
	return gatherNum[T, float64](n, row, off, valid)
}

func gatherNum[T any, V unsafex.Number](n *rwNode, row reflect.Type, off, valid uintptr) func([]T) {
	return gather[T](n, row, off, valid, func(v []V) { n.b.raw = unsafex.Bytes(v) })
}

// gather reads the values of V of a field of T into a buffer it reuses, and hands them to set.
// It returns nil when the field does not have the layout of a V.
func gather[T, V any](n *rwNode, row reflect.Type, off, valid uintptr, set func(vals []V)) func([]T) {
	if !checkLayout(row, n.gt, off, reflect.TypeFor[V]().Size()) {
		return nil
	}
	var vals []V
	return func(rows []T) {
		vals = slices.Grow(vals[:0], len(rows))[:len(rows)]
		if n.null == nullSQL {
			n.b.nulls = slices.Grow(n.b.nulls[:0], len(rows))[:len(rows)]
			unsafex.GatherNull(rows, off, valid, vals, n.b.nulls)
		} else {
			unsafex.Gather(rows, off, vals)
		}
		set(vals)
		n.b.n = len(rows)
	}
}
