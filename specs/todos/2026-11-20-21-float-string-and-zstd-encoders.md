---
model: sonnet
effort: high
pr: batch
automerge: false
---

# Encoders for floats, strings, decimals and timestamps: ALP, ALP-RD, FSST, byte parts, datetime parts, Zstd

## Problem

After the integer encoders, floats, strings, decimals and timestamps are still written
plain. They are most of the bytes of TPC-H and ClickBench. Each of these encoders turns
its values into integer children that the integer encoders then compress.

Upstream paths are in `~/code/other/vortex/encodings/`.

## Proposal

In `internal/encode`, each with its `estimate` twin, as in the integer encoders spec:

1. `vortex.alp` (`alp/src/alp/compress.rs` and the `alp` crate 0.0.6): find the exponent
   pair `(e, f)` on a sample as upstream does, encode `round(v * 10^e * 10^-f)` as `i32` or
   `i64`, and put every value that does not decode back to the same bits in patches. NaN,
   infinities and `-0` always go to patches.
2. `vortex.alprd` (`alp/src/alp_rd/`): pick the cut `right_bit_width`, build the
   dictionary of the most frequent left parts (at most 8 entries upstream), left parts
   outside it go to patches.
3. `vortex.fsst` (`fsst/src/compress.rs` and the `fsst-rs` crate 0.7.0): train a symbol
   table on a sample of the column (the published FSST algorithm: 5 rounds, up to 255
   symbols, 1 to 8 bytes), then encode with the longest match and escapes. One table per
   chunk. The output layout is the 3-buffer form the decoder spec describes.
4. `vortex.decimal_byte_parts`: a decimal whose values fit a narrower signed integer is
   written as that integer (the most significant part, no lower part).
5. `vortex.datetimeparts`: a timestamp split into days, seconds and subseconds, each of
   the narrowest type.
6. `vortex.zstd`, with `github.com/klauspost/compress/zstd`: frames of the valid values
   (numbers as raw little-endian bytes, strings with a `u32` LE length each), frame sizes
   as upstream cuts them, no dictionary in this version. One encoder per writer, reused.
7. Not written, and why, in `docs/goals.md` under goal 3: `vortex.pco` (see the open
   question), `vortex.onpair`, `vortex.variant`, `vortex.parquet.variant`,
   `vortex.masked`, `vortex.bytebool`. The reader decodes them all. "The writer does not
   have to emit every encoding."
8. ALP with fixed exponents and FSST with a fixed symbol table are deterministic: they
   get byte-exact vectors in `testdata/vectors/`, from the same generator as the integer
   encoders. FSST training is not compared byte for byte: only the encode step, with a
   table taken from the reference.

## Tests

- Round trip, seeded, per encoder: decimal-like prices, measurements with noise, integers
  stored as floats, random bits, NaN and `-0`, all null, one value. Strings: URLs, words,
  UUIDs, empty strings, a 1 MB string, bytes that are not UTF-8 (binary columns).
- Bit-exact floats: every value read back has the same bits as the one written.
- The vectors of step 8.
- `estimate` within 10% of the real size for each encoder.
- FSST: the trained table never has a symbol longer than 8 bytes or more than 255 symbols,
  and a column with no repetition is not made larger than its input plus a fixed overhead.
- Compression ratio, as a floor that can only rise: on the string columns of the committed
  `default_mixed` data, our FSST output is at most 1.15 times the reference's.
- `make interop`: one file per encoder, forced, read by the reference.
- `testing.AllocsPerRun`: 0 allocation per call into reused buffers, Zstd included.

## To verify

- How upstream picks the ALP exponents (sample size, the candidate pairs) and when it
  prefers ALP-RD: `alp/src/alp/compress.rs`, `alp/src/alp_rd/mod.rs`.
- The FSST training parameters of `fsst-rs` (sample size, rounds, the gain function).
- The frame size upstream uses for `vortex.zstd`, and the compression level.
- Whether the klauspost encoder's output is read by the Rust `zstd` crate in every mode
  we use: `make interop` proves it.

## Open questions

- **A Pco encoder.** The reference uses `vortex.pco` for numbers in compact mode.
  Recommended answer: not in this version. The decoder alone is the largest format of the
  edition. Compact mode uses `vortex.zstd` for numbers too, and the compressor spec
  measures what it costs in file size against the reference.
