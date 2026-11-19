---
model: sonnet
effort: high
pr: batch
automerge: false
---

# Hand-written readers for the two wire formats a Vortex file uses: flatbuffers and protobuf

## Problem

A Vortex file is described by flatbuffers (postscript, footer, dtype, layout tree, array
tree) and by protobuf (the metadata of each encoding and layout, and every scalar value:
statistics, constants, fill values). The repo may not import a module for either
(`CLAUDE.md`: standard library plus `klauspost/compress`). Both readers face untrusted
bytes, so they must never panic or allocate from a length they did not check.

Upstream paths below are in `~/code/other/vortex`.

## Proposal

1. `internal/flatbuf`, a reader only (the builder comes with the writer):
   - a `Table` view over `[]byte`: vtable lookup, scalar fields with defaults, strings,
     byte vectors, vectors of scalars, of tables, of structs, and unions (type + value);
   - every offset is checked against the buffer before use: root offset, vtable offset and
     size, field offsets, vector lengths times element size, string bounds. A violation is
     an error with the offset, never a panic and never a read out of bounds;
   - a depth limit for nested tables, passed by the caller;
   - upstream writes its flatbuffers with `finish_minimal`: no size prefix, no file
     identifier (`vortex-array/src/flatbuffers/traits.rs:66-69`).
2. `internal/fbs`: typed accessors over `flatbuf.Table` for the tables of the four schemas
   a file uses. Hand-written from the `.fbs` files, with the field ids in comments:
   - `vortex-file/flatbuffers/vortex-file/footer.fbs`: `Postscript`, `PostscriptSegment`,
     `Footer`, `ArraySpec`, `LayoutSpec`, struct `SegmentSpec`, `FileStatistics`;
   - `vortex-layout/flatbuffers/vortex-layout/layout.fbs`: `Layout`;
   - `vortex-array/flatbuffers/vortex-array/array.fbs`: `Array`, `ArrayNode`, struct
     `Buffer`, `ArrayStats`;
   - `vortex-array/flatbuffers/vortex-dtype/dtype.fbs`: `DType` and its union.
   `message.fbs` (IPC) is not used by files: leave it out.
3. `internal/protowire`: a decoder for the protobuf wire format. Varint, zigzag, fixed32,
   fixed64, length-delimited, packed repeated scalars, unknown fields skipped, groups
   rejected. An iterator over `(field number, wire type, value)` with bounds checks. No
   reflection, no generated code: each message gets a small decode function next to the
   code that uses it, in later specs.
4. `internal/scalar`: decode `vortex.scalar.ScalarValue`
   (`vortex-array/proto/scalar.proto`, mapping in `vortex-array/src/scalar/proto.rs`) into
   a small Go value: null, bool, signed (sint64), unsigned, f16 bits, f32, f64, string,
   bytes (decimals are little-endian bytes at their storage width), list.
5. Each package gets a `doc.go` that names the upstream file it mirrors.

## Tests

- `internal/flatbuf`: buffers built by hand in the test (a tiny builder in the `_test.go`
  file is fine) for every accessor, with defaults for absent fields.
- `internal/flatbuf`: every bounds check has a test with the smallest bad buffer that
  triggers it. A fuzz target reads random bytes through every accessor: no panic.
- `internal/fbs`: for each of 5 committed fixtures, slice the postscript by hand from the
  last bytes (8-byte trailer: version `u16`, postscript length `u16`, `VTXF`) and read
  `Postscript`, then `Footer`, `Layout` and `DType` through the accessors. Assert the
  number of array specs, layout specs and segments, non-zero.
- `internal/protowire`: table-driven, every wire type, truncated input at every byte,
  a varint of 11 bytes, a length past the end.
- `internal/scalar`: every arm of the `oneof`, encoded by hand.
- `make check` stays at 100%.

## To verify

- The exact field order and ids of each table at tag `0.93.0`. Field ids follow the
  declaration order unless an `(id: n)` attribute says otherwise.
- Whether any table uses flatbuffers features beyond the list of step 1 (nested
  flatbuffers, vectors of unions, shared strings, 64-bit offsets). Implement only what the
  four schemas use.
- `~/code/other/vortex-go` (`mprammer/vortex-go`, Apache-2.0) parses the footer in
  `file.go`. Read it for the approach. Copy nothing without its license notice.
