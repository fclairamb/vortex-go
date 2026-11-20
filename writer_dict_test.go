package vortex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

// roundTrip writes the Data in the given writes, checks what is read back, and returns the file.
func roundTrip(t *testing.T, ty Type, d Data, vals []val, writes ...int) *File {
	t.Helper()
	var chunks [][]Data
	lo := 0
	for _, n := range writes {
		chunks = append(chunks, []Data{sliceOf(d, ty, lo, lo+n)})
		lo += n
	}
	f, _ := writeFile(t, NewSchema(Field{"c", ty}), chunks...)
	got, err := readBack(f, ty)
	if err != nil || len(got) != len(vals) {
		t.Fatalf("%d rows, %v, want %d", len(got), err, len(vals))
	}
	for i := range vals {
		if !eqVal(got[i], vals[i]) {
			t.Fatalf("row %d is %s, want %s", i, describeVal(got[i]), describeVal(vals[i]))
		}
	}
	return f
}

// sliceOf is the rows lo to hi of a Data of numbers, strings or bytes.
func sliceOf(d Data, ty Type, lo, hi int) Data {
	var out Data
	switch d.kind {
	case dPrim:
		w := numberWidth(d.pk)
		out = Data{&body{kind: dPrim, pk: d.pk, n: hi - lo, raw: d.raw[lo*w : hi*w]}}
	case dStr:
		out = OfStrings(d.strs[lo:hi])
	case dDec:
		out = OfDecimals(d.decs[lo:hi])
	case dBool:
		out = OfBools(d.bools[lo:hi])
	default:
		out = OfBytes(d.bins[lo:hi])
	}
	if d.nulls != nil {
		out = out.Nulls(d.nulls[lo:hi])
	}
	return out
}

func int32Case(n int, value func(i int) (int32, bool)) (Data, []val) {
	v, nulls := make([]int32, n), make([]bool, n)
	vals := make([]val, n)
	for i := range v {
		var null bool
		v[i], null = value(i)
		nulls[i] = null
		vals[i] = val{b: binary.LittleEndian.AppendUint32(nil, uint32(v[i])), null: null}
	}
	return Of(v).Nulls(nulls), vals
}

func strCase(n int, value func(i int) (string, bool)) (Data, []val) {
	v, nulls := make([]string, n), make([]bool, n)
	vals := make([]val, n)
	for i := range v {
		var null bool
		v[i], null = value(i)
		nulls[i] = null
		vals[i] = val{b: []byte(v[i]), null: null}
	}
	return OfStrings(v).Nulls(nulls), vals
}

func TestDictionaryOfNumbersAndStrings(t *testing.T) {
	lowered(t, &rowBlock, 100)
	lowered(t, &testOptions, WriteOptions{Compression: None}) // the probe without a compressor
	nullable := Int32Type().AsNullable()
	d, vals := int32Case(450, func(i int) (int32, bool) { return int32(i % 7 * 1000), i%11 == 5 })
	f := roundTrip(t, nullable, d, vals, 150, 1, 299)
	if got := layoutTree(f, f.layout.children[0]); !strings.HasPrefix(got, "zoned(450)[dict(450)[flat(8) flat(450)]") {
		t.Errorf("%s: nulls are a value of the dictionary", got)
	}
	// A column of bytes that cannot be null has u8 codes.
	b, bvals := make([]int8, 300), make([]val, 300)
	for i := range b {
		b[i], bvals[i] = int8(i%3), val{b: []byte{byte(i % 3)}}
	}
	f = roundTrip(t, Int8Type(), Of(b), bvals, 300)
	if got := f.layout.children[0].children[0]; f.layoutEncodings[got.encoding] != "vortex.dict" || got.metadata[0] == 0x08 {
		t.Errorf("u8 codes: %x", got.metadata)
	}
	// Strings, binary and floats.
	s, svals := strCase(300, func(i int) (string, bool) { return strings.Repeat("v", i%4), false })
	f = roundTrip(t, Utf8Type(), s, svals, 300)
	if got := layoutTree(f, f.layout.children[0]); !strings.Contains(got, "dict(300)") {
		t.Errorf("%s", got)
	}
	bin, binVals := make([][]byte, 200), make([]val, 200)
	for i := range bin {
		bin[i] = []byte{byte(i % 5)}
		binVals[i] = val{b: bin[i]}
	}
	f = roundTrip(t, BinaryType(), OfBytes(bin), binVals, 200)
	if got := layoutTree(f, f.layout.children[0]); !strings.Contains(got, "dict(200)") {
		t.Errorf("%s", got)
	}
	fl := make([]float64, 200)
	for i := range fl {
		fl[i] = []float64{1.5, math.NaN(), -0.25}[i%3]
	}
	fvals := make([]val, len(fl))
	for i, v := range fl {
		fvals[i] = val{b: binary.LittleEndian.AppendUint64(nil, math.Float64bits(v))}
	}
	f = roundTrip(t, Float64Type(), Of(fl), fvals, 200)
	if got := layoutTree(f, f.layout.children[0]); !strings.Contains(got, "dict(200)") {
		t.Errorf("%s", got)
	}
	// Only the first block decides.
	d, vals = int32Case(300, func(i int) (int32, bool) { return int32(i), false })
	f = roundTrip(t, Int32Type(), d, vals, 300)
	if got := layoutTree(f, f.layout.children[0]); strings.Contains(got, "dict") {
		t.Errorf("unique values: %s", got)
	}
	// Not a type that has a dictionary.
	bools := make([]bool, 300)
	f, _ = writeFile(t, NewSchema(Field{"b", BoolType()}), []Data{OfBools(bools)})
	if got := layoutTree(f, f.layout.children[0]); strings.Contains(got, "dict") {
		t.Errorf("bools: %s", got)
	}
}

