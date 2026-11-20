---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# `make compat` reads the fixtures of every published release, and a nightly job runs it

## Problem

Goal 3 of `docs/goals.md`: "every upstream release that publishes fixtures is read. Today
that is 28 releases, from 0.64.0 to 0.93.0". `go test ./...` only knows the pinned release,
and the 6 dataset files (TPC-H, ClickBench) are not committed. Older releases use
identifiers and footer versions the current writer no longer emits (`vortex.stats`, for
one).

## Proposal

1. `scripts/fixtures.sh --all` reads
   `https://vortex-compat-fixtures.s3.amazonaws.com/versions.json` and fetches every
   release into `.cache/fixtures/<version>/`, with the SHA-256 check of each manifest.
   Files already present with the right hash are not downloaded again.
2. `make oracle-all` runs `tools/oracle` on each release with the `vortex-data` version of
   that release, and writes the `.expected.json` files next to the cached files. When a
   release cannot be installed, fall back to the pinned version and record it in
   `.cache/fixtures/<version>/ORACLE`.
3. `compat_test.go`, behind the build tag `compat`: for every file under `.cache/fixtures/`,
   open, scan every column, compare each canonical hash with the oracle's. One subtest per
   release and file. No `.cache/`: the test fails with "run make fixtures".
4. `make compat`: `fixtures --all`, `oracle-all`, then `go test -tags compat -run Compat
   ./...`. It is not part of `make check`.
5. Fix what it finds. Each fix comes with the smallest fixture that shows it, copied from
   the cache into `testdata/fixtures/compat/<version>/` with its JSON, so `go test ./...`
   keeps covering the code offline. The 6 dataset files of the pinned release are checked
   by `make compat` too.
6. `.github/workflows/nightly.yml`: `make compat` and `make fuzz FUZZTIME=30m`, on a
   schedule and on `workflow_dispatch`. It caches `.cache/` on the hash of `versions.json`.
7. The README gets a line with the range of releases read, generated from the list that
   passed (`go generate`, checked by CI like the support matrix).
8. `docs/testing.md` section 9: "All published releases" and "Long fuzzing" become "in
   place".

## Tests

- `make compat` is green on the 28 releases. The commit message gives the count of files
  and columns compared.
- For every fix of step 5, the copied fixture fails before the fix and passes after.
- `go test ./...` still runs with the network off and without `.cache/`.

## To verify

- Whether every release from 0.64.0 has the same manifest format and the same bucket
  layout (`v<version>/arrays/manifest.json`).
- Whether `vortex-data` wheels exist on PyPI for each of the 28 releases on macOS arm64 and
  Linux x86_64.
- The file-format version field of old releases, and what upstream's reader does with it:
  `vortex-file/src/` (search for the version constant).

## Open questions

- **A release that the pure Go reader cannot read without a large amount of code for a
  format nobody writes anymore.** Recommended answer: read it anyway. Goal 3 says every
  release, and upstream promises that a frozen edition stays readable forever. If one
  release needs more than the rest together, stop and report it instead of dropping it.

## Resolved open questions

- **A release that needs a large decoder for a dead format:** read it anyway. If one release needs more than the rest together, stop and report it instead of dropping it. (autopilot: recommended answer)
