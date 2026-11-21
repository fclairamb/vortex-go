package vortex

import (
	"bytes"
	"math"
	"runtime"
	"strings"
	"testing"
)

// TestScanFast checks the statistics found on the numbers where they are against the general
// comparison of two rows: every type of number, with nulls, NaNs and the two zeros.
func TestScanFast(t *testing.T) {
	nan, negZero := math.NaN(), math.Copysign(0, -1)
	nulls := []bool{true, false, false, true, false, false, false, false, false, false}
	cases := []struct {
		t Type
		d Data
	}{
		{Int8Type(), Of([]int8{3, -128, 127, 0, -128, 127, 5, 5, -1, 2})},
		{Int16Type(), Of([]int16{3, -32768, 32767, 0, -32768, 32767, 5, 5, -1, 2})},
		{Int32Type(), Of([]int32{3, math.MinInt32, math.MaxInt32, 0, math.MinInt32, math.MaxInt32, 5, 5, -1, 2})},
		{Int64Type(), Of([]int64{3, math.MinInt64, math.MaxInt64, 0, math.MinInt64, math.MaxInt64, 5, 5, -1, 2})},
		{Uint8Type(), Of([]uint8{3, 0, 255, 9, 0, 255, 5, 5, 1, 2})},
		{Uint16Type(), Of([]uint16{3, 0, 65535, 9, 0, 65535, 5, 5, 1, 2})},
		{Uint32Type(), Of([]uint32{3, 0, math.MaxUint32, 9, 0, math.MaxUint32, 5, 5, 1, 2})},
		{Uint64Type(), Of([]uint64{3, 0, math.MaxUint64, 9, 0, math.MaxUint64, 5, 5, 1, 2})},
		{Float32Type(), Of([]float32{3, float32(negZero), 0, float32(nan), 0, float32(negZero), 5, float32(nan), -1, 2})},
		{Float64Type(), Of([]float64{3, negZero, 0, nan, 0, negZero, 5, nan, -1, 2})},
		{TimestampType(Microsecond, ""), Of([]int64{3, -9, 7, 0, -9, 7, 5, 5, -1, 2})},
	}
	for _, c := range cases {
		for _, withNulls := range []bool{false, true} {
			b := *c.d.body
			if withNulls {
				b.nulls = nulls
			}
			r := newRanger(c.t)
			for _, span := range [][2]int{{0, 10}, {1, 3}, {3, 4}, {0, 1}, {6, 8}, {4, 4}} {
				lo, hi := span[0], span[1]
				wmin, wmax, wnans := -1, -1, 0
				for i := lo; i < hi; i++ {
					switch {
					case isNull(b.nulls, i):
					case r.class == scFloat && math.IsNaN(rawFloat(b.raw, r.width, i)):
						wnans++
					case wmin < 0:
						wmin, wmax = i, i
					default:
						if r.order(&b, i, wmin) < 0 {
							wmin = i
						}
						if r.order(&b, i, wmax) > 0 {
							wmax = i
						}
					}
				}
				if imin, imax, nans := r.scan(&b, lo, hi); imin != wmin || imax != wmax || nans != wnans {
					t.Errorf("%s, nulls %v, rows %d to %d: min %d, max %d, %d NaNs, want %d, %d, %d",
						c.t, withNulls, lo, hi, imin, imax, nans, wmin, wmax, wnans)
				}
			}
		}
		// Sorted, or not, without a null or a NaN.
		r := newRanger(c.t)
		got, ok := r.sortedFast(c.d.body)
		if !ok || got {
			t.Errorf("%s: sorted %v, %v", c.t, got, ok)
		}
		one := *c.d.body
		one.raw, one.n = one.raw[:r.width], 1
		if got, ok := r.sortedFast(&one); !ok || !got {
			t.Errorf("%s: one row is not sorted", c.t)
		}
	}
	// Decimals of at most 8 bytes are compared as their low word.
	decs := []Decimal{DecimalFromInt64(4), DecimalFromInt64(-7), DecimalFromInt64(9), DecimalFromInt64(-7), DecimalFromInt64(9)}
	for _, n := range [][]bool{nil, {false, true, false, false, true}} {
		wantMin, wantMax := 1, 2
		if n != nil {
			wantMin = 3
		}
		r := newRanger(DecimalOf(12, 2))
		if imin, imax, nans := r.scan(&body{kind: dDec, decs: decs, nulls: n, n: 5}, 0, 5); imin != wantMin || imax != wantMax || nans != 0 {
			t.Errorf("decimals, nulls %v: min %d, max %d, %d NaNs", n != nil, imin, imax, nans)
		}
	}
	if imin, imax, _ := scanSmallDecimals(decs, nil, 2, 2); imin != -1 || imax != -1 {
		t.Errorf("no decimal: min %d, max %d", imin, imax)
	}
	// A column that is not one of numbers read where they are has no fast path.
	for _, ty := range []Type{Float16Type(), Utf8Type(), BoolType(), DecimalOf(5, 1)} {
		r := newRanger(ty)
		if _, ok := r.scanFast(newBody(ty), 0, 0); ok {
			t.Errorf("%s has a fast path", ty)
		}
	}
}

