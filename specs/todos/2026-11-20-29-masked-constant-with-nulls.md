---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Write `vortex.masked`: a chunk whose valid values are all equal becomes a constant under a validity

## Problem

`vortex.masked` is read and never written. `docs/goals.md:88-89` says a column never needs
it. The reference writer does use it: when every valid value of a chunk is the same and
some rows are null, it writes a `vortex.constant` wrapped in a `vortex.masked` that holds
the validity (`~/code/other/vortex/vortex-compressor/src/compressor/constant/mod.rs:181-203`).

Our constant encoders refuse that case, "a null among values included":
`encode.Constant` (`internal/encode/seq.go:62-87`), `ConstantBytes` (`seq.go:96-98`),
`ConstantBools` (`seq.go:121-123`). Such a chunk goes to the other schemes (run-end,
dictionary, sparse), which cost more than a scalar and a bitmap.

## Proposal

1. `internal/encode`: `IDMasked = "vortex.masked"` and `Masked(e, values *Node, nulls
   []bool, rows int) *Node`: child 0 the values, child 1 the validity, no metadata, no
   buffer (the shape `internal/array/masked.go:5` decodes). Add it to `Encodings()`
   (`internal/encode/node.go:324`).
2. The three constant encoders get a twin that takes the first valid value and checks the
   other valid ones against it. All valid: the constant alone, as today. Some null: the
   constant under `Masked`. All null: the null constant, as today.
3. `internal/compress`: where the constant is tried first (`schemes.go:135`,
   `schemes.go:192`, `types.go:37`, `types.go:136`), use the twin. It wins only when it is
   smaller than the plain array, as upstream (`constant/mod.rs:155-169`).
4. `wforce.go`: `"masked"` forces it, for the interop file.
5. `docs/goals.md`: remove `vortex.masked` from the list of what is never written.
   `go generate` for the matrix.

## Tests

- `internal/encode`: round trip through our decoder for numbers, strings and booleans:
  one valid value among nulls, nulls at both ends, half null. A float compared by its
  bits (NaN, `-0`). Two distinct valid values: no masked array.
- `internal/compress`: a chunk of 8,192 rows, 30% null, one value, is `vortex.masked`
  over `vortex.constant` and smaller than what the compressor picked before.
- Negative: no null gives `vortex.constant` alone, all null gives the null constant.
- `make interop`: a forced `masked` file per type, read by the reference.
- `TestWrittenMatchesFiles` (spec 28) meets `vortex.masked`.
- `testing.AllocsPerRun`: 0 allocation per call into reused buffers.
- The size ratios of `testdata/size-ratios.json` do not go up.

## To verify

- The dtype the child of `vortex.masked` must have (non-nullable values under a nullable
  parent, or the same type): `~/code/other/vortex/vortex-array/src/arrays/masked/array.rs`.
- That the statistics of the root go on the `vortex.masked` node (`wcompress.go:108-110`
  sets `statsID` from the node).
- `vortex.masked` is in the edition since `core 2025.10.0` (`internal/edition/table.go`).
