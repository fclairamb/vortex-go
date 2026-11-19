---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# The fixtures and the edition table of the pinned release are in the repo, and a test proves it

## Problem

`.vortex-version` pins upstream release `0.93.0`, but nothing in the repo depends on it yet.
Every later spec needs two things from that release: the test files, and the list of
identifiers a reader must decode. A Renovate PR that only bumps `.vortex-version` must be red
until both follow (`docs/testing.md` section 2, "Upstream changes", and
`docs/upstream-update.md`). This spec is the first gate of `docs/testing.md` section 9.

## Proposal

1. `scripts/fixtures.sh [version]` (bash, `curl`, `shasum -a 256`; the version defaults to
   `.vortex-version`):
   - downloads `https://vortex-compat-fixtures.s3.amazonaws.com/v<version>/arrays/manifest.json`
     and every file it lists, and checks each SHA-256 against the manifest;
   - the synthetic files go to `testdata/fixtures/<version>/` with `manifest.json`. For
     0.93.0 that is 37 files, 1 MB. They are committed;
   - the dataset files (names starting with `tpch_` or `clickbench_`, 6 files, 4.9 MB) go to
     `.cache/fixtures/<version>/`. `.cache/` is added to `.gitignore`.
   - `make fixtures` runs it. `make fixtures VERSION=0.92.0` fetches another release into
     `.cache/` only.
2. `scripts/edition.sh [version]` writes `internal/edition/table.go` from upstream's
   `vortex-edition/src/declarations/core/*.rs` at tag `<version>` (fetched from
   `https://raw.githubusercontent.com/vortex-data/vortex/<version>/...`, or read from a local
   clone given as `VORTEX_SRC`). The generated file holds:
   - `Version = "0.93.0"` and `ID = "core 2026.08.3"`, the newest `core` edition whose
     `min_library_version` is at most the pinned release;
   - one `Member{Kind, ID, Since}` per identifier, every older `core` edition included
     (members are inherited). Kinds: `array`, `layout`, `dtype`, `aggregate`.
   - a `// Code generated ... DO NOT EDIT.` header.
   Keep the parser dumb: one `EditionMember::<kind>(&"<id>")` per line.
3. `internal/edition/edition.go`: `Members() []Member` and `Has(kind, id) bool`.
4. `make upstream` runs `fixtures` then `edition` (the oracle joins it in the next spec).
5. The gate, `upstream_test.go` at the root, reads `.vortex-version` and fails when:
   - `testdata/fixtures/<version>/manifest.json` is missing;
   - a file of the manifest that is not a dataset is missing, or its SHA-256 differs;
   - `edition.Version` is not the pinned version.
   The messages say what to run: `make upstream`.
6. `docs/testing.md` section 9: the row "Fixtures and edition table of the pinned release"
   becomes "in place".

## Tests

- `upstream_test.go`: the gate above passes on the committed tree.
- `upstream_test.go`: the check function is given a temp folder. Cases: no folder, a file
  missing, a file with a flipped byte, a version that differs. Each one fails with the
  message that names `make upstream`.
- `internal/edition`: `Has("array", "fastlanes.bitpacked")` is true, `Has("array",
  "fastlanes.delta")` is false, `Has("layout", "vortex.zoned")` is true.
- `internal/edition`: the count per kind matches the table below.
- `make check` stays green, and `go test ./...` runs with the network off.

## To verify

- At upstream `main` on 2026-11-18 the `core` family has 50 members: 34 arrays, 6 layouts
  (`vortex.chunked`, `vortex.dict`, `vortex.flat`, `vortex.stats`, `vortex.struct`,
  `vortex.zoned`), 4 dtypes (`vortex.date`, `vortex.time`, `vortex.timestamp`,
  `vortex.uuid`) and 6 aggregates (`vortex.min`, `vortex.max`, `vortex.bounded_min`,
  `vortex.bounded_max`, `vortex.null_count`, `vortex.nan_count`). Check the counts at tag
  `0.93.0` and use what the tag says.
- The default edition of the writer is in `vortex/src/editions/mod.rs`. Check that it is
  `core 2026.08.3` at tag `0.93.0`. If the rule "newest core edition the release supports" is
  wrong, read the default from that file instead.
- `~/code/other/vortex` is a shallow clone of `main`, not of the tag. Fetch the tag before
  reading it (`git fetch --depth 1 origin tag 0.93.0`).
