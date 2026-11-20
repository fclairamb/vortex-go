# Interface

This is the planned public API. Implemented so far: `Open`, `OpenFile`, `Schema`,
`NumRows`, `Close`, `Type`, `Kind`, `Field`, the errors, and `Scan` with `Query` (empty, and
`columns` must be empty), `Batch`, `Column` (`Type`, `Null`, `Bool`, `Field`), `Values` and
`Number`, for numbers, booleans, structs and the storage of dates, times and timestamps, and
`Rows`, which reads a file into Go structs, and `NewWriter` with `Writer`, `Data`, `Of`,
`OfBools`, `OfStrings`, `OfBytes`, `OfDecimals`, `OfNulls`, `OfStruct`, `OfList`,
`WriteOptions` and the schema constructors (plain arrays only, `Compression` has no effect
yet). `NewRowWriter` is not there yet.

Once the code exists, the source of truth becomes `api.txt` (the output of `go doc -all`,
checked by CI) and this file only keeps the reasons.

## Example

```go
f, err := vortex.OpenFile("trades.vortex")
if err != nil {
    return err
}
defer f.Close()

type Trade struct {
    ID    int64            `vortex:"id"`
    Price float64          `vortex:"price"`
    City  sql.Null[string] `vortex:"city"`
}

q := vortex.Query{Filter: vortex.Col("price").Gt(100)}
for t, err := range vortex.Rows[Trade](ctx, f, q) {
    if err != nil {
        return err
    }
    use(t)
}
```

The same scan, by columns:

```go
for b, err := range f.Scan(ctx, q, "id", "price", "name") {
    if err != nil {
        return err
    }
    ids, err := vortex.Values[int64](b.Col(0))
    if err != nil {
        return err
    }
    prices, err := vortex.Values[float64](b.Col(1))
    if err != nil {
        return err
    }
    name := b.Col(2)
    for i := range b.Len() {
        use(ids[i], prices[i], name.Bytes(i))
    }
}
```

## The full surface

```go
// Open
func Open(r io.ReaderAt, size int64) (*File, error)
func OpenFile(path string) (*File, error)
func (f *File) Schema() *Schema
func (f *File) NumRows() int64
func (f *File) Close() error

// Select rows
type Query struct {
    Filter Expr  // exact, zero value keeps every row
    Rows   Range // half-open {Start, End}; zero value is the whole file, End == 0 is the end
}
func Col(path ...string) Ref // .Eq .Ne .Lt .Le .Gt .Ge .In .IsNull
func And(e ...Expr) Expr     // also Or, Not

// Read rows: the projection comes from T
func Rows[T any](ctx context.Context, f *File, q Query) iter.Seq2[T, error]

// Read columns: no column means all of them
func (f *File) Scan(ctx context.Context, q Query, columns ...string) iter.Seq2[*Batch, error]
func (b *Batch) Len() int
func (b *Batch) Col(i int) *Column
func (c *Column) Type() Type
func (c *Column) Null(i int) bool
func (c *Column) Bool(i int) bool
func (c *Column) Bytes(i int) []byte          // utf8, binary, uuid
func (c *Column) Decimal(i int) Decimal       // any precision
func (c *Column) Field(i int) *Column         // struct child
func (c *Column) Elems() *Column              // list, fixed-size list and map values
func (c *Column) List(i int) (start, end int) // range in Elems
func Values[T Number](c *Column) ([]T, error) // error when the column is not a T

// Write: both only need an io.Writer
func NewRowWriter[T any](w io.Writer, o WriteOptions) *RowWriter[T]
func (w *RowWriter[T]) Write(rows []T) (int, error)
func (w *RowWriter[T]) Close() error

func NewWriter(w io.Writer, s *Schema, o WriteOptions) *Writer
func (w *Writer) Write(cols ...Data) error
func (w *Writer) Close() error
func Of[T Number](v []T) Data         // also OfBools, OfStrings, OfBytes, OfDecimals
func OfStruct(fields ...Data) Data
func OfList(offsets []int, elems Data) Data
func OfNulls(n int) Data              // a column of the null type
func (d Data) Nulls(null []bool) Data // marks the null rows, for any Data
var ErrWriterClosed error             // Write after Close

// Schema constructors, plain functions. The kinds have the names Int8, Utf8, Date...
// already, so the constructors carry a Type suffix.
func NewSchema(fields ...Field) *Schema
func Int8Type() Type // ... Int16Type to Float64Type, BoolType, Utf8Type, BinaryType, NullType, UUIDType
func DecimalOf(precision, scale int) Type
func ListOf(elem Type) Type
func FixedSizeListOf(elem Type, n int) Type
func MapOf(key, value Type) Type
func StructOf(fields ...Field) Type
func DateType(u Unit) Type; func TimeType(u Unit) Type; func TimestampType(u Unit, zone string) Type
func (t Type) AsNullable() Type

type WriteOptions struct{ Compression Compression } // Default, Compact, None

// A decimal of any precision: the unscaled value, on 256 bits
type Decimal struct{ /* 32 bytes */ }
func DecimalFromInt64(v int64) Decimal
func DecimalFromBig(v *big.Int) (Decimal, error) // error above 256 bits
func (d Decimal) Int64() (v int64, ok bool)
func (d Decimal) Big() *big.Int
func (d Decimal) Text(scale int) string
```

