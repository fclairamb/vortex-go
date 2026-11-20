package vortex

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"
)

type rwInner struct {
	A int32
	B string
	C sql.Null[float64]
}

type rwEmbedded struct {
	E1 int16
	E2 string `vortex:"e2"`
}

// rwAll has a field of every kind the writer takes, and the tag options.
type rwAll struct {
	rwEmbedded
	I8      int8
	I16     int16
	I32     int32
	I64     int64
	I       int
	U8      uint8
	U16     uint16
	U32     uint32
	U64     uint64
	U       uint
	F32     float32
	F64     float64
	B       bool
	S       string
	Bin     []byte
	ID      [16]byte
	T       time.Time
	D       time.Duration
	Dec     Decimal         `vortex:"dec,decimal(20,3)"`
	DecInt  int64           `vortex:"decint,decimal(10,2)"`
	Date    time.Time       `vortex:"date,date"`
	TSms    time.Time       `vortex:"tsms,timestamp(ms)"`
	TSs     time.Time       `vortex:"tss,timestamp(s)"`
	TSns    time.Time       `vortex:"tsns,timestamp(ns)"`
	Tm      time.Duration   `vortex:"tm,time(us)"`
	TmS     time.Duration   `vortex:"tms,time(s)"`
	TmMs    time.Duration   `vortex:"tmms,time(ms)"`
	NI      sql.Null[int64] `vortex:"ni"`
	NS      sql.Null[string]
	NBin    sql.Null[[]byte]
	NT      sql.Null[time.Time]
	NU      sql.Null[[16]byte]
	ND      sql.Null[Decimal] `vortex:"nd,decimal(10,2)"`
	NB      sql.Null[bool]
	NDur    sql.Null[time.Duration]
	NDate   sql.Null[time.Time] `vortex:"ndate,date"`
	PF      *float64
	PS      *string
	PI      *int8
	PDec    *int64 `vortex:"pdec,decimal(5,1)"`
	PTime   *time.Time
	St      rwInner
	PSt     *rwInner
	L       []int32
	LS      []string
	LP      []*int64
	LL      [][]int16
	LSt     []rwInner
	LT      []time.Time `vortex:"lt,timestamp(ms)"`
	LB      [][]byte
	Arr     [3]int32
	ArrS    [2]string
	ArrP    [2]*int32
	ArrU    [2][16]byte
	M       map[string]int64
	MS      map[int32][]string
	MSt     map[string]rwInner
	PL      *[]int32
	Skip    int   `vortex:"-"`
	Renamed int64 `vortex:"renamed,optional"`
	private int
}

func rwFill(r *rand.Rand, b []byte) {
	for i := range b {
		b[i] = byte(r.Uint32())
	}
}

func rwMicro(r *rand.Rand, unit int64) time.Time {
	return time.Unix(r.Int64N(4e9)-1e9, r.Int64N(1e9/unit)*unit).UTC()
}

