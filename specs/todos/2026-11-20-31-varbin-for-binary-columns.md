---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Write `vortex.varbin` for binary columns: offsets that compress, in place of 16-byte views

## Problem

`vortex.varbin` is read and never written. A binary column is written as a
`vortex.varbinview`: 16 bytes of view per value that no scheme compresses. For binary,
our compressor only tries a constant, a dictionary, and zstd in `Compact`
(`internal/compress/types.go:36-55`).

The reference has one more scheme for binary columns, `vortex.binary.varbin`
(`~/code/other/vortex/vortex-btrblocks/src/schemes/binary/varbin.rs`, registered in
`session.rs:91`): the values in one buffer, and an offsets child that the integer schemes
compress. For values of one width the offsets are a sequence and cost nothing. It is not
a candidate for utf8 (`varbin.rs:44-46`).

## Proposal

1. `internal/encode`: `IDVarBin = "vortex.varbin"` and `VarBinBytes(e, v [][]byte, nulls
   []bool) *Node`: metadata `{offsets_ptype}`, buffer 0 the bytes of the values end to
   end, child 0 the offsets (rows + 1, the narrowest unsigned type), child 1 the validity.
   The shape `internal/array/varbin.go:10-11` decodes. A null row has no byte. Its
   `estimate` twin, as the other encoders. Add it to `Encodings()`
   (`internal/encode/node.go:324`).
2. `internal/compress/types.go`: a `varbin` candidate when `utf8` is false, picked on the
   sample like the others. Its offsets child is compressed as integers
   (`compress.go:274-287`, the `kid` helpers).
3. `wforce.go`, `encodeForcedStrings`: `"varbin"` forces it.
4. `go generate` for the matrix.

## Tests

- `internal/encode`: round trip of empty values, a 1 MB value, nulls, all null, one row.
  `estimate` within 10% of the real size.
- `internal/compress`: 10,000 binary values of 16 random bytes are `vortex.varbin` with
  `vortex.sequence` offsets, and the array is under 17 bytes a value (it is 32 as views).
- Negative: a utf8 column never gets `vortex.varbin`. A binary column of 3 distinct
  values still gets a dictionary.
- `make interop`: a forced `varbin` file, with and without nulls, read by the reference.
- `TestWrittenMatchesFiles` (spec 28) meets `vortex.varbin`.
- `testing.AllocsPerRun`: 0 allocation per call into reused buffers.
- The size ratios of `testdata/size-ratios.json` do not go up.

## To verify

- The scheme exists at the tag 0.93.0 (the clone in `~/code/other/vortex` is `main` of
  2026-11-18). If it is newer than the pinned release, keep the encoder and the forced
  file, and say in `docs/goals.md` that the compressor is ahead of the pinned reference.
- Where the reference tries it among the binary schemes, and how a tie is broken
  (`session.rs:88-95`: registration order).