// TestQueueOfChunks writes one column that fills many chunks in one Write, and others that
// fill few: the chunks wait together, more than one for a column, and the file is the same
// bytes with one thread and with eight, with a short queue and with a long one.
func TestQueueOfChunks(t *testing.T) {
	lowered(t, &chunkBytes, 20_000)
	lowered(t, &rowBlock, 512)
	s, cols := wideColumns(40_000, "random")
	var first []byte
	rows := ""
	for _, procs := range []int{1, 8} {
		for _, queue := range []int{1, 16, 1000} {
			lowered(t, &minQueue, queue)
			old := runtime.GOMAXPROCS(procs)
			got := writeWith(t, Default, s, cols, 40_000)
			runtime.GOMAXPROCS(old)
			f := openBytes(t, got)
			sum, err := digest(f, &scanOptions{batchRows: 1000})
			if err != nil || f.NumRows() != 40_000 {
				t.Fatalf("%d rows, %v", f.NumRows(), err)
			}
			if rows == "" {
				rows = sum
			} else if sum != rows {
				t.Fatalf("%d threads, a queue of %d: other rows", procs, queue)
			}
			if queue == 16 && procs == 1 {
				first = got
			} else if queue == 16 && !bytes.Equal(got, first) {
				t.Fatal("the file depends on the number of threads")
			}
		}
	}
}

// TestWriteBuffer checks WriteOptions.Buffer: under the size of a chunk, a column never has
// more than one chunk that waits, and a larger one lets several wait. The rows are the same.
func TestWriteBuffer(t *testing.T) {
	lowered(t, &chunkBytes, 20_000)
	lowered(t, &rowBlock, 512)
	s, cols := wideColumns(40_000, "random")
	rows := ""
	for _, c := range []struct{ buffer, slots int }{{0, 2}, {1, 1}, {1 << 30, 2}} {
		var buf bytes.Buffer
		w := NewWriter(&buf, s, WriteOptions{Buffer: c.buffer})
		if err := w.Write(cols...); err != nil {
			t.Fatal(err)
		}
		most := 0
		for _, k := range w.root.kids {
			most = max(most, k.col.slots)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if c.slots == 1 && most != 1 || c.slots > 1 && most < 2 {
			t.Errorf("Buffer %d: a column had %d chunks that waited", c.buffer, most)
		}
		sum, err := digest(openBytes(t, buf.Bytes()), &scanOptions{batchRows: 1000})
		if err != nil {
			t.Fatal(err)
		}
		if rows == "" {
			rows = sum
		} else if sum != rows {
			t.Errorf("Buffer %d: other rows", c.buffer)
		}
	}
}

// TestWriteChecksOnEveryCore checks that the Data of a large Write, checked on every core, is
// refused for the first column that is wrong, as a small one is, and that the writer goes on
// with the columns of the next Write.
func TestWriteChecksOnEveryCore(t *testing.T) {
	old := runtime.GOMAXPROCS(4)
	defer runtime.GOMAXPROCS(old)
	const n = 3 * parallelRows
	s := NewSchema(Field{"a", Int64Type()}, Field{"b", Utf8Type()}, Field{"c", Utf8Type()})
	good, bad := make([]string, n), make([]string, n)
	for i := range good {
		good[i], bad[i] = "ok", "ok"
	}
	bad[n-1] = "\xff"
	var buf bytes.Buffer
	w := NewWriter(&buf, s, WriteOptions{})
	err := w.Write(Of(make([]int64, n)), OfStrings(bad), OfStrings(bad))
	if err == nil || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("a string that is not UTF-8: %v", err)
	}
	w = NewWriter(&buf, s, WriteOptions{})
	if err := w.Write(Of(make([]int64, n)), OfStrings(good), OfStrings(good)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if f := openBytes(t, buf.Bytes()); f.NumRows() != n {
		t.Fatalf("%d rows", f.NumRows())
	}
}