func rwRow(r *rand.Rand, allNull bool) rwAll {
	null := func() bool { return allNull || r.IntN(4) == 0 }
	row := rwAll{
		rwEmbedded: rwEmbedded{E1: int16(r.Int32()), E2: strings.Repeat("e", r.IntN(5))},
		I8:         int8(r.Int32()), I16: int16(r.Int32()), I32: r.Int32(), I64: r.Int64() - 1<<62, I: int(r.Int64()),
		U8: uint8(r.Uint32()), U16: uint16(r.Uint32()), U32: r.Uint32(), U64: r.Uint64(), U: uint(r.Uint64()),
		F32: r.Float32() - 0.5, F64: r.NormFloat64(), B: r.IntN(2) == 0, S: strings.Repeat("é", r.IntN(30)),
		T: rwMicro(r, 1e3), D: time.Duration(r.Int64N(86400e9)),
		Dec: DecimalFromInt64(r.Int64() - 1<<62), DecInt: r.Int64N(1e9) - 5e8,
		Date: time.Unix(r.Int64N(30000)*86400-5e8, 0).UTC().Truncate(24 * time.Hour),
		TSms: rwMicro(r, 1e6), TSs: time.Unix(r.Int64N(4e9)-1e9, 0).UTC(), TSns: time.Unix(r.Int64N(4e9), r.Int64N(1e9)).UTC(),
		Tm: time.Duration(r.Int64N(86400e6)) * time.Microsecond, TmS: time.Duration(r.Int64N(86400)) * time.Second,
		TmMs: time.Duration(r.Int64N(86400e3)) * time.Millisecond,
		L:    make([]int32, r.IntN(5)), LS: make([]string, r.IntN(4)), LP: make([]*int64, r.IntN(4)), LL: make([][]int16, r.IntN(3)),
		LSt: make([]rwInner, r.IntN(3)), LT: make([]time.Time, r.IntN(3)), LB: make([][]byte, r.IntN(3)),
		M: map[string]int64{}, MS: map[int32][]string{}, MSt: map[string]rwInner{},
		Renamed: r.Int64N(100),
	}
	rwFill(r, row.ID[:])
	if r.IntN(3) != 0 {
		row.Bin = bytes.Repeat([]byte{byte(r.Uint32())}, r.IntN(9)+1)
	}
	for i := range row.L {
		row.L[i] = r.Int32()
	}
	for i := range row.LS {
		row.LS[i] = fmt.Sprint(r.IntN(1000))
	}
	for i := range row.LP {
		if !null() {
			v := r.Int64()
			row.LP[i] = &v
		}
	}
	for i := range row.LL {
		row.LL[i] = make([]int16, r.IntN(3))
	}
	for i := range row.LSt {
		row.LSt[i] = rwInner{A: r.Int32(), B: "x", C: sql.Null[float64]{V: 1.5, Valid: !null()}}
	}
	for i := range row.LT {
		row.LT[i] = rwMicro(r, 1e6)
	}
	for i := range row.LB {
		row.LB[i] = bytes.Repeat([]byte{7}, 1+r.IntN(3))
	}
	for i := range row.Arr {
		row.Arr[i] = r.Int32()
		row.ArrS[i%2] = fmt.Sprint(i)
	}
	for i := range 2 {
		rwFill(r, row.ArrU[i][:])
		if !null() {
			v := r.Int32()
			row.ArrP[i] = &v
		}
	}
	for range r.IntN(4) {
		row.M[fmt.Sprint(r.IntN(100))] = r.Int64()
		row.MS[r.Int32N(50)] = []string{"a", "b"}[:r.IntN(3)]
		row.MSt[fmt.Sprint(r.IntN(9))] = rwInner{A: 1, B: "m"}
	}
	row.NI = sql.Null[int64]{V: r.Int64(), Valid: !null()}
	row.NS = sql.Null[string]{V: "ns", Valid: !null()}
	row.NBin = sql.Null[[]byte]{V: []byte{1, 2}, Valid: !null()}
	row.NT = sql.Null[time.Time]{V: rwMicro(r, 1e3), Valid: !null()}
	row.NU = sql.Null[[16]byte]{V: row.ID, Valid: !null()}
	row.ND = sql.Null[Decimal]{V: DecimalFromInt64(r.Int64N(1e9)), Valid: !null()}
	row.NB = sql.Null[bool]{V: true, Valid: !null()}
	row.NDur = sql.Null[time.Duration]{V: time.Duration(r.Int64N(1e9)), Valid: !null()}
	row.NDate = sql.Null[time.Time]{V: time.Unix(86400*r.Int64N(1000), 0).UTC(), Valid: !null()}
	if !null() {
		v := r.Float64()
		row.PF = &v
	}
	if !null() {
		v := "ps"
		row.PS = &v
	}
	if !null() {
		v := int8(r.Int32())
		row.PI = &v
	}
	if !null() {
		v := r.Int64N(1e4)
		row.PDec = &v
	}
	if !null() {
		v := rwMicro(r, 1e3)
		row.PTime = &v
	}
	if !null() {
		row.PSt = &rwInner{A: r.Int32(), B: "p", C: sql.Null[float64]{V: 2.5, Valid: !null()}}
	}
	if !null() {
		v := []int32{1, 2}
		row.PL = &v
	}
	row.St = rwInner{A: r.Int32(), B: "st", C: sql.Null[float64]{V: r.Float64(), Valid: !null()}}
	return row
}

// nullsFix gives what a row that was written and read must look like: the null values of a
// sql.Null are zero.
func rwWant(rows []rwAll) []rwAll {
	out := make([]rwAll, len(rows))
	for i, r := range rows {
		out[i] = r
		out[i].Skip = 0
		if !out[i].NI.Valid {
			out[i].NI.V = 0
		}
		if !r.NS.Valid {
			out[i].NS.V = ""
		}
		if !r.NBin.Valid {
			out[i].NBin.V = nil
		}
		if !r.NT.Valid {
			out[i].NT.V = time.Time{}
		}
		if !r.NU.Valid {
			out[i].NU.V = [16]byte{}
		}
		if !r.ND.Valid {
			out[i].ND.V = Decimal{}
		}
		if !r.NB.Valid {
			out[i].NB.V = false
		}
		if !r.NDur.Valid {
			out[i].NDur.V = 0
		}
		if !r.NDate.Valid {
			out[i].NDate.V = time.Time{}
		}
		out[i].St.C = nullZero(r.St.C)
		out[i].LSt = slicesMap(r.LSt, func(in rwInner) rwInner { in.C = nullZero(in.C); return in })
		if r.PSt != nil {
			c := *r.PSt
			c.C = nullZero(c.C)
			out[i].PSt = &c
		}
	}
	return out
}

