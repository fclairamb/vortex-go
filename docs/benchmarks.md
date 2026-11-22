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
| vortex-go | `bench_cases_test.go` (root module), `BenchmarkCases/<case>`, and `BenchmarkCases/<case>/mapped` for a case that reads | `go test -bench` |
| bench/ | `bench/bench_test.go` (module `bench/`), `BenchmarkCases/<case>/vortex-go`, `/vortex-go-mapped` and `/parquet-go` | the cases parquet-go has (full scan, 3 columns, filter): the same rows, in a Vortex file and in a Parquet file, read into the same structs with each library |
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

Every case is run with 1 thread and with all cores: one `go test -cpu 1`, then one
`go test -cpu <cores>`, for Go, a Tokio runtime with that many workers and a scan concurrency
of that many for Rust. Not one `-cpu 1,<cores>`: the sub-benchmarks of `BenchmarkCases` then
run once before `-cpu` is applied, on every core, and that run counted as a run at 1 thread. One thread shows the
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
 "cases": [{"name", "threads", "go", "go_mapped", "rust", "go_rows", "go_rows_mapped", "parquet_go",
            "ratio_to_rust", "ratio_to_rust_mapped", "ratio_to_parquet_go",
            "ratio_to_parquet_go_mapped"}]}
```

`go`, `rust`, `go_rows` and `parquet_go` hold `ns_per_op`, `rows_per_s` and `mb_per_s`, the
median of the runs (5, 3 for the ones of `bench/`), not the mean. `rust` is absent for a case
that harness does not run. `ratio_to_rust` is the Go time over the Rust time: above 1, vortex-go
is slower.

`go_rows` and `parquet_go` are the two harnesses of `bench/`, for the cases it runs: vortex-go
and parquet-go reading the same rows into the same structs. `go` is not the number to put next
to `parquet_go`: it is a scan of batches, with no struct. `ratio_to_parquet_go` is `go_rows`
over `parquet_go`. `make bench-json` fails when `bench/` gives no parquet-go result.

Every case that reads is measured twice by vortex-go: on a file `OpenFile` opened, and on a
file `OpenMapped` mapped in memory. `go_mapped` and `go_rows_mapped` are the second, with
their ratios `ratio_to_rust_mapped` and `ratio_to_parquet_go_mapped`. A case that writes has
none. The Rust harness (`open_path`) and parquet-go read the file: they have one time, which
both of ours are compared with. The history of the ratio to Rust is the one of `OpenFile`.

The website has a page for each comparison, `rust.html` and `parquet.html`, filled by
`bench/cmd/benchsite` from the same `bench.json`.

`benchstat` on a pull request (`make bench-compare`) compares one commit with `main`. The
history is for what it cannot see: ten small slow-downs that each pass. On a pull request,
CI runs every benchmark on all the cores of the runner, with the TPC-H and ClickBench files
of the pinned release (`make fixtures`), so the cases of the small files are compared too.

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

## Finding where the time goes

Two traps cost a morning on 2026-11-21, on a Mac:

- **A CPU profile on macOS blames the system calls.** `go tool pprof` showed 40 to 70% of a
  scan in `pread`, where a clock around the read gave 2%. Read the profile for the share of
  one Go function against another, never for the share of the reads. For the split between
  the reads and the decoding, put a clock around them, or read `getrusage` (user and system
  time) around a scan.
- **A loaded machine moves every number, Rust's too.** Take the best of several runs
  (`-count 5`), run the two harnesses one after the other, and compare one column at a time: a
  copy of `bench/cases.json` whose `project` is one column gives the time of that column in
  both languages, which says which decoder to look at.

What the reader does to stay close to the reference, so that a change does not undo it:

- the buffers of the decoded arrays come from a pool of the scan (`internal/array/arena.go`)
  and go back to it when the batch that reads them is done: a scan works in the same memory
  from one batch to the next, and a batch is valid until the next one;
- a segment is read to the same alignment it has in the file, and the decoders read and write
  numbers where they are (`unsafex.Slice`), with no copy to a slice of bytes and back;
- FoR, zigzag and ALP write over the integers their child just unpacked; a FoR over
  bit-packed integers adds its reference as each block is unpacked, as the reference reader
  does, 8 bytes at a time: a value is under the high bit of its lane, so the low bits of the
  reference add to all the lanes of the word with no carry between them;
- the rows of a dictionary layout are made a batch at a time, from the codes of the batch;
- a comparison of an integer column with one literal is a test of range on the values of the
  batch (`expr_fast.go`), not a walk of the filter per row: the value minus the low end of the
  range, as an unsigned integer of its width, against the width of the range, one comparison
  and no branch;
- FSST decodes the codes of all the rows in one pass, 8 codes at a time, as the reference does:
  a word of 8 codes with no escape is 8 stores with no test in between (`unsafex.Store64`).
  The rows are the declared lengths, their sum is checked, and the rows are decoded one by
  one only to say which one is wrong. A code with no symbol is found for the 8 codes of a word
  at once, from the carry out of each byte of the word plus 256 minus the symbols. The ranges
  of the rows are made in one pass over the lengths and the offsets of the codes, where their
  children decoded them, which checks them too, in the sign of one or, with no branch per row;
- a FastLanes block is unpacked 8 bytes at a time, which is 8, 4 or 2 lanes for words of 8, 16
  or 32 bits: one shift and one mask for all of them (`unpackBlock`);
- the validity of the rows of a dictionary is made a byte at a time, with no test per row;
  with at most 256 values, no more than rows, the bit of each value is first put in a byte of
  its own, and a row is one load (`gatherValidity`);
- the codes of a dictionary are checked against its values once, 4 at a time with no branch,
  before the rows are made, and not at all when they come unpacked from fewer bits than the
  values need (`Array.bound`): the rows are then made with no clamp of the code;
- the rows of a dictionary of strings of at most 256 values, no more than rows, read the start
  and the end of their value in one lookup, from a table of pairs made for the batch;
- the buffers of a pool are in classes a quarter of a power of two apart, not powers of two: a
  scan of a file of few rows, whose last batch is never given back, allocates at most a
  quarter more than it decodes, not up to twice;
- runs of numbers fill their rows run by run (`fillRuns`), a take of numbers copies a word per
  row, not a slice, and a constant number is a doubling copy: no copy call per row;
- a node of the tree of an array allocates nothing for its children (`fbs.List` is a value),
  the nodes of a decode come 16 at a time, and their buffer indices are in the node: the kept
  rows of a sparse filter walk the tree again at every batch.

What the writer does with its memory, for the same reason:

- the compressors of a closed writer go to a pool (`wcompress.go`), and the next writer takes
  them with their memory;
- an `Encoder` keeps the blocks of its arena from one `Reset` to the next, so the same calls
  take no new memory, and the first block is small: a writer has two encoders per column;
- the memory of the FSST and OnPair trainings, of the Pco coder and of the tables of a
  dictionary is taken from a pool for one call and given back at its end, so that the
  columns share as many as there are calls at once;
- `allocDirty` gives a buffer the encoder writes whole, with no clearing first. `alloc` clears
  only what an earlier use wrote: the rest of a block is still zero. In the tests of
  `internal/encode` a new block is full of a pattern, so that a byte read and never written
  makes other bytes than the reference.

What the writer does to choose the encodings of strings with no more work than the reference:

- a dictionary of strings is not tried when more than half of the valid rows have distinct
  heads, the length and the first 4 bytes, as the reference's `StringDictScheme` does: a
  column of free text is not made a dictionary on the sample, with its values compressed, only
  to lose;
- the probe of the dictionary layout stops as soon as it knows: it applies the best scheme of
  the sample only when it is the dictionary, and compresses the values and the codes of that
  dictionary only when the dictionary does not already beat the plain array without them;
- the training of FSST takes the best candidates from a heap, as the crate does, not from a
  sort of all of them: a round keeps 255 symbols out of thousands of candidates.

What the writer does to choose the encodings of numbers with no more work than the reference:

- in a trial (the sample, and the children of its candidates), integers are not tried as a
  dictionary when more than half of their valid values are distinct, nor as a sparse array
  when their most frequent value is under nine tenths of them, and floats are not tried as a
  dictionary when more than half are distinct, as the estimates of the reference rule them
  out (`IntDictScheme`, `SparseScheme`, `FloatDictScheme`). The count stops as soon as it
  knows, and its table is taken from a pool and grows with the values: a column of unique
  values costs half of its rows, not a table of all of them in each compressor;
- a scheme is never twice in a chain of parents, and the exclusion rules of the reference
  leave out more under some children (no dictionary, run-end nor sparse under a zigzag, no
  dictionary, run-end, RLE nor sparse on the ends of a run-end, no dictionary, frame of
  reference, zigzag nor sequence on the codes of a dictionary, and so on: `excl*` in
  `internal/compress`). The trees of the trials were most of the work of the writer on
  ClickBench;
- the values of a frame of reference are bit-packed, with no other candidate, as the reference
  does (`FoRScheme`): a frame of reference and a dictionary of its values was a dictionary
  with its values framed, tried a second time;
- the encoder of the trials only measures: bit-packing gives buffers of the right sizes and
  does not write them (`Encoder.Measure`). A trial only reads the sizes of its arrays;
- the probe of the dictionary layout keeps the scheme its sample chose, with the number and a
  hash of its rows: when the first chunk is the rows of the probe, it is not sampled again.
  The hash holds no reference to the rows, so the probe does not keep a buffer alive.
