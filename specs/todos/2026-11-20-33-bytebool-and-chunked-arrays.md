---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Write `vortex.bytebool` and the array `vortex.chunked`, when forced

## Problem

Two arrays of the edition are read and never written, and nothing in a writer picks them:

- `vortex.bytebool`: a byte per boolean. `vortex.bool` holds the same values in a bit
  each (`docs/goals.md:90-91`). No scheme of the reference compressor makes one: it is
  only in its compat fixtures and its Python API
  (`~/code/other/vortex/vortex-test/compat-gen/src/fixtures/arrays/synthetic/encodings/bytebool.rs`).
- the array `vortex.chunked` (not the layout, which is written): several arrays end to
  end. The reference compressor makes the array canonical first
  (`vortex-compressor/src/compressor/cascade.rs:93`), and our writer cuts its own chunks
  (`wcol.go:87-110`), one array per flat segment.

Writing them brings no byte and no speed. It brings one thing: the proof, by the
reference reader, that the bytes we would write for them are right.

## Proposal

1. `internal/encode`:
   - `IDByteBool` and `ByteBool(e, v []bool, nulls []bool) *Node`: buffer 0 a byte per
     row (0 or 1, 0 for a null), child 0 the validity (`internal/array/bytebool.go:5-6`).
   - `IDChunked` and `Chunked(e, parts []*Node, rows []int) *Node`: child 0 the offsets
     of the parts as `u64` (parts + 1), then the parts (`internal/array/chunked.go:8-9`).
   Both in `Encodings()` (`internal/encode/node.go:324`).
2. `wforce.go`: `"bytebool"` on a boolean column. `"chunked"` on any column of numbers,
   booleans or strings: the rows of the chunk cut in 3 parts (one of them empty when
   there are rows enough), each written plain.
3. The compressor never picks either. `docs/goals.md:78-92` says so: written only when a
   test forces them, and why.
4. `go generate` for the matrix.

## Tests

- `internal/encode`: round trip through our decoder: booleans with and without nulls,
  one row, 8,193 rows. Chunked: 1 part, 3 parts, an empty part, parts with nulls.
- `TestForcedEncodings` (`writer_encoders_test.go:220`): `bytebool` and `chunked` in
  `encodingIDs`, the file has the identifier and reads back the same rows.
- Negative: `bytebool` forced on a number column is the error "the bytebool encoding
  cannot take" (`wforce.go:44`). A file written without `force`, in any mode, never has
  either identifier.
- `make interop`: one forced file each, read by the reference
  (`interop_test.go:58`, `writeInteropEncoders`).
- `TestWrittenMatchesFiles` (spec 28) meets both.

## To verify

- The validity of a `vortex.chunked` array: on the parts only, or also on the parent
  (`~/code/other/vortex/vortex-array/src/arrays/chunked/vtable/mod.rs`).
- Where the statistics of the root go when the root is `vortex.chunked`
  (`wcompress.go:108-110`).

## Open questions

- **Does "written when forced" earn a "yes" in the matrix?** No user of the package can
  get these two arrays in a file: only a test can. Recommended answer: yes, write them
  forced and show "yes", with the sentence of step 3 in `docs/goals.md`. The matrix then
  says the reference reads our bytes for every array of the edition. The other answer is
  to drop this spec and keep the two "no" with their reason, which is where the document
  stands today.

## Resolved open questions

- **"Written when forced" earns a "yes":** write both arrays when forced and show "yes" in the matrix, with the sentence of step 3 in `docs/goals.md`. (autopilot: recommended answer)