func nullZero[V any](n sql.Null[V]) sql.Null[V] {
	if !n.Valid {
		n.V = *new(V)
	}
	return n
}

func slicesMap[A, B any](in []A, f func(A) B) []B {
	out := make([]B, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// writeRows writes the rows in writes of the given sizes, and returns the file.
func writeRowsOf[T any](t *testing.T, rows []T, o WriteOptions, sizes ...int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewRowWriter[T](&buf, o)
	for len(rows) > 0 {
		n := min(len(rows), sizes[0])
		sizes = append(sizes[1:], sizes[0])
		got, err := w.Write(rows[:n])
		if err != nil || got != n {
			t.Fatalf("Write = %d, %v", got, err)
		}
		rows = rows[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	return buf.Bytes()
}

func fixAll(r *rwAll) {
	r.Bin = bytes.Clone(r.Bin)
	r.NBin.V = bytes.Clone(r.NBin.V)
	if len(r.Bin) == 0 {
		r.Bin = nil
	}
	if len(r.NBin.V) == 0 {
		r.NBin.V = nil
	}
	for i := range r.LB {
		r.LB[i] = bytes.Clone(r.LB[i])
	}
}

func TestRowWriterRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name    string
		n       int
		allNull bool
		sizes   []int
	}{
		{"empty", 0, false, []int{1}},
		{"one row", 1, false, []int{1}},
		{"small writes", 300, false, []int{1, 7, 100}},
		{"larger than a chunk", 20000, false, []int{20000}},
		{"larger than a chunk, in parts", 20000, false, []int{5000, 1, 9000}},
		{"all null", 50, true, []int{50}},
	} {
		for _, level := range []Compression{Default, Compact, None} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, level), func(t *testing.T) {
				r := rand.New(rand.NewPCG(1, uint64(tc.n)))
				rows := make([]rwAll, tc.n)
				for i := range rows {
					rows[i] = rwRow(r, tc.allNull)
				}
				file := writeRowsOf(t, rows, WriteOptions{Compression: level}, tc.sizes...)
				f := openBytes(t, file)
				if f.NumRows() != int64(tc.n) {
					t.Fatalf("%d rows", f.NumRows())
				}
				got := readRows[rwAll](t, f, Query{}, fixAll)
				want := rwWant(rows)
				for i := range want {
					fixAll(&want[i])
				}
				if len(got) != len(want) {
					t.Fatalf("%d rows, want %d", len(got), len(want))
				}
				for i := range want {
					if !reflect.DeepEqual(got[i], want[i]) {
						t.Fatalf("row %d:\n got %+v\nwant %+v", i, got[i], want[i])
					}
				}
			})
		}
	}
}

// rwFlat has only fields that are read without reflection.
type rwFlat struct {
	I8   int8
	I16  int16
	I32  int32
	I64  int64
	I    int
	U8   uint8
	U16  uint16
	U32  uint32
	U64  uint64
	U    uint
	F32  float32
	F64  float64
	B    bool
	S    string
	Bin  []byte
	ID   [16]byte
	Dec  Decimal       `vortex:",decimal(30,2)"`
	T    time.Time     `vortex:",timestamp(ns)"`
	TD   time.Time     `vortex:",date"`
	D    time.Duration `vortex:",time(ms)"`
	DN   time.Duration
	NI8  sql.Null[int8]
	NI64 sql.Null[int64]
	NU32 sql.Null[uint32]
	NF   sql.Null[float32]
	NF64 sql.Null[float64]
	NB   sql.Null[bool]
	NS   sql.Null[string]
	NBin sql.Null[[]byte]
	NID  sql.Null[[16]byte]
	NDec sql.Null[Decimal] `vortex:",decimal(30,2)"`
	NT   sql.Null[time.Time]
	ND   sql.Null[time.Duration]
}

