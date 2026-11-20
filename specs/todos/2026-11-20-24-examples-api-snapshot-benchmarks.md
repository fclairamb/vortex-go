---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# The examples, the README, the API snapshot and the benchmarks are generated and checked

## Problem

`docs/goals.md` goal 1 has four "done when" lines that nothing checks yet: a README example
of 10 lines or fewer that runs as a test, an `Example` function per use case, a doc comment
on every exported identifier, and a committed snapshot of the API. Goal 4 asks for published
speed numbers. `README.md` still says "design only".

## Proposal

1. `example_test.go`: one `Example` function with an `// Output:` block per use case of
   `docs/testing.md` section 6: read rows, read columns, filter, write rows, write columns.
   Earlier specs added some: complete the list, do not duplicate.
2. `go generate ./...` (a small generator under `internal/gen`, standard library only,
   covered by tests like the rest):
   - copies the body of `ExampleRows` into the code block of `README.md`, between two
     marker comments;
   - writes the support matrix of `README.md` from `internal/edition` and the decoder and
     encoder registries: one row per identifier, "read" and "written";
   - writes `api.txt` from `go doc -all`. `make api` runs it.
   CI fails when any of the three is stale: `make api` then `git diff --exit-code`.
3. A test counts the lines of the README example: 10 or fewer, error handling included.
4. `README.md`: remove "design only", add install, the example, the matrix, the releases
   read (from the compat spec) and the limits (`docs/goals.md`, non-goals).
5. `docs/interface.md`: its header says `api.txt` becomes the source of truth once the code
   exists. Remove the "full surface" block and keep the reasons.
6. `.golangci.yml`: turn on the rule that requires a doc comment on every exported
   identifier (`revive`, `exported`), and fix what it reports.
7. Benchmarks, `bench_test.go`, `go test -bench . -benchmem`:
   - data: a seeded Go generator (10 million rows, written once to `.cache/`), and the TPC-H
     and ClickBench files when `.cache/fixtures/` has them. Benchmarks skip without data.
   - cases: open, full scan, 3 columns out of 16, a filter that keeps 1%, a row range, the
     row API, a column write, a row write, and one decode and one encode per encoding.
   - metrics: rows per second and MB per second of decoded data (`b.ReportMetric`).
   - `make bench` runs them. `make bench-compare` runs `benchstat` against `main`.
8. `bench/` is a separate Go module (its own `go.mod`, a `replace` to the parent) with the
   parquet-go comparison on the same data. It stays out of the root `go.mod`, out of
   `make deps` and out of the coverage gate.
9. `docs/benchmarks.md`: the numbers measured on one named machine, with the date, the
   commit and the ratio to parquet-go. The ratio to the Rust reference (through Python, in
   `tools/oracle`) is published "whatever it is" (`docs/goals.md` goal 4).
10. `.github/workflows/build.yml`: a non-blocking job posts the `benchstat` output on the
    PR. `docs/testing.md` section 9: the last rows become "in place".

## Tests

- `go test ./...` runs every `Example`.
- The generator: each of its three outputs, on a temp tree, fresh and stale.
- The line-count test of step 3, with a case of 11 lines that fails.
- Error messages, asserted as text, for the common mistakes of `docs/testing.md` section 6:
  unknown column, struct field against a column of another type, not a Vortex file,
  unsupported encoding.
- `make check`, `make api` and `git diff --exit-code` in CI.

## To verify

- Whether `benchstat` can be run with `go run golang.org/x/perf/cmd/benchstat@latest` in CI
  without adding a module to the root `go.mod`. It must not.
- That `go doc -all` output is stable between Go 1.26 and 1.27. If not, generate `api.txt`
  with 1.27 only and say so in `make api`.

## Resolved open questions

- Use `github.com/parquet-go/parquet-go`, in `bench/go.mod` only. (autopilot: recommended answer)

## Open questions

- **Which parquet-go.** Recommended answer: `github.com/parquet-go/parquet-go`, the one
  `docs/goals.md` names as the familiar API. It lives in `bench/go.mod` only.
