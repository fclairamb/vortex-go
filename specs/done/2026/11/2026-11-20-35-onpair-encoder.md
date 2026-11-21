---
model: sonnet
effort: high
pr: batch
automerge: false
---

# An OnPair encoder: the compressor picks the smaller of FSST and `vortex.onpair` for strings

## Problem

`vortex.onpair` is read and never written (`docs/goals.md:84-85`: "the compressor can add
OnPair if it saves bytes on the benchmarks against the reference").

The reference registers both string schemes and keeps the smaller one on the sample of
each chunk (`~/code/other/vortex/vortex-btrblocks/src/session.rs:81-85`,
`schemes/string/onpair.rs:98-120`). We only have FSST (`internal/compress/types.go:49`).
The `Default` ratio of ClickBench `hits_5k`, mostly strings, is our worst one: 1.077
(`testdata/size-ratios.json`).

## Proposal

1. `internal/encode/onpair.go`, after `onpair_compress`
   (`~/code/other/vortex/encodings/onpair/src/compress.rs`) and the `onpair` crate 0.4.1
   it calls (`Cargo.toml:207`), with its `DEFAULT_CONFIG`:
   - train the dictionary of tokens on the strings of the chunk, as the crate does;
   - write each string as the codes of the longest tokens that cover it.
   The array, as `internal/array/onpair.go:10-14` decodes it: metadata `{1:
   uncompressed_lengths_ptype, 3: dict_size, 4: codes_len, 5: dict_offsets_ptype, 6:
   codes_ptype, 7: codes_offsets_ptype}`, buffer 0 the bytes of the dictionary, children
   the dictionary offsets, the codes, the code offsets (rows + 1), the uncompressed
   lengths and the validity. An all-null chunk is a null constant, as upstream
   (`compress.rs`, the `AllOr::None` case).
2. `IDOnPair` in `Encodings()` (`internal/encode/node.go:324`), an `estimate` twin, and
   `OnPairBytes(e, v, nulls)` next to `FSSTBytes`.
3. `internal/compress/types.go`: an `sOnPair` candidate for utf8, after `sFSST` (the
   registration order is the tie-break upstream). Its integer children are compressed
   like those of FSST (`compress.go:274-287`). Not for binary.
4. `wforce.go`, `encodeForcedStrings`: `"onpair"`.
5. Vectors in `testdata/vectors/`, from `tools/vectors`: the encode step with a
   dictionary taken from the reference is byte-exact. Training is not compared byte for
   byte, as for FSST.
6. `docs/goals.md`: remove `vortex.onpair` from the list of what is never written, and
   the new ratios. `README.md` "Limits": drop OnPair from the last line. `go generate`.

## Tests

- Round trip, seeded: URLs, words, UUIDs, empty strings, a 1 MB string, one row, all
  null, strings with nulls.
- The vectors of step 5.
- `estimate` within 10% of the real size.
- The dictionary never has more tokens than the configuration allows, and a column with
  no repetition is not made larger than its input plus a fixed overhead.
- Negative: a binary column never gets `vortex.onpair`.
- Compression ratio, as a floor that can only rise: on the string columns of the
  committed `default_mixed` data, our OnPair output is at most 1.15 times the reference's.
- `make interop`: a forced `onpair` file, with and without nulls, read by the reference.
  The size gate: no ratio of `testdata/size-ratios.json` goes up, and
  `clickbench_hits_5k.regular` is committed at its new value.
- `TestWrittenMatchesFiles` (spec 28) meets `vortex.onpair`.
- `testing.AllocsPerRun`: 0 allocation per call into reused buffers.
- `make bench`: the write speed of the string benchmarks is recorded. Trying a second
  scheme on the sample must not cost more than 10% on a chunk that ends as FSST.

## To verify

- The source of the `onpair` crate 0.4.1 is not on this Mac. `cargo fetch` in
  `tools/vectors` puts it in `~/.cargo/registry/src/`. Read its license before porting
  anything, and keep its notice if code is taken from it (CLAUDE.md, "References").
- The training of the crate: the sample, the number of rounds, `max_dict_bits` of
  `DEFAULT_CONFIG`, how a pair is chosen.
- That the reference's `hits_5k` file in `.cache/fixtures/` holds `vortex.onpair`
  arrays at 0.93.0. If it holds none, the scheme never wins there and the ratio will not
  move: say so in `docs/goals.md` in place of a new number.
- `vortex.onpair` is in the edition since `core 2026.08.1` (`internal/edition/table.go`).
