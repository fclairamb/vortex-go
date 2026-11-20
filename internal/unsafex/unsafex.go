// Package unsafex is the only package of the module that imports unsafe. It casts a byte
// slice to a slice of numbers, without copying when it can.
package unsafex

import "unsafe"

// Number is the set of the numeric types a byte slice can be cast to.
type Number interface {
	~int8 | ~int16 | ~int32 | ~int64 | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~float32 | ~float64
}

// Slice returns the n values of type T that the bytes of b hold, n being len(b) divided by
// the size of T. The values are in the byte order of the machine: callers only use it for
// little-endian data. It shares the memory of b when b is aligned for T, and copies it
// otherwise. A trailing partial value is ignored.
func Slice[T Number](b []byte) []T {
	var zero T
	size := int(unsafe.Sizeof(zero))
	n := len(b) / size
	if n == 0 {
		return nil
	}
	if uintptr(unsafe.Pointer(&b[0]))%unsafe.Alignof(zero) == 0 {
		return unsafe.Slice((*T)(unsafe.Pointer(&b[0])), n)
	}
	out := make([]T, n)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(&out[0])), n*size), b)
	return out
}
