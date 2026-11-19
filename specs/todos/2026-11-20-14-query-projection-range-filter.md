---
model: sonnet
effort: high
pr: batch
automerge: false
---

# A scan takes a column list, a row range and an exact filter, and skips what the statistics rule out

## Problem

After the decoding specs, `Scan` reads every column of every row. `docs/interface.md` plans
three ways to read less: the `columns ...string` of `Scan`, `Query.Rows` and `Query.Filter`.
Without them a caller pays for the whole file, and the zoned layout statistics are unused.

## Proposal

1. **Projection.** `Scan(ctx, q, columns...)` reads only the named top-level columns, in the
   order given. `b.Col(0)` is the first name passed. No name means every column, in schema
   order. A column that the filter needs but the caller did not ask for is read and not
   returned. An unknown name is the error of the first iteration, before any read.
2. **Row range.** `type Range struct{ Start, End int64 }`, half-open. The zero value is the
   whole file, so `End == 0` means "to the end". A range outside the file is clamped. `Start
   > End` (with `End != 0`) is the error of the first iteration. Chunks and zones wholly
   outside the range are never read.
3. **Filter.** In `expr.go`:
   - `Col(path ...string) Ref`, with `Eq`, `Ne`, `Lt`, `Le`, `Gt`, `Ge`, `In` and `IsNull`
     on `Ref`, and `And`, `Or`, `Not`. `Expr` is a small tree, not an interface the caller
     implements. The zero `Expr` keeps every row.
   - Literals are Go values: any integer or float type, `bool`, `string`, `[]byte`,
     `vortex.Decimal`, `time.Time` (date and timestamp), `time.Duration` (time). The filter
     is checked against the schema once, before any read: a literal that does not fit the
     column (a string against an `i32`, an integer out of the column's range for `Eq`) is the
     error of the first iteration. The message names the column, its type and the literal.
   - Nulls follow SQL: a comparison with a null is unknown, `And`, `Or` and `Not` are
     three-valued, and a row is kept only when the result is true. `IsNull` is never unknown.
   - Floats: `NaN` equals nothing, like in Go. Write the choice in the doc comment.
4. **Evaluation.** Per batch, the filter gives a selection (a bitmap or a list of indexes,
   pick one). Columns that are only returned are decoded after the filter. A batch with no
   selected row is not yielded. The yielded batch is dense: `Len()` is the number of kept
   rows.
5. **Pruning.** Before reading a zone of a `vortex.zoned` (or older `vortex.stats`) layout,
   the filter is evaluated against its statistics: `min`, `max`, their bounded forms (a
   truncated bound is still a bound, never an exact value), `null_count` and `nan_count`.
   The result is "no row can match", "every row matches" or "read it". Only the first one
   skips. A missing statistic means "read it". The same goes for a chunk that the row range
   or the filter rules out as a whole.
6. A test hook counts the segments read, so tests can assert that pruning happened.
7. Update `api.txt` and the examples list of `docs/testing.md` section 6 if it changed.

## Tests

- `scan_query_test.go`: random queries on every fixture, 200 per file, seeded, the seed
  printed on failure. A query is a column subset, a row range and a filter built from the
  schema (comparisons with values taken from the file, `In`, `IsNull`, and `And`/`Or`/`Not`
  on top). The result must equal a plain Go loop over the fully decoded data.
- The same with batch sizes of 1, 7, 1024 and larger than a chunk.
- Null semantics, table-driven: each operator against null, `Not(unknown)`, `Or(true,
  unknown)`, `And(false, unknown)`.
- Pruning: on `tpch`-like generated data sorted on a column, a filter keeping 1% reads
  fewer segments than a full scan (the hook). A filter on a column with truncated string
  bounds still returns exact results.
- Errors, with their text: unknown column, unknown nested field, literal of the wrong
  type, `Start > End`.
- `testing.AllocsPerRun`: a running filtered scan allocates nothing per batch.

## To verify

- How upstream serializes the statistics of a zone, and the exact meaning of the bounded
  min and max: `vortex-layout/src/layouts/zoned/` and the aggregate functions named in the
  edition (`vortex.bounded_min`, `vortex.bounded_max`).
- Whether older files (`vortex.stats`) store the same statistics under other names.

## Open questions

- **The shape of `Range`.** `docs/interface.md` names the type but not its fields.
  Recommended answer: `struct{ Start, End int64 }`, half-open, `End == 0` meaning the end of
  the file, as in the proposal. It keeps the zero value useful and needs no constructor.
