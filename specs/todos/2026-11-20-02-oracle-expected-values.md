---
model: sonnet
effort: high
pr: batch
automerge: false
---

# The oracle writes the expected values of every fixture, and generates the fixtures upstream lacks

## Problem

The Rust implementation is the judge (`CLAUDE.md`, `docs/testing.md` section 1). The Go
tests need its verdict without running it: `go test ./...` is offline, with no Python and no
Rust. So the expected values of each fixture must be computed once by the reference reader
and committed next to the file.

Upstream also has no fixture for some identifiers of the target edition: at least
`vortex.onpair`, `vortex.masked`, `vortex.uuid`, `vortex.variant` and
`vortex.parquet.variant`.

## Proposal

1. `tools/oracle/oracle.py`, run with `uv run --with vortex-data==<.vortex-version>`. No Go
   file under `tools/`: it is not part of the module. Two commands:
   - `expected <file>...` reads each file with the reference reader and writes
     `<file>.expected.json`;
   - `check <dir>` is for the writer (later spec): leave a stub that exits 2.
2. `<file>.expected.json`:
   - `rows`, and `schema`: the DType as a tree (`kind`, `nullable`, and by kind `ptype`,
     `precision`, `scale`, `fields`, `element`, `size`, `ext_id`, `ext_metadata`);
   - per top-level column, and per nested field as a dotted path: `nulls` and `sha256`, the
     canonical hash;
   - per column: the first 5 and last 5 values as text, to debug a mismatch;
   - `encodings` and `layouts`: the identifiers the reference reader met in this file, when
     its API gives them.
3. The canonical hash is defined once, in `docs/testing.md` section 1. Complete the table
   there for the types it lacks, then implement exactly that:
   - fixed-size list: each element in order, no length;
   - map: a list of structs `{key, value}`;
   - date, time, timestamp: the storage integer. UUID: the 16 bytes, no length;
   - decimal: 16 bytes up to precision 38, 32 bytes above, whatever the storage width;
   - null type: the validity byte only;
   - variant: left for the variant spec, the oracle writes `"sha256": null` for it.
4. `tools/oracle/genfixtures.py` writes, with the reference writer, the fixtures upstream
   lacks into `testdata/fixtures/<version>/extra/`: `onpair.vortex`, `masked.vortex`,
   `uuid.vortex`, `variant.vortex`, plus `empty.vortex` (zero rows) and `wide.vortex` (200
   columns). Small data, fixed seed. They are committed with their `.expected.json`.
5. `make oracle` runs `genfixtures` then `expected` on every file under
   `testdata/fixtures/<version>/`. `make upstream` becomes fixtures, edition, oracle.
6. Run it and commit the JSON files. Never edit one by hand.
7. `fixtures_test.go` at the root: every `*.vortex` under `testdata/fixtures/` has an
   `.expected.json` that parses into the Go struct the later tests will use
   (`internal/testutil`, test-only helpers), with `rows` and a hash for every column.

## Tests

- `fixtures_test.go`: the check of step 7, plus a temp-folder case for a fixture with no
  JSON and one with a JSON that lacks a column.
- `internal/testutil`: the JSON loader, on a valid file and on a truncated one.
- By hand, once, written in the commit message: `make oracle` twice gives byte-identical
  JSON files (no timestamp, sorted keys).

## To verify

- The Python API of `vortex-data` 0.93.0: how to open a file, read it as Arrow, and get the
  DType tree. If it cannot list the encodings met in a file, drop `encodings` and `layouts`
  from the JSON and say so in the commit message.
- Whether the reference writer can be forced to emit `vortex.onpair`, `vortex.masked` and
  the variant encodings from Python. For each one it cannot, do not fake the file: list it
  in `testdata/fixtures/<version>/extra/MISSING.md` with what was tried. The completeness
  spec decides what to do with that list.
- The 37 synthetic fixtures are written by `vortex-test/compat-gen` upstream. Read it to
  learn what each file is meant to hold. Done: see "What the synthetic fixtures hold" in
  `docs/testing.md`. The source defines 40 files, the published manifest lists 37 (no
  `delta`, `patched`, `decimal_byte_parts_v2`), and each file is a multi-column struct.
