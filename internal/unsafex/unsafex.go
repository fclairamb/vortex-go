// Package unsafex is the only package of the module that imports unsafe. It casts a byte
// slice to a slice of numbers, without copying when it can, and it stores values in the
// fields of a slice of structs.
package unsafex

import (
	"reflect"
	"unsafe"
)

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

// Put stores vals[i] in the field of row i of rows, a slice of structs, that sits off bytes
// into the row. V must be the exact type of the field, or have its layout: the destination is
// typed memory of rows, so the garbage collector sees every pointer that a V holds, and the
// write barrier applies. off and the size of V come from reflect, which the caller checks.
// It writes min(rows.Len(), len(vals)) rows.
func Put[V any](rows reflect.Value, off uintptr, vals []V) {
	base, size := rows.UnsafePointer(), rows.Type().Elem().Size()
	for i := range min(rows.Len(), len(vals)) {
		*(*V)(unsafe.Add(base, uintptr(i)*size+off)) = vals[i]
	}
}

// PutFunc is Put for values that get returns, one per row.
func PutFunc[V any](rows reflect.Value, off uintptr, get func(i int) V) {
	base, size := rows.UnsafePointer(), rows.Type().Elem().Size()
	for i := range rows.Len() {
		*(*V)(unsafe.Add(base, uintptr(i)*size+off)) = get(i)
	}
}

// PutNull is Put for a field of a type like sql.Null[V]: the value goes at off, and a bool
// at offValid says whether the row is not null. A null row gets the zero value.
func PutNull[V any](rows reflect.Value, off, offValid uintptr, vals []V, null func(i int) bool) {
	base, size := rows.UnsafePointer(), rows.Type().Elem().Size()
	for i := range min(rows.Len(), len(vals)) {
		row := unsafe.Add(base, uintptr(i)*size)
		valid := !null(i)
		*(*bool)(unsafe.Add(row, offValid)) = valid
		if valid {
			*(*V)(unsafe.Add(row, off)) = vals[i]
		} else {
			*(*V)(unsafe.Add(row, off)) = *new(V)
		}
	}
}

// PutNullFunc is PutNull for values that get returns, one per row. get is not called for a
// null row.
func PutNullFunc[V any](rows reflect.Value, off, offValid uintptr, get func(i int) V, null func(i int) bool) {
	base, size := rows.UnsafePointer(), rows.Type().Elem().Size()
	for i := range rows.Len() {
		row := unsafe.Add(base, uintptr(i)*size)
		valid := !null(i)
		*(*bool)(unsafe.Add(row, offValid)) = valid
		if valid {
			*(*V)(unsafe.Add(row, off)) = get(i)
		} else {
			*(*V)(unsafe.Add(row, off)) = *new(V)
		}
	}
}

// Bytes returns the memory of v as bytes, sharing it. The values are in the byte order of the
// machine: callers only use it for little-endian files.
func Bytes[T Number](v []T) []byte {
	if len(v) == 0 {
		return nil
	}
	var zero T
	return unsafe.Slice((*byte)(unsafe.Pointer(&v[0])), len(v)*int(unsafe.Sizeof(zero)))
}

// Size returns the bytes of a value of type T.
func Size[T Number]() int {
	var zero T
	return int(unsafe.Sizeof(zero))
}

// StringBytes views the bytes of s without copying them. The result must not be written.
func StringBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
