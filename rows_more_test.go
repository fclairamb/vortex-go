package vortex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// allBatch reads the columns of the file in one batch.
func allBatch(t *testing.T, f *File) *Batch {
	t.Helper()
	bs, err := scanAll(t, f, maxBatchRows)
	if err != nil || len(bs) != 1 {
		t.Fatalf("%d batches, %v", len(bs), err)
	}
	return bs[0]
}

type embedded struct {
	Z int32 `vortex:"i32_r"`
}

type tagRow struct {
	embedded
	A       *int64           `vortex:"i64_n"`
	B       sql.Null[string] `vortex:"utf8_n"`
	Skip    int              `vortex:"-"`
	Missing string           `vortex:"nope,optional"`
	hidden  int
	Plain   string  `vortex:"utf8_r"`
	PS      *string `vortex:"utf8_r"`
	Name    int8    `vortex:",optional"`
	I8      int8    `vortex:"i8_r,unknown,optional"`
}

func TestRowsTags(t *testing.T) {
	f := openFixture(t, matrixPath)
	b := allBatch(t, f)
	rows := readRows[tagRow](t, f, Query{}, nil)
	if len(rows) != 12 {
		t.Fatalf("%d rows", len(rows))
	}
	for i, r := range rows {
		if r.Skip != 0 || r.Missing != "" || r.hidden != 0 || r.Name != 0 {
			t.Fatalf("row %d: %+v", i, r)
		}
		if r.Z != at[int32](colByName(f, b, "i32_r"), i) || r.Plain != string(colByName(f, b, "utf8_r").Bytes(i)) ||
			*r.PS != r.Plain || r.I8 != at[int8](colByName(f, b, "i8_r"), i) {
			t.Fatalf("row %d: %+v", i, r)
		}
		n := colByName(f, b, "i64_n")
		if (r.A == nil) != n.Null(i) || r.A != nil && *r.A != at[int64](n, i) {
			t.Fatalf("row %d: A = %v", i, r.A)
		}
		u := colByName(f, b, "utf8_n")
		if r.B.Valid == u.Null(i) || r.B.Valid && r.B.V != string(u.Bytes(i)) || !r.B.Valid && r.B.V != "" {
			t.Fatalf("row %d: B = %v", i, r.B)
		}
	}
}

type innerRow struct {
	A int32  `vortex:"a"`
	B string `vortex:"b"`
}

type nullInner struct {
	C int64   `vortex:"c"`
	D float32 `vortex:"d"`
}

type nestedRow struct {
	Inner innerRow `vortex:"inner"`
	Flat  struct {
		A int32 `vortex:"a"`
	} `vortex:"inner"`
	Ptr   *nullInner          `vortex:"nullable_inner"`
	Null  sql.Null[nullInner] `vortex:"nullable_inner"`
	Same  sql.Null[innerRow]  `vortex:"inner"`
	Value float64             `vortex:"value"`
}

