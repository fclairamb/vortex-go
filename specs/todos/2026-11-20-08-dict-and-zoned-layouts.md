---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Files written with the default strategy are read: the dictionary layout and the zoned layout

## Problem

The 37 synthetic fixtures are written with a bare flat layout
(`vortex-test/compat-gen/src/adapter.rs:53-61` upstream). A real file is not: the default
write strategy gives, per column, `zoned(data: chunked or dict or flat, zones: flat)`
(`vortex-file/src/strategy.rs:218-327`). Three layout identifiers of the edition are not
read yet: `vortex.zoned`, `vortex.dict` and the older `vortex.stats`. Without them the
TPC-H and ClickBench files, and any file a user has, fail at the first column.

Upstream paths are in `~/code/other/vortex/vortex-layout/src/layouts/`.

## Proposal

1. **Fixtures first.** No committed file has these layouts. `tools/oracle/genfixtures.py`
   gains files written by the reference writer with its default settings, into
   `testdata/fixtures/<version>/extra/`, each well under 200 KB:
   - `default_ints.vortex`: 30,000 rows, 4 integer columns (sorted, low cardinality,
     random, nullable). More than one zone and more than one chunk;
   - `default_mixed.vortex`: the same plus floats, strings (low and high cardinality),
     booleans, decimals, a timestamp, a nullable struct and a list;
   - `default_compact.vortex`: `default_mixed` written in compact mode.
   Run `make oracle` and commit them with their `.expected.json`. Columns that need a
   decoder of a later spec go to `pending`.
2. `vortex.zoned` (`zoned/mod.rs:105-171`): two children, the data (same type) and the
   zones table. Metadata: a version byte (1), then a protobuf `{zone_len: uint32,
   aggregate_specs: [{id: string, options: bytes}]}` (`zoned/mod.rs:519-561`,
   `zoned/schema.rs:28-34`). Reading the column is reading child 0. Parse and keep the
   metadata and the zones child: the query spec uses them to skip zones. An unknown
   aggregate id is not an error: that statistic is ignored.
3. `vortex.stats` (`zoned/mod.rs:193`, metadata at `:564-600`): same children. Metadata:
   `zone_len` as `u32` LE, then a bitset of the statistics present. Kept for the same use.
4. `vortex.dict` (`dict/mod.rs:60-116`): child 0 is the values (the column's type), child
   1 the codes. Metadata is a protobuf `{codes_ptype, is_nullable_codes?,
   all_values_referenced?}` (`dict/mod.rs:183-191`). The values are read once per layout
   and reused for every batch of codes. A code past the values is `ErrCorrupt`.
5. Layout readers get a row range, so a batch never decodes a whole column: a chunked
   layout reads only the chunks that overlap, and a dictionary layout reads its values
   once and the codes of the range.
6. A layout identifier outside the edition (`vortex.list`, `vortex.indexed`) keeps giving
   the "unsupported layout" error that names it.

## Tests

- The harness: `default_ints` passes. `default_mixed` and `default_compact` pass for the
  columns whose encodings are decoded, the others are in `pending` with their identifier.
- The layouts met in `default_ints` include `vortex.zoned`, `vortex.dict`, `vortex.chunked`
  and `vortex.flat`: assert it, so a change of the reference writer that drops one is seen.
- Zoned metadata: parsed `zone_len` is 8,192 for the generated files, and the aggregate
  ids are the six of the edition.
- A counting reader: reading rows 0 to 100 of `default_ints` reads fewer segments than a
  full scan, and the dictionary values of a column are read once for a full scan.
- Hand-built layouts for the error paths: zoned with one child, a bad version byte, dict
  with one child, a code past the values, row counts that do not add up.
- A `vortex.stats` layout: hand-built here from a `default_ints` column (same children,
  the older metadata). The compat spec reads real ones from older releases.

## To verify

- How to ask the Python writer for the default strategy and for compact mode, and how to
  get more than one chunk with 30,000 rows (the block size is 8,192 rows, the target chunk
  1 MiB: `vortex-file/src/strategy.rs:36,141-142`).
- The names of the fields of the zones table. Upstream builds them from the aggregate,
  for example `vortex.max()` or `vortex.bounded_max(64)`. Check them in a real file.
- Zone maps are skipped under a nullable struct (`zoned/writer.rs:127-132`): the nullable
  struct of `default_mixed` should show it.
