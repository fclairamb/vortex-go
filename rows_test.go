package vortex

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	matrixPath = "testdata/fixtures/0.93.0/extra/types_matrix.vortex"
	fixtureDir = "testdata/fixtures/0.93.0/"
)

func openFixture(t *testing.T, path string) *File {
	t.Helper()
	f, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// readRows reads every row of T, with fix applied to each row in the loop body (to copy
// the views).
func readRows[T any](t *testing.T, f *File, q Query, fix func(*T)) []T {
	t.Helper()
	var out []T
	for r, err := range Rows[T](context.Background(), f, q) {
		if err != nil {
			t.Fatal(err)
		}
		if fix != nil {
			fix(&r)
		}
		out = append(out, r)
	}
	return out
}

func rowsErr[T any](f *File, q Query) error {
	for _, err := range Rows[T](context.Background(), f, q) {
		return err
	}
	return nil
}

// same compares two values the way a person would: pointers followed, NaN equal to NaN.
func same(got, want any) bool { return show(reflect.ValueOf(got)) == show(reflect.ValueOf(want)) }

// show prints a value with its pointers followed, so that two values print the same when
// they hold the same: NaN prints as NaN.
func show(v reflect.Value) string {
	var parts []string
	switch v.Kind() {
	case reflect.Invalid:
		return "nil"
	case reflect.Pointer:
		if v.IsNil() {
			return "nil"
		}
		return show(v.Elem())
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return fmt.Sprint(v)
		}
		for i := range v.Len() {
			parts = append(parts, show(v.Index(i)))
		}
		return "[" + strings.Join(parts, " ") + "]"
	case reflect.Struct:
		if v.Type() == timeType && v.CanInterface() {
			return fmt.Sprint(v.Interface())
		}
		for i := range v.NumField() {
			parts = append(parts, show(v.Field(i)))
		}
		return "{" + strings.Join(parts, " ") + "}"
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			parts = append(parts, fmt.Sprint(it.Key().Interface())+":"+show(it.Value()))
		}
		slices.Sort(parts)
		return "map" + strings.Join(parts, " ")
	}
	return fmt.Sprint(v)
}

// colByName returns the column of a batch of all the columns of f.
func colByName(f *File, b *Batch, name string) *Column {
	for i, fl := range f.Schema().Fields() {
		if fl.Name == name {
			return b.Col(i)
		}
	}
	panic("no column " + name)
}

// oracle is the value of row i of a column as a Go type of the same kind as dst, read with the
// column accessors, nil for a null row. It does not use Rows.
func oracle(c *Column, i int, dst reflect.Type) any {
	if c.Null(i) {
		return nil
	}
	vt := c.Type()
	switch vt.Kind() {
	case Bool:
		return c.Bool(i)
	case Utf8:
		if dst.Kind() == reflect.String {
			return string(c.Bytes(i))
		}
		return slices.Clone(c.Bytes(i))
	case Binary:
		return slices.Clone(c.Bytes(i))
	case UUID:
		return [16]byte(c.Bytes(i))
	case KindDecimal:
		if dst == decimalType {
			return c.Decimal(i)
		}
	case Time:
		if dst == durationType {
			return time.Duration(storageInt(c, i)) * time.Microsecond
		}
	case Date:
		if dst == timeType {
			return time.Unix(storageInt(c, i)*86400, 0).UTC()
		}
	case Timestamp:
		if dst == timeType {
			return time.UnixMicro(storageInt(c, i)).UTC()
		}
	}
	return numberOracle(c, i, dst)
}

// storageInt is row i of a column of integers.
func storageInt(c *Column, i int) int64 {
	if v, err := Values[int32](c); err == nil {
		return int64(v[i])
	}
	v, _ := Values[int64](c)
	return v[i]
}

// numberOracle reads row i of a numeric column at the width of the Go kind of dst.
func numberOracle(c *Column, i int, dst reflect.Type) any {
	switch dst.Kind() {
	case reflect.Int8:
		return at[int8](c, i)
	case reflect.Int16:
		return at[int16](c, i)
	case reflect.Int32:
		return at[int32](c, i)
	case reflect.Int64, reflect.Int:
		return at[int64](c, i)
	case reflect.Uint8:
		return at[uint8](c, i)
	case reflect.Uint16:
		return at[uint16](c, i)
	case reflect.Uint32:
		return at[uint32](c, i)
	case reflect.Uint64, reflect.Uint:
		return at[uint64](c, i)
	case reflect.Float32:
		return at[float32](c, i)
	}
	return at[float64](c, i)
}

func at[T Number](c *Column, i int) T {
	v, err := Values[T](c)
	if err != nil {
		panic(err)
	}
	return v[i]
}