## Writing

- **One `Data` per field, the physical shape; the schema gives the logical type.**
  `Of[int64]` for a timestamp (the storage), `Of[uint16]` for the bits of a `Float16`,
  `OfBytes` of 16 bytes each for a UUID, `OfList(offsets, elems)` for a list, a fixed-size
  list (regular offsets) and a map (`elems` is `OfStruct(keys, values)`).
- **Errors name the field** (`a.b` for a struct field, `a[]` for the elements of a list) and
  close the writer for writing. A schema that cannot be written (decimal precision 0, a date in
  seconds, a variant) is reported by the first `Write` or `Close`, since `NewWriter` returns
  no error.
- **A non-nullable field takes no validity.** The reference reader rejects a validity child
  on a non-nullable dtype, so `Nulls` on such a field is an error when a row is null.
- **Chunks.** Each `Write` of one row or more adds a chunk to every column; a column with a
  single chunk is a flat layout. A file with no row has one empty chunk. A struct needs one
  field at least to have a length.
- **Lists are `vortex.listview`**, maps are `vortex.map` over a listview of structs: it is
  the canonical form, and the one a map requires.

## Opening, schema and errors

```go
type Kind uint8 // Null, Bool, Int8...Int64, Uint8...Uint64, Float16...Float64, KindDecimal, Utf8,
                // Binary, List, FixedSizeList, Map, Struct, Date, Time, Timestamp, UUID, Variant
type Unit uint8 // Nanosecond, Microsecond, Millisecond, Second, Day

type Type struct{ /* unexported */ }
func (t Type) Kind() Kind
func (t Type) Nullable() bool
func (t Type) Precision() int; func (t Type) Scale() int // Decimal
func (t Type) Elem() Type; func (t Type) Size() int      // List, FixedSizeList
func (t Type) Key() Type; func (t Type) Value() Type     // Map
func (t Type) Fields() []Field                           // Struct
func (t Type) Unit() Unit; func (t Type) Zone() string   // Date, Time, Timestamp
func (t Type) Equal(o Type) bool
func (t Type) String() string

type Field struct{ Name string; Type Type }
type Schema struct{ /* unexported */ }
func (s *Schema) Fields() []Field
func (s *Schema) Field(name string) (Field, bool)
func (s *Schema) String() string

var ErrNotVortex, ErrUnsupportedVersion, ErrCorrupt error // test with errors.Is
```

- **`Type` is a value with unexported fields**, compared with `Equal`, not `==`: it holds
  nested types. An accessor of another kind returns the zero value. The constructors are in
  the writing section.
- **`Null` types are always nullable.** The oracle reports them so.
- **Errors carry the byte offset.** `ErrNotVortex`: bad magic at either end, or fewer
  than 12 bytes. `ErrUnsupportedVersion`: a version other than 1. `ErrCorrupt`: anything
  out of bounds or malformed, an unknown extension type and `Union` included.
- **Reads.** `Open` reads the last 64 KiB in one call, and one more range when the footer
  reaches further back. A file larger than 64 KiB costs one more read for its first four
  bytes, so a typical open is two reads.

## Types

The target edition has 12 logical types. `Union` exists upstream but is in no edition, so
it is out of scope.

| Vortex type | Field of a row struct | Column |
|---|---|---|
| Null | none, leave it out | `Null(i)` is always true |
| Bool | `bool` | `Bool(i)` |
| `u8` to `u64`, `i8` to `i64`, `f32`, `f64` | the same Go type | `Values[T]` |
| `f16` | `float32` | `Values[float32]` |
| Decimal, precision up to 18 | `int64`, the unscaled value, or `vortex.Decimal` | `Values[int64]`, or `Decimal(i)` |
| Decimal, precision 19 to 76 | `vortex.Decimal` | `Decimal(i)` |
| Utf8 | `string` (copies) or `[]byte` (view) | `Bytes(i)` |
| Binary | `[]byte` | `Bytes(i)` |
| List | `[]T` | `Elems()` and `List(i)` |
| FixedSizeList | `[N]T`, or `[]T` | `Elems()` and `List(i)` |
| Map | `map[K]V` | `Elems()` is a struct of key and value, and `List(i)` |
| Struct | a struct | `Field(i)` |
| Date | `time.Time` | the storage, `Values[int32]` or `Values[int64]` |
| Time | `time.Duration` since midnight | the storage, `Values[int32]` or `Values[int64]` |
| Timestamp | `time.Time` | the storage, `Values[int64]` |
| UUID | `[16]byte` | `Bytes(i)`, 16 bytes |
| Variant | `any` (`nil`, `bool`, `int64`, `float64`, `string`, `[]byte`, `time.Time`, `VariantDecimal`, `[]any`, `map[string]any`) | `Variant(i)`, allocates |

- **Exact match.** An `int32` column does not go into an `int64` field. The error names
  both types. `int` and `uint` count as 64 bits. Widening can come later, it is additive.