func flatRows(n int, seed uint64) []rwFlat {
	r := rand.New(rand.NewPCG(seed, 5))
	rows := make([]rwFlat, n)
	for i := range rows {
		x := &rows[i]
		x.I8, x.I16, x.I32, x.I64, x.I = int8(i), int16(-i), int32(i*3), r.Int64(), int(r.Int64())
		x.U8, x.U16, x.U32, x.U64, x.U = uint8(i), uint16(i*7), r.Uint32(), r.Uint64(), uint(r.Uint64())
		x.F32, x.F64, x.B, x.S = r.Float32(), r.Float64(), i%3 == 0, fmt.Sprint("s", i%7)
		x.Bin = []byte(fmt.Sprint("b", i%5))
		rwFill(r, x.ID[:])
		x.Dec = DecimalFromInt64(int64(i) - 100)
		x.T, x.TD, x.D, x.DN = time.Unix(int64(i)*1000-50000, int64(i)).UTC(), time.Unix(int64(i)*86400-86401, 0), time.Duration(i)*time.Millisecond, time.Duration(i)
		x.NI8, x.NI64 = sql.Null[int8]{V: 1, Valid: i%2 == 0}, sql.Null[int64]{V: int64(i), Valid: i%3 == 0}
		x.NU32, x.NF = sql.Null[uint32]{V: 4, Valid: i%4 == 0}, sql.Null[float32]{V: 1.5, Valid: i%5 == 0}
		x.NF64, x.NB = sql.Null[float64]{V: 2.5, Valid: i%2 == 1}, sql.Null[bool]{V: true, Valid: i%3 == 1}
		x.NS, x.NBin = sql.Null[string]{V: "n", Valid: i%2 == 0}, sql.Null[[]byte]{V: []byte{1}, Valid: i%4 == 1}
		x.NID, x.NDec = sql.Null[[16]byte]{V: x.ID, Valid: i%3 == 2}, sql.Null[Decimal]{V: x.Dec, Valid: i%2 == 1}
		x.NT, x.ND = sql.Null[time.Time]{V: x.T.Truncate(time.Microsecond), Valid: i%2 == 0}, sql.Null[time.Duration]{V: x.D, Valid: i%3 == 0}
	}
	return rows
}