// reflectSet is the reference fill: it sets dst from a value of the oracle, nil being null.
func reflectSet(dst reflect.Value, v any) {
	switch {
	case dst.Kind() == reflect.Pointer:
		if v != nil {
			dst.Set(reflect.New(dst.Type().Elem()))
			reflectSet(dst.Elem(), v)
		}
	case isSQLNull(dst.Type()):
		if v != nil {
			dst.Field(1).SetBool(true)
			reflectSet(dst.Field(0), v)
		}
	default:
		dst.Set(reflect.ValueOf(v).Convert(dst.Type()))
	}
}

// reflectRows reads every row of the struct type rt (one level of fields, with tags) from the
// file with reflection only.
func reflectRows(t *testing.T, f *File, rt reflect.Type, batchRows int) []reflect.Value {
	t.Helper()
	var out []reflect.Value
	for b, err := range f.scan(context.Background(), &scanOptions{batchRows: batchRows}) {
		if err != nil {
			t.Fatal(err)
		}
		for i := range b.Len() {
			row := reflect.New(rt).Elem()
			for k := range rt.NumField() {
				sf := rt.Field(k)
				col := colByName(f, b, sf.Tag.Get("vortex"))
				dt := sf.Type
				for dt.Kind() == reflect.Pointer {
					dt = dt.Elem()
				}
				if isSQLNull(dt) {
					dt = dt.Field(0).Type
				}
				reflectSet(row.Field(k), oracle(col, i, dt))
			}
			out = append(out, row)
		}
	}
	return out
}

// matrixRow holds every kind of Go field of the fixed-width kinds, bytes and strings, plain
// and nullable, each read from the columns of types_matrix.
type matrixRow struct {
	Bool   bool                    `vortex:"bool_r"`
	BoolN  sql.Null[bool]          `vortex:"bool_n"`
	I8     int8                    `vortex:"i8_r"`
	I8N    sql.Null[int8]          `vortex:"i8_n"`
	I16    int16                   `vortex:"i16_r"`
	I16N   sql.Null[int16]         `vortex:"i16_n"`
	I32    int32                   `vortex:"i32_r"`
	I32N   sql.Null[int32]         `vortex:"i32_n"`
	I64    int64                   `vortex:"i64_r"`
	I64N   sql.Null[int64]         `vortex:"i64_n"`
	U8     uint8                   `vortex:"u8_r"`
	U8N    sql.Null[uint8]         `vortex:"u8_n"`
	U16    uint16                  `vortex:"u16_r"`
	U16N   sql.Null[uint16]        `vortex:"u16_n"`
	U32    uint32                  `vortex:"u32_r"`
	U32N   sql.Null[uint32]        `vortex:"u32_n"`
	U64    uint64                  `vortex:"u64_r"`
	U64N   sql.Null[uint64]        `vortex:"u64_n"`
	F16    float32                 `vortex:"f16_r"`
	F16N   sql.Null[float32]       `vortex:"f16_n"`
	F32    float32                 `vortex:"f32_r"`
	F32N   sql.Null[float32]       `vortex:"f32_n"`
	F64    float64                 `vortex:"f64_r"`
	F64N   sql.Null[float64]       `vortex:"f64_n"`
	Int    int                     `vortex:"i64_r"`
	Uint   uint                    `vortex:"u64_r"`
	IntN   sql.Null[int]           `vortex:"i64_n"`
	Dec    Decimal                 `vortex:"decimal_r"`
	DecN   sql.Null[Decimal]       `vortex:"decimal_n"`
	DecI   int64                   `vortex:"decimal_r"`
	DecIN  sql.Null[int64]         `vortex:"decimal_n"`
	Str    string                  `vortex:"utf8_r"`
	StrN   sql.Null[string]        `vortex:"utf8_n"`
	Utf8B  []byte                  `vortex:"utf8_r"`
	Utf8BN sql.Null[[]byte]        `vortex:"utf8_n"`
	Bin    []byte                  `vortex:"binary_r"`
	BinN   sql.Null[[]byte]        `vortex:"binary_n"`
	Date   time.Time               `vortex:"date_r"`
	DateN  sql.Null[time.Time]     `vortex:"date_n"`
	DateI  int32                   `vortex:"date_r"`
	Time   time.Duration           `vortex:"time_r"`
	TimeN  sql.Null[time.Duration] `vortex:"time_n"`
	TimeI  int64                   `vortex:"time_r"`
	TS     time.Time               `vortex:"timestamp_r"`
	TSN    sql.Null[time.Time]     `vortex:"timestamp_n"`
	TSI    int64                   `vortex:"timestamp_r"`
	UUID   [16]byte                `vortex:"uuid_r"`
	UUIDN  sql.Null[[16]byte]      `vortex:"uuid_n"`
}

func fixBytes(r *matrixRow) {
	r.Utf8B, r.Bin = slices.Clone(r.Utf8B), slices.Clone(r.Bin)
	r.Utf8BN.V, r.BinN.V = slices.Clone(r.Utf8BN.V), slices.Clone(r.BinN.V)
}

