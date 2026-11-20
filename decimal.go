package vortex

import (
	"errors"
	"math/big"
	"strings"
)

// Decimal is the unscaled value of a decimal of any precision, on 256 bits in two's
// complement: 32 bytes, no pointer. The scale is in the Type of the column, not in each
// value: Text takes it. The representation is unique, so == and Cmp agree on equality;
// Cmp also orders.
type Decimal struct {
	// w is the 256 bits, least significant word first.
	w [4]uint64
}

// DecimalFromInt64 returns the decimal whose unscaled value is v.
func DecimalFromInt64(v int64) Decimal {
	ext := uint64(v >> 63)
	return Decimal{w: [4]uint64{uint64(v), ext, ext, ext}}
}

var (
	two255 = new(big.Int).Lsh(big.NewInt(1), 255)
	two256 = new(big.Int).Lsh(big.NewInt(1), 256)
)

// ErrDecimalRange reports a value that does not fit in 256 bits.
var ErrDecimalRange = errors.New("vortex: decimal out of the range of 256 bits")

// DecimalFromBig returns the decimal whose unscaled value is v. It returns an error when v
// does not fit in 256 bits, that is when it is outside -2^255 to 2^255-1.
func DecimalFromBig(v *big.Int) (Decimal, error) {
	if v.Cmp(two255) >= 0 || v.Cmp(new(big.Int).Neg(two255)) < 0 {
		return Decimal{}, ErrDecimalRange
	}
	u := v
	if v.Sign() < 0 {
		u = new(big.Int).Add(v, two256) // two's complement
	}
	var be [32]byte
	u.FillBytes(be[:])
	var d Decimal
	for i := range d.w {
		for _, b := range be[32-8*(i+1) : 32-8*i] {
			d.w[i] = d.w[i]<<8 | uint64(b)
		}
	}
	return d, nil
}

// decimalFromLE sign-extends the little-endian two's complement value in b, of 1 to 32
// bytes.
func decimalFromLE(b []byte) Decimal {
	var d Decimal
	ext := uint64(0)
	if b[len(b)-1]&0x80 != 0 {
		ext = ^uint64(0)
	}
	for i := range d.w {
		d.w[i] = ext
	}
	for i := len(b) - 1; i >= 0; i-- {
		w := &d.w[i/8]
		*w = *w&^(0xff<<(8*(i%8))) | uint64(b[i])<<(8*(i%8))
	}
	return d
}

// le returns the 32 bytes of the value, little-endian.
func (d Decimal) le() [32]byte {
	var b [32]byte
	for i := range b {
		b[i] = byte(d.w[i/8] >> (8 * (i % 8)))
	}
	return b
}

func (d Decimal) negative() bool { return int64(d.w[3]) < 0 }

// Int64 returns the value as an int64, and false when it does not fit.
func (d Decimal) Int64() (int64, bool) {
	ext := uint64(int64(d.w[0]) >> 63)
	return int64(d.w[0]), d.w[1] == ext && d.w[2] == ext && d.w[3] == ext
}

// Cmp compares the values: -1 when d is less than o, 0 when equal, +1 when greater.
func (d Decimal) Cmp(o Decimal) int {
	if d.negative() != o.negative() {
		if d.negative() {
			return -1
		}
		return 1
	}
	for i := len(d.w) - 1; i >= 0; i-- {
		switch {
		case d.w[i] < o.w[i]:
			return -1
		case d.w[i] > o.w[i]:
			return 1
		}
	}
	return 0
}

// Big returns the value as a new big.Int.
func (d Decimal) Big() *big.Int {
	le := d.le()
	var be [32]byte
	for i, b := range le {
		be[31-i] = b
	}
	v := new(big.Int).SetBytes(be[:])
	if d.negative() {
		v.Sub(v, two256)
	}
	return v
}

// Text prints the value with the point scale digits from the right: the sign when negative,
// the digits, no exponent. A negative scale adds zeros: the value is multiplied by
// 10^-scale.
func (d Decimal) Text(scale int) string {
	digits := d.Big().Text(10)
	sign := ""
	if digits[0] == '-' {
		sign, digits = "-", digits[1:]
	}
	switch {
	case scale <= 0:
		if digits != "0" {
			digits += strings.Repeat("0", -scale)
		}
		return sign + digits
	case len(digits) <= scale:
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	return sign + digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
}