// TestRowWriterFastEqualsReflect makes the file twice, with the unsafe reads and with
// reflection only: the bytes are the same, for every field kind.
func TestRowWriterFastEqualsReflect(t *testing.T) {
	rows := flatRows(10000, 3)
	var fast, slow bytes.Buffer
	wf, ws := NewRowWriter[rwFlat](&fast, WriteOptions{}), NewRowWriter[rwFlat](&slow, WriteOptions{})
	for i, c := range wf.cols {
		if c.fast == nil {
			t.Fatalf("column %d has no fast path", i)
		}
	}
	for i := range ws.cols {
		ws.cols[i].fast = nil
	}
	for _, w := range []*RowWriter[rwFlat]{wf, ws} {
		if n, err := w.Write(rows[:3000]); err != nil || n != 3000 {
			t.Fatal(n, err)
		}
		if n, err := w.Write(rows[3000:]); err != nil || n != 7000 {
			t.Fatal(n, err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(fast.Bytes(), slow.Bytes()) {
		t.Fatal("the file made with unsafe reads is not the one made with reflection")
	}
	got := readRows[rwFlat](t, openBytes(t, fast.Bytes()), Query{}, func(r *rwFlat) {
		r.Bin, r.NBin.V = bytes.Clone(r.Bin), bytes.Clone(r.NBin.V)
	})
	for i, g := range got {
		w := rows[i]
		w.TD = w.TD.UTC().Truncate(24 * time.Hour)
		w.NI8.V, w.NI64.V = nullZero(w.NI8).V, nullZero(w.NI64).V
		if !same(g, nullFlat(w)) {
			t.Fatalf("row %d:\n got %+v\nwant %+v", i, g, nullFlat(w))
		}
	}
}

func nullFlat(w rwFlat) rwFlat {
	w.NI8, w.NI64, w.NU32, w.NF, w.NF64 = nullZero(w.NI8), nullZero(w.NI64), nullZero(w.NU32), nullZero(w.NF), nullZero(w.NF64)
	w.NB, w.NS, w.NBin, w.NID, w.NDec = nullZero(w.NB), nullZero(w.NS), nullZero(w.NBin), nullZero(w.NID), nullZero(w.NDec)
	w.NT, w.ND = nullZero(w.NT), nullZero(w.ND)
	return w
}

func TestRowWriterAllocs(t *testing.T) {
	type row struct {
		A int64
		B float64
		C int32
		D bool
		E []byte
		F sql.Null[int64]
		G string
		H time.Time
	}
	rows := make([]row, 1000)
	for i := range rows {
		rows[i] = row{A: int64(i), B: 1.5, C: 3, D: true, E: []byte("abc"), F: sql.Null[int64]{V: 1, Valid: i%2 == 0}, G: "g"}
	}
	w := NewRowWriter[row](io.Discard, WriteOptions{})
	for _, c := range w.cols {
		if c.fast == nil {
			t.Fatal("no fast path")
		}
	}
	for range 5 {
		if _, err := w.Write(rows); err != nil {
			t.Fatal(err)
		}
	}
	if n := testing.AllocsPerRun(20, func() {
		if _, err := w.Write(rows[:100]); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("%v allocations per Write", n)
	}
}

type rwTagged struct {
	Name string `vortex:"n"`
	Skip int    `vortex:"-"`
}

func schemaOf[T any](t *testing.T) *Schema {
	t.Helper()
	w := NewRowWriter[T](io.Discard, WriteOptions{})
	if w.err != nil {
		t.Fatal(w.err)
	}
	return w.w.schema
}

func sameSchema(t *testing.T, got, want *Schema) {
	t.Helper()
	if !got.root.Equal(want.root) {
		t.Fatalf("schema\n got %s\nwant %s", got, want)
	}
}

func TestRowWriterSchema(t *testing.T) {
	f := func(name string, ty Type) Field { return Field{Name: name, Type: ty} }
	sameSchema(t, schemaOf[rwTagged](t), NewSchema(f("n", Utf8Type())))
	sameSchema(t, schemaOf[struct {
		A time.Time
		B time.Time     `vortex:",date"`
		C time.Time     `vortex:"c,timestamp(ms)"`
		D time.Time     `vortex:"d,timestamp(us,Europe/Paris)"`
		E time.Time     `vortex:",timestamp(s, UTC)"`
		F time.Duration `vortex:",time(us)"`
		G time.Duration
		H time.Duration `vortex:",time(ns)"`
		I Decimal       `vortex:",decimal(38,10)"`
		J int64         `vortex:",decimal(18,2)"`
		K int           `vortex:",decimal(5,0)"`
		L []time.Time   `vortex:",timestamp(ms)"`
		M *time.Time    `vortex:",date"`
		N time.Time     `vortex:"n, date,optional,"`
	}](t), NewSchema(
		f("A", TimestampType(Microsecond, "")), f("B", DateType(Day)), f("c", TimestampType(Millisecond, "")),
		f("d", TimestampType(Microsecond, "Europe/Paris")), f("E", TimestampType(Second, "UTC")),
		f("F", TimeType(Microsecond)), f("G", TimeType(Nanosecond)), f("H", TimeType(Nanosecond)),
		f("I", DecimalOf(38, 10)), f("J", DecimalOf(18, 2)), f("K", DecimalOf(5, 0)),
		f("L", ListOf(TimestampType(Millisecond, ""))), f("M", DateType(Day).AsNullable()), f("n", DateType(Day)),
	))
	sameSchema(t, schemaOf[struct {
		A int8
		B int16
		C int32
		D int64
		E int
		F uint8
		G uint16
		H uint32
		I uint64
		J uint
		K float32
		L float64
		M bool
		N string
		O []byte
		P [16]byte
	}](t), NewSchema(f("A", Int8Type()), f("B", Int16Type()), f("C", Int32Type()), f("D", Int64Type()),
		f("E", Int64Type()), f("F", Uint8Type()), f("G", Uint16Type()), f("H", Uint32Type()), f("I", Uint64Type()),
		f("J", Uint64Type()), f("K", Float32Type()), f("L", Float64Type()), f("M", BoolType()), f("N", Utf8Type()),
		f("O", BinaryType()), f("P", UUIDType())))
	type in struct{ X int32 }
	sameSchema(t, schemaOf[struct {
		rwEmbedded
		A sql.Null[int32]
		B *string
		C in
		D *in
		E []*int64
		F [3]string
		G map[string][]int32
		H map[int32]sql.Null[string]
		I [4]byte
		J []in
		K [2][16]byte
		L sql.Null[[]byte]
		M **int `vortex:"-"`
	}](t), NewSchema(f("E1", Int16Type()), f("e2", Utf8Type()), f("A", Int32Type().AsNullable()),
		f("B", Utf8Type().AsNullable()), f("C", StructOf(f("X", Int32Type()))),
		f("D", StructOf(f("X", Int32Type())).AsNullable()), f("E", ListOf(Int64Type().AsNullable())),
		f("F", FixedSizeListOf(Utf8Type(), 3)), f("G", MapOf(Utf8Type(), ListOf(Int32Type()))),
		f("H", MapOf(Int32Type(), Utf8Type().AsNullable())), f("I", FixedSizeListOf(Uint8Type(), 4)),
		f("J", ListOf(StructOf(f("X", Int32Type())))), f("K", FixedSizeListOf(UUIDType(), 2)),
		f("L", BinaryType().AsNullable())))
}

func TestRowWriterSchemaInFile(t *testing.T) {
	type row struct {
		T time.Time `vortex:"t,timestamp(us,Europe/Paris)"`
	}
	file := writeRowsOf(t, []row{{time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)}}, WriteOptions{}, 1)
	f := openBytes(t, file)
	field, _ := f.Schema().Field("t")
	if !field.Type.Equal(TimestampType(Microsecond, "Europe/Paris")) {
		t.Fatal(field.Type)
	}
	got := readRows[row](t, f, Query{}, nil)
	if len(got) != 1 || !got[0].T.Equal(time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)) {
		t.Fatal(got)
	}
}

type (
	rwChan   struct{ C chan int }
	rwKeyCh  struct{ M map[chan int]int }
	rwKeyPtr struct{ M map[*int]int }
	rwValCh  struct{ M map[int]chan int }
	rwDec    struct{ D Decimal }
	rwNone   struct{ private int }
	rwTwice  struct {
		A int `vortex:"x"`
		B int `vortex:"x"`
	}
	rwNested struct{ In struct{ C chan int } }
	rwList   struct{ L []chan int }
	rwAny    struct{ A any }
	rwPP     struct{ P **int }
	rwSN     struct{ P sql.Null[*int] }
	rwNN     struct{ P sql.Null[sql.Null[int]] }
	rwZero   struct{ A [0]int }
	rwFloat  struct{ C complex128 }
	rwEmptyS struct{ S struct{} }
)

func TestRowWriterErrors(t *testing.T) {
	type s = struct {
		X int32 `vortex:"x,date"`
	}
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"not a struct", errOf[int](), "vortex: RowWriter: int is not a struct"},
		{"a pointer", errOf[*rwTagged](), "vortex: RowWriter: *vortex.rwTagged is not a struct"},
		{"chan", errOf[rwChan](), "vortex: RowWriter: field C (chan int): no Vortex type holds a Go chan"},
		{"complex", errOf[rwFloat](), "field C (complex128): no Vortex type holds a Go complex128"},
		{"any", errOf[rwAny](), "field A (interface {}): no Vortex type holds a Go interface"},
		{"map key", errOf[rwKeyCh](), "field M (map[chan int]int): map key: no Vortex type holds a Go chan"},
		{"nullable map key", errOf[rwKeyPtr](), "field M (map[*int]int): map key: *int is nullable, a key cannot be"},
		{"map value", errOf[rwValCh](), "field M (map[int]chan int): map value: no Vortex type holds a Go chan"},
		{"decimal without options", errOf[rwDec](), `field D (vortex.Decimal): a vortex.Decimal needs its precision and scale in the tag`},
		{"no field", errOf[rwNone](), "vortex: RowWriter: no field of vortex.rwNone is written"},
		{"empty nested", errOf[rwEmptyS](), "field S (struct {}): no field of struct {} is written"},
		{"twice", errOf[rwTwice](), `field B (int): the column "x" is written twice`},
		{"nested", errOf[rwNested](), "field In.C (chan int): no Vortex type holds a Go chan"},
		{"in a list", errOf[rwList](), "field L ([]chan int): elements: no Vortex type holds a Go chan"},
		{"pointer to pointer", errOf[rwPP](), "field P (**int): no Vortex type holds a Go ptr"},
		{"null of pointer", errOf[rwSN](), "no Vortex type holds a Go ptr"},
		{"null of null", errOf[rwNN](), "field P (sql.Null[database/sql.Null[int]]): no Vortex type holds a Go struct"},
		{"zero array", errOf[rwZero](), "field A ([0]int): an array of no element has no Vortex type"},
		{"unknown option", errOf[struct {
			A int `vortex:"a,bogus"`
		}](), `field A (int): bad tag option "bogus": unknown option`},
		{"unbalanced", errOf[struct {
			A Decimal `vortex:"a,decimal(5,2"`
		}](), `bad tag option "decimal(5,2": unbalanced parentheses`},
		{"nested parentheses", errOf[struct {
			A Decimal `vortex:"a,decimal((5),2)"`
		}](), `bad tag option "decimal((5),2)": unbalanced parentheses`},
		{"decimal arguments", errOf[struct {
			A Decimal `vortex:"a,decimal(5)"`
		}](), `bad tag option "decimal(5)": want decimal(precision,scale)`},
		{"decimal precision", errOf[struct {
			A Decimal `vortex:"a,decimal(77,2)"`
		}](), `bad tag option "decimal(77,2)": the precision is 1 to 76`},
		{"decimal scale", errOf[struct {
			A Decimal `vortex:"a,decimal(5,6)"`
		}](), `bad tag option "decimal(5,6)": the precision is 1 to 76 and the scale 0 to the precision`},
		{"decimal not a number", errOf[struct {
			A Decimal `vortex:"a,decimal(x,6)"`
		}](), `bad tag option "decimal(x,6)"`},
		{"date argument", errOf[struct {
			A time.Time `vortex:"a,date(s)"`
		}](), `bad tag option "date(s)": date takes no argument`},
		{"timestamp unit", errOf[struct {
			A time.Time `vortex:"a,timestamp(days)"`
		}](), `bad tag option "timestamp(days)": the unit is s, ms, us or ns`},
		{"timestamp arguments", errOf[struct {
			A time.Time `vortex:"a,timestamp(us,UTC,x)"`
		}](), `bad tag option "timestamp(us,UTC,x)": want timestamp(unit)`},
		{"time zone", errOf[struct {
			A time.Duration `vortex:"a,time(us,UTC)"`
		}](), `bad tag option "time(us,UTC)": want time(unit)`},
		{"option on the wrong type", errOf[s](), `field X (int32): the option "date" does not apply to int32`},
		{"option on a string", errOf[struct {
			A string `vortex:"a,timestamp(us)"`
		}](), `the option "timestamp(us)" does not apply to string`},
		{"option on a struct", errOf[struct {
			A rwInner `vortex:"a,date"`
		}](), `the option "date" does not apply to vortex.rwInner`},
		{"decimal on int32", errOf[struct {
			A int32 `vortex:"a,decimal(5,2)"`
		}](), `the option "decimal(5,2)" needs an int64 or a vortex.Decimal`},
		{"decimal on int64 too large", errOf[struct {
			A int64 `vortex:"a,decimal(19,2)"`
		}](), `the option "decimal(19,2)": an int64 holds a precision of 18 at most`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", tc.err, tc.want)
			}
		})
	}
}

