---
model: sonnet
effort: high
pr: batch
automerge: false
---

# Write the `vortex.stats` layout: `WriteOptions.Edition` makes a file that older readers open

## Problem

The layout `vortex.stats` is read and never written. It is the older form of
`vortex.zoned` (`zoned.go:107-118`, `layout.go:58-59`): readers up to Vortex 0.83 only
know it (`~/code/other/vortex/vortex-layout/src/layouts/zoned/legacy_builder.rs:1-6`).

The reference writes it in one case: the write targets an edition that has no
`vortex.zoned` (`vortex-layout/src/layouts/zoned/writer.rs:134-141`, `write_legacy_stream`
at `:240`). Its writer takes the edition as an option (`with_edition` in the Python API).

Our `WriteOptions` only has `Compression` (`api.txt:625-627`). Every file targets the
newest edition, `core 2026.08.3` (`internal/edition/table.go:9`), and a reader older than
0.84 cannot open it.

## Proposal

1. `WriteOptions.Edition string`: the edition the file must be readable with, as
   `internal/edition` names them (`"core 2025.05.0"`). Empty is the newest. An unknown
   edition is an error of the first `Write` or `Close`, like a bad schema.
2. `internal/edition`: `Allows(edition, kind, id) bool`, from the `Since` of each member
   (`table.go:11-62`). Editions are ordered by their date.
3. The writer only emits what the edition allows:
   - layout `vortex.zoned` (since `core 2026.08.0`) becomes `vortex.stats`: metadata the
     zone length as `u32` little-endian then the bitset of the statistics present
     (`zoned.go:107-113`), and the legacy zone table, one column per statistic in the
     order of the upstream `Stat`, a `max` or `min` followed by its `_is_truncated`
     column (`legacy_builder.rs:3-6`). `wcol.go:161` (`writeZones`), `wstats.go:350-444`.
   - the aggregates (since `core 2026.08.0`) are not written: the file statistics are the
     legacy ones only (`wfstats.go:308`, `:344`).
   - `vortex.listview` (since `core 2025.10.0`) becomes `vortex.list` (spec 30).
   - the compressor drops the candidates the edition does not have: `fastlanes.rle` and
     `vortex.masked` (`core 2025.10.0`), `vortex.sequence` and `vortex.zstd`
     (`core 2025.06.0`).
   - a type the edition cannot hold is an error that names the type and the edition:
     `fixed_size_list` before `core 2025.10.0`, `map` before `core 2026.08.2`, `uuid`
     before `core 2026.08.3`.
4. `docs/interface.md` (the writer options), `api.txt`, an `Example`. `docs/goals.md`
   goal 3: the editions a file can target. Register `vortex.stats` in `writtenLayouts`
   (`wstats.go:451`). `go generate`.

## Tests

- For each edition of the table: every type the edition holds is written and read back
  by our reader, and every identifier of the file has `Since` at or before the edition.
- `Edition: "core 2025.05.0"`: the file has `vortex.stats` and no `vortex.zoned`, and
  `Scan` with a filter skips the same zones as with the newest edition
  (`writer_zones_test.go` cases, run for both).
- Negative: an unknown edition, a `uuid` column for `core 2025.05.0`, a
  `fixed_size_list` for `core 2025.06.0`: an error, and nothing valid is left behind.
- `make interop`: the files of `core 2025.05.0` are read by `vortex-data` 0.64.0 and
  0.83.0 (`uv run --with vortex-data==<version>`, as `scripts/oracle-all.sh:13-15`), and
  by the pinned one. The files of the newest edition are still read by the pinned one.
- `TestWrittenMatchesFiles` (spec 28) meets the layout `vortex.stats`.
- 100% coverage: the legacy table has no branch that only an old reader reaches.

## To verify

- The list of editions and what each holds: `~/code/other/vortex/vortex-edition/src/declarations/core/`
  (`v2025_05.rs` to `v2026_08_3.rs`). The `Since` column of our table comes from it.
- Which release first reads each edition, so that the interop run picks a release that
  must succeed. `legacy_builder.rs:3` gives 0.83 as the last one before `vortex.zoned`.
- Whether older readers need anything else from the footer: the file format version, the
  legacy `field_stats` (`~/code/other/vortex/vortex-file/src/writer.rs:175-190`).
- How the reference fails for a type the edition cannot hold (`validate_dtype_editions`,
  `vortex-file/src/writer.rs:303-305`): the same cases must fail here.

## Open questions

- **The option.** Recommended answer: `WriteOptions.Edition string`, empty for the
  newest. One field, the names of the upstream editions, nothing else to learn. The
  alternative is a constant per edition (`vortex.Core2025_05`), which `api.txt` then has
  to grow at each upstream release.
