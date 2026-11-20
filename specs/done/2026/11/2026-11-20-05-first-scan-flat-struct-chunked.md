---
model: sonnet
effort: high
pr: batch
automerge: false
---

# `Scan` reads whole columns of numbers and booleans, and a test harness compares them with the oracle

## Problem

`Open` knows the layout tree but reads no data. This spec builds the path from a layout to
a decoded column, for the simplest arrays, and the harness every later decoding spec will
extend: scan a fixture, hash each column, compare with the oracle's hash.

Upstream paths are in `~/code/other/vortex`.

## Proposal

1. **The public column API** of `docs/interface.md`, in `scan.go` and `column.go`:
   `Scan(ctx, q Query, columns ...string) iter.Seq2[*Batch, error]`, `Batch.Len`,
   `Batch.Col`, `Column.Type`, `Column.Null`, `Column.Bool`, `Column.Field`, and
   `Values[T Number](c *Column) ([]T, error)`. `Query` exists as an empty struct for now,
   and `columns` must be empty (an error otherwise, until the query spec). Sequential, one
   goroutine. A batch is at most 8,192 rows, set by an unexported option that tests change.
2. **Layouts** (`vortex-layout/src/layouts/`): a reader per identifier, found through the
   footer's `layout_specs` table.
   - `vortex.flat` (`flat/mod.rs`): one segment, no child. Its metadata is a protobuf with
     one optional field: ignore it.
   - `vortex.struct` (`struct_/mod.rs:82-112`): when the struct is nullable, child 0 is the
     validity (Bool, not nullable), then one child per field.
   - `vortex.chunked` (`chunked/mod.rs`): the children are row chunks of the same type.
     Their row counts must add up to the parent's.
   - any other identifier: an "unsupported layout" error that names it.
3. **A serialized array** (`vortex-array/src/serde.rs:56-160`, read at `:659-768`). A flat
   segment is: buffers, each after its padding, then the `Array` flatbuffer padded to 8,
   then its length as `u32` LE in the last 4 bytes.
   - `Array{root: ArrayNode, buffers: [Buffer]}`; `Buffer{padding u16, alignment_exponent
     u8, compression u8, length u32}`. Buffers are taken in order from offset 0, skipping
     `padding` each time. A `compression` other than 0 is an error;
   - `ArrayNode{encoding u16, metadata, children, buffers [u16], stats}`. `encoding` is an
     index into the footer's `array_specs`. `buffers` are indexes into `Array.buffers`;
   - the dtype of a node is not stored: it comes from the parent, so each decoder is called
     with the dtype it must produce and the row count.
4. **Validity** (`vortex-array/src/array/vtable/mod.rs:238-268`): no validity child means
   every row is valid. A child is a Bool array of the same length, not nullable. "All null"
   is a constant `false` child.
5. **Decoders**, one file each under `internal/array/`, registered by identifier:
   - `vortex.null`: no buffer;
   - `vortex.bool`: metadata `{offset: uint32}` (a bit offset under 8), buffer 0 is the
     bitmap, child 0 the validity;
   - `vortex.primitive`: buffer 0 is the values, little-endian, child 0 the validity;
   - `vortex.struct`: validity first when present, then the fields;
   - `vortex.chunked`: child 0 is the chunk offsets (`u64`, chunks + 1), then the chunks;
   - `vortex.constant`: buffer 0 is a protobuf `ScalarValue` (numbers, bools and null in
     this spec, the other types join with their own spec);
   - `vortex.ext`: child 0 is the storage. Date, time and timestamp columns give their
     storage (`docs/interface.md`, "Types");
   - `vortex.masked`: child 0 is the values, child 1 the validity;
   - `vortex.bytebool`: buffer 0 has one byte per value.
6. **Typed slices.** `internal/unsafex` is the only package that imports `unsafe`. It casts
   an aligned `[]byte` to `[]T`. When a buffer is not aligned for `T`, it is copied first.
   A test fails when any other package imports `unsafe`.
7. **Canonical hash in Go**, test-only, in `internal/testutil`: the definition of
   `docs/testing.md` section 1, for the types of this spec.
8. **The harness**, `corpus_test.go`: for every fixture, scan and compare the row count,
   the null count and the hash of each column with the `.expected.json`. A table `pending`
   lists the fixtures (or columns) that still need a later spec, each with the identifier
   it waits for. A pending entry that passes makes the test fail, so the list can only
   shrink. Every identifier met and not registered gives the "unsupported encoding" error
   with its name.

## Tests

- The harness: `primitives`, `booleans`, `null`, `chunked`, `bytebool`, `struct_nested`
  (its string field pending) and `datetime` pass. The rest is in `pending`.
- Batch sizes 1, 7, 1024 and 100,000 give the same hashes.
- `Values[T]` with the wrong `T` returns an error naming both types. `Bool(i)` on a number
  column panics (tested with `recover`). `Field(i)` out of range panics.
- Hand-built segments for each decoder's error paths: missing buffer, buffer too short for
  the row count, validity of the wrong length, chunk offsets that decrease, an encoding
  index outside `array_specs`, a compressed buffer.
- An unaligned buffer gives the same values as an aligned one.

## To verify

- How the reader maps a row range of a flat layout to the array: one segment is one array,
  sliced after decoding.
- `vortex.ext` over a timestamp with a time zone: `Type().Zone()` gives it.
- Whether a root layout can be something other than `vortex.struct` in the fixtures.
