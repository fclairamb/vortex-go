// Package scalar decodes a protobuf-serialized vortex.scalar.ScalarValue into a small Go value.
//
// It mirrors vortex-array/proto/scalar.proto (message ScalarValue) and the mapping in
// vortex-array/src/scalar/proto.rs, at the pinned upstream tag. The variant and union arms
// (fields 11 and 12) need the DType machinery and are rejected for now.
package scalar
