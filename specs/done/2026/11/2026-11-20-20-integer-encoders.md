---
model: sonnet
effort: high
pr: batch
automerge: false
---

# Encoders for the integer encodings, checked byte for byte against the reference where it is deterministic

## Problem

The writer only emits plain arrays, so its files are several times larger than the
reference writer's. Goal 3 of `docs/goals.md` asks for files at most 10% larger on TPC-H
and ClickBench. The first step is the set of lightweight encoders that everything else
cascades into. This spec writes the encoders. Choosing between them is the compressor
spec.

Upstream paths are in `~/code/other/vortex`.

## Proposal

1. `internal/encode`, one function per encoding. Each takes a typed slice (and validity)
   and returns the encoded parts that the array writer of the writer spec serializes:
   - `fastlanes.bitpacked`: a given bit width, FastLanes blocks of 1024, values that do
     not fit go to patches (indices and values, with chunk offsets as upstream writes
     them). Also a function that returns the best width from a histogram of bit lengths,
     as upstream picks it (`encodings/fastlanes/src/bitpacking/array/bitpack_compress.rs`);
   - `fastlanes.for`: reference = the minimum, child = value minus reference, unsigned;
   - `vortex.zigzag`;
   - `fastlanes.rle` and `vortex.runend` (any value type for run-end);
   - `vortex.dict` (array form): codes of the narrowest unsigned type, values in order of
     first appearance, for integers, floats, strings and binary;
   - `vortex.sequence`: detected when every step is equal;
   - `vortex.constant`: detected when every valid value is equal;
   - `vortex.sparse`: a fill value (the most frequent one, or null) and patches.
2. Each encoder has a cheap `estimate` twin that returns the encoded size in bytes from a
   sample, without building the output. The compressor spec uses it.
3. Encoders write into buffers owned by the writer. No allocation per call once running.
4. **Byte-exact vectors** (`docs/testing.md` section 4). For the deterministic encoders
   (bit-packing at a given width, FoR, zigzag, RLE, run-end, dictionary, sequence), the
   reference produces vectors: input, parameters, and each output buffer and child.
   - `tools/vectors/`: the generator. It calls the encoders of the reference
     implementation directly and writes `testdata/vectors/<encoding>/*.json` (inputs and
     outputs as hex). Committed, with the command that made them in a `README.md`;
   - our encoder must produce the same bytes from the same input and parameters, and our
     decoder must give the input back.
5. A written file may now contain these identifiers: the edition conformance test of the
   writer spec covers them.

## Tests

- Round trip, seeded, per encoder and per type width: random data, sorted data, constant
  data, all null, one value, 1,023, 1,024 and 1,025 values, the limits of each type.
- The vectors of step 4: byte equality for each one.
- `estimate` is within 5% of the real encoded size on the same data, for each encoder.
- Detection: `sequence` and `constant` say no on data that is almost a sequence or almost
  constant (one value off, at the start, the middle and the end).
- `make interop`: one file per encoder, forced, read by the reference.
- `testing.AllocsPerRun`: 0 allocation per call into reused buffers.

## To verify

- How to call a single encoder of the reference with fixed parameters. From Python if
  `vortex-data` 0.93.0 exposes them. Otherwise `tools/vectors/` is a small Rust program
  (its own `Cargo.toml`, outside the Go module) built against the `vortex` crates at the
  pinned tag: `cargo` is then needed to regenerate the vectors, never to run the tests.
- Whether the patch layout (chunk offsets) is deterministic for a given input.
- Whether the reference writes `fastlanes.bitpacked` (v1) or `fastlanes.bitpacked.v2` by
  default at 0.93.0 (`encodings/fastlanes/src/bitpacking/plugin/mod.rs:75-88`). Only the
  identifier that is in the edition may be written.

## Resolved open questions

- A Rust program in `tools/vectors/` is allowed if Python cannot reach the encoders: a test tool outside the module, like the Python oracle. (autopilot: recommended answer)

## Open questions

- **A Rust program in `tools/vectors/`**, if Python cannot reach the encoders.
  Recommended answer: yes. It is a test tool outside the module, like the Python oracle,
  and `docs/testing.md` already says "the Rust code produces vectors".

## Verified against 0.93.0

- `vortex-data` 0.93.0 does not expose the encoders (only `ZigZagArray.encode`), so
  `tools/vectors` is a Rust program on the `vortex-*` crates `=0.93.0`.
- The patch layout is deterministic: indices in u8, u16, u32 or u64 by the length of the array
  (under 255, 65,535, 2^32), values, and one u64 offset per block of 1,024 (the number of patches
  before it), with `offset_within_chunk` 0. A null is never a patch; bit-packing a signed type
  fails on a negative valid value, and `bitpack_to_best_bit_width` fails when the best width is
  the bits of the type.
- The default is `fastlanes.bitpacked` (v1, one width in the metadata); `.v2` is written only
  for blocks of different widths, which these encoders do not make. `fastlanes.for` likewise
  writes v1 (`fastlanes.for.v2` for one reference per block).
- FoR's child has the type of the input (signed stays signed, wrapping subtraction), not an
  unsigned one; null rows are written 0. A sequence needs a non-zero step, no null, and no
  overflow; one value is a sequence of step 0 (the step is a signed integer in the metadata, even
  for unsigned types). A constant is all equal or all null. A sparse array whose values all equal
  its fill is a constant array. The reference picks the most frequent value among equal counts
  in hash order, ours takes the first one. An all-null validity is a constant `false` array.
