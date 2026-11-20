package pco

import "math"

// mantissaBits is the number of explicit mantissa bits of a float type.
func mantissaBits(t Type) int {
	switch t {
	case F16:
		return 10
	case F32:
		return 23
	}
	return 52
}

// mid is the middle value of the latents of t.
func (t Type) mid() uint64 { return 1 << (t.Bits() - 1) }

// mask is the largest value of the latents of t.
func (t Type) mask() uint64 {
	if t.Bits() == 64 {
		return math.MaxUint64
	}
	return 1<<t.Bits() - 1
}

// fromOrdered turns an order-preserving latent into the bits of the number: the inverse of
// toOrdered. Unsigned integers are their own latents, signed ones have their sign bit flipped,
// and floats have their sign bit flipped, and all the others too when they were negative.
func (t Type) fromOrdered(l uint64) uint64 {
	switch {
	case t.Float():
		if l&t.mid() != 0 {
			return l ^ t.mid()
		}
		return ^l & t.mask()
	case t.Signed():
		return l ^ t.mid()
	}
	return l
}

// floatToOrdered is the order-preserving latent of the bits of a float: the inverse of
// fromOrdered for a float type.
func (t Type) floatToOrdered(b uint64) uint64 {
	if b&t.mid() != 0 {
		return ^b & t.mask()
	}
	return b | t.mid()
}

// validFloatBase reports whether the FloatMult base, as a latent, is a finite nonzero float.
func validFloatBase(t Type, latent uint64) bool {
	b := t.fromOrdered(latent)
	exp := (b >> mantissaBits(t)) & (1<<(t.Bits()-1-mantissaBits(t)) - 1)
	return exp != 1<<(t.Bits()-1-mantissaBits(t))-1 && b&^t.mid() != 0
}

// floatMul multiplies two floats of type t, given and returned as bits. Each product is rounded
// to the type once.
func floatMul(t Type, a, b uint64) uint64 {
	switch t {
	case F16:
		return uint64(f32ToF16(f16ToF32(uint16(a)) * f16ToF32(uint16(b))))
	case F32:
		return uint64(math.Float32bits(math.Float32frombits(uint32(a)) * math.Float32frombits(uint32(b))))
	}
	return math.Float64bits(math.Float64frombits(a) * math.Float64frombits(b))
}

// intFloatFromLatent maps a latent to the integer-valued float it stands for: 0 is the lowest
// negative integer float, the middle is +0. Up to 2^(mantissa+1) the integers are exact, above
// it the latent counts the floats one after the other.
func intFloatFromLatent(t Type, l uint64) uint64 {
	mid := t.mid()
	absMask := mid - 1
	gpi := uint64(1) << (mantissaBits(t) + 1)
	neg := uint64(0)
	if l < mid {
		neg = t.mask()
	}
	abs := (l ^ neg) & absMask
	var bits uint64
	if abs < gpi {
		bits = numericalBits(t, abs)
	} else {
		bits = abs + numericalBits(t, gpi) - gpi
	}
	return bits | neg&mid
}

// numericalBits is the float of type t that holds the integer v, which is exact for v up to
// 2^(mantissa+1), as bits.
func numericalBits(t Type, v uint64) uint64 {
	switch t {
	case F16:
		return uint64(f32ToF16(float32(v)))
	case F32:
		return uint64(math.Float32bits(float32(v)))
	}
	return math.Float64bits(float64(v))
}

// f16ToF32 widens a half float, exactly.
func f16ToF32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1f
	man := uint32(h & 0x3ff)
	switch {
	case exp == 0x1f:
		return math.Float32frombits(sign | 0x7f800000 | man<<13)
	case exp != 0:
		return math.Float32frombits(sign | (exp+112)<<23 | man<<13)
	case man == 0:
		return math.Float32frombits(sign)
	}
	// A subnormal half is a normal float.
	e := uint32(113)
	for man&0x400 == 0 {
		man <<= 1
		e--
	}
	return math.Float32frombits(sign | e<<23 | (man&0x3ff)<<13)
}

// f32ToF16 narrows a float to a half float, rounding to nearest, ties to even, the way the
// half crate does: a NaN stays a NaN and keeps the top bits of its payload.
func f32ToF16(f float32) uint16 {
	b := math.Float32bits(f)
	sign := uint16(b>>16) & 0x8000
	exp := int32(b>>23) & 0xff
	man := b & 0x7fffff
	if exp == 0xff {
		var nan uint16
		if man != 0 {
			nan = 0x200
		}
		return sign | 0x7c00 | nan | uint16(man>>13)
	}
	e := exp - 127 + 15
	if e >= 0x1f {
		return sign | 0x7c00
	}
	if e <= 0 {
		if e < -10 {
			return sign
		}
		man |= 0x800000
		shift := uint(14 - e)
		half := uint16(man >> shift)
		round := uint32(1) << (shift - 1)
		if man&round != 0 && man&(3*round-1) != 0 {
			half++
		}
		return sign | half
	}
	half := sign | uint16(e)<<10 | uint16(man>>13)
	if man&0x1000 != 0 && man&0x2fff != 0 {
		half++ // a carry out of the mantissa grows the exponent, up to infinity
	}
	return half
}
