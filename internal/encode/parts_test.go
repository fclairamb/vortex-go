package encode

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/fclairamb/vortex-go/internal/array"
)

// decimalRaw is the values as decimals of width bytes, two's complement, little-endian.
func decimalRaw(vals []int64, width int) []byte {
	out := make([]byte, 0, len(vals)*width)
	for _, v := range vals {
		b := binary.LittleEndian.AppendUint64(nil, uint64(v))
		ext := byte(0)
		if v < 0 {
			ext = 0xff
		}
		for len(b) < width {
			b = append(b, ext)
		}
		out = append(out, b[:width]...)
	}
	return out
}

func TestDecimalBytes(t *testing.T) {
	e := New()
	cases := map[string][]int64{
		"bytes":   {0, 1, -1, 127, -128},
		"shorts":  {0, 300, -300, 32767, -32768},
		"ints":    {0, 70000, -70000, math.MaxInt32, math.MinInt32},
		"longs":   {0, 1 << 40, -1 << 40, math.MaxInt64, math.MinInt64},
		"one":     {-5},
		"nothing": {},
	}
	wants := map[string]int{"bytes": 1, "shorts": 2, "ints": 4, "longs": 8, "one": 1, "nothing": 1}
	for name, vals := range cases {
		for _, width := range []int{1, 2, 4, 8, 16, 32} {
			if width < wants[name] && name != "one" && name != "nothing" {
				continue
			}
			for _, nl := range []string{"none", "some", "all"} {
				e.Reset()
				nulls := nullPattern(nl, len(vals))
				raw := decimalRaw(vals, width)
				n, ok := DecimalBytes(e, raw, width, nulls)
				if !ok {
					t.Fatalf("%s/%d/%s: refused", name, width, nl)
				}
				want := wants[name]
				if nl == "all" {
					want = 1
				}
				if got := len(n.Kids[0].Bufs[0].Data) / max(len(vals), 1); len(vals) > 0 && got != want && nl == "none" {
					t.Fatalf("%s/%d: parts of %d bytes, want %d", name, width, got, want)
				}
				a := decode(t, n, array.DType{Kind: array.Decimal, Nullable: hasNull(nulls)}, len(vals))
				for i, v := range vals {
					if isNull(nulls, i) {
						if !a.Null(i) {
							t.Fatalf("%s/%d/%s: row %d is valid", name, width, nl, i)
						}
						continue
					}
					var got uint64
					for k := a.Width - 1; k >= 0; k-- {
						got = got<<8 | uint64(a.Data[i*a.Width+k])
					}
					if got<<(64-8*a.Width)>>(64-8*a.Width) != uint64(v)<<(64-8*a.Width)>>(64-8*a.Width) || int64(got<<(64-8*a.Width))>>(64-8*a.Width) != v {
						t.Fatalf("%s/%d/%s: row %d = %d, want %d", name, width, nl, i, int64(got), v)
					}
				}
			}
		}
	}
}

func TestDecimalBytesRefuses(t *testing.T) {
	e := New()
	wide := make([]byte, 16)
	wide[8] = 1 // 2^64
	if _, ok := DecimalBytes(e, wide, 16, nil); ok {
		t.Fatal("a value above an i64 is written")
	}
	if _, ok := DecimalBytes(e, wide, 16, []bool{true}); !ok {
		t.Fatal("a null above an i64 is refused")
	}
	neg := make([]byte, 16) // -2^63 - 1
	for i := range neg {
		neg[i] = 0xff
	}
	neg[7] = 0x7f
	if _, ok := DecimalBytes(e, neg, 16, nil); ok {
		t.Fatal("a value below an i64 is written")
	}
	for _, w := range []int{0, 3, 64} {
		if _, ok := DecimalBytes(e, make([]byte, 6), w, nil); ok {
			t.Fatalf("a width of %d", w)
		}
	}
	if _, ok := DecimalBytes(e, make([]byte, 5), 4, nil); ok {
		t.Fatal("a size that is not a multiple of the width")
	}
	if got := EstimateDecimalBytes(nil, 3, nil, 5); got != 0 {
		t.Fatalf("estimate %d for a bad width", got)
	}
}

