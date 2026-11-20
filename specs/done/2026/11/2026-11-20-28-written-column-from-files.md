---
model: sonnet
effort: low
pr: batch
automerge: false
---

# `vortex.map` is written but the support matrix says no: check the Written column against the files we write

## Problem

The support matrix of the README says `vortex.map` is not written. It is: `encodeList`
wraps the listview of a map in a `vortex.map` node (`wencode.go:312`). The identifier is
missing from the hand list `writtenArrays` (`wstats.go:456-462`), which feeds
`edition.RegisterWritten` and the matrix.

Nothing compares these hand lists (`wstats.go:446-466`, `internal/encode/node.go:324-331`)
with what the writer puts in a file. `TestSupportedMatchesRegistries` only counts
(`edition_test.go:142-160`). The next specs each add an identifier to the Written column,
and each needs a test that proves it.

## Proposal

1. Add `"vortex.map"` to `writtenArrays` (`wstats.go:456`).
2. A test `TestWrittenMatchesFiles` (in `edition_test.go`): write the files the writer
   tests already build (every type of `allTypes()`, `writer_test.go:59`, in the three
   compression modes, and one file per forced encoding, `writer_encoders_test.go:18-24`),
   open each and collect its `arrayEncodings` and `layoutEncodings`
   (`edition_test.go:163,181` use the same fields). The set met must equal the set
   `edition.Supported()` marks `Written`, for the kinds `array` and `layout`, both ways:
   - an identifier written and not registered fails, with the file that holds it;
   - an identifier registered and never met fails.
3. `go generate` (the matrix of `README.md` and of `website/`), `api.txt` unchanged.

## Tests

- `TestWrittenMatchesFiles` passes with `vortex.map` registered, and fails without it
  (check by hand once, the message names `vortex.map` and the file).
- A sub-test with a fake registry: one identifier too many and one missing each give one
  message, as `checkSeen` is tested (`edition_test.go:130-140`).
- `make check`.

## To verify

- Whether a file exposes the aggregates and extension types it holds, to check the kinds
  `aggregate` and `dtype` the same way. If not, leave them out and say so in the test.
