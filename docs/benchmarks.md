# Benchmarks

Speed is measured, not gated (see [testing.md](testing.md) section 5). The numbers below are
from one machine, on one day. They are not a promise: rerun them with `make bench`.

| | |
|---|---|
| Machine | Apple M2 Pro, 10 cores, macOS 27.2.1, arm64 |
| Go | 1.27.4 |
| Date | 2026-11-20 |
| Commit | `0da1cd1` and the benchmark files of the change that added this page |
| Data | `.cache/bench/gen16-10000000.vortex`: 16 columns, 10 million rows, seed 20261120, 551 MB |
| Reference | vortex-data 0.93.0 (Rust, through Python), `tools/oracle/bench.py` |
| parquet-go | `github.com/parquet-go/parquet-go` v0.32.1, default options, in `bench/` |

The data has 6 integer columns (a sequence, a timestamp, a random one, two narrow ones, a
full-range one), 3 float columns, 2 booleans, 3 string columns (8 cities, 5000 names, unique
notes) and 2 nullable columns. The TPC-H and ClickBench files of `.cache/fixtures/` are also
scanned when they are there.

## What runs

- `make bench`: `go test -bench . -benchmem ./...`, the benchmarks of the root package
  (`bench_test.go`) and of the encoders (`internal/encode/bench_test.go`). They skip without
  their data. The first run writes the generated file into `.cache/bench/`.
- `make bench-compare`: the same, on `main` and on the tree, then `benchstat`. CI posts it on
  the PR (non-blocking).
- `make bench-parquet`: the comparison with parquet-go, in the separate module `bench/`. It
  reads the generated file, writes its Parquet copy once, and runs the same cases on both.
- `make bench-rust`: the reference reader through Python.

`rows/s` counts the rows of the file read (the rows the query keeps for a filter), and
`decoded-MB/s` the width of the numbers and the length of the strings that come out.

## Reading, 10 million rows

| Case | Time | rows/s | decoded MB/s | allocs/op |
|---|---|---|---|---|
| Open | 0.77 ms | | | 5,060 |
| Full scan, 16 columns | 934 ms | 10.7 M | 1,175 | 36,347 |
| 3 columns out of 16 | 154 ms | 65.0 M | 1,348 | 3,055 |
| Filter that keeps 1% | 266 ms | | | 3,398 |
| Row range, 10% of the rows | 115 ms | 8.7 M | 957 | 4,183 |
| Row API, 4 fields | 724 ms | 13.8 M | 584 | 20.0 M |

The row API allocates a string per row for the two string fields, which is where its 20
million allocations come from. Fixed-width and `[]byte` fields allocate nothing per row
(`TestNoAllocation`).

## Writing

| Case | Time | rows/s | allocs/op |
|---|---|---|---|
| Columns, 16 columns, 1 Mi rows | 1,748 ms | 0.6 M | 617,309 |
| Rows, 4 fields, 1 Mi rows | 309 ms | 3.4 M | 6,356 |

## Ratio to parquet-go

Same data, same machine, `bench/`. A ratio above 1 means vortex-go is faster. parquet-go has
no row-level filter: it reads the three columns of every row and filters in Go.

| Case | vortex-go | parquet-go | Ratio |
|---|---|---|---|
| Full scan into structs, 16 columns | 2.29 s | 7.65 s | 3.3 |
| 3 columns out of 16 | 0.47 s | 4.51 s | 9.7 |
| Filter that keeps 1% | 0.17 s | 5.55 s | 33 |
| Write 1 Mi rows of 3 fields | 150 ms | 36 ms | 0.24 |
| File size | 551 MB | 1,161 MB | 0.47 |

The goal "scan and write at least as fast as parquet-go" is met for reading and not for
writing: parquet-go writes rows 4 times faster in this case, and writes an uncompressed file
twice our size. It is not met, and it is published as it is.

## Ratio to the Rust reference

Through Python, `to_arrow_table()` on each case, best of 5. A ratio above 1 means vortex-go is
slower. These are published whatever they are (`docs/goals.md`, goal 4).

| Case | vortex-go | Rust | Ratio |
|---|---|---|---|
| Open | 0.77 ms | 0.18 ms | 4.2 |
| Full scan | 934 ms | 509 ms | 1.8 |
| 3 columns out of 16 | 154 ms | 20.8 ms | 7.4 |
| Filter that keeps 1% | 266 ms | 13.0 ms | 20 |

## One encoder and one decoder per encoding

`BenchmarkEncode/<encoding>` runs one encoder on 65,536 values, into the buffers of an
encoder that has already done the same work: 0 allocation each. `BenchmarkDecode/<encoding>`
scans the fixture of the encoding (`testdata/fixtures/`), which is a few thousand rows, so it
measures the fixed cost of a scan as much as the decoder. Run them to see all of them; a few,
on this machine:

| Encoding | Encode MB/s | Decode MB/s |
|---|---|---|
| bitpacked | 1,350 | 301 |
| frame of reference | 955 | 244 |
| run-end | 2,049 | 192 |
| dictionary | 332 | 114 |
| ALP | 511 | 312 |
| FSST | 414 | 298 |
| zstd | 203 | 122 |
