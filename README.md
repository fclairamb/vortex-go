# vortex-go

[![Build](https://img.shields.io/github/actions/workflow/status/fclairamb/vortex-go/build.yml?branch=main&label=build&logo=github)](https://github.com/fclairamb/vortex-go/actions/workflows/build.yml)
[![Coverage](https://codecov.io/gh/fclairamb/vortex-go/graph/badge.svg)](https://codecov.io/gh/fclairamb/vortex-go)
[![Go Reference](https://pkg.go.dev/badge/github.com/fclairamb/vortex-go.svg)](https://pkg.go.dev/github.com/fclairamb/vortex-go)
[![Go Report Card](https://goreportcard.com/badge/github.com/fclairamb/vortex-go)](https://goreportcard.com/report/github.com/fclairamb/vortex-go)
[![Go](https://img.shields.io/github/go-mod/go-version/fclairamb/vortex-go?logo=go)](go.mod)
[![License](https://img.shields.io/github/license/fclairamb/vortex-go?color=blue)](LICENSE)

A pure Go reader and writer for the [Vortex](https://vortex.dev) columnar file format.

The site, with how to use it and how it compares with the Rust implementation: <https://fclairamb.github.io/vortex-go/>.

## Goals

Ranked. The details and the "done when" lists are in [docs/goals.md](docs/goals.md).

1. **Easy to use.** A file to Go structs, or Go structs to a file, in a few lines and no configuration.
2. **Correct.** The Rust implementation is the judge, for what we read and for what we write.
3. **Complete.** Every encoding, layout and type of the upstream `core` edition.
4. **Fast, without allocation.** A running scan or writer allocates nothing.
5. **Self-contained.** No cgo. The standard library, plus `klauspost/compress` for zstd.
6. **100% statement coverage.** No exclusion.

## Install

```bash
go get github.com/fclairamb/vortex-go
```

Go 1.26 or later. The only dependency is `klauspost/compress`, for zstd.

## Example

Read the rows of a file into structs. This block is copied from `ExampleRows` in
[example_test.go](example_test.go) by `go generate`, and a test keeps it at 10 lines or fewer.

<!-- example:begin -->
```go
f, err := vortex.OpenFile("testdata/fixtures/0.93.0/extra/default_ints.vortex")
if err != nil {
    log.Fatal(err)
}
defer f.Close()
type Row struct {
    Sorted sql.Null[int64] `vortex:"sorted"`
}
for r, err := range vortex.Rows[Row](context.Background(), f, vortex.Query{Rows: vortex.Range{End: 3}}) {
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(r.Sorted.V)
}
```
<!-- example:end -->

Columns, filters, and writing (`NewWriter` for columns, `NewRowWriter` for structs) have an
`Example` function each. The API is in [api.txt](api.txt), the reasons for its shape in
[docs/interface.md](docs/interface.md).

## Releases read

The files of every published release are read, and checked against the reference reader.

<!-- compat:begin -->
Reads the fixtures of every published release, 28 of them, from 0.64.0 to 0.93.0, checked every night against the reference reader.
<!-- compat:end -->

## Support matrix

What the target edition of the pinned release contains, and what this package reads and
writes. Generated from the decoder and encoder registries, never written by hand.

<!-- matrix:begin -->
| Kind | Identifier | Read | Written |
|---|---|---|---|
| aggregate | `vortex.bounded_max` | yes | yes |
| aggregate | `vortex.bounded_min` | yes | yes |
| aggregate | `vortex.max` | yes | yes |
| aggregate | `vortex.min` | yes | yes |
| aggregate | `vortex.nan_count` | yes | yes |
| aggregate | `vortex.null_count` | yes | yes |
| array | `fastlanes.bitpacked` | yes | yes |
| array | `fastlanes.for` | yes | yes |
| array | `fastlanes.rle` | yes | yes |
| array | `vortex.alp` | yes | yes |
| array | `vortex.alprd` | yes | yes |
| array | `vortex.bool` | yes | yes |
| array | `vortex.bytebool` | yes | no |
| array | `vortex.chunked` | yes | no |
| array | `vortex.constant` | yes | yes |
| array | `vortex.datetimeparts` | yes | yes |
| array | `vortex.decimal` | yes | yes |
| array | `vortex.decimal_byte_parts` | yes | yes |
| array | `vortex.dict` | yes | yes |
| array | `vortex.ext` | yes | yes |
| array | `vortex.fixed_size_list` | yes | yes |
| array | `vortex.fsst` | yes | yes |
| array | `vortex.list` | yes | yes |
| array | `vortex.listview` | yes | yes |
| array | `vortex.map` | yes | yes |
| array | `vortex.masked` | yes | yes |
| array | `vortex.null` | yes | yes |
| array | `vortex.onpair` | yes | no |
| array | `vortex.parquet.variant` | yes | no |
| array | `vortex.pco` | yes | no |
| array | `vortex.primitive` | yes | yes |
| array | `vortex.runend` | yes | yes |
| array | `vortex.sequence` | yes | yes |
| array | `vortex.sparse` | yes | yes |
| array | `vortex.struct` | yes | yes |
| array | `vortex.varbin` | yes | yes |
| array | `vortex.varbinview` | yes | yes |
| array | `vortex.variant` | yes | no |
| array | `vortex.zigzag` | yes | yes |
| array | `vortex.zstd` | yes | yes |
| dtype | `vortex.date` | yes | yes |
| dtype | `vortex.time` | yes | yes |
| dtype | `vortex.timestamp` | yes | yes |
| dtype | `vortex.uuid` | yes | yes |
| layout | `vortex.chunked` | yes | yes |
| layout | `vortex.dict` | yes | yes |
| layout | `vortex.flat` | yes | yes |
| layout | `vortex.stats` | yes | yes |
| layout | `vortex.struct` | yes | yes |
| layout | `vortex.zoned` | yes | yes |
<!-- matrix:end -->

Some identifiers are read and never written, each for a reason: see the writer section of
[docs/goals.md](docs/goals.md#3-complete).

## Speed

[docs/benchmarks.md](docs/benchmarks.md) says how the numbers are made. They are recorded on
every merge to `main`, with the Rust reference measured on the same machine, in the
`bench-data` branch. `make bench` runs the Go benchmarks, `make bench-json` all three harnesses.

## Limits

- No query engine: projection and filters only, no joins, no aggregations.
- No Arrow in the core module, no `preview` or `unstable` edition, no third-party encodings.
- Our files are not byte-identical to the Rust writer's: the format leaves the choice of
  encodings to the writer. They are at most 10% larger on TPC-H and ClickBench.
- No Pco and no OnPair encoder: both are read, neither is written.

See the non-goals in [docs/goals.md](docs/goals.md#non-goals).

## How it is tested

[docs/testing.md](docs/testing.md) says how each goal is checked: the corpus of files
written by the reference implementation, the oracle that gives the expected values, the
check of our writer by the reference reader, the allocation tests and the gates.

```bash
make check
```

## Following upstream

`.vortex-version` pins the upstream release we follow. Renovate opens a PR on each new
release. See [docs/upstream-update.md](docs/upstream-update.md).

## License

MIT, see [LICENSE](LICENSE).
