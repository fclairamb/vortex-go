---
model: sonnet
effort: high
pr: batch
automerge: false
---

# `Rows[T]` reads a file into Go structs, with no allocation per row for fixed-width fields

## Problem

Goal 1 of `docs/goals.md` is "a file to Go structs in a few lines". The column API exists
after the previous specs, the row API does not. It is the API most callers will use, and
the README example depends on it.

## Proposal

1. `func Rows[T any](ctx context.Context, f *File, q Query) iter.Seq2[T, error]` in
   `rows.go`. `T` is a struct. The projection comes from its fields, the scan is the one of
   `Scan`, and each row is yielded by value.
2. **The plan**, built once per call from `reflect.TypeFor[T]()` and the schema, before any
   read. A mismatch is the error of the first iteration:
   - the column of a field is its tag, `vortex:"name"`, else the field name as written.
     `vortex:"-"` skips the field. `vortex:"name,optional"` gives the zero value when the
     column is missing. Unexported fields are skipped. Embedded structs are flattened.
   - the type mapping is the table of `docs/interface.md`, section "Types". Exact match: an
     `i32` column does not go into an `int64` field. `int` and `uint` count as 64 bits.
   - a nullable column needs `sql.Null[X]` or `*X`. A non-nullable column accepts both and
     the plain type. A nullable column into a plain field is an error that says so.
   - `string` copies, `[]byte` is a view valid in the loop body.
   - date and timestamp go to `time.Time` (UTC unless the type has a zone) or to their
     storage integer. Time goes to `time.Duration` or its storage integer. UUID goes to
     `[16]byte`.
   - nested: struct to struct, list to `[]X`, fixed-size list to `[N]X` or `[]X`, map to
     `map[K]V`. Slices and maps allocate per row, and the doc says so.
   - the error names the field, its Go type, the column and its Vortex type.
3. **The fill.** One internal package holds every use of `unsafe`: `internal/unsafex`. It
   already has the slice casts of the decoders, or gets them now. Add the field writes: for
   each batch, fill a reused `[]T` column by column, with `unsafe.Add(base, i*size+offset)`.
   The measured cost was 4.8 ns per row for 6 fields, against 26 ns with `reflect`
   (`docs/goals.md`, "Decisions taken").
   - only for the fixed-width kinds, `[]byte`, `string` and `sql.Null[X]` of those.
     Pointers, slices, maps and nested structs with such fields may go through `reflect`.
   - offsets and sizes come from `reflect`, never from a guess. The plan refuses a `T` whose
     layout it does not understand.
4. A test fails when a package other than `internal/unsafex` imports `unsafe`
   (`docs/testing.md` section 8). Write it now if no earlier spec did.
5. `Example` functions: read rows, read rows with a filter. Update `api.txt`.

## Tests

- `rows_test.go`: for every fixture, a generated struct type (`reflect.StructOf`) that
  covers every column readable as fixed-width or bytes: the rows equal the column scan.
- The unsafe fill against a `reflect` fill, for every field kind of step 2, on the same
  batches. The `reflect` fill only exists in the test file.
- Hand-written structs for: `sql.Null`, pointers, `optional`, `-`, embedded structs, nested
  struct, slice, array, map, `time.Time`, `time.Duration`, `[16]byte`, `vortex.Decimal`,
  `int64` for a decimal of precision 18.
- Errors, with their text: `T` is not a struct, missing column, `i32` into `int64`,
  nullable into a plain field, a decimal of precision 20 into `int64`, `[4]X` for a
  fixed-size list of 3.
- `testing.AllocsPerRun` on a struct of `int64`, `float64`, `int32`, `bool`, `[]byte` and
  `sql.Null[int64]`: 0 allocation per row once the scan runs.
- `break` in the loop stops the scan: no goroutine left (count them before and after).
- With `-race`: two `Rows` loops on the same `File` at once.

## To verify

- That writing a `[]byte` or `string` header through `unsafe.Add` keeps the garbage
  collector correct: the destination must be typed memory of `[]T`, never a `[]byte`
  buffer. `go vet` and `-race` with `-d=checkptr` must pass. If one kind cannot be made
  safe, fill it with `reflect` and note it.