func TestDictionaryRuns(t *testing.T) {
	lowered(t, &rowBlock, 100)
	// Few values at first, then more than a dictionary holds: new dictionaries.
	for _, limit := range []struct {
		name  string
		setup func()
	}{
		{"values", func() { lowered(t, &maxDictLen, 50) }},
		{"bytes", func() { lowered(t, &maxDictBytes, 16*60) }},
	} {
		t.Run(limit.name, func(t *testing.T) {
			limit.setup()
			s, vals := strCase(1000, func(i int) (string, bool) {
				if i < 100 {
					return fmt.Sprintf("s%d", i%3), false
				}
				return fmt.Sprintf("value-%d", i/3), i%50 == 7
			})
			f := roundTrip(t, Utf8Type().AsNullable(), s, vals, 333, 667)
			got := layoutTree(f, f.layout.children[0])
			if strings.Count(got, "dict(") < 3 || !strings.Contains(got, "chunked(1000)[dict") {
				t.Errorf("%s: dictionaries of 50 values", got)
			}
		})
	}
	// The first value of a run is taken whatever its size.
	lowered(t, &maxDictBytes, 1)
	s, vals := strCase(300, func(i int) (string, bool) { return "a long value of more than twelve bytes", i%4 == 1 })
	f := roundTrip(t, Utf8Type().AsNullable(), s, vals, 300)
	if got := layoutTree(f, f.layout.children[0]); !strings.Contains(got, "dict") {
		t.Errorf("%s", got)
	}
}

func TestDictionaryCodeChunks(t *testing.T) {
	lowered(t, &rowBlock, 100)
	lowered(t, &chunkBytes, 100)
	d, vals := int32Case(1000, func(i int) (int32, bool) { return int32(i % 4), false })
	f := roundTrip(t, Int32Type(), d, vals, 1000)
	if got := layoutTree(f, f.layout.children[0]); !strings.Contains(got, "chunked(1000)[flat(50)") {
		t.Errorf("codes are chunked: %s", got)
	}
}

func TestSmallBlocksOfEveryType(t *testing.T) {
	lowered(t, &rowBlock, 6)
	lowered(t, &chunkBytes, 100)
	g := &gen{r: newRand(9)}
	for name, ty := range allTypes() {
		checkRoundTrip(t, name, ty, g, 4, 20, 1, 0, 30)
	}
	g = &gen{r: newRand(9), allNull: true}
	for name, ty := range allTypes() {
		if ty.nullable {
			checkRoundTrip(t, name, ty, g, 9, 10)
		}
	}
}

func TestWriteFaultsEverywhere(t *testing.T) {
	lowered(t, &rowBlock, 8)
	lowered(t, &chunkBytes, 64)
	lowered(t, &smallWrites, 16)
	fields := []Field{{"a", Int64Type()}, {"b", Utf8Type().AsNullable()}, {"c", StructOf(Field{"x", Float64Type()}).AsNullable()},
		{"d", Int32Type()}, {"e", ListOf(Float64Type())}}
	write := func(w *failingWriter) error {
		wr := NewWriter(w, NewSchema(fields...), WriteOptions{})
		g := &gen{r: newRand(1)}
		for _, n := range []int{30, 25} {
			cols := make([]Data, len(fields))
			for i, f := range fields {
				cols[i], _ = g.make(f.Type, n)
			}
			cols[3], _ = int32Case(n, func(i int) (int32, bool) { return int32(i % 3), false })
			if err := wr.Write(cols...); err != nil {
				return err
			}
		}
		return wr.Close()
	}
	calls := 0
	for n := 1; ; n++ {
		w := &failingWriter{failAt: n}
		err := write(w)
		if err == nil {
			calls = w.calls
			break
		}
		if !errors.Is(err, errFault) {
			t.Fatalf("call %d: %v", n, err)
		}
		if n > 5000 {
			t.Fatal("the writer never succeeded")
		}
	}
	if calls < 30 {
		t.Errorf("only %d calls to the io.Writer", calls)
	}
}
