---
model: sonnet
effort: high
pr: batch
automerge: false
---

# `Open` reads the trailer, the footer and the schema of a Vortex file

## Problem

Nothing of the public API exists. The first piece is opening a file and saying what it
holds: `Open`, `OpenFile`, `Schema`, `NumRows`, `Close` (`docs/interface.md`). It is also
where a file that is not Vortex, or is cut short, must be turned into a clear error.

Upstream paths are in `~/code/other/vortex`. The prose spec is `docs/specs/file-format.md`.

## Proposal

1. `file.go`:
   ```go
   func Open(r io.ReaderAt, size int64) (*File, error)
   func OpenFile(path string) (*File, error)
   func (f *File) Schema() *Schema
   func (f *File) NumRows() int64
   func (f *File) Close() error
   ```
   `Close` closes the file `OpenFile` opened, and is a no-op for `Open`. A second `Close`
   returns nil.
2. **The trailer** (`vortex-file/src/lib.rs:137-170`, reader in
   `vortex-file/src/footer/deserializer.rs`):
   - the file starts with `VTXF` and ends with 8 bytes: version `u16` LE (must be 1),
     postscript length `u16` LE, `VTXF`;
   - read the last `min(size, 65535)` bytes in one `ReadAt`. The postscript is the
     flatbuffer just before the trailer;
   - the postscript gives four segments (`offset`, `length`, `alignment_exponent`):
     `dtype` (optional), `layout`, `statistics` (optional) and `footer`. When the first read
     does not reach the smallest offset, do one more read. So opening is two reads at most;
   - every offset and length is checked against `size` before it is read.
3. **The footer** (`footer.fbs`): keep the table of array ids (`array_specs`), the table of
   layout ids (`layout_specs`) and the segment map (`segment_specs`: absolute offset,
   length, alignment exponent). Segments must be sorted by offset and inside the file. The
   compression and encryption fields are reserved: upstream ignores them, so do we.
4. **The layout tree** is parsed into an internal tree (`encoding` index, `row_count`,
   `metadata`, `children`, `segments`) with a depth limit. It is not interpreted yet.
   `NumRows` is the root's `row_count`.
5. **The schema**, in `schema.go`, from `dtype.fbs` (reader:
   `vortex-array/src/dtype/serde/flatbuffers.rs:182-327`):
   - `type Type struct` (a value, comparable with `Equal`), with `Kind()`, `Nullable()`,
     and by kind: `Precision()`, `Scale()`, `Elem()`, `Size()`, `Key()`, `Value()`,
     `Fields()`, `Unit()`, `Zone()`, `String()`;
   - `Kind`: `Null`, `Bool`, `Int8`...`Int64`, `Uint8`...`Uint64`, `Float16`, `Float32`,
     `Float64`, `Decimal`, `Utf8`, `Binary`, `List`, `FixedSizeList`, `Map`, `Struct`,
     `Date`, `Time`, `Timestamp`, `UUID`, `Variant`;
   - `type Field struct{ Name string; Type Type }`;
   - `type Schema struct`: `Fields() []Field`, `Field(name string) (Field, bool)`,
     `String()`. The root dtype of a file is a struct: anything else is an error.
   - extension types: `vortex.date` and `vortex.time` (1 metadata byte, the unit),
     `vortex.timestamp` (`[unit u8][tz length u16 LE][tz]`), `vortex.uuid` (empty or 1
     byte). Units: 0 ns, 1 us, 2 ms, 3 s, 4 days. An unknown extension id is an error that
     names it. `Union` is an error: it is in no edition (`docs/goals.md`, non-goals).
   - a file with no dtype segment is an error for now.
6. **Errors.** Sentinel errors that the caller can test with `errors.Is`: `ErrNotVortex`
   (bad magic at either end, or shorter than 12 bytes), `ErrUnsupportedVersion`,
   `ErrCorrupt` (anything out of bounds or malformed). Messages carry the byte offset.
7. First `api.txt`, written by hand from `go doc -all .` (the generator comes later), and
   `docs/interface.md` gets `Type`, `Kind`, `Field` and the errors.

## Tests

- For every fixture: `Open` succeeds, `NumRows` equals `rows` and `Schema` equals `schema`
  of the `.expected.json` (a converter from the JSON tree to `Type` lives in
  `internal/testutil`).
- `OpenFile` on a missing path wraps `fs.ErrNotExist`.
- A counting `ReaderAt`: at most 2 reads to open any fixture.
- Damage, each with its sentinel: empty file, 11 bytes, wrong first magic, wrong last
  magic, version 2, postscript length larger than the file, a segment past the end,
  unsorted segments, a root dtype that is not a struct, an unknown extension id.
- `Type.String()` for one type of each kind, and `Equal` on equal and different types.

## Open questions

- **The shape of `Type` and `Schema`.** `docs/interface.md` names them without fields.
  Recommended answer: the methods of step 5. A struct with unexported fields and accessor
  methods can grow without breaking callers, and the writer spec adds the constructors.