// errOf is the error of a RowWriter of T that cannot be made: its first Write returns it, and
// so does Close when there was no Write.
func errOf[T any]() error {
	var buf bytes.Buffer
	w := NewRowWriter[T](&buf, WriteOptions{})
	_, err := w.Write(make([]T, 1))
	if err == nil {
		return nil
	}
	if n, err2 := w.Write(nil); n != 0 || err2 != err {
		return fmt.Errorf("second Write = %d, %v: not the first error %v", n, err2, err)
	}
	if err2 := w.Close(); err2 != err {
		return fmt.Errorf("Close = %v: not the first error %v", err2, err)
	}
	if err2 := w.Close(); err2 != nil {
		return fmt.Errorf("second Close = %v", err2)
	}
	if buf.Len() != 0 {
		return errors.New("something was written")
	}
	w = NewRowWriter[T](&buf, WriteOptions{})
	if err2 := w.Close(); err2 == nil || err2.Error() != err.Error() {
		return fmt.Errorf("Close without Write = %v, want %v", err2, err)
	}
	return err
}

// failAt is an io.Writer that fails at its nth call.
type failAt struct {
	n, calls int
}

var errFail = errors.New("disk full")

func (f *failAt) Write(p []byte) (int, error) {
	f.calls++
	if f.calls >= f.n {
		return 0, errFail
	}
	return len(p), nil
}

