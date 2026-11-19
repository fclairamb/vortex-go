---
model: sonnet
effort: high
pr: batch
automerge: false
---

# The two string encodings are decoded: FSST and OnPair

## Problem

The reference writer compresses most string columns with `vortex.fsst`, and since edition
`core 2026.08.1` it may use `vortex.onpair`. Neither is decoded, so the string columns of
any real file are unreadable.

Upstream paths are in `~/code/other/vortex/encodings/`.

## Proposal

1. `vortex.fsst` (`fsst/src/`, decode in `canonical.rs`, layout in
   `array/plugin.rs:108-190`):
   - metadata `{uncompressed_lengths_ptype, codes_offsets_ptype}`;
   - buffer 0 the symbols (up to 255, 8 bytes each, little-endian `u64`), buffer 1 the
     length of each symbol (1 to 8), buffer 2 the code bytes;
   - children: uncompressed lengths, code offsets (rows + 1), validity;
   - decode: each code byte is a symbol index, and 255 is the escape: the next byte is a
     literal. The output of a row is exactly its uncompressed length;
   - an older form with 2 buffers exists (`plugin.rs`): decode it too, the compat spec
     will meet it.
2. `vortex.onpair` (`onpair/src/`, decode in `decode.rs` and `canonical.rs`):
   - metadata at `array/mod.rs:78-100`, buffer 0 the dictionary bytes;
   - children: dictionary offsets, codes, code offsets, uncompressed lengths, validity;
   - decode: each code is a dictionary entry, copied to the output.
3. Both decode a whole array into one byte buffer plus offsets, owned by the scan and
   reused between batches. The inner loop writes 8 bytes at a time when the output has
   room, and falls back to a byte loop near the end. No `unsafe` outside
   `internal/unsafex`.
4. Hostile input: a symbol length above 8, a code past the symbol table, an escape as the
   last byte, a row that decodes to more or fewer bytes than its declared length, a
   dictionary offset past the dictionary. All are `ErrCorrupt`, and the output buffer is
   sized from the declared lengths after they are checked against a limit derived from
   the file size.
5. Entries leave `pending`: `fsst`, `extra/onpair`, and the string columns of
   `default_mixed`.

## Tests

- The harness: `fsst` and `extra/onpair` pass, and the string columns of `default_mixed`.
- FSST without a file: a symbol table built by hand in the test, strings encoded by a
  naive encoder in the test (longest match, escapes for the rest), decoded, compared.
  Cases: empty string, a string of escapes only, symbols of every length from 1 to 8, a
  string ending exactly at the buffer end (the 8-byte fast path must not write past it).
- OnPair: the same approach, with a hand-built dictionary.
- The hostile cases of step 4, one test each.
- `testing.AllocsPerRun`: decoding into a reused buffer allocates nothing.
- Batch sizes 1, 7 and 1024 on `fsst`.

## To verify

- The exact FSST symbol and escape conventions of the `fsst-rs` crate (0.7.0,
  `Cargo.toml:167` upstream): escape code value, symbol byte order, and whether symbol
  lengths are stored per symbol or implied.
- The OnPair format: `encodings/onpair/src/` is the only reference, read `decode.rs` in
  full. The metadata uses tags 1, 3, 4, 5, 6 and 7: find what each one is.
- If the oracle spec could not produce an `onpair` fixture (see
  `testdata/fixtures/<version>/extra/MISSING.md`), build the test input from the bytes of
  upstream's own unit tests in `encodings/onpair/src/`, and say so in the commit message.
