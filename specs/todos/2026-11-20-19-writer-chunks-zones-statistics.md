---
model: sonnet
effort: high
pr: batch
automerge: false
---

# The writer cuts columns into chunks and writes the statistics that let a reader skip them

## Problem

The first writer emits one flat segment per column and per `Write` call, with no
statistics. A reader, ours or the reference one, must then decode everything: no zone can
be skipped, and the size of a chunk depends on how the caller batches. The reference
writer has a fixed pipeline for this (`vortex-file/src/strategy.rs:218-327` upstream).

Upstream paths are in `~/code/other/vortex`.

## Proposal

1. **Chunking**, independent of the caller's `Write` sizes:
   - rows are buffered per column and cut at multiples of the row block size, 8,192 rows
     (`strategy.rs:141`);
   - a data chunk targets 1 MiB of uncompressed data, rounded to whole row blocks
     (`strategy.rs:36,142`);
   - the buffered data of a column is bounded (upstream: 2 MiB, `strategy.rs:228`), so
     memory depends on the column count and not on the file size;
   - the layout of a column is `vortex.chunked` over `vortex.flat`, a single chunk
     unwrapped.
2. **Zone statistics.** Each column is wrapped in `vortex.zoned`
   (`vortex-layout/src/layouts/zoned/`): child 0 the data, child 1 the zones table, one
   row per 8,192 rows. Metadata: version byte 1, then the protobuf `{zone_len,
   aggregate_specs}` (`zoned/mod.rs:519-561`). Statistics, as upstream chooses them
   (`zoned/writer.rs:316-348`):
   - strings and binary: `vortex.bounded_max` and `vortex.bounded_min`, bounds truncated
     to 64 bytes, options the `u64` LE 64. A truncated max is rounded up so it stays a
     bound;
   - other types: `vortex.max` and `vortex.min`, NaN skipped;
   - floats: `vortex.nan_count`. Every type: `vortex.null_count`;
   - no zone statistics under a nullable struct (`zoned/writer.rs:127-132`);
   - the zones table is a struct array written as a flat layout, with the field names and
     types upstream uses (`zoned/schema.rs:92-103`).
3. **Array statistics**: each flat array carries `ArrayStats` on its root node (min, max,
   null count, `is_constant`, `is_sorted` when cheap), with string bounds truncated to 64
   bytes and marked inexact (`vortex-layout/src/layouts/flat/writer.rs:105-141`).
4. **File statistics**: the `statistics` segment of the postscript, per top-level field,
   in the form the pinned release writes (`vortex-file/src/footer/file_statistics.rs`,
   `vortex-layout/src/layouts/file_stats.rs:371-392`).
5. **Dictionary layout**, `vortex.dict` (`vortex-layout/src/layouts/dict/writer.rs`): for
   primitive, string and binary columns, when a probe of the first 8,192 rows shows low
   cardinality. Values in child 0, codes in child 1, metadata `{codes_ptype, ...}`.
   Upstream's limits: at most 65,535 values and 1 MiB of values per dictionary, a new
   dictionary run when one is passed. Until the compressor spec, "low cardinality" is a
   fixed rule: distinct values under 50% of the probe.
6. Our reader's pruning (the query spec) must work on our own files: it is the first
   check that the statistics are right.

## Tests

- Round trip in Go on the data of the writer spec, plus: 100,000 rows written 1 row per
  `Write`, and in a single `Write`. Both give the same layout tree (chunk row counts).
- The layout of a written file, asserted: `struct` over `zoned` over `chunked` or `dict`.
- Statistics against a plain Go computation, per zone: min, max, null count, NaN count,
  for every type. Strings longer than 64 bytes: the stored max is at least the true max.
  A zone that is all null. A float zone with only NaN.
- Pruning on our files: a filter keeping 1% of a sorted column reads fewer segments than
  a full scan, and returns exact results.
- `make interop`: the reference reads these files, and a filter pushed to the reference
  reader (`tools/oracle check` gains a filter case) returns the same rows as ours. This
  is what proves the statistics: a wrong bound makes the reference skip a zone it needs.
- Dictionary: a column of 10 distinct strings uses `vortex.dict`, a column of unique
  strings does not, a column whose cardinality rises after the probe starts a new run.
- Peak heap while writing 5 million rows of 4 columns stays under a fixed bound.
- Write faults at call N, for every N, as in the writer spec.

## To verify

- The exact names and types of the zones table fields in a file written by 0.93.0, read
  from `extra/default_ints.vortex` with our own reader.
- Whether 0.93.0 writes the legacy `field_stats` or the newer aggregate form in
  `FileStatistics`, and whether the reference reader needs the segment at all.
- The rule that rounds a truncated max up (and what happens when the 64 bytes are all
  `0xFF`): `vortex-array/src/aggregate_fn/fns/bounded_max/mod.rs`.