func TestRowWriterFailingWriter(t *testing.T) {
	type row struct {
		A int64
		S string
		B []byte
	}
	rows := make([]row, 3000)
	for i := range rows {
		rows[i] = row{int64(i), strings.Repeat("s", 100+i%50), bytes.Repeat([]byte{byte(i)}, 100)}
	}
	smallWrites, arenaBlock = 4<<10, 4<<10 // many calls of the io.Writer
	t.Cleanup(func() { smallWrites, arenaBlock = 32<<10, 64<<10 })
	failed := 0
	for n := 1; ; n++ {
		w := NewRowWriter[row](&failAt{n: n}, WriteOptions{Compression: None})
		var err error
		taken := 0
		for part := 0; part < len(rows) && err == nil; part += 1000 {
			var k int
			k, err = w.Write(rows[part : part+1000])
			taken += k
		}
		if err != nil {
			if !errors.Is(err, errFail) || taken%1000 != 0 || taken >= len(rows) {
				t.Fatalf("n=%d: Write = %d, %v", n, taken, err)
			}
			if _, err2 := w.Write(rows[:1]); !errors.Is(err2, errFail) {
				t.Fatalf("n=%d: later Write = %v", n, err2)
			}
			if err2 := w.Close(); !errors.Is(err2, errFail) {
				t.Fatalf("n=%d: Close = %v", n, err2)
			}
			if err2 := w.Close(); err2 != nil {
				t.Fatalf("n=%d: second Close = %v", n, err2)
			}
			failed++
			continue
		}
		if err := w.Close(); err != nil {
			if !errors.Is(err, errFail) {
				t.Fatalf("n=%d: Close = %v", n, err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("n=%d: second Close = %v", n, err)
			}
			if _, err2 := w.Write(rows[:1]); !errors.Is(err2, ErrWriterClosed) {
				t.Fatalf("n=%d: Write after Close = %v", n, err2)
			}
			failed++
			continue
		}
		break
	}
	if failed < 3 {
		t.Fatalf("only %d calls of the io.Writer fail", failed)
	}
}

func TestRowWriterAfterClose(t *testing.T) {
	var buf bytes.Buffer
	w := NewRowWriter[rwTagged](&buf, WriteOptions{})
	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(make([]rwTagged, 2)); !errors.Is(err, ErrWriterClosed) {
		t.Fatal(err)
	}
	f := openBytes(t, buf.Bytes())
	if f.NumRows() != 0 {
		t.Fatal(f.NumRows())
	}
	if _, ok := f.Schema().Field("n"); !ok {
		t.Fatal(f.Schema())
	}
}

// The data of a row that was written is copied: the caller reuses its rows.
func TestRowWriterRowsCanBeReused(t *testing.T) {
	type row struct {
		S string
		B []byte
		N sql.Null[int64]
		L []int32
	}
	var buf bytes.Buffer
	w := NewRowWriter[row](&buf, WriteOptions{})
	rows := make([]row, 4)
	for call := range 5 {
		for i := range rows {
			rows[i] = row{S: fmt.Sprint(call, i), B: []byte{byte(call), byte(i)}, N: sql.Null[int64]{V: int64(call), Valid: i != 0}, L: []int32{int32(i)}}
		}
		if _, err := w.Write(rows); err != nil {
			t.Fatal(err)
		}
		for i := range rows {
			rows[i] = row{S: "overwritten", B: []byte{9, 9}, L: []int32{-1}}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := readRows[row](t, openBytes(t, buf.Bytes()), Query{}, func(r *row) { r.B = bytes.Clone(r.B) })
	for i, r := range got {
		call, k := i/4, i%4
		want := row{S: fmt.Sprint(call, k), B: []byte{byte(call), byte(k)}, N: sql.Null[int64]{V: int64(call), Valid: k != 0}, L: []int32{int32(k)}}
		if k == 0 {
			want.N.V = 0
		}
		if !reflect.DeepEqual(r, want) {
			t.Fatalf("row %d = %+v, want %+v", i, r, want)
		}
	}
	if len(got) != 20 {
		t.Fatal(len(got))
	}
}

// A time before 1970 rounds down in its unit, and a zone does not change the instant.
func TestRowWriterTimeRounding(t *testing.T) {
	type row struct {
		Ms   time.Time `vortex:",timestamp(ms)"`
		S    time.Time `vortex:",timestamp(s)"`
		Day  time.Time `vortex:",date"`
		DayN sql.Null[time.Time]
		Us   time.Duration `vortex:",time(us)"`
	}
	paris, _ := time.LoadLocation("Europe/Paris")
	in := time.Date(1969, 12, 31, 22, 59, 59, 999_999_999, time.UTC).In(paris)
	rows := []row{{in, in, in, sql.Null[time.Time]{}, 1999}}
	got := readRows[row](t, openBytes(t, writeRowsOf(t, rows, WriteOptions{}, 1)), Query{}, nil)
	g := got[0]
	if !g.Ms.Equal(time.Date(1969, 12, 31, 22, 59, 59, 999_000_000, time.UTC)) ||
		!g.S.Equal(time.Date(1969, 12, 31, 22, 59, 59, 0, time.UTC)) ||
		!g.Day.Equal(time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC)) || g.Us != 1*time.Microsecond {
		t.Fatalf("%+v", g)
	}
}

// fastFor gives up on a field that is not laid out as expected, or that has no fast path.
func TestRowWriterFastRefusals(t *testing.T) {
	type row struct {
		A int32
		N sql.Null[int8]
		P *int
		L []int32
	}
	rt := reflect.TypeFor[row]()
	root, err := newRwStruct(rt, "")
	if err != nil {
		t.Fatal(err)
	}
	for i := range root.kids {
		root.kids[i].init()
	}
	if fastFor[row](root.kids[0], rt, root.offs[0]) == nil {
		t.Fatal("no fast path for int32")
	}
	if fastFor[row](root.kids[1], rt, root.offs[1]) == nil {
		t.Fatal("no fast path for sql.Null[int8]")
	}
	if fastFor[row](root.kids[1], rt, rt.Size()) != nil {
		t.Fatal("a sql.Null that is outside of the row has a fast path")
	}
	if fastFor[row](root.kids[2], rt, root.offs[2]) != nil || fastFor[row](root.kids[3], rt, root.offs[3]) != nil {
		t.Fatal("a pointer and a slice have no fast path")
	}
	if gather[row](root.kids[0], rt, root.offs[0], 0, func(v []int64) {}) != nil {
		t.Fatal("a field of 4 bytes is read as 8")
	}
}
