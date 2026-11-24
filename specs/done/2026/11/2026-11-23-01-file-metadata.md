---
model: opus
effort: high
pr: own
automerge: false
---

# Files carry user-defined metadata

## Goal

A writer stores small key/value entries in the file, next to its rows, and a reader gets them
back by their key: `WriteOptions.Metadata map[string][]byte`, `Writer.SetMetadata` (and
`RowWriter.SetMetadata`) for what is known once the rows are written, `File.MetadataKeys` and
`File.Metadata(key) ([]byte, bool, error)`. It works with `Open`, `OpenFile` and `OpenMapped`.

## Why

dbbat archives captured rows as one file per connection, and needs a few values that are not
rows: a version of its schema, and `dbbat.query_offsets`, a JSON index of where each query's
rows start, only known once the rows are written. The format has a place for them, the
postscript's `metadata` field, so they belong there rather than in a side file or a column.

vortex-go already parsed the field (`internal/fbs`: `Postscript.Metadata()`), but neither
exposed nor wrote it, and did not check it: a file the reference reader refuses opened here.

## Implementation

- `metadata.go`: the limits as exported constants (`MaxMetadataEntries` 16,
  `MaxMetadataKeyBytes` 64), the checks of a key (non-empty, valid UTF-8, at most 64 bytes),
  `SetMetadata` (copies the value; a 17th key, a bad key or a value over `maxSegment` is an
  error of the call, the writer goes on), the options' map taken through it in `NewWriter` (an
  error there is the error of the first `Write` or `Close`, like a bad schema).
- `writer.go` `footer()`: the values, in the order of their keys, right after the data and
  before the dtype, aligned to 1; the postscript table gets its fifth field, a vector of
  `PostscriptMetadata { key, segment }`, left out when there is no entry (as upstream).
- `file.go` `openTail`: `readMetadata` checks what the reference reader checks (at most 16
  entries, keys as above and unique, a segment present, `offset + length` within the file, an
  alignment exponent of at most 16 and an offset aligned to it) and keeps the values that lie
  in the bytes `Open` read anyway. `File.Metadata` reads any other value then, its bytes only
  (a view of the mapping for `OpenMapped`), and wraps `os.ErrClosed` after `Close`.
- Interop: a second binary in `tools/variantjson`, `filemeta` (same pinned crates, no extra
  build in CI): `write` makes `testdata/metadata/rust.vortex` with the reference writer and
  prints what the reference reader reads back (`make metadata`); `read` prints the entries of
  any file, which `make interop` diffs against what `TestInteropMetadata` wrote.

## Tests

- `TestMetadataReference`: the reference's file, its 16 entries, through the three opens.
- `TestMetadataRoundTrip`, `TestMetadataReads` (a value in the tail costs no read, one outside
  costs one, and its read error is returned), `TestMetadataLimits`, `TestRowWriterMetadata`,
  `TestMetadataCorrupt` (postscripts rebuilt with each broken rule), `ExampleFile_Metadata`.
- `make interop`: the reference reads the entries of our file, for the newest edition and
  `core 2025.05.0`; vortex-data 0.64 and 0.83 read the rows of the latter.

## To verify

Checked against upstream at tag 0.93.0 (`vortex-file/flatbuffers/vortex-file/footer.fbs`,
`src/footer/{postscript,mod,serializer,deserializer}.rs`, `src/{writer,file,open}.rs`):

- [x] **Schema.** `Postscript.metadata: [PostscriptMetadata]`, field 4, appended after
  `footer`; `PostscriptMetadata { key: string (required); segment: PostscriptSegment (required) }`.
  The field first appears in 0.87.0 (absent from 0.86.0 and earlier). Old readers ignore it:
  vortex-data 0.64.0 and 0.83.0 read our `core 2025.05.0` file with metadata.
- [x] **Not edition-gated.** Nothing in `vortex-edition/src/declarations/core/` mentions it; it
  is a footer feature, written whatever the edition.
- [x] **Limits.** `MAX_METADATA_SEGMENTS = 16`, `MAX_METADATA_KEY_BYTES = 64` (bytes, not
  characters), keys non-empty and unique. Checked by the writer (`validate_metadata_segments`,
  also exposed as `VortexWriteOptions::validate_metadata`) and by the postscript reader, which
  rejects the file. The key being a flatbuffers string, the verifier also requires UTF-8.
- [x] **Segment placement and alignment.** The serializer sorts the entries by key and writes
  each value right after the data segments, before the dtype, layout, statistics and footer
  flatbuffers, padded to the alignment of the `ByteBuffer` given (1 for plain bytes). The
  segment has no compression and no encryption (`_compression`, `_encryption` unset).
- [x] **Reader checks** (`deserializer.rs`): `offset + length` must not overflow nor pass the file
  size (not the postscript start), the offset must be aligned, and the alignment exponent at
  most 16 (`Alignment::try_from_untrusted_exponent`, 64 KiB).
- [x] **Writer API.** `VortexWriteOptions::with_metadata_segment(key, bytes)` and
  `with_metadata_segments(iter)`; a repeated key replaces the value. No way to add an entry
  once the stream is written: the Rust writer takes the whole stream at once.
- [x] **File API.** Not loaded by default: `VortexOpenOptions::include_metadata()` reads every
  value at open, then `VortexFile::metadata_segment(key)` / `metadata_segments()`; without it
  only the locators are known (`Footer::metadata_segment`). We read lazily instead, a value at
  a time, keeping the ones the tail read already holds. The Python API (vortex-data 0.93.0)
  exposes none of it; the JNI binding does.

## Resolved open questions

- **`Metadata` returns an error**: a value beyond the tail is read on demand from an
  `io.ReaderAt`, which can fail; reading every value at `Open` instead would make each open pay
  for values that may be large (up to 4 GiB each) and seldom asked for.
- **Two ways to set metadata**: the options for what is known up front, `SetMetadata` for what
  the rows decide; the Go writer streams, unlike the Rust one, so the second is needed.
