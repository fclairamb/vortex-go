---
model: sonnet
effort: high
pr: batch
automerge: false
---

# The integer encodings are decoded: bit-packing, frame of reference, RLE, run-end, dictionary, sparse, sequence, zigzag

## Problem

The reference writer almost never stores integers as plain `vortex.primitive`. It cascades
lightweight encodings, and the same ones carry dictionary codes, offsets and validity for
every other type. Until they are decoded, no file written with the default settings can be
read.

Upstream paths are in `~/code/other/vortex`. The bit-packing layout is FastLanes: 1024
values per block. Upstream uses the `fastlanes` crate, version 0.8.1 (`Cargo.toml:163`).

## Proposal

One decoder per identifier, registered like the earlier ones. Each decodes into a typed
slice for the whole array, with loops written per bit width or per type where it matters
(no assembly, `CLAUDE.md`).

1. **Patches**, shared (`vortex-array/src/patches.rs:57-70`): metadata `{len, offset,
   indices_ptype, chunk_offsets_len?, chunk_offsets_ptype?, offset_within_chunk?}`, children
   indices and values, then optional chunk offsets. A patch overwrites the value at its
   index. Used by bit-packing, ALP and sparse.
2. `fastlanes.bitpacked` (`encodings/fastlanes/src/bitpacking/plugin/v1.rs`, decode in
   `bitpacking/array/bitpack_decompress/`): metadata `{bit_width, offset (under 1024),
   patches?}`, buffer 0 the packed blocks, children: patches then validity. Every width
   from 0 to 64, for 8, 16, 32 and 64 bit types. Signed types are packed as unsigned.
3. `fastlanes.for` (`for_/plugin/v1.rs`): the metadata is a protobuf `ScalarValue`, the
   reference. Child 0 is the encoded array. Value = encoded + reference, wrapping.
4. `fastlanes.rle` (`rle/vtable/`): metadata `{values_len, indices_len, indices_ptype,
   values_idx_offsets_len, values_idx_offsets_ptype, offset}`, children values, indices,
   values index offsets. Decode in `rle/array/rle_decompress/`.
5. `vortex.zigzag` (`encodings/zigzag/`): child 0 is unsigned, value = `(n >> 1) ^ -(n & 1)`.
6. `vortex.sequence` (`encodings/sequence/src/array/`): metadata `{base, multiplier}`, two
   `ScalarValue`. No buffer. Value i = base + i * multiplier.
7. `vortex.runend` (`vortex-array/src/arrays/runend/`): metadata `{ends_ptype, num_runs,
   offset}`, children ends and values. Any value type, booleans included.
8. `vortex.dict` (`vortex-array/src/arrays/dict/`): metadata `{values_len, codes_ptype,
   is_nullable_codes?, all_values_referenced?}`, children codes and values. Any value type.
   A null code is a null row. The values are decoded once per array, not once per row.
9. `vortex.sparse` (`encodings/sparse/`): metadata `{patches}`, buffer 0 the fill value
   (`ScalarValue`), children patch indices and patch values. Any value type.
10. Every index (code, run end, patch index, offset) is checked against its target before
    use. Run ends must increase. A code past the dictionary is `ErrCorrupt`.
11. Entries leave `pending` in the harness.

## Tests

- The harness: `bitpacked`, `for`, `rle`, `zigzag`, `sequence`, `runend`, `dict`, `sparse`
  and `constant` pass, for every column of each.
- Bit-packing, without a file: for each type width and each bit width from 0 to the type
  width, pack 3,000 seeded values with a plain bit-by-bit packer written in the test, in
  the FastLanes block layout, decode, compare. With and without patches, with an `offset`
  of 0, 1 and 1023, and a length that is not a multiple of 1024.
- Property tests, seeded, for run-end, dictionary, sparse and RLE: build the encoded form
  from random data with a naive encoder in the test, decode, compare.
- Sequence: negative multiplier, overflow wrapping, a nullable one.
- Error paths with hand-built arrays: bit width above the type width, packed buffer too
  short, patch index past the end, decreasing run ends, last run end below the length, a
  code past the dictionary, a reference of the wrong type.
- `testing.AllocsPerRun`: decoding a bit-packed block into a reused buffer allocates
  nothing.

## To verify

- The FastLanes bit layout: the order of values inside a 1024 block is transposed across
  lanes, it is not a plain run of bits. Read the `fastlanes` crate source (crates.io, or
  `~/.cargo/registry` if it is there) before writing the unpacker, and take a few packed
  blocks from the `bitpacked` fixture as the first test vectors.
- `fastlanes.bitpacked.v2`, `fastlanes.for.v2` and `vortex.decimal_byte_parts.v2` exist
  upstream but are in no edition. If a 0.93.0 fixture or dataset file contains one, decode
  it too and say so in the commit message. If none does, leave them out.
- `fastlanes.delta` is in the `unstable` edition: out of scope.
- Whether the `sparse` fill value can be null, and what the dtype of the result is then.
