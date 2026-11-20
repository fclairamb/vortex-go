# tools/vectors

Writes `testdata/vectors/<encoding>/<type>.json`: what the encoders of the reference
implementation (the `vortex` crates at the pinned tag, `=0.93.0`) make of an input, so that
our encoders can be checked byte for byte. It is a Rust program outside the Go module: `cargo`
is needed to regenerate the vectors, never to run the tests.

```bash
cd tools/vectors
cargo run --release -- ../../testdata/vectors
```

Per encoding (`bitpacked`, `for`, `zigzag`, `rle`, `runend`, `dict`, `sequence`, `sparse`,
`constant`, `alp`, `alprd`, `fsst`) and per type, a file of vectors, one per line:

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
same bytes). Zstd has no vectors: the bytes of a frame depend on the compressor, and the
reference reader reading ours is what `make interop` checks. Bump the version in `Cargo.toml` with `.vortex-version`.
