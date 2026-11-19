# Goals

The goals are ranked. When two of them conflict, the higher one wins.

Each goal has a "done when" list. Everything in those lists is checked by a test or a
gate described in [testing.md](testing.md).

## 1. Easy to use

Reading a file into Go structs, or writing a slice of structs to a file, takes a few
lines and no configuration.

- Two levels only: rows (struct tags) and columns (typed slices). See [interface.md](interface.md).
- Defaults work. No option is required to read or write a file.
- Familiar to parquet-go users: struct tags, `Write([]T) (int, error)`, `Close()`.
- Errors name the column, the encoding and the byte offset.

Done when:

- The README example reads a file into structs in 10 lines or fewer, error handling
  included, and that example runs as a test.
- Every main use case has a runnable `Example` function.
- Every exported identifier has a doc comment.
- The exported API is a committed snapshot, so every change to it shows in review.

## 2. Correct

The Rust implementation is the reference. We never decide by ourselves what a file contains.

Reader:

- Every value read equals what the reference reader returns for the same file.
- A corrupt or truncated file gives an error. Never a panic, never a hang, never an
  allocation larger than the file justifies.

Writer:

- Every file written is opened by the reference reader and gives back the values we wrote.
- A file only contains encodings of the target edition (see goal 3).

## 3. Complete

Upstream groups what a writer may emit into "editions". The target is the default `core`
edition of the release pinned in `.vortex-version`. For 0.93.0 that is `core 2026.08.3`,
about 50 identifiers: array encodings, layouts, extension types and statistics.

Renovate opens a PR on each upstream release, see [upstream-update.md](upstream-update.md).

Reader, done when:

- Every identifier of the target edition is decoded.
- Every logical type of the target edition is read, nullable and not nullable, nested
  types included. The mapping to Go types is in [interface.md](interface.md#types).
- Every upstream release that publishes fixtures is read. Today that is 28 releases,
  from 0.64.0 to 0.93.0.

Writer, done when:

- Every logical type can be written.
- The compressor picks encodings per chunk, like the reference one, and our files are at
  most 10% larger than the reference writer's on TPC-H and ClickBench.

The writer does not have to emit every encoding. The reader has to decode every one.

The support matrix in the README is generated from the code, never written by hand.

## 4. Fast, without allocation

- A scan allocates nothing per batch once it is running.
- The row API allocates nothing per row for fixed-width and `[]byte` fields.
- The writer allocates nothing per `Write` call once it is running.
- Memory use depends on batch size, column count and parallelism. Not on file size.
- Scans use every core by default.

Speed targets, to revise after the first measurements:

- Scan and write at least as fast as parquet-go on the same data.
- The ratio to the Rust reference is published, whatever it is.

## 5. Self-contained

- **No cgo.** Not in this module, not in anything it imports.
- **Standard library, plus one module.** `github.com/klauspost/compress` is the only
  dependency allowed, for zstd (Go 1.27 has no public zstd package). It is pure Go.
  Everything else is hand-written: flatbuffers, the protobuf wire format, FastLanes, ALP,
  FSST and Pco.
- No assembly in the first version.
- `unsafe` only in one internal package, for two things: casting decoded bytes to typed
  slices, and writing the fields of a row struct.
- MIT license.

Adding another module is Florent's decision, case by case.

## 6. 100% statement coverage

- Every statement of every package is executed by `go test ./...`.
- No exclusion list. Code that no test can reach is deleted.
- The run is offline. It only uses files committed in the repository.

Coverage does not prove correctness. Goal 2 does. Coverage proves there is no code we never ran.

## Non-goals

- **Byte-identical output with the Rust writer.** The format leaves layout, segment order
  and encoding choice to the writer.
- **Arrow in the core module.** An adapter can live in a separate module later.
- **A query engine.** Projection and filters only. No joins, no aggregations.
- **The `preview` and `unstable` editions**, and third-party encodings.
- **Big-endian platforms.**
- **Object store clients.** Bring an `io.ReaderAt`.
- **The `Union` type.** It exists upstream but is in no edition.
- **For later, all additive:** row lookups by index, dictionary pass-through, user
  metadata, reusing a cached footer, `Splits(n)` for one scan per goroutine, widening
  (an `int32` column into an `int64` field).

## Open decisions

1. **Name.** The module is `github.com/fclairamb/vortex-go` and the package is `vortex`.
   `mprammer/vortex-go` already exists (a scalar-only reader, one commit).
2. **Minimum Go version.** 1.26, with CI on 1.26 and 1.27.
3. **Variant.** No Go mapping yet. The proposal is in [interface.md](interface.md#types).

## Decisions taken

- **Zstd uses `github.com/klauspost/compress`** (Florent, 2026-11-19). The target edition
  includes `vortex.zstd`. The alternative was our own decoder: Go's internal, decode-only
  one is 2,277 lines, ours would have had to be covered to 100%, and the "compact" writer
  mode needs an encoder too.
- **Decimals above 18 digits use `vortex.Decimal`** (Florent, 2026-11-19). One value type
  of 32 bytes, no allocation. Go has no 128 or 256 bit integer, and `*big.Int` allocates
  per value. Up to 18 digits, an `int64` works too.
- **Row structs are filled with `unsafe`** (Florent, 2026-11-19), in the same internal
  package as the slice casts. Measured on an Apple M4 with Go 1.27, for a struct of 6
  fields: 4.8 ns per row with `unsafe`, 26 ns with `reflect`, 3.5 ns for hand-written
  code. No allocation in any of them. `reflect` costs about 3.7 ns per field.
