---
model: sonnet
effort: high
pr: batch
automerge: false
---

# Write Variant columns: `vortex.variant` over `vortex.parquet.variant`

## Problem

`vortex.variant` and `vortex.parquet.variant` are read and never written: the writer has
no column for the Variant type (`docs/goals.md:86-87`). `docs/goals.md` goal 3 says
"every logical type can be written". Variant is the one that cannot.

Reading exists: `Column.Variant` (`column.go:147-158`), `any` fields in `Rows[T]`
(`rows.go:36`, `:350`), the decoder of the Parquet Variant encoding (`internal/variant`)
and of the two arrays (`internal/array/variant.go:35-37`, `:113-114`). There is no
`VariantType()`, no `Data` for variants, and no encoder.

## Proposal

1. `internal/variant`: `Append(meta, value []byte, v any) (meta, value []byte, err error)`,
   the inverse of the decoder, after `VariantEncoding.md` of apache/parquet-format: the
   metadata is the sorted dictionary of the object keys of the row, the value the encoded
   tree. Integers take the narrowest width, objects have their fields sorted by key.
2. Public API (`api.txt`, `docs/interface.md`):
   - `VariantType() Type`.
   - `OfVariants(vals []any) Data`: a `nil` is a null row. The values are the ones
     `Column.Variant` returns (`column.go:147-150`): `bool`, `int64`, `float64`,
     `string`, `[]byte`, `time.Time`, `VariantDecimal`, `[]any`, `map[string]any`.
     Another Go type is an error of `Write` that names it and the row.
   - `RowWriter[T]`: a field of type `any` is a Variant column, as `Rows[T]` reads it.
3. `wencode.go`: a Variant chunk is a `vortex.variant` (no shredded type, child 0 the
   core storage) around a `vortex.parquet.variant`: metadata `{has_value: true}`, no
   typed value, children the validity when the column is nullable, the metadata (binary)
   and the value (binary). No shredding. The two binary children go through the
   compressor as binary columns do.
4. Statistics: the null count only. No min, no max, no zone pruning on a Variant.
5. `docs/goals.md`: remove the two arrays from the list of what is never written.
   Register both identifiers. `go generate`.

## Tests

- `internal/variant`: `Append` then the decoder gives the same value, for every type of
  step 2, nested 3 levels, an empty object, an empty array, a key used twice in a row of
  objects, integers at the edges of each width. Nesting deeper than `maxDepth`
  (`internal/variant/variant.go:16`) is an error.
- Negative: a `struct{}`, a `chan`, a `map[int]any` are errors that name the Go type.
- Writer round trip: 20,000 rows of mixed values with nulls, in the three compression
  modes, read back with `Column.Variant` and with `Rows[T]`.
- `RowWriter[T]` with an `any` field, then `Rows[T]`: the same rows.
- The hand-built Variant files of the reader tests (`column_variant_test.go`) are read as
  before.
- `make interop`: a Variant file read by the reference, each row compared as JSON (see
  To verify for the tool).
- `TestWrittenMatchesFiles` (spec 28) meets both identifiers.
- `testing.AllocsPerRun`: 0 allocation per `Write` of scalar values into a running writer.

## To verify

- What the reference writes for a Variant column at 0.93.0, node by node:
  `~/code/other/vortex/vortex-array/src/arrays/variant/`,
  `encodings/parquet-variant/src/vtable/mod.rs`, `encodings/parquet-variant/src/builder.rs`.
  Our file must have the same shape.
- How the reference reads the file for the check. `vortex-data` 0.93.0 has no Variant in
  its Python API (`testdata/fixtures/0.93.0/extra/MISSING.md`), so
  `tools/oracle/oracle.py` may not be able to. If so, a small Rust reader next to
  `tools/vectors` (cargo, pinned to the same release) prints each row as JSON.
- Whether a file written by us counts for `notSeen` (`edition_test.go:16-19`). It should
  not: the corpus is what the reference wrote.
- The statistics the reference keeps for a Variant column, in the zone map and the footer.

## Open questions

- **Shredding.** The reference can store typed columns next to the value (the
  `typed_value` of Parquet). Recommended answer: not in this spec. Unshredded files are
  valid and every reader opens them. Shredding is a choice of the compressor, to spec
  when a benchmark has Variant data.
- **Go integers and floats other than `int64` and `float64`.** Recommended answer:
  accept every Go integer and float type and widen it. `map[string]any` built by
  `encoding/json` then works without a conversion pass (`json.Number` excluded: an error).

## Resolved open questions

- **Shredding:** not in this spec; write unshredded files only. (autopilot: recommended answer)
- **Other Go integers and floats:** accept every Go integer and float type and widen it to `int64`/`float64`; `json.Number` is an error. (autopilot: recommended answer)
