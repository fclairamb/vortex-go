---
model: opus
effort: high
pr: batch
automerge: false
---

# A scan uses every core, merges its reads, and survives any corrupt file or failing reader

## Problem

Two promises of `docs/goals.md` are not kept yet:

- goal 4: "scans use every core by default", memory that depends on batch size and not on
  file size, and no allocation per batch;
- goal 2: a corrupt or truncated file gives an error, "never a panic, never a hang, never
  an allocation larger than the file justifies".

Both touch the same code, the pipeline between the `io.ReaderAt` and the yielded batch, so
they are done together. The concurrency is why this spec is on opus.

## Proposal

1. **Pipeline.** A scan plans its work as units (a chunk of the projected columns), then:
   - reads are grouped: ranges closer than a gap are merged into one `ReadAt`, and reads
     are issued in parallel. The gap is small for `*os.File` and in-memory readers, large
     for anything else (`docs/interface.md`, "I/O");
   - decoding runs on `GOMAXPROCS` goroutines, ahead of the consumer by a bounded number of
     units. Batches are yielded in file order;
   - leaving the loop (`break`, `return`, panic in the body) and a cancelled `ctx` stop
     every goroutine before the iterator returns. No goroutine outlives the loop;
   - the first error stops the scan and is yielded once, wrapped with the column, the
     encoding and the byte offset.
2. **Memory.** Buffers come from a pool owned by the scan and go back to it when the
   consumer moves to the next batch. Peak memory is bounded by batch size, column count and
   the read-ahead, whatever the file size.
3. **Hostile input.** One rule everywhere: no length, count or offset read from the file
   is trusted before it is checked against what is left of the file or of the buffer.
   - an allocation sized by the file is capped by a limit derived from the file size and
     the row count. Above it: an error;
   - recursion (nested types, layouts, arrays) has a depth limit;
   - every decoder returns an error on a short buffer. No decoder indexes without a check.
4. **Fuzzing.** `FuzzOpenScan` in `fuzz_test.go`: open, then scan everything, on arbitrary
   bytes. Seeded with every fixture. Properties: no panic, done within a deadline, peak
   allocation under the limit of step 3. `make fuzz` runs it for `FUZZTIME` (30s by
   default). Every crasher found is committed under `testdata/fuzz/` with its fix.
5. **Fault injection**, in `internal/testutil`: a `ReaderAt` that fails at call N, one that
   returns short reads, one that returns `io.EOF` with the last bytes, one that blocks until
   the context is cancelled.
6. `docs/testing.md` section 9: "Allocation" and "Fuzzing, 30 seconds" become "in place".
   CI runs `make fuzz` in `.github/workflows/build.yml`, on the Go 1.27 leg only.

## Tests

- Systematic damage, on every fixture: truncate at each of 200 offsets spread over the
  file, and flip each byte of the footer and postscript. Open plus scan returns an error or
  the right data, within a deadline, under the allocation limit.
- I/O faults: for every N up to the number of reads of a scan, the reader fails at call N.
  The scan yields one error that wraps the cause (`errors.Is`). Short reads give the same
  result as full reads.
- Cancellation: a cancelled `ctx` yields `context.Canceled` and leaves no goroutine. A
  `break` after the first batch leaves no goroutine. Checked by counting goroutines, with a
  retry loop, never a bare sleep.
- Concurrency, with `-race`: 16 scans at once on one `File`, each with its own query, all
  results equal to the sequential ones. `Close` during a scan gives an error, never a crash.
- Order: with `GOMAXPROCS` of 1, 2 and 8, the batches come in the same order with the same
  content.
- `testing.AllocsPerRun`: 0 allocation per batch in a running scan.
- Peak heap: scanning a generated file of 2 million rows (written with the reference
  writer by `tools/oracle`, kept in `.cache/`, test skipped when the file is absent) stays
  under a fixed bound. A second, small committed file runs the same test in `go test ./...`.
- Read merging: a counting reader shows fewer `ReadAt` calls than segments for a full scan.

## To verify

- How upstream sizes its read-ahead and merges reads: `vortex-file/src/` (the read driver
  and segment source) and `vortex-io/`. Take the idea, not the numbers.
- That every error path added here is reachable by a test. The coverage gate is 100%: an
  error branch that no damaged file and no I/O fault can trigger is dead code, delete it.