- **Date, time and timestamp give their storage at the column level.** `Type()` says which
  one it is, with its unit and time zone. The row level converts to the Go type, and also
  accepts the storage type (an `int64` field for a timestamp). A UUID is stored as a list
  of 16 `u8`, and is read and written as 16 bytes.
- **The decimal kind is `KindDecimal`**, since `Decimal` is the value type. A
  `DecimalFromBig` error (`ErrDecimalRange`) means the value is outside -2^255 to 2^255-1.
- **Decimals keep their scale in the schema**, not in each value. A `vortex.Decimal` is the
  unscaled value on 256 bits: 32 bytes, no allocation. `Text(scale)` prints it, `Big()`
  allocates.
- **The writer mirrors the table.** `Data` has the physical shape and the `*Schema` gives
  the logical type: `Of[int64]` for a timestamp, `OfBytes` for a UUID,
  `OfList(offsets, OfStruct(keys, values))` for a map.
- **Variant is `any`.** A field of a row struct of type `any` takes a Variant column;
  `Column.Variant(i) (any, error)` decodes one value at the column level. It allocates, and
  the error is a damaged value: the bytes of a Variant are only parsed when a row is asked
  for. A decimal inside a Variant is `VariantDecimal{Value Decimal; Scale int}`: a Variant
  gives each decimal its own scale.

## Rules that go with it

- **Memory.** A batch and its views are valid for one loop iteration. Use `slices.Clone`
  to keep values. A view never points into the file, so a stale view gives wrong data,
  never a crash.
- **Row structs.**
  - `string` copies and is always safe.
  - `[]byte` is a view, valid in the loop body.
  - `sql.Null[T]` or a pointer for nullable columns. The pointer allocates.
  - Nested structs, slices and maps are supported. Slices and maps allocate per row.
  - A field that is a number, a bool, a decimal, a time, a UUID, a `string` or a `[]byte`,
    plain or in `sql.Null`, is filled for a whole batch at once with no allocation per row
    (`internal/unsafex`, offsets and sizes from `reflect`). A plain nested struct of such
    fields is filled in place. Pointers, slices, maps, `any` and nullable structs go through
    `reflect`.
  - A date or a timestamp is a `time.Time` in UTC, or in the zone of its type when it has
    one (a zone the system does not know is an error of the first iteration). A time is a
    `time.Duration` since midnight. A named type of the right kind is accepted, except for
    the time types, which must be `time.Time` and `time.Duration` themselves.
  - Two fields may read the same column. A column that no field reads is not read.
- **Missing column.** An error, unless the tag says `vortex:"city,optional"`, which gives
  the zero value.
- **Errors.** A bad column name, a filter that does not fit the column type, or a struct
  field that does not fit its column, comes back as the error of the first iteration,
  before any data is read. A `T` that cannot be written comes back from the first `Write`.
- **Wrong accessor.** `Values[T]` returns an error, because the width of a number comes
  from the file. The per-row accessors (`Bool`, `Bytes`, `Decimal`, `Field`, `Elems`, `List`) panic on
  a column of another type, like an index out of range. `Type()` tells before calling.
- **Nested fields.** `Col("address", "city")` filters on a field of a struct. `Scan` only
  takes top-level columns, the children come with `Field(i)`.
- **Filters are exact.** The caller never has to filter again.
- **Parallelism.** Decoding runs ahead on `GOMAXPROCS` internal goroutines, a bounded
  number of segments ahead of the loop. Batches come in file order. Leaving the loop
  (`break`, `return`, a panic) or cancelling the context stops them before the iterator
  returns. `Close` during a scan makes it return an error that wraps `os.ErrClosed`.
- **I/O.** Reads are merged and issued in parallel. The gap is chosen from the reader:
  small for `*os.File` and memory (4 KiB, reads of 1 MiB at most), large for anything else
  (1 MiB, reads of 16 MiB at most). A reader that returns fewer bytes without an error is
  called again for the rest.

## Why this shape

- **Iterators, not `Next`/`Err`/`Close`.** `range` handles the cleanup. Rows are yielded by
  value, so a reused pointer cannot bite.
- **Columns are arguments of `Scan`.** `b.Col(0)` is the first name passed. `Query` only
  selects rows, so it means the same thing for `Rows` and `Scan`.
- **No `Limit`.** `break` does it, and stops the decoding.
- **No `ctx` to open a file.** An `io.ReaderAt` cannot be cancelled and the footer is two
  or three reads. The scans take one.
- **`Column` to read, `Data` to write.** A `Column` is a view owned by the scan. A `Data`
  wraps slices owned by the caller. One type for both would need two sets of rules.
- **Options by value.** `Query{}` and `WriteOptions{}` are the defaults, with no `nil`.
- **No reference counting.** The scan owns the memory and reuses it.
- **No session, no registry to set up.** Defaults live in the package.
- **From parquet-go:** struct tags and `Write([]T) (int, error)`. Not `Row`, `Value`,
  `RowGroup` or `Page`: Vortex has no row groups and no pages.
