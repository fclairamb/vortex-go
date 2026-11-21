# Testing

How we check each goal of [goals.md](goals.md).

Status on 2026-11-20: only the gates of section 9 marked "in place" exist. Everything
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
- 37 are synthetic, each a struct of columns aimed at one type or encoding (`primitives`, `list`, `map`, `alp`, `fsst`,
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
| Decimal | Little-endian two's complement, 16 bytes up to precision 38, 32 bytes above, whatever the storage width |
| Utf8, binary | `uint32` length, then the bytes |
| Struct | Each field in order |
| List | `uint32` length, then each element |
| Fixed-size list | Each element in order, no length |
| Map | A list of structs `{key, value}` |
| Date, time, timestamp | The storage integer (no timezone, no unit) |
| UUID | The 16 bytes, no length |
| Null type | The validity byte only |
| Variant | The canonical JSON of the value (below), hashed like a string: `uint32` length, then the bytes |

Every element of a list, every field of a struct and every entry of a map is a row of its
own type, so it has its validity byte too. A null struct, list or map has no more bytes
after its validity byte. The hash of a nested struct field (`inner.a`) is taken over the
field as flattened: it is null where the parent is null.

**Canonical JSON of a Variant.** A Variant row is rendered as JSON text, then hashed like a
string. Nothing is left to a printer: no space; the keys of an object sorted by their bytes;
an integer is its digits; a float is `%.17g` (17 significant digits), with `.0` added when
that looks like an integer, and `NaN`, `Infinity` and `-Infinity` bare; a string escapes the
quote, the backslash and the control characters (`\u00XX`), nothing else; a null is `null`.
What JSON has no type for is an object with one key: a binary is `{"$binary":"<hex>"}`, a
date, a time of day or a timestamp is `{"$time":"<UTC>"}` (`2006-01-02T15:04:05.999999999Z`,
no trailing zero in the fraction), a decimal is `{"$decimal":"<digits with the point>"}`.
`testutil.CanonicalJSON` is the Go side. The Python side in `tools/oracle/oracle.py` has not
been run: the reference's Python package cannot read a Variant column (`extra/MISSING.md`).

**The oracle.** `make oracle` runs `tools/oracle/genfixtures.py` (the fixtures upstream
lacks, into `testdata/fixtures/<version>/extra/`) then `tools/oracle/oracle.py expected`
on every fixture. `<file>.expected.json` holds `rows`, `schema` (the DType tree, parsed
from the reference's text form of the DType, since its Python API hides map, extension and
element types), `columns` (per column and per nested struct field as a dotted path:
`nulls`, `sha256`, `first` and `last` 5 values as text) and `encodings` (the identifiers
the reference reader met). There is no `layouts`: the reference Python API does not expose
them. Keys are sorted and nothing is time-dependent, so a second run is byte-identical.
Never edit these files by hand. What the reference writer cannot produce is listed in
`testdata/fixtures/<version>/extra/MISSING.md`.

**What the synthetic fixtures hold.** Read from `vortex-test/compat-gen/src/fixtures/arrays/synthetic/`
at tag 0.93.0. A fixture is not "one encoding, one column": each file is a struct of several
columns, built as one flat-layout array (no compressor), and upstream's `expected_encodings`
check asserts the encoding it targets is in the tree. The second group below is named after
an encoding, the first after a canonical type.

| File | Content (upstream description) |
|---|---|
| `booleans` | Mixed true/false, including a nullable column |
| `chunked` | Chunks of variable size with nullable data |
| `datetime` | Timestamps, dates and times in several units |
| `decimal` | Several precisions and scales, nullable variants included |
| `decimal_canonical_widths` | The canonical width of each precision, with the bounds of each |
| `decimal_narrow_storage` | Stored narrower than the canonical width of the precision |
| `decimal_wide_storage` | Stored wider than the canonical width of the precision |
| `decimal_gap_values` | Narrow storage, with values that need the canonical width |
| `decimal_high_scale` | Narrow storage, with a scale near the precision |
| `decimal_null_slots` | Nullable, with nonzero values in the null slots |
| `fixed_size_list` | Fixed-size lists (3-element vectors) |
| `list`, `listview` | Variable-length lists of integers and strings, in both layouts |
| `map` | Sorted and unsorted keys, nullable values, null maps |
| `null` | An all-null `NullArray` next to an integer column |
| `primitives` | u8 to u64, i32, i64, f32, f64, plus a nullable i32 |
| `struct_nested` | A struct holding a struct with primitive and string fields |
| `varbin`, `varbinview` | Strings: empty, unicode, emoji, a nullable column; `varbinview` adds strings over 12 bytes |
| `alp`, `alprd` | Near-integer floats and decimal-like prices (f32, f64); real-valued doubles with small deltas |
| `bitpacked` | Unsigned integers that fit in fewer bits than their type |
| `bytebool` | Booleans for the ByteBool encoding |
| `constant` | Constant int, float, string, bool and null columns |
| `datetimeparts` | Timestamps for the DateTimeParts encoding |
| `decimal_byte_parts` | Fixed-precision decimals for DecimalByteParts |
| `decimal_byte_parts_narrow_msp` | DecimalByteParts whose MSP is narrower or wider than the canonical width |
| `dict` | Low-cardinality strings and integers |
| `for` | Integers clustered around a base (Frame of Reference) |
| `fsst` | Strings with common substrings and prefixes |
| `pco` | Various numeric patterns |
| `rle` | Primitives with long runs |
| `runend` | Variable-length runs |
| `sequence` | Arithmetic sequences (0,1,2... and stepped), nullable included |
| `sparse` | Mostly-null or mostly-default arrays with a few other values |
| `zigzag` | Signed integers of small absolute value |
| `zstd` | Primitive and string arrays compressed with Zstd |

Upstream's source at 0.93.0 defines three more files that the published `v0.93.0` manifest
does not list, so they are not in our corpus: `delta` (monotonically increasing unsigned
integers), `patched` (patches applied on top of an inner array) and `decimal_byte_parts_v2`
(wide decimals split into a most significant part plus 64-bit lower parts). Upstream's
`check` compares decoded arrays, never bytes.

## 2. Completeness

- **The edition table.** The identifiers of the target edition are a Go table, copied
  from upstream (`vortex-edition/src/declarations/core/`). A test fails when one of them
  has no decoder.
- **Seen in a real file.** The decoder records every identifier it meets. A test fails
  when an identifier of the edition is never met across the corpus. So every decoder has
  run on a file written by the reference writer, and a missing fixture is detected.
- **Types.** One test per logical type, nullable and not nullable, alone and nested.
- **Releases.** `make compat` runs the whole corpus of every published release: it fetches
  them into `.cache/fixtures/<version>/` (`scripts/fixtures.sh --all`), writes the expected
  values with the `vortex-data` of that release (`scripts/oracle-all.sh`, the pinned one when
  it cannot be installed, noted in `ORACLE`), then runs `compat_test.go` (build tag `compat`).
  The nightly job runs it. A file that fails is copied, the smallest one, into
  `testdata/fixtures/compat/<version>/`.
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
- **Corrupt files.** Native Go fuzzing (`FuzzOpenScan`) on open plus scan, seeded with
  the fixtures of at most 32 KiB. Three properties: no panic, done within a deadline, no
  allocation above a limit derived from the file size and its rows. A small file may hold
  any number of rows (a constant does), so the fuzzer scans the first 65,536 rows, and
  takes flats of that many rows at most. Every crasher found is committed under
  `testdata/fuzz/`. 30 seconds on each PR, longer every night.
- **Systematic damage.** Each file is truncated at 200 offsets, and each byte of its
  footer, postscript and trailer is flipped. Same three properties, the allocation one on
  every 16th copy (the others run in parallel, which a global allocation count cannot
  measure).
- **I/O faults.** A reader that fails at call N, for every N, and a reader that returns
  short reads. The scan must return an error that wraps the cause. The readers are in
  `internal/testutil`, with one that returns `io.EOF` with the last bytes and one that
  blocks until a context is cancelled.
- **Concurrency.** Everything runs with `-race`. 16 scans at once on one `File` give the
  sequential results; `GOMAXPROCS` of 1, 2 and 8 give the same batches; a cancelled
  context, a `break` or a panic in the loop leave no goroutine; `Close` during a scan is an
  error.
- **Memory.** The peak heap of a scan stays under a fixed bound, on a committed file and on
  2 million rows written by `make bigfile` into `.cache/` (skipped when absent).

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
  decoder must give back the input. `tools/vectors` (Rust, outside the module) writes
  them for bit-packing (with its patches and its best width), FoR, zigzag, RLE, run-end,
  dictionary, sequence, constant and sparse; `make vectors` regenerates them, the tests of
  `internal/encode` read them. ALP and FSST are not covered yet. Pco has no vector of ours:
  its stream is valid Pco, not the crate's, so its tests write then read with our decoder
  (`internal/pco`), and `make interop` has the reference read a forced `pco` file per type.
- **Edition conformance.** Every identifier found in a file we wrote is in the target edition.
- **Size.** On the TPC-H and ClickBench datasets, our file is at most 1.10 times the size
  of the reference writer's. The measured ratio is committed and can only go down.
  `TestSizeRatios` (in `make interop`) reads the rows of the reference's `tpch_lineitem`,
  `tpch_orders` and `clickbench_hits_5k` files from `.cache/fixtures/`, writes them in
  `Default` and `Compact`, and compares the sizes with the reference's files of the same
  mode. The six ratios are in `testdata/size-ratios.json`; `SIZE_RATIOS_UPDATE=1` rewrites
  it after an improvement. The files go to the oracle too, with the hashes of the
  reference's own files: the reference reader must read back the same rows.
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
  - the base branch, compared with `benchstat` and posted on the PR as one comment that each
    run edits (`bench/cmd/benchcomment` writes it). `scripts/bench-pr.sh` interleaves the
    runs of both sides, so the drift of the runner hits both alike, and a change under 5%
    counts as noise,
  - parquet-go on the same data, in a separate module under `bench/` so it stays out of
    our `go.mod`,
  - the Rust reference, measured natively on the same machine by a Cargo project
    (`tools/bench-rust/`, pinned to `.vortex-version`). Rust is needed in `bench.yml` only,
    never in `go test ./...` or `make check`.
- History: `bench.yml` runs the three on every merge to `main` (one runner, 15 minutes at
  most) and appends `bench.json` to `history.jsonl` on the `bench-data` branch. The cases
  are one list, `bench/cases.json`, run with 1 thread and with all cores.

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
| Fixtures and edition table of the pinned release | `go test ./...` | yes | in place |
| Expected values of every fixture exist and parse | `go test ./...` | yes | in place |
| Corpus against the oracle | `go test ./...` | yes | in place |
| Edition table complete and seen | `go test ./...` | yes | in place |
| Allocation | `go test ./...` | yes | in place |
| Examples, API snapshot, README | `go test ./...`, `make api` | yes | in place |
| Reference reads our files, and filters on our zone statistics | `make interop` | yes | in place |
| File size ratio | `make interop` | yes | in place |
| Fuzzing, 30 seconds | `make fuzz` | yes | in place |
| All published releases | `make compat` | nightly | in place |
| Long fuzzing | `make fuzz FUZZTIME=30m` | nightly | in place |
| Benchmarks against main | `make bench-compare` | no, posted on the PR | in place |
| Benchmark history | `bench.yml` | no | in place |
