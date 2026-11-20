// Package flatbuf reads flatbuffers, by hand, from untrusted bytes, and builds them (Builder).
//
// A Vortex file writes its flatbuffers with finish_minimal: no size prefix and no file
// identifier (vortex-array/src/flatbuffers/traits.rs). The root offset is the first four bytes.
//
// Every offset is checked against the buffer before it is used, so a hostile buffer gives an
// *Error carrying the offending offset, never a panic and never a read out of bounds. Vectors
// are views over the buffer: no allocation is made from a length read in the buffer, except
// the slices of scalars that are bounded by the buffer size itself.
//
// Field ids follow the declaration order of the table in the .fbs file; the caller passes a
// negative-free constant. A union occupies two ids: its type, then its value.
package flatbuf
