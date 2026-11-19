# Testing

How we check each goal of [goals.md](goals.md).

Status on 2026-11-19: only the gates of section 9 marked "in place" exist. Everything
else is the plan.

## Principles

- **The reference implementation is the judge.** Expected values always come from the
  Rust implementation (through its Python package, `vortex-data`). Never from our own code.
- **`go test ./...` is offline.** No network, no Python, no Rust. Everything it needs is
  committed in `testdata/`.
- **External tools stay outside the module.** Python runs in two places only: to produce
  expected values, and to check the files our writer produces. It is a CI tool, not a
  dependency.
- **Every gate is a `make` target.** What CI runs is what runs locally.

## 1. The corpus

Upstream publishes test files for every release in a public bucket,
`https://vortex-compat-fixtures.s3.amazonaws.com` (checked on 2026-11-19):

- `versions.json` lists 28 releases, from 0.64.0 to 0.93.0.
- `v0.93.0/arrays/manifest.json` lists 43 files with their SHA-256.
- 37 are synthetic, one per type or encoding (`primitives`, `list`, `map`, `alp`, `fsst`,
  `pco`, `zstd`, `sparse`...). They weigh 1 MB in total.
- 6 are datasets: TPC-H `lineitem` and `orders`, ClickBench `hits`, each written in
  "regular" and "compact" mode. They weigh 4.9 MB in total.

What we do with it:

- The 37 synthetic files of the pinned release are committed in `testdata/fixtures/<version>/`.
- The datasets and the older releases are downloaded by `make fixtures` into `.cache/`,
  and checked against the SHA-256 of the manifest.
- Upstream has no file for some identifiers of the target edition (at least `onpair`,
  `masked`, `uuid` and `variant`). We generate those with the reference writer. The
  script is committed in `tools/`.

**Expected values.** `tools/oracle` (Python, pinned `vortex-data`) reads each file with
the reference reader and writes `<file>.expected.json` next to it:

- the schema and the row count,
- per column: the null count and a canonical SHA-256,
- the first and last values, to debug a mismatch.

These JSON files are committed. The Go tests only read them.

**Canonical hash.** One definition, implemented twice (Go and the oracle). For each row
of a column: one validity byte, then the value if it is valid.

| Type | Bytes |
|---|---|
| Integers, floats | Little-endian, raw bits (NaN payloads and `-0` are kept) |
| Bool | One byte |
| Decimal | Little-endian two's complement, 16 or 32 bytes |
| Utf8, binary | `uint32` length, then the bytes |
| Struct | Each field in order |
| List | `uint32` length, then each element |

## 2. Completeness

- **The edition table.** The identifiers of the target edition are a Go table, copied
  from upstream (`vortex-edition/src/declarations/core/`). A test fails when one of them
  has no decoder.
- **Seen in a real file.** The decoder records every identifier it meets. A test fails
  when an identifier of the edition is never met across the corpus. So every decoder has
  run on a file written by the reference writer, and a missing fixture is detected.
- **Types.** One test per logical type, nullable and not nullable, alone and nested.
- **Releases.** `make compat` runs the whole corpus of every published release.
- **Writer.** The same table says which encodings the writer emits.
- **The matrix.** `go generate` writes the support matrix of the README from the table.
  CI fails when it is stale.
- **Upstream changes.** `.vortex-version` pins the upstream release. It selects the
  fixtures folder and the `vortex-data` version of the oracle. Renovate opens a PR on
  each release. A test fails when `testdata/fixtures/<version>/` is missing for the
  pinned version, or when the edition table was generated from another version. So a
  version bump alone is red. This test is written first, before any decoder.
  `make upstream` brings both in. See [upstream-update.md](upstream-update.md).

## 3. Reader validity

- **Against the reference.** For every file and every column, our canonical hash equals
  the oracle's. Checked for a scan of all columns and for each column alone.
- **Scan semantics.** Random queries (filter, row range, limit, column subset) on the
  corpus. The result must equal a plain Go loop over the fully decoded data. The seed is
  printed on failure.
- **Batch boundaries.** Batch sizes of 1, 7, 1024, and larger than a chunk.
- **Corrupt files.** Native Go fuzzing (`testing.F`) on open plus full scan, seeded with
  the whole corpus. Three properties: no panic, no hang, no allocation above a limit
  derived from the file size. Every crasher found is committed. 30 seconds on each PR,
  longer every night.
- **Systematic damage.** Each file is truncated at many offsets, and each byte of its
  footer is flipped. Same three properties.
- **I/O faults.** A reader that fails at call N, for every N, and a reader that returns
  short reads. The scan must return an error that wraps the cause.
- **Concurrency.** Everything runs with `-race`. One test runs many scans on one `File`.

## 4. Writer validity

- **Round trip in Go.** Generated data, written then read back, must be equal. It covers
  every type and the hard cases: empty, all null, constant, sorted, NaN, `-0`, integer
  limits, empty and long strings, deep nesting. Seeded.
- **The reference reads our files.** A Go test writes files, and the canonical hashes of
  the source data, into a folder. The oracle opens them with the reference reader and
  compares. Run against the pinned `vortex-data` and against the latest release.