func TestEstimateDecimalBytes(t *testing.T) {
	e := New()
	vals := []int64{1, 2, -3, 40000, 5}
	for _, width := range []int{4, 16} {
		raw := decimalRaw(vals, width)
		for _, nulls := range [][]bool{nil, {false, true, false, false, true}} {
			n, _ := DecimalBytes(e, raw, width, nulls)
			if got := EstimateDecimalBytes(raw, width, nulls, len(vals)); got != n.Size() {
				t.Fatalf("estimate %d, size %d", got, n.Size())
			}
		}
	}
}

var ticksOf = map[string]int64{"s": TicksSecond, "ms": TicksMilli, "us": TicksMicro, "ns": TicksNano}

func TestDateTimeParts(t *testing.T) {
	e := New()
	for unit, ticks := range ticksOf {
		day := 86_400 * ticks
		cases := map[string][]int64{
			"recent": {0, 1, day - 1, day, day + 1, 1_700_000_000 * ticks, 1_700_000_000*ticks + 123},
			"before": {-1, -day, -day - 1, -1_700_000_000 * ticks, -1_700_000_000*ticks - 7},
			"far":    {math.MaxInt64, math.MinInt64, 0},
			"short":  {5 * day, -5 * day},
			"one":    {42},
		}
		for name, v := range cases {
			for _, nl := range []string{"none", "some", "all"} {
				e.Reset()
				nulls := nullPattern(nl, len(v))
				n, ok := DateTimeParts(e, v, nulls, ticks)
				if !ok {
					t.Fatalf("%s/%s: refused", unit, name)
				}
				dt := array.DType{Kind: array.Prim, PType: array.I64, Ticks: int32(ticks), Nullable: hasNull(nulls)}
				a := decode(t, n, dt, len(v))
				for i, x := range v {
					got := int64(binary.LittleEndian.Uint64(a.Data[8*i:]))
					if isNull(nulls, i) {
						if !a.Null(i) {
							t.Fatalf("%s/%s/%s: row %d is valid", unit, name, nl, i)
						}
					} else if got != x {
						t.Fatalf("%s/%s/%s: row %d = %d, want %d", unit, name, nl, i, got, x)
					}
				}
				if want := EstimateDateTimeParts(v, nulls, len(v), ticks); want != n.Size() {
					t.Fatalf("%s/%s/%s: estimate %d, size %d", unit, name, nl, want, n.Size())
				}
			}
		}
	}
}

// TestDateTimePartsNarrow checks the width of the days, the narrowest that holds the valid ones,
// and that the seconds are the i32 the reference reads.
func TestDateTimePartsNarrow(t *testing.T) {
	e := New()
	day := int64(86_400)
	for _, c := range []struct {
		days int64
		want int
	}{{100, 1}, {-128, 1}, {128, 2}, {40_000, 4}, {-1 << 31, 4}, {1 << 31, 8}} {
		e.Reset()
		n, _ := DateTimeParts(e, []int64{0, c.days * day}, nil, TicksSecond)
		if got := len(n.Kids[0].Bufs[0].Data) / 2; got != c.want {
			t.Fatalf("%d days: %d bytes, want %d", c.days, got, c.want)
		}
		if len(n.Kids[1].Bufs[0].Data) != 8 {
			t.Fatal("the seconds are not an i32")
		}
	}
	// the days of a null are not counted
	e.Reset()
	n, _ := DateTimeParts(e, []int64{3 * day, 1 << 40 * day / 1024}, []bool{false, true}, TicksSecond)
	if len(n.Kids[0].Bufs[0].Data) != 2 {
		t.Fatalf("a null widened the days: %d bytes", len(n.Kids[0].Bufs[0].Data))
	}
	for _, ticks := range []int64{0, 7, 100} {
		if _, ok := DateTimeParts(e, []int64{1}, nil, ticks); ok {
			t.Fatalf("%d ticks are accepted", ticks)
		}
	}
}
