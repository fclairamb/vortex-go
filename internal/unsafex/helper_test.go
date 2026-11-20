package unsafex

import "unsafe"

// unsafeBytes views the memory of a []uint64 as bytes.
func unsafeBytes(u []uint64) []byte {
	return unsafe.Slice((*byte)(unsafe.Pointer(&u[0])), len(u)*8)
}