- **Byte-exact encoders.** Some encoders are deterministic: bit-packing, FoR, zigzag, RLE,
  run-end, dictionary, sequence, ALP with fixed exponents, FSST with a fixed symbol
  table. For these, the Rust code produces vectors (input, parameters, output buffers),
  committed in `testdata/vectors/`. Our encoder must produce the same bytes, and our
  decoder must give back the input.
- **Edition conformance.** Every identifier found in a file we wrote is in the target edition.
- **Size.** On the TPC-H and ClickBench datasets, our file is at most 1.10 times the size
  of the reference writer's. The measured ratio is committed and can only go down.
- **Write faults.** An `io.Writer` that fails at call N, for every N. `Write` or `Close`
  returns the error.

We do not compare whole files byte by byte. The format leaves layout, segment order and
encoding choice to the writer, and upstream's own compatibility check
(`vortex-test/compat-gen`) compares decoded arrays, never bytes.

## 5. Speed and allocation

**Allocation is a blocking test.** It is deterministic, so it runs in `go test` with
`testing.AllocsPerRun`:

- 0 allocation per batch in a running scan.
- 0 allocation per row in `Rows[T]` with fixed-width and `[]byte` fields.
- 0 allocation per `Write` call in a running writer.
- Peak heap during the scan of a large generated file stays under a fixed bound.

**Speed is measured, not gated**, until we have a machine stable enough to gate on.

- Benchmarks are `go test -bench` with `-benchmem`.
- Data: the TPC-H and ClickBench files, and a generated file of 10 million rows (seeded
  Go generator).
- Cases: open, full scan, 3 columns out of 16, a filter that keeps 1%, a row range, the
  row API, a write, and one decode and encode benchmark per encoding.
- Numbers: rows per second, MB per second of decoded data, bytes and allocations per
  operation, file size.
- Baselines:
  - the previous commit, compared with `benchstat` and posted on the PR,
  - parquet-go on the same data, in a separate module under `bench/` so it stays out of
    our `go.mod`,
  - the Rust reference through Python, for information.

## 6. Ease of use

- **Examples are tests.** `Example` functions with an `// Output:` block for: read rows,
  read columns, filter, write rows, write columns.
- **The README cannot drift.** Its code block is copied from the example file by
  `go generate`. CI fails when it is stale.
- **Line budget.** The "read into structs" example is 10 lines or fewer, error handling
  included. A test counts.
- **API snapshot.** `api.txt` is the output of `go doc -all`. CI fails when it differs, so
  every change of the public API is a visible diff.
- **Error messages.** Tests assert the text for the common mistakes: unknown column, type
  mismatch between a struct field and a column, not a Vortex file, unsupported encoding.
- **Doc comments.** The linter requires one on every exported identifier.

## 7. Coverage

- **The gate.** `make cover` runs every package with `-coverpkg=./...` and fails when one
  statement block was never executed. It prints the blocks.
- **No exclusion list.**
- **Offline.** The gate only uses committed files.
- **Codecov shows it on the PR.** CI uploads `coverage.txt` after the gate. The upload
  never fails the build: `make cover` stays the blocking gate.
- **How error paths get covered.** By the corrupt files and the I/O faults of section 3
  and the write faults of section 4. An error branch that no file and no I/O fault can
  trigger is dead code: delete it.
- **Panics for programmer errors** (a per-row accessor such as `Bool(i)` on a column of
  another type) are tested with `recover`. `Values[T]` on the wrong type is an error, not
  a panic.

## 8. Self-containment

- `make nocgo` fails when this module, or anything it imports, has a cgo file.
- `make deps` fails when the build needs a module other than `github.com/klauspost/compress`.
- Tests run with `CGO_ENABLED=0`, except the race detector, which needs the cgo toolchain
  on Linux.
- **`unsafe` stays in its package.** A test fails when another package imports it.
- **The `unsafe` row fill is checked against `reflect`.** For every field type, a test
  fills the same rows both ways and compares them. The `reflect` fill only lives in the
  test.

## 9. Gates

| Gate | Command | Blocking | Status |
|---|---|---|---|
| No cgo | `make nocgo` | yes | in place |
| No module beyond klauspost/compress | `make deps` | yes | in place |
| 100% coverage | `make cover` | yes | in place |
| Race detector | `make race` | yes | in place |
| Lint | `make lint` | yes | in place |
| Fixtures and edition table of the pinned release | `go test ./...` | yes | planned, first |
| Corpus against the oracle | `go test ./...` | yes | planned |
| Edition table complete and seen | `go test ./...` | yes | planned |
| Allocation | `go test ./...` | yes | planned |
| Examples, API snapshot, README | `go test ./...`, `make api` | yes | planned |
| Reference reads our files | `make interop` | yes | planned |
| File size ratio | `make interop` | yes | planned |
| Fuzzing, 30 seconds | `make fuzz` | yes | planned |
| All published releases | `make compat` | nightly | planned |
| Long fuzzing | `make fuzz FUZZTIME=30m` | nightly | planned |
| Benchmarks against main | `make bench` | no, posted on the PR | planned |
