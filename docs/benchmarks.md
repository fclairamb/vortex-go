# Benchmarks

Speed is measured, not gated (see [testing.md](testing.md) section 5). There is no table of
numbers here: a table made by hand on one machine, on one day, is not a measure. The numbers
are recorded on every merge to `main`, with the Rust reference measured on the same machine,
and the ratio to it is published whatever it is (`docs/goals.md`, goal 4).

## What is measured

One list of cases, [`bench/cases.json`](../bench/cases.json), run by three harnesses on the
same files:

| Harness | Where | Runs |
|---|---|---|
| vortex-go | `bench_cases_test.go` (root module), `BenchmarkCases/<case>` | `go test -bench` |
| parquet-go | `bench/bench_test.go` (module `bench/`), `BenchmarkCases/<case>/parquet-go` | the cases parquet-go has: full scan, 3 columns, filter |
| Rust | `tools/bench-rust/`, a Cargo project, the `vortex` crate pinned to `.vortex-version` | release build, native |

The data, a case is `<data>/<operation>`:

- `gen16`: 16 columns, 10 million rows, generated from a seed (`bench_test.go`), written once
  into `.cache/bench/`;
- `tpch_lineitem`: the TPC-H `lineitem` file of the pinned release (60,175 rows);
- `clickbench_hits`: the ClickBench `hits` file of the pinned release (5,000 rows).

The operations: `open`, `scan_full`, `scan_3col` (3 columns of the table), `scan_filter` (a
filter that keeps about 1% of the rows, on the 3 columns), `scan_range` (10% of the rows, from
the middle), and `write_default`, `write_none`, `write_compact`, which write the first 1,048,576
rows of the file again, decoded once outside the timed loop, with each compression mode. For
Rust, `none` is a compressor with no scheme and `compact` is the compact preset of BtrBlocks.

Every case is run with 1 thread and with all cores: `-cpu 1,<cores>` for Go, a Tokio runtime
with that many workers and a scan concurrency of that many for Rust. One thread shows the
decoders, all cores show the scan pipeline. Each harness reads the file from disk with a warm
page cache (one warm-up run for Rust, the file written by the first benchmark for Go). The Rust
scans decode every chunk to canonical arrays, like the Go scans decode every batch: a scan that
only builds the compressed arrays would not be the same work. `mb_per_s` is the decoded size
of the values, counted by each harness its own way: compare times, not MB/s, across languages.

## Where the numbers are

`.github/workflows/bench.yml` runs `make bench-json` on one runner, on every push to `main`
and on `workflow_dispatch`, so that Go and Rust come from the same machine. It appends one line
to `history.jsonl` on the `bench-data` branch (an orphan branch with `history.jsonl` and the
latest `bench.json`), and uploads `bench.json` as an artifact. On a pull request that touches
the benchmarks it runs without the push. It is never a required check. The job is capped at 15
minutes and has no schedule.

A line is:

```json
{"commit", "date", "vortex_version", "go_version", "rust_version",
 "machine": {"os", "cpu", "cores"},
 "cases": [{"name", "threads", "go", "rust", "parquet_go", "ratio_to_rust"}]}
```

`go`, `rust` and `parquet_go` hold `ns_per_op`, `rows_per_s` and `mb_per_s`, the median of
the runs (5, 3 for parquet-go), not the mean. `rust` and `parquet_go` are absent for a case
that harness does not run. `ratio_to_rust` is the Go time over the Rust time: above 1, vortex-go
is slower.

`benchstat` on a pull request (`make bench-compare`) compares one commit with `main`. The
history is for what it cannot see: ten small slow-downs that each pass.

## Run it locally

```bash
make bench          # the Go benchmarks, with their allocations
make bench-compare  # this tree against main, with benchstat
make bench-rust     # builds the Rust harness (cargo, network, a few minutes the first time)
make bench-json     # the three harnesses, merged into bench.json (about 10 minutes)
```

`make bench-rust` and `make bench-json` are not part of `make check`: `go test ./...` stays
offline, with no Rust. `VORTEX_BENCH_ROWS` lowers the size of the generated file. `bench.json`
is made by `bench/cmd/benchjson`, which reads the three outputs and matches them by case name.

When the Rust crate moves, `tools/bench-rust/Cargo.toml` and `.vortex-version` move together
(Renovate does it in one PR, `TestBenchRustPinFollowsVortexVersion` fails when only one did).

## One encoder and one decoder per encoding

`BenchmarkEncode/<encoding>` runs one encoder on 65,536 values, into the buffers of an
encoder that has already done the same work: 0 allocation each. `BenchmarkDecode/<encoding>`
scans the fixture of the encoding (`testdata/fixtures/`), which is a few thousand rows, so it
measures the fixed cost of a scan as much as the decoder. Run `make bench` to see all of them.

## The run-to-run spread

The spread on the hosted runner is not measured yet: it needs the workflow to have run 3 times
on `main`. Until it is written here, read a difference under 10% as noise. Above 10% between
two runs of the same commit, raise the run count (`BENCH_RUNS`) before anything else.
