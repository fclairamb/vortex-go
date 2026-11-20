---
model: sonnet
effort: high
pr: batch
automerge: false
---

# The benchmark numbers are recorded on every merge to `main`, next to the Rust reference's measured on the same machine

## Not before the writer is merged

Florent, 2026-11-20: "Do not start implementation until #10 is merged". This spec and the
two after it (26, 27) start only when the writer pull request (#10) is merged into `main`.
While it is open, stop here and report: write no code, and leave this branch as it is.

## Problem

After the examples and benchmarks spec, `make bench` exists and `docs/benchmarks.md` holds
numbers measured once, by hand, on one machine. Two things are missing (Florent,
2026-11-20: "add some go benchmarks to keep track of the performances" and "how it
compares, especially in terms of performance, to the rust version"):

- **No history.** `benchstat` on a PR compares one commit with `main`. A slow-down that
  lands in ten small steps is never seen.
- **No fair Rust number.** `docs/testing.md` section 5 measures "the Rust reference through
  Python, for information". That number includes the conversion to Arrow and to Python
  objects. It is not the speed of the Rust reader.

`docs/goals.md` goal 4: "The ratio to the Rust reference is published, whatever it is."

## Proposal

1. **One list of cases**, `bench/cases.json`: a name, a data file and an operation. The
   three harnesses (ours, parquet-go, Rust) run the same list on the same files.
   - data: TPC-H `lineitem`, ClickBench `hits`, and the generated file of 10 million rows;
   - operations: open, full scan, 3 columns out of 16, a filter that keeps 1%, a row
     range, a write in each of the three compression modes.
2. `bench_test.go` (root module): the cases of the earlier spec are the base. Add the ones
   the list needs and it lacks (a write per mode, per data file) and name the
   sub-benchmarks after the case names, so a result is matched by name.
3. `tools/bench-rust/`: a small Cargo project. The `vortex` crate is pinned to the release
   of `.vortex-version`, built in release mode. It runs the cases of `bench/cases.json`
   and prints one JSON line per case: `{"case", "threads", "ns_per_op", "rows_per_s",
   "mb_per_s"}`. Each case is run with 1 thread and with all cores, and so are the Go
   ones (`GOMAXPROCS`).
4. `bench/cmd/benchjson` (in the `bench/` module, standard library only): reads the
   `go test -bench` output of the root module, the output of the parquet-go benchmarks and
   the Rust lines, and writes `bench.json`:
   `{commit, date, vortex_version, go_version, rust_version, machine: {os, cpu, cores},
   cases: [{name, threads, go, rust, parquet_go, ratio_to_rust}]}`. The median of the runs
   is kept, not the mean.
5. `make bench-rust` builds the Rust harness. `make bench-json` runs the three and writes
   `bench.json`. Neither is in `make check`: `go test ./...` stays offline, with no Rust.
6. `.github/workflows/bench.yml`, on push to `main` and on `workflow_dispatch`. One job on
   one runner, so the Go and the Rust numbers come from the same machine:
   - fetch the fixtures (cached like `nightly.yml` does), build the Rust harness (cargo
     registry and `target/` in `actions/cache`), run `make bench-json`;
   - append `bench.json` as one line to `history.jsonl` on the branch `bench-data`, an
     orphan branch that holds only `history.jsonl` and the latest `bench.json`, and push it;
   - upload `bench.json` as an artifact.
   On a pull request that touches `bench/`, `tools/bench-rust/`, `bench_test.go` or the
   workflow, the same job runs without the push. It is never a required check.
7. **The Rust pin follows `.vortex-version`.** `.github/renovate.json`: the custom manager
   also matches the `vortex` line of `tools/bench-rust/Cargo.toml`, so both move in one
   PR. `upstream_test.go` fails when the two differ. `docs/upstream-update.md`: one line.
8. `docs/benchmarks.md`: the hand-made table goes. It says how the numbers are made, where
   the history is and how to run it locally. `docs/testing.md` section 5: "the Rust
   reference through Python" becomes the Rust harness, and section 9 gets the row
   "Benchmark history | `bench.yml` | no | in place". The Python measure is removed from
   `tools/oracle` if the earlier spec added one.

**Budget.** The repository is private: its Actions minutes are counted. One run per merge
to `main`, 15 minutes at most (`timeout-minutes: 15`), no `schedule` trigger. The measured
duration of a run goes in the PR description.

## Tests

- `bench/cmd/benchjson`, on committed sample outputs: the three sources merged by case
  name, the median of 5 runs, a case with no Rust line (the `rust` field is empty, no
  error), a case of `bench/cases.json` with no Go result (error), a malformed line (error).
- `upstream_test.go`: the Cargo pin equals `.vortex-version`. Offline: it reads two files.
- A test that every case of `bench/cases.json` names a data file and an operation the Go
  benchmarks know.
- The workflow, on the PR: one green run, with `bench.json` in its artifacts and a Rust
  number and a Go number for every case.
- `make check` and `make deps` still pass: nothing new in the root `go.mod`.

## To verify

- The Rust API at tag `0.93.0` for open, scan with projection and filter, row range and
  write (`vortex-file`). `~/code/other/vortex` is a clone of `main`, not of the tag.
- The build time of the Rust harness on the runner, cold and with the cache.
- That the workflow's `GITHUB_TOKEN` with `contents: write` can push to `bench-data`. The
  ruleset protects `main` only.
- The run-to-run spread on the hosted runner: run the workflow 3 times and write the
  spread in `docs/benchmarks.md`. Above 10%, raise the run count before anything else.
- That the Rust harness and the Go benchmarks read the file the same way (both from disk,
  both with a warm page cache), so the comparison is fair.

## Open questions

- **Rust measured natively, with a Cargo project in this repository.** Recommended answer:
  yes. It is the only fair number. Rust is needed in `bench.yml` only, never in
  `go test ./...` or `make check`.
- **Where the history lives.** Recommended answer: the `bench-data` branch. `main` needs
  a pull request for every commit, and a results commit per merge would be noise.
- **Threads.** Recommended answer: publish both, 1 thread and all cores. One thread shows
  the decoders, all cores shows the scan pipeline.