func TestRowsNestedStructs(t *testing.T) {
	f := openFixture(t, fixtureDir+"struct_nested.vortex")
	b := allBatch(t, f)
	p, err := newRowPlan(f, reflect.TypeFor[nestedRow]())
	if err != nil {
		t.Fatal(err)
	}
	fast := map[string]bool{}
	for _, c := range p.cells {
		fast[reflect.TypeFor[nestedRow]().FieldByIndex(c.path[:1]).Name+"/"+string(rune('0'+len(c.path)))] = c.fast != nil
	}
	if !fast["Inner/2"] || !fast["Value/1"] || fast["Ptr/1"] || fast["Null/1"] || fast["Same/1"] {
		t.Fatalf("fast cells: %v", fast)
	}
	rows := readRows[nestedRow](t, f, Query{}, nil)
	inner, nullable := colByName(f, b, "inner"), colByName(f, b, "nullable_inner")
	if len(rows) != 3 {
		t.Fatalf("%d rows", len(rows))
	}
	for i, r := range rows {
		a, bb := at[int32](inner.Field(0), i), string(inner.Field(1).Bytes(i))
		if r.Inner != (innerRow{a, bb}) || r.Flat.A != a || r.Same.V != r.Inner || !r.Same.Valid ||
			r.Value != at[float64](colByName(f, b, "value"), i) {
			t.Fatalf("row %d: %+v", i, r)
		}
		if nullable.Null(i) {
			if r.Ptr != nil || r.Null.Valid || r.Null.V != (nullInner{}) {
				t.Fatalf("row %d: %+v", i, r)
			}
			continue
		}
		want := nullInner{at[int64](nullable.Field(0), i), at[float32](nullable.Field(1), i)}
		if r.Ptr == nil || *r.Ptr != want || !r.Null.Valid || r.Null.V != want {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	var nulls int
	for _, r := range rows {
		if r.Ptr == nil {
			nulls++
		}
	}
	if nulls == 0 || nulls == 3 {
		t.Fatalf("%d null structs", nulls)
	}
}

// elemsOf reads row i of a list, a fixed-size list or a map column with the accessors, as the
// slice of the oracle values of its elements of the Go type elem.
func elemsOf(c *Column, i int, elem reflect.Type) reflect.Value {
	start, end := c.List(i)
	out := reflect.MakeSlice(reflect.SliceOf(elem), end-start, end-start)
	for j := start; j < end; j++ {
		dt := elem
		if isSQLNull(dt) {
			dt = dt.Field(0).Type
		}
		for dt.Kind() == reflect.Pointer {
			dt = dt.Elem()
		}
		reflectSet(out.Index(j-start), oracle(c.Elems(), j, dt))
	}
	return out
}

type listRow struct {
	Ints    []int32           `vortex:"int_list"`
	Strs    []string          `vortex:"str_list"`
	Nullish *[]int32          `vortex:"nullable_int_list"`
	Opt     sql.Null[[]int32] `vortex:"nullable_int_list"`
}

type vecRow struct {
	Vec  [3]float64 `vortex:"vectors"`
	Vecs []float64  `vortex:"vectors"`
	NV   *[2]int32  `vortex:"nullable_vectors"`
}

type mapRow struct {
	Attrs  map[int32]sql.Null[string] `vortex:"attrs"`
	Sorted map[string]int64           `vortex:"sorted_attrs"`
	Opt    *map[int32]*string         `vortex:"nullable_attrs"`
}

func TestRowsLists(t *testing.T) {
	f := openFixture(t, fixtureDir+"list.vortex")
	b := allBatch(t, f)
	for i, r := range readRows[listRow](t, f, Query{}, nil) {
		ints, strs := colByName(f, b, "int_list"), colByName(f, b, "str_list")
		if !same(r.Ints, elemsOf(ints, i, reflect.TypeFor[int32]()).Interface()) ||
			!same(r.Strs, elemsOf(strs, i, reflect.TypeFor[string]()).Interface()) {
			t.Fatalf("row %d: %+v", i, r)
		}
		n := colByName(f, b, "nullable_int_list")
		if n.Null(i) != (r.Nullish == nil) || n.Null(i) == r.Opt.Valid ||
			!n.Null(i) && (!same(*r.Nullish, elemsOf(n, i, reflect.TypeFor[int32]()).Interface()) || !same(r.Opt.V, *r.Nullish)) {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	f = openFixture(t, fixtureDir+"fixed_size_list.vortex")
	b = allBatch(t, f)
	for i, r := range readRows[vecRow](t, f, Query{}, nil) {
		v := elemsOf(colByName(f, b, "vectors"), i, reflect.TypeFor[float64]()).Interface().([]float64)
		if !slices.Equal(r.Vecs, v) || !slices.Equal(r.Vec[:], v) {
			t.Fatalf("row %d: %+v, want %v", i, r, v)
		}
		n := colByName(f, b, "nullable_vectors")
		if n.Null(i) != (r.NV == nil) || !n.Null(i) && !slices.Equal(r.NV[:], elemsOf(n, i, reflect.TypeFor[int32]()).Interface().([]int32)) {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	f = openFixture(t, fixtureDir+"map.vortex")
	b = allBatch(t, f)
	for i, r := range readRows[mapRow](t, f, Query{}, nil) {
		attrs, sorted, opt := colByName(f, b, "attrs"), colByName(f, b, "sorted_attrs"), colByName(f, b, "nullable_attrs")
		start, end := attrs.List(i)
		want := map[int32]sql.Null[string]{}
		for j := start; j < end; j++ { // a later entry replaces an earlier one with the same key
			e := attrs.Elems()
			v := e.Field(1)
			s := sql.Null[string]{Valid: !v.Null(j)}
			if s.Valid {
				s.V = string(v.Bytes(j))
			}
			want[at[int32](e.Field(0), j)] = s
		}
		if !reflect.DeepEqual(r.Attrs, want) {
			t.Fatalf("row %d: %v, want %v", i, r.Attrs, want)
		}
		start, end = sorted.List(i)
		if len(r.Sorted) > end-start {
			t.Fatalf("row %d: %v", i, r.Sorted)
		}
		for j := start; j < end; j++ {
			e := sorted.Elems()
			if r.Sorted[string(e.Field(0).Bytes(j))] != at[int64](e.Field(1), j) {
				t.Fatalf("row %d: %v", i, r.Sorted)
			}
		}
		if opt.Null(i) != (r.Opt == nil) || r.Opt != nil && len(*r.Opt) > 0 && len(*r.Opt) > opt.Elems().arr.Len {
			t.Fatalf("row %d: %v", i, r.Opt)
		}
		if r.Opt != nil {
			s, e := opt.List(i)
			for j := s; j < e; j++ {
				v := opt.Elems().Field(1)
				got := (*r.Opt)[at[int32](opt.Elems().Field(0), j)]
				if (got == nil) != v.Null(j) || got != nil && *got != string(v.Bytes(j)) {
					t.Fatalf("row %d: %v", i, got)
				}
			}
		}
	}
}

// listsRow holds a list of every kind of element, read by the reflect decoders.
type listsRow struct {
	Bool  []bool            `vortex:"in_list_bool_r"`
	I8    []int8            `vortex:"in_list_i8_r"`
	I16   []int16           `vortex:"in_list_i16_r"`
	I32   []int32           `vortex:"in_list_i32_r"`
	I64   []int64           `vortex:"in_list_i64_r"`
	Int   []int             `vortex:"in_list_i64_r"`
	U8    []uint8           `vortex:"in_list_u8_r"`
	U16   []uint16          `vortex:"in_list_u16_r"`
	U32   []uint32          `vortex:"in_list_u32_r"`
	U64   []uint64          `vortex:"in_list_u64_r"`
	Uint  []uint            `vortex:"in_list_u64_r"`
	F16   []float32         `vortex:"in_list_f16_r"`
	F32   []float32         `vortex:"in_list_f32_r"`
	F64   []float64         `vortex:"in_list_f64_r"`
	Dec   []Decimal         `vortex:"in_list_decimal_r"`
	DecI  []int64           `vortex:"in_list_decimal_r"`
	Str   []string          `vortex:"in_list_utf8_r"`
	Raw   [][]byte          `vortex:"in_list_binary_r"`
	Date  []time.Time       `vortex:"in_list_date_r"`
	DateI []int32           `vortex:"in_list_date_r"`
	Time  []time.Duration   `vortex:"in_list_time_r"`
	TimeI []int64           `vortex:"in_list_time_r"`
	TS    []time.Time       `vortex:"in_list_timestamp_r"`
	TSI   []int64           `vortex:"in_list_timestamp_r"`
	NullI []sql.Null[int32] `vortex:"in_list_i32_n"`
	PtrI  []*int32          `vortex:"in_list_i32_n"`
	Strct []struct {
		V int32 `vortex:"v"`
	} `vortex:"in_list_struct_r_r"`
	StrN []*struct {
		V sql.Null[int32] `vortex:"v"`
	} `vortex:"in_list_struct_n_n"`
	Lists [][]int32            `vortex:"in_list_list_r_r"`
	Maps  []map[string]int32   `vortex:"in_list_map_r_r"`
	MapsN []*map[string]*int32 `vortex:"in_list_map_n_n"`
}

func TestRowsListElementKinds(t *testing.T) {
	f := openFixture(t, matrixPath)
	b := allBatch(t, f)
	rows := readRows[listsRow](t, f, Query{}, nil)
	rv := reflect.TypeFor[listsRow]()
	total := 0
	for i, r := range rows {
		for k := range rv.NumField() {
			sf := rv.Field(k)
			col, ok := colNamed(f, b, sf.Tag.Get("vortex"))
			if !ok {
				t.Fatalf("no column for %s", sf.Name)
			}
			if sf.Type.Kind() != reflect.Slice || strings.Contains(sf.Name, "Strct") || strings.Contains(sf.Name, "StrN") ||
				strings.Contains(sf.Name, "Lists") || strings.Contains(sf.Name, "Maps") {
				continue
			}
			want := elemsOf(col, i, sf.Type.Elem())
			if !same(reflect.ValueOf(r).Field(k).Interface(), want.Interface()) {
				t.Fatalf("row %d field %s: %v, want %v", i, sf.Name, reflect.ValueOf(r).Field(k), want)
			}
			total += want.Len()
		}
	}
	if total == 0 {
		t.Fatal("no list element compared")
	}
}

func colNamed(f *File, b *Batch, name string) (*Column, bool) {
	name, _, _ = strings.Cut(name, ",")
	for i, fl := range f.Schema().Fields() {
		if fl.Name == name {
			return b.Col(i), true
		}
	}
	return nil, false
}

type timesRow struct {
	Seconds time.Time           `vortex:"ts_seconds"`
	Millis  time.Time           `vortex:"ts_millis_tz"`
	Micros  *time.Time          `vortex:"ts_nullable_micros"`
	Date    time.Time           `vortex:"date_days"`
	Clock   time.Duration       `vortex:"time_seconds"`
	Nanos   time.Time           `vortex:"ts_nanos"`
	East    time.Time           `vortex:"ts_eastern"`
	EastN   sql.Null[time.Time] `vortex:"ts_eastern"`
	DateI   int32               `vortex:"date_days"`
	ClockI  int32               `vortex:"time_seconds"`
	NanosI  int64               `vortex:"ts_nanos"`
}

func TestRowsTimes(t *testing.T) {
	f := openFixture(t, fixtureDir+"datetime.vortex")
	b := allBatch(t, f)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone database")
	}
	for i, r := range readRows[timesRow](t, f, Query{}, nil) {
		sec, ms := storageInt(colByName(f, b, "ts_seconds"), i), storageInt(colByName(f, b, "ts_millis_tz"), i)
		days, clock := storageInt(colByName(f, b, "date_days"), i), storageInt(colByName(f, b, "time_seconds"), i)
		ns, east := storageInt(colByName(f, b, "ts_nanos"), i), storageInt(colByName(f, b, "ts_eastern"), i)
		if !r.Seconds.Equal(time.Unix(sec, 0)) || r.Seconds.Location() != time.UTC ||
			!r.Millis.Equal(time.UnixMilli(ms)) || !r.Date.Equal(time.Unix(days*86400, 0)) ||
			r.Clock != time.Duration(clock)*time.Second || !r.Nanos.Equal(time.Unix(0, ns)) ||
			!r.East.Equal(time.Unix(east, 0)) || r.East.Location().String() != ny.String() || !r.EastN.V.Equal(r.East) ||
			r.DateI != int32(days) || r.ClockI != int32(clock) || r.NanosI != ns {
			t.Fatalf("row %d: %+v", i, r)
		}
		mc := colByName(f, b, "ts_nullable_micros")
		if mc.Null(i) != (r.Micros == nil) || r.Micros != nil && !r.Micros.Equal(time.UnixMicro(storageInt(mc, i))) {
			t.Fatalf("row %d: %v", i, r.Micros)
		}
	}
}

type partsRow struct {
	US  time.Time  `vortex:"ts_us"`
	NS  time.Time  `vortex:"ts_ns"`
	MS  time.Time  `vortex:"ts_ms"`
	USN *time.Time `vortex:"ts_us_nullable"`
	S   time.Time  `vortex:"ts_s"`
	Pre time.Time  `vortex:"ts_pre_1970"`
	Sub time.Time  `vortex:"ts_ns_subsecond"`
	Day time.Time  `vortex:"ts_day_boundary"`
}

func TestRowsTimestampUnits(t *testing.T) {
	f := openFixture(t, fixtureDir+"datetimeparts.vortex")
	b := allBatch(t, f)
	negative := 0
	for i, r := range readRows[partsRow](t, f, Query{}, nil) {
		g := func(n string) int64 { return storageInt(colByName(f, b, n), i) }
		if !r.US.Equal(time.UnixMicro(g("ts_us"))) || !r.NS.Equal(time.Unix(0, g("ts_ns"))) ||
			!r.MS.Equal(time.UnixMilli(g("ts_ms"))) || !r.S.Equal(time.Unix(g("ts_s"), 0)) ||
			!r.Pre.Equal(time.UnixMilli(g("ts_pre_1970"))) || !r.Sub.Equal(time.Unix(0, g("ts_ns_subsecond"))) ||
			!r.Day.Equal(time.UnixMilli(g("ts_day_boundary"))) {
			t.Fatalf("row %d: %+v", i, r)
		}
		if r.Pre.Before(time.Unix(0, 0)) {
			negative++
		}
	}
	if negative == 0 {
		t.Fatal("no time before 1970")
	}
}

type decRow struct {
	D52   Decimal           `vortex:"dec_5_2"`
	D52I  int64             `vortex:"dec_5_2"`
	D104  Decimal           `vortex:"dec_10_4"`
	D180  int               `vortex:"dec_18_0"`
	Null  sql.Null[Decimal] `vortex:"dec_nullable_7_3"`
	NullI *int64            `vortex:"dec_nullable_7_3"`
}

type wideDecRow struct {
	P19 Decimal `vortex:"i128_p19_s2"`
	P38 Decimal `vortex:"i128_p38_s2"`
	P39 Decimal `vortex:"i256_p39_s2"`
	P76 Decimal `vortex:"i256_p76_s0"`
}

func TestRowsDecimals(t *testing.T) {
	f := openFixture(t, fixtureDir+"decimal.vortex")
	b := allBatch(t, f)
	for i, r := range readRows[decRow](t, f, Query{}, nil) {
		c := colByName(f, b, "dec_nullable_7_3")
		v, _ := colByName(f, b, "dec_5_2").Decimal(i).Int64()
		if r.D52 != colByName(f, b, "dec_5_2").Decimal(i) || r.D52I != v || r.D104 != colByName(f, b, "dec_10_4").Decimal(i) ||
			r.Null.Valid == c.Null(i) || r.Null.Valid && r.Null.V != c.Decimal(i) || (r.NullI == nil) != c.Null(i) {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
	f = openFixture(t, fixtureDir+"decimal_canonical_widths.vortex")
	b = allBatch(t, f)
	for i, r := range readRows[wideDecRow](t, f, Query{}, nil) {
		if r.P19 != colByName(f, b, "i128_p19_s2").Decimal(i) || r.P38 != colByName(f, b, "i128_p38_s2").Decimal(i) ||
			r.P39 != colByName(f, b, "i256_p39_s2").Decimal(i) || r.P76 != colByName(f, b, "i256_p76_s0").Decimal(i) {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
}

func TestRowsUUID(t *testing.T) {
	type row struct {
		ID   *[16]byte          `vortex:"id"`
		Null sql.Null[[16]byte] `vortex:"nullable_id"`
	}
	f := openFixture(t, "testdata/fixtures/0.95.0/extra/uuid.vortex")
	b := allBatch(t, f)
	for i, r := range readRows[row](t, f, Query{}, nil) {
		id, n := colByName(f, b, "id"), colByName(f, b, "nullable_id")
		if (r.ID == nil) != id.Null(i) || r.ID != nil && *r.ID != [16]byte(id.Bytes(i)) ||
			r.Null.Valid == n.Null(i) || r.Null.Valid && r.Null.V != [16]byte(n.Bytes(i)) {
			t.Fatalf("row %d: %+v", i, r)
		}
	}
}

func TestRowsVariant(t *testing.T) {
	f, err := open(variantFile())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		V any `vortex:"v"`
	}
	got := readRows[row](t, f, Query{Rows: Range{End: 4}}, nil)
	micros := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	want := []any{map[string]any{"k": int64(5)}, VariantDecimal{Value: DecimalFromInt64(12345), Scale: 2}, micros, nil}
	for i, w := range want {
		if !reflect.DeepEqual(got[i].V, w) {
			t.Errorf("row %d = %#v, want %#v", i, got[i].V, w)
		}
	}
	if err := rowsErr[row](f, Query{}); err == nil || !strings.Contains(err.Error(), "cut short") {
		t.Fatalf("a damaged variant: %v", err)
	}
	type nullable struct {
		V sql.Null[any] `vortex:"v"`
	}
	if r := readRows[nullable](t, f, Query{Rows: Range{End: 4}}, nil); !r[0].V.Valid || r[3].V.Valid {
		t.Fatalf("%+v", r)
	}
}

func TestRowsQuery(t *testing.T) {
	type row struct {
		Sorted sql.Null[int64] `vortex:"sorted"`
	}
	f := openFixture(t, fixtureDir+"extra/default_ints.vortex")
	q := Query{Filter: Col("sorted").Lt(1003), Rows: Range{End: 10}}
	got := readRows[row](t, f, q, nil)
	if len(got) != 3 || got[0].Sorted.V != 1000 || got[2].Sorted.V != 1002 || !got[2].Sorted.Valid {
		t.Fatalf("%v", got)
	}
	if err := rowsErr[row](f, Query{Filter: Col("missing").Eq(1)}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("a filter on a missing column: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, err := range Rows[row](ctx, f, Query{}) {
		if err == nil || !strings.Contains(err.Error(), "canceled") {
			t.Fatalf("a canceled context: %v", err)
		}
		return
	}
	t.Fatal("no error")
}

func TestRowsErrors(t *testing.T) {
	f := openFixture(t, matrixPath)
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"not a struct", rowsErr[int](f, Query{}), "vortex: Rows: int is not a struct"},
		{"no field", rowsErr[struct{}](f, Query{}), "no field of struct {} is read from a column"},
		{"only skipped", rowsErr[struct {
			A int `vortex:"-"`
			b int
		}](f, Query{}), "no field of"},
		{"only optional", rowsErr[struct {
			A int `vortex:"nope,optional"`
		}](f, Query{}), "no field of"},
		{"missing column", rowsErr[struct {
			City string `vortex:"city"`
		}](f, Query{}), `vortex: Rows: field City (string): column "city" not found`},
		{"i32 into int64", rowsErr[struct {
			ID int64 `vortex:"i32_r"`
		}](f, Query{}), `vortex: Rows: field ID (int64): column "i32_r" of type i32: want int32`},
		{"nullable into plain", rowsErr[struct {
			ID int32 `vortex:"i32_n"`
		}](f, Query{}), `column "i32_n" of type i32?: the column is nullable: the field must be a sql.Null[X] or a pointer`},
		{"null column", rowsErr[struct {
			N int `vortex:"null_n"`
		}](f, Query{}), `column "null_n" of type null: no Go type holds a column of type null`},
		{"element nullable", rowsErr[struct {
			V []int32 `vortex:"in_list_i32_n"`
		}](f, Query{}), `elements: the column is nullable`},
		{"element type", rowsErr[struct {
			V []int64 `vortex:"in_list_i32_r"`
		}](f, Query{}), `elements: want int32`},
		{"fixed-size list of 3 into [4]", rowsErr[struct {
			V [4]sql.Null[int32] `vortex:"fsl_n_r"`
		}](f, Query{}), `a fixed-size list of 3 does not fit [4]sql.Null[int32]: want [3]X or []X`},
		{"array for a list", rowsErr[struct {
			V [3]int32 `vortex:"list_r_r"`
		}](f, Query{}), `want a slice`},
		{"map keys", rowsErr[struct {
			V map[int32]int32 `vortex:"map_r_r"`
		}](f, Query{}), `keys: want string or []byte`},
		{"map values", rowsErr[struct {
			V map[string]string `vortex:"map_r_r"`
		}](f, Query{}), `values: want int32`},
		{"nested field", rowsErr[struct {
			S struct {
				V int64 `vortex:"v"`
			} `vortex:"struct_r_r"`
		}](f, Query{}), `field S.V (int64): column "v" of type i32: want int32`},
		{"nested in a struct slice", rowsErr[struct {
			S []struct{ W int32 } `vortex:"in_list_struct_r_r"`
		}](f, Query{}), `field S.W (int32): column "W" not found`},
		{"decimal 19 into int64", rowsErr[struct {
			D int64 `vortex:"decimal_r"`
		}](openFixture(t, fixtureDir+"decimal_canonical_widths.vortex"), Query{}), ``},
	} {
		if c.name == "decimal 19 into int64" {
			c.err = rowsErr[struct {
				D int64 `vortex:"i128_p19_s2"`
			}](openFixture(t, fixtureDir+"decimal_canonical_widths.vortex"), Query{})
			c.want = `a decimal of precision 19 does not fit in int64: want vortex.Decimal`
		}
		if c.err == nil || !strings.Contains(c.err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, c.err, c.want)
		}
	}
}

// TestRowsMismatches checks that a field of the wrong Go type for each kind of column is an
// error, and that the right one is not.
func TestRowsMismatches(t *testing.T) {
	f := openFixture(t, matrixPath)
	bad := map[string]reflect.Type{
		"bool_r": reflect.TypeFor[int](), "i8_r": reflect.TypeFor[int](), "decimal_r": reflect.TypeFor[string](),
		"utf8_r": reflect.TypeFor[int](), "binary_r": reflect.TypeFor[string](), "uuid_r": reflect.TypeFor[[15]byte](),
		"date_r": reflect.TypeFor[string](), "time_r": reflect.TypeFor[time.Time](), "timestamp_r": reflect.TypeFor[time.Duration](),
		"list_r_r": reflect.TypeFor[map[string]int](), "fsl_r_r": reflect.TypeFor[int](), "map_r_r": reflect.TypeFor[[]int](),
		"struct_r_r": reflect.TypeFor[int](), "f64_r": reflect.TypeFor[any](), "date_n": reflect.TypeFor[*time.Duration](),
		"i64_r": reflect.TypeFor[uint](), "i32_n": reflect.TypeFor[sql.Null[string]](), "u64_r": reflect.TypeFor[int](), "decimal_n": reflect.TypeFor[*uint64](),
	}
	for col, gt := range bad {
		rt := reflect.StructOf([]reflect.StructField{{Name: "F", Type: gt, Tag: reflect.StructTag(`vortex:"` + col + `"`)}})
		if _, err := newRowPlan(f, rt); err == nil || !strings.Contains(err.Error(), "want ") {
			t.Errorf("%s as %s: %v", col, gt, err)
		}
	}
	// A variant is read as any only.
	type v struct {
		V int `vortex:"v"`
	}
	vf, err := open(variantFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := rowsErr[v](vf, Query{}); err == nil || !strings.Contains(err.Error(), "want any") {
		t.Fatalf("%v", err)
	}
}

func TestRowsZoneError(t *testing.T) {
	_, err := baseDecoder(timeType, Type{kind: Timestamp, zone: "Nowhere/Nothing", unit: Second}, "")
	if err == nil {
		t.Fatal("an unknown zone must be an error")
	}
	if loc, err := locationOf(""); err != nil || loc != time.UTC {
		t.Fatalf("%v %v", loc, err)
	}
}

func TestTimeOfBeforeTheEpoch(t *testing.T) {
	for _, v := range []int64{-1, -999, -1000, -1001, 0, 1, 1999} {
		if got, want := timeOf(v, Millisecond, time.UTC), time.UnixMilli(v); !got.Equal(want) {
			t.Errorf("%d ms: %v, want %v", v, got, want)
		}
	}
	if got := timeOf(-3, Day, time.UTC); !got.Equal(time.Date(1969, 12, 29, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("-3 days: %v", got)
	}
}

func TestCheckLayout(t *testing.T) {
	row, leaf := reflect.TypeFor[struct{ A, B int64 }](), reflect.TypeFor[int64]()
	if !checkLayout(row, leaf, 8, 8) {
		t.Fatal("the second field fits")
	}
	for _, c := range []struct{ off, size uintptr }{{16, 8}, {4, 8}, {0, 4}} {
		if checkLayout(row, leaf, c.off, c.size) {
			t.Errorf("offset %d, size %d must be refused", c.off, c.size)
		}
	}
	if guard(false, func(*Column, reflect.Value) error { return nil }) != nil {
		t.Fatal("a refused layout must give no fill")
	}
}

type allocRow struct {
	I  int64            `vortex:"i64_r"`
	F  float64          `vortex:"f64_r"`
	S  int32            `vortex:"i32_r"`
	B  bool             `vortex:"bool_r"`
	Y  []byte           `vortex:"binary_r"`
	N  sql.Null[int64]  `vortex:"i64_n"`
	NS sql.Null[[]byte] `vortex:"binary_n"`
	T  string           `vortex:"utf8_r"`
	TN sql.Null[string] `vortex:"utf8_n"`
}

// TestRowsFillAllocations checks that the fill of fixed-width fields, bytes, strings and
// nullable ones allocates for a batch of rows, the same for 1 row and for all, so nothing for
// a row.
func TestRowsFillAllocations(t *testing.T) {
	f := openFixture(t, matrixPath)
	p, err := newRowPlan(f, reflect.TypeFor[allocRow]())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range p.cells {
		if c.fast == nil {
			t.Fatalf("a field of %v is not filled by the fast path", c.path)
		}
	}
	// The fill is measured in the iteration that reads the batch: a batch is valid for the
	// loop iteration that reads it, and the scan gives its memory back when the loop ends.
	var n int
	var one, all float64
	for b := range f.scan(context.Background(), &scanOptions{batchRows: maxBatchRows, columns: p.columns}) {
		n = b.Len()
		rows := reflect.ValueOf(make([]allocRow, n))
		// The fill allocates for the batch (the closures that read it, the block of the
		// strings), never for a row.
		first := rows.Slice(0, 1)
		one = testing.AllocsPerRun(50, func() { _ = p.fill(b, first) })
		all = testing.AllocsPerRun(50, func() { _ = p.fill(b, rows) })
	}
	if n == 0 {
		t.Fatal("the scan returned no batch")
	}
	if one != all || all > 9 {
		t.Fatalf("a fill of 1 row allocates %v times, of %d rows %v", one, n, all)
	}
}

// TestRowsAllocationsPerRow checks that a scan of many rows allocates per batch, not per row.
func TestRowsAllocationsPerRow(t *testing.T) {
	type row struct {
		A, B, C, D sql.Null[int64] `vortex:"sorted"`
	}
	type quad struct {
		A sql.Null[int64] `vortex:"sorted"`
		B sql.Null[int64] `vortex:"low_card"`
		C sql.Null[int64] `vortex:"random"`
		D sql.Null[int64] `vortex:"nullable"`
	}
	f := openFixture(t, fixtureDir+"extra/default_ints.vortex")
	n := 0
	run := func() {
		n = 0
		for _, err := range Rows[quad](context.Background(), f, Query{}) {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
	}
	run()
	allocs := testing.AllocsPerRun(5, run)
	if n != 30000 || allocs > float64(n)/100 {
		t.Fatalf("%d rows, %v allocations", n, allocs)
	}
}

func TestRowsBreakStopsScan(t *testing.T) {
	f := openFixture(t, fixtureDir+"extra/default_ints.vortex")
	type row struct {
		A int32           `vortex:"nope,optional"`
		B sql.Null[int64] `vortex:"sorted"`
	}
	before := runtime.NumGoroutine()
	n := 0
	for _, err := range Rows[row](context.Background(), f, Query{}) {
		if err != nil {
			t.Fatal(err)
		}
		if n++; n == 5 {
			break
		}
	}
	if n != 5 {
		t.Fatalf("%d rows", n)
	}
	// The workers of the scan end after the loop returns, not before: wait for them, as the
	// tests of fault_test.go do, rather than count them at once.
	waitGoroutines(t, before)
}

func TestRowsConcurrentLoops(t *testing.T) {
	type row struct {
		A sql.Null[int64] `vortex:"sorted"`
		B sql.Null[int64] `vortex:"random"`
	}
	f := openFixture(t, fixtureDir+"extra/default_ints.vortex")
	sums := make([]int64, 2)
	var wg sync.WaitGroup
	for k := range sums {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r, err := range Rows[row](context.Background(), f, Query{}) {
				if err != nil {
					t.Error(err)
					return
				}
				sums[k] += r.A.V ^ r.B.V
			}
		}()
	}
	wg.Wait()
	if sums[0] != sums[1] || sums[0] == 0 {
		t.Fatalf("sums %v", sums)
	}
}

// TestRowsDecimalPrecisionEdge checks the edge of the int64 rule: a decimal of precision 18
// fits in an int64 field and is read with its unscaled values, one of precision 19 or 20 does not.
func TestRowsDecimalPrecisionEdge(t *testing.T) {
	f := openFixture(t, fixtureDir+"decimal_wide_storage.vortex")
	b := allBatch(t, f)
	c := colByName(f, b, "i128_p18_s2")
	if c.Type().Precision() != 18 {
		t.Fatalf("precision %d, want 18", c.Type().Precision())
	}
	type row struct {
		D int64 `vortex:"i128_p18_s2"`
	}
	rows := readRows[row](t, f, Query{}, nil)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for i, r := range rows {
		if want, _ := c.Decimal(i).Int64(); r.D != want {
			t.Fatalf("row %d: %d, want %d", i, r.D, want)
		}
	}
	if rows[0].D != -999999999999999999 || rows[len(rows)-1].D != 999999999999999999 {
		t.Fatalf("edge values: %+v", rows)
	}

	// Precision 20: no fixture has one, so the decoder is asked directly.
	for _, p := range []uint8{19, 20} {
		_, err := decimalDecoder(reflect.TypeFor[int64](), Type{kind: KindDecimal, precision: p}, errors.New("mismatch"))
		want := fmt.Sprintf("a decimal of precision %d does not fit in int64: want vortex.Decimal", p)
		if err == nil || err.Error() != want {
			t.Errorf("precision %d: %v, want %q", p, err, want)
		}
	}
}