// TestRowsUnsafeFillMatchesReflect checks the fill that uses unsafe against a fill that uses
// only reflect, on the same batches, for every kind of field.
func TestRowsUnsafeFillMatchesReflect(t *testing.T) {
	f := openFixture(t, matrixPath)
	got := readRows(t, f, Query{}, fixBytes)
	want := reflectRows(t, f, reflect.TypeFor[matrixRow](), maxBatchRows)
	if len(got) != 12 || len(want) != 12 {
		t.Fatalf("%d and %d rows, want 12", len(got), len(want))
	}
	for i := range got {
		gv, wv := reflect.ValueOf(got[i]), want[i]
		for k := range gv.NumField() {
			if !same(gv.Field(k).Interface(), wv.Field(k).Interface()) {
				t.Errorf("row %d field %s: got %v, want %v", i, gv.Type().Field(k).Name, gv.Field(k), wv.Field(k))
			}
		}
	}
	// Some of the values must really be there: a fill that writes nothing matches a zero.
	if !got[0].BoolN.Valid && !got[1].BoolN.Valid && !got[2].BoolN.Valid {
		t.Fatal("no valid value in the first rows")
	}
	nulls := 0
	for _, r := range got {
		if !r.I64N.Valid {
			nulls++
		}
	}
	if nulls == 0 || nulls == len(got) {
		t.Fatalf("%d null rows of %d", nulls, len(got))
	}
}

// fixtureType is the Go struct type to read the columns of a file as their fixed-width or
// bytes kinds, a pointer for a nullable column.
func fixtureType(f *File) reflect.Type {
	gos := map[Kind]reflect.Type{
		Bool: reflect.TypeFor[bool](), Int8: reflect.TypeFor[int8](), Int16: reflect.TypeFor[int16](),
		Int32: reflect.TypeFor[int32](), Int64: reflect.TypeFor[int64](), Uint8: reflect.TypeFor[uint8](),
		Uint16: reflect.TypeFor[uint16](), Uint32: reflect.TypeFor[uint32](), Uint64: reflect.TypeFor[uint64](),
		Float16: reflect.TypeFor[float32](), Float32: reflect.TypeFor[float32](), Float64: reflect.TypeFor[float64](),
		KindDecimal: decimalType, Utf8: reflect.TypeFor[string](), Binary: reflect.TypeFor[[]byte](),
		UUID: reflect.TypeFor[[16]byte](),
	}
	var fields []reflect.StructField
	for i, fl := range f.Schema().Fields() {
		gt, ok := gos[fl.Type.Kind()]
		switch fl.Type.Kind() {
		case Date, Time:
			gt, ok = reflect.TypeFor[int64](), true
			if storage(fl.Type) == 2 || fl.Type.Unit() >= Millisecond {
				gt = reflect.TypeFor[int32]()
			}
		case Timestamp:
			gt, ok = reflect.TypeFor[int64](), true
		}
		if !ok {
			continue
		}
		if fl.Type.Nullable() {
			gt = reflect.PointerTo(gt)
		}
		fields = append(fields, reflect.StructField{Name: fmt.Sprintf("F%d", i), Type: gt,
			Tag: reflect.StructTag(fmt.Sprintf(`vortex:%q`, fl.Name))})
	}
	return reflect.StructOf(fields)
}

// TestRowsFixtures reads every column of every fixture that fits a fixed-width or bytes field
// into a generated struct, and compares the rows with the column accessors.
func TestRowsFixtures(t *testing.T) {
	read := 0
	for _, path := range corpus(t) {
		f := openFixture(t, path)
		rt := fixtureType(f)
		if rt.NumField() == 0 {
			continue
		}
		p, err := newRowPlan(f, rt)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, batchRows := range []int{maxBatchRows, 7} {
			for b, err := range f.scan(context.Background(), &scanOptions{batchRows: batchRows, columns: p.columns}) {
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				rows := reflect.MakeSlice(reflect.SliceOf(rt), b.Len(), b.Len())
				if err := p.fill(b, rows); err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				for k := range rt.NumField() {
					name := rt.Field(k).Tag.Get("vortex")
					c := b.Col(slices.Index(p.columns, name))
					dt := rt.Field(k).Type
					if dt.Kind() == reflect.Pointer {
						dt = dt.Elem()
					}
					for i := range b.Len() {
						got, want := rows.Index(i).Field(k).Interface(), oracle(c, i, dt)
						if b, ok := want.([]byte); ok {
							if p, ok := got.(*[]byte); ok && p != nil {
								got = *p
							}
							if !bytes.Equal(got.([]byte), b) {
								t.Fatalf("%s %s row %d: %v, want %v", path, name, i, got, want)
							}
							continue
						}
						if !same(got, want) {
							t.Fatalf("%s %s row %d: %v, want %v", path, name, i, got, want)
						}
					}
				}
				read += b.Len() * rt.NumField()
			}
		}
	}
	if read < 100000 {
		t.Fatalf("only %d values compared", read)
	}
}
