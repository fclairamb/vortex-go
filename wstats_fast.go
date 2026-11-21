package vortex

import "github.com/fclairamb/vortex-go/internal/unsafex"

// The statistics of a column of numbers are found on the numbers where they are, a type at a
// time: the general path reads a value through its width for every comparison.

// scanNumbers is ranger.scan for the numbers v: the rows of the smallest and of the largest
// value of the valid rows lo to hi, the first one of each (-1 when there is none), and the
// count of the NaNs, which are neither.
func scanNumbers[T unsafex.Number](v []T, nulls []bool, lo, hi int) (imin, imax, nans int) {
	imin, imax = -1, -1
	var least, most T
	for i := lo; i < hi; i++ {
		x := v[i]
		switch {
		case nulls != nil && nulls[i]:
		case x != x: //nolint:gocritic // a NaN is the one value that differs from itself
			nans++
		case imin < 0:
			imin, imax, least, most = i, i, x, x
		case x < least:
			imin, least = i, x
		case x > most:
			imax, most = i, x
		}
	}
	return imin, imax, nans
}

// sortedNumbers reports whether no value of v is under the one before. v has no NaN.
func sortedNumbers[T unsafex.Number](v []T) bool {
	for i := 1; i < len(v); i++ {
		if v[i-1] > v[i] {
			return false
		}
	}
	return true
}

// numberKey names a type of numbers read where they are: its class and its width.
func numberKey(class statClass, width int) int { return int(class)<<4 | width }

// typedNumbers runs a function on the numbers of the buffer, by their type. It is not ok for a
// column that is not one of numbers read where they are: a half float, a decimal, a string.
func typedNumbers[R any](r *ranger, b *body,
	f8 func([]int8) R, f16 func([]int16) R, f32 func([]int32) R, f64 func([]int64) R,
	u8 func([]uint8) R, u16 func([]uint16) R, u32 func([]uint32) R, u64 func([]uint64) R,
	g32 func([]float32) R, g64 func([]float64) R,
) (out R, ok bool) {
	switch numberKey(r.class, r.width) {
	case numberKey(scInt, 1):
		return f8(unsafex.Slice[int8](b.raw)), true
	case numberKey(scInt, 2):
		return f16(unsafex.Slice[int16](b.raw)), true
	case numberKey(scInt, 4):
		return f32(unsafex.Slice[int32](b.raw)), true
	case numberKey(scInt, 8):
		return f64(unsafex.Slice[int64](b.raw)), true
	case numberKey(scUint, 1):
		return u8(b.raw), true
	case numberKey(scUint, 2):
		return u16(unsafex.Slice[uint16](b.raw)), true
	case numberKey(scUint, 4):
		return u32(unsafex.Slice[uint32](b.raw)), true
	case numberKey(scUint, 8):
		return u64(unsafex.Slice[uint64](b.raw)), true
	case numberKey(scFloat, 4):
		return g32(unsafex.Slice[float32](b.raw)), true
	case numberKey(scFloat, 8):
		return g64(unsafex.Slice[float64](b.raw)), true
	}
	return out, false
}

// extremes is what scan returns.
type extremes struct{ imin, imax, nans int }

func scanOf[T unsafex.Number](nulls []bool, lo, hi int) func([]T) extremes {
	return func(v []T) extremes {
		imin, imax, nans := scanNumbers(v, nulls, lo, hi)
		return extremes{imin, imax, nans}
	}
}

// scanFast is scan for a column of numbers read where they are. It is not ok for the others.
func (r *ranger) scanFast(b *body, lo, hi int) (extremes, bool) {
	n := b.nulls
	return typedNumbers(r, b,
		scanOf[int8](n, lo, hi), scanOf[int16](n, lo, hi), scanOf[int32](n, lo, hi), scanOf[int64](n, lo, hi),
		scanOf[uint8](n, lo, hi), scanOf[uint16](n, lo, hi), scanOf[uint32](n, lo, hi), scanOf[uint64](n, lo, hi),
		scanOf[float32](n, lo, hi), scanOf[float64](n, lo, hi))
}

// sortedFast reports whether the n rows of a column of numbers without a null or a NaN are
// sorted. It is not ok for a column that is not one of numbers read where they are.
func (r *ranger) sortedFast(b *body) (sorted, ok bool) {
	return typedNumbers(r, b,
		sortedNumbers[int8], sortedNumbers[int16], sortedNumbers[int32], sortedNumbers[int64],
		sortedNumbers[uint8], sortedNumbers[uint16], sortedNumbers[uint32], sortedNumbers[uint64],
		sortedNumbers[float32], sortedNumbers[float64])
}
