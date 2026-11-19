// Package protowire decodes the protobuf wire format, by hand: varints, zigzag, fixed32,
// fixed64, length-delimited fields and packed repeated scalars. There is no reflection and no
// generated code; each message gets a small decode function next to the code that uses it.
//
// It mirrors what prost (upstream's protobuf runtime) accepts: groups are rejected, unknown
// fields are yielded like any other so the caller skips them by ignoring their number.
// The input is untrusted: every length is checked against the bytes left before it is used.
package protowire
