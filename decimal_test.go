package vortex

import (
	"errors"
	"math/big"
	"testing"
)

func bigOf(t *testing.T, s string) *big.Int {
	t.Helper()
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		t.Fatalf("bad number %q", s)
	}
	return v
}

func TestDecimalRoundTrip(t *testing.T) {
	pow := func(n uint) *big.Int { return new(big.Int).Lsh(big.NewInt(1), n) }
	one := big.NewInt(1)
	values := []*big.Int{
		big.NewInt(0), big.NewInt(1), big.NewInt(-1),
		big.NewInt(1<<63 - 1), big.NewInt(-1 << 63),
		new(big.Int).Sub(pow(63), big.NewInt(-1)), new(big.Int).Neg(new(big.Int).Add(pow(63), one)),
		new(big.Int).Sub(pow(127), one), new(big.Int).Neg(pow(127)), pow(127), new(big.Int).Neg(new(big.Int).Add(pow(127), one)),
		new(big.Int).Sub(pow(255), one), new(big.Int).Neg(pow(255)),
		bigOf(t, "123456789012345678901234567890123456789012345678901234567890"),
	}
	for _, v := range values {
		d, err := DecimalFromBig(v)
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		if got := d.Big(); got.Cmp(v) != 0 {
			t.Errorf("round trip of %v gives %v", v, got)
		}
		if d.Text(0) != v.String() {
			t.Errorf("Text(0) of %v is %s", v, d.Text(0))
		}
	}
	for _, v := range []*big.Int{pow(255), new(big.Int).Neg(new(big.Int).Add(pow(255), one)), pow(256)} {
		if _, err := DecimalFromBig(v); !errors.Is(err, ErrDecimalRange) {
			t.Errorf("%v must be out of range, got %v", v, err)
		}
	}
}

func TestDecimalInt64(t *testing.T) {
	for _, v := range []int64{0, 1, -1, 1<<63 - 1, -1 << 63} {
		d := DecimalFromInt64(v)
		if got, ok := d.Int64(); !ok || got != v {
			t.Errorf("Int64 of %d = %d, %v", v, got, ok)
		}
		if d.Big().Int64() != v {
			t.Errorf("Big of %d = %v", v, d.Big())
		}
	}
	for _, s := range []string{"9223372036854775808", "-9223372036854775809", "18446744073709551616", "340282366920938463463374607431768211456"} {
		d, err := DecimalFromBig(bigOf(t, s))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := d.Int64(); ok {
			t.Errorf("%s must overflow an int64", s)
		}
	}
	// Words above the first that are not the sign extension.
	if _, ok := (Decimal{w: [4]uint64{1, 0, 1, 0}}).Int64(); ok {
		t.Error("a value in the third word must overflow")
	}
	if _, ok := (Decimal{w: [4]uint64{1, 0, 0, 1}}).Int64(); ok {
		t.Error("a value in the fourth word must overflow")
	}
}

func TestDecimalText(t *testing.T) {
	cases := []struct {
		v     string
		scale int
		want  string
	}{
		{"12345", 0, "12345"}, {"12345", 2, "123.45"}, {"12345", 5, "0.12345"}, {"5", 3, "0.005"},
		{"-5", 3, "-0.005"}, {"-12345", 2, "-123.45"}, {"0", 2, "0.00"}, {"0", 0, "0"},
		{"-7", 0, "-7"}, {"12", -2, "1200"}, {"-12", -1, "-120"}, {"0", -3, "0"},
		{"100", 2, "1.00"},
	}
	for _, c := range cases {
		d, err := DecimalFromBig(bigOf(t, c.v))
		if err != nil {
			t.Fatal(err)
		}
		if got := d.Text(c.scale); got != c.want {
			t.Errorf("Text(%s, %d) = %s, want %s", c.v, c.scale, got, c.want)
		}
	}
}

func TestDecimalCmp(t *testing.T) {
	order := []string{"-57896044618658097711785492504343953926634992332820282019728792003956564819968", "-18446744073709551617", "-2", "-1", "0", "1", "18446744073709551616", "57896044618658097711785492504343953926634992332820282019728792003956564819967"}
	for i, a := range order {
		da, _ := DecimalFromBig(bigOf(t, a))
		for j, b := range order {
			db, _ := DecimalFromBig(bigOf(t, b))
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := da.Cmp(db); got != want {
				t.Errorf("Cmp(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestDecimalFromLE(t *testing.T) {
	cases := []struct {
		b    []byte
		want int64
	}{
		{[]byte{0xfe}, -2}, {[]byte{0x7f}, 127}, {[]byte{0x00, 0x80}, -32768}, {[]byte{1, 0, 0, 0}, 1},
		{[]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, -1},
	}
	for _, c := range cases {
		if got, ok := decimalFromLE(c.b).Int64(); !ok || got != c.want {
			t.Errorf("%v = %d, want %d", c.b, got, c.want)
		}
	}
	wide := make([]byte, 32)
	wide[31], wide[16] = 0x80, 3
	if got := decimalFromLE(wide).Big(); got.Sign() >= 0 || got.Cmp(DecimalFromInt64(0).Big()) == 0 {
		t.Errorf("a 256-bit negative = %v", got)
	}
	if decimalFromLE(wide).le() != [32]byte(wide) {
		t.Error("le is not the inverse of decimalFromLE")
	}
}

func TestDecimalAllocs(t *testing.T) {
	a, b := DecimalFromInt64(-12345), DecimalFromInt64(7)
	var sink int
	allocs := testing.AllocsPerRun(100, func() {
		d := DecimalFromInt64(42)
		v, _ := d.Int64()
		sink += d.Cmp(a) + d.Cmp(b) + int(v)
		_ = decimalFromLE([]byte{1, 2, 3, 4})
	})
	if allocs != 0 {
		t.Errorf("%v allocations", allocs)
	}
	_ = sink
}
