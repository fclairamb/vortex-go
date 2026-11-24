# tools/vectors

Writes `testdata/vectors/<encoding>/<type>.json`: what the encoders of the reference
implementation (the `vortex` crates at the pinned tag, `=0.94.0`) make of an input, so that
our encoders can be checked byte for byte. It is a Rust program outside the Go module: `cargo`
is needed to regenerate the vectors, never to run the tests.

```bash
cd tools/vectors
cargo run --release -- ../../testdata/vectors
```

Per encoding (`bitpacked`, `for`, `zigzag`, `rle`, `runend`, `dict`, `sequence`, `sparse`,
`constant`, `alp`, `alprd`, `fsst`, `onpair`) and per type, a file of vectors, one per line:

- `input`: the values, little-endian hex (`strings`: one hex string per row, for the
  dictionary of strings and binary); `valid`: the validity bitmap, least significant bit
  first, absent when no row is null; `params`: the width of a bit-packing, `best` for the
  width picked from the histogram (`histogram` is then the one the reference counted);
- `output`: the array the reference makes, as the node it serializes to: `id`, `metadata`,
  `buffers` and `children` (the same four, recursively), all hex; `null` when the reference
  declines the input (a sequence that is not one, a bit-packing that is not worth it).

The inputs come from a seeded generator, so a run gives the same files. Sparse inputs are
chosen so that the most frequent value is unique: the reference picks among equal counts in
hash order. `go test ./internal/encode` reads the files (`TestVectorsBytes`,
`TestVectorsDecode`).

The vectors of floats and strings are `alp` (the exponents given in `params` as `alp_e` and
`alp_f`, or the ones the search picks, `best`), `alprd` (the search, or a cut given as
`right_bits` and `dict`) and `fsst` (the table the reference trains, which is in the output:
the Go test encodes the strings with it and with its own training, and both must give the
same bytes). `onpair` is the same for vortex-onpair (`DEFAULT_CONFIG`): the Go test gives the dictionary of the
reference to `OnPairBytesWith` and every byte must be the reference's. Our own training is not
compared byte for byte: its sample is not the crate's (the random generator is not ported), so it
makes the reference's dictionary on some vectors only (`TestOnPairTraining` logs how many).
`fsst/default_mixed.json` and `onpair/default_mixed.json` are not vectors: it holds the sizes of what the reference's FSST makes of
the string columns of the `default_mixed` fixture, for the compression ratio test of the root
package (`TestFSSTCompressionRatio`, `TestOnPairCompressionRatio`). Its strings reach the program in a file: `MIXED_STRINGS_OUT=/tmp/s.json
go test -run TestFSSTCompressionRatio .` writes it, then `MIXED_STRINGS=/tmp/s.json cargo run
--release -- ../../testdata/vectors` makes the sizes. Zstd has no vectors: the bytes of a frame depend on the compressor, and the
reference reader reading ours is what `make interop` checks. Bump the version in `Cargo.toml` with `.vortex-version`.
