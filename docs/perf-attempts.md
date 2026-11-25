# Performance attempts that did not pay

What was tried on the writer and the reader to get closer to the Rust reference, how it was
measured, what it gave, and why it was dropped. It exists so that the effort can be continued —
by this AI or another — without repeating the attempts below, and with the measurement habits
that they cost to learn.

The successes are not the subject of this file: they are in `git log --grep '^perf'` (PR #72 is
the one of 2026-11-24) and in the numbers of [benchmarks.md](benchmarks.md).

Everything here was measured on the same machine (M2 Pro) with the protocol of the next
section; the ratios quoted against Rust come from the CI bench (`bench.json` on the
`bench-data` branch), where Go and Rust run on the same EPYC runner.

## How to measure, and how not to

- **Never compare across sessions or processes.** The benchmark's own `B/op` for the *same*
  `main` code moved from 358 to 425 MiB between two `go test` runs (a `sync.Pool` and the GC
  feed each other), and wall times drift ±5 % between sessions. The protocol that works is:
  change the code, run the bench, `git stash`, run the same bench, `git stash pop`, compare
  within that one session, `min` of 3 to 5 runs.
- **Remove the instrumentation before measuring.** A counter added to `canRows` (a
  `sync.Map.Store` per call) alone moved `gen16/scan_filter` 1 thread from 15.61 to 16.78 ms.
- **A profiled run is about twice as slow**: compare percentages inside a profile, never its
  `ns/op` against an unprofiled run.
- `-bench 'Cases/<name>$'` runs the parent *and* its `/mapped` child, so such a profile mixes
  the mapped and the unmapped paths. Use `Cases/<name>/mapped$` for the mapped one.
- On macOS, `syscall.rawsyscalln` in a scan profile is the kernel copy — and, for a mapped
  file, the page faults — not an application call count. `runtime.madvise ←
  runtime.sysUsedOS` (up to 23 % of a profile) is the reclaim of recycled spans, i.e.
  allocation churn, not a bug (see [benchmarks.md](benchmarks.md), the traps section).
- Small cases are noisy: `tpch_lineitem/write_compact` 4 threads swings 26.8-29.5 ms between
  runs (±10 %). Act on nothing under 5 %, and use `count ≥ 5` for anything below 10 %.
- Gates before every push: `make check` (nocgo, deps, 100 % of the blocks, race detector,
  about 7 minutes) and `~/go/bin/golangci-lint run ./...`. One change per PR, merged only with
  a measured gain on the PR's own benchstat against `main`.

## The attempts

### 1. Row-wise decoding for the encodings that have no path (2026-11-24/25)

The rows path (`internal/array/rows.go`) decodes only the rows a filter or a range keeps, but
`canRows` has no case for every encoding. Implemented the missing `vortex.dict` path (codes row
by row through `childRows`, the values decoded whole, `TakeDict` for the rest); all tests
passed, including the end-to-end range and filter tests added with PR #24.

First find, with a counter in `canRows`, which encodings actually block a path, per file:

| file | no rows path |
|---|---|
| `gen16` | `vortex.dict`, `vortex.bool` (`vortex.alprd` for a filter) |
| `tpch_lineitem` | none |
| `clickbench_hits` | `vortex.runend`, `vortex.sparse`, `vortex.datetimeparts` |

Same-session A/B (`gen16`, min of 5): `scan_filter` 1 thread −3.2 %, 4 threads +4.4 %, mapped
1 thread −7.7 %, mapped 4 threads +7.0 %; `scan_range` 1 thread −0.3 %, 4 threads −6.0 %,
mapped ±1.4 %: **no stable direction** → reverted.

Why it cannot pay, in hindsight: TPC-H has no fallback at all; the ClickBench file is 5,000
rows, so a fallback decodes at most 5,000 rows of a few columns; and on `gen16` the loss of
`scan_range` is 47 % kernel copy and 23 % page reclaim (profiles below), not the fallback
decodes. *Lesson: count the blocked encodings first — ten minutes — before writing a path.*

### 2. Caches for the zstd compressors (2026-11-24)

A writer creates one compressor per column and queue slot (40 or more for a wide file) and the
`sync.Pool` they went into is emptied by the collections that the writer's own allocation
(~350 MiB per `gen16` write) triggers.

- **Package-level, unbounded**: same-session A/B gave −1.1 to −1.6 % of time and **−18 % of
  B/op** on the four `gen16` write cases. Rejected anyway: it keeps 40 compressors
  (≈60-80 MiB) for the life of the process, which no caller can release.
- **On the `*Schema`, bounded by `compKeep` per kind of data** (no package state; the caller
  already holds the schema, and `f.Schema()` returns the same pointer, so the CI bench would
  have seen the reuse): time ±2 %; `B/op` inconclusive (+6 to +27 % in bench runs against
  `main`, while a controlled test of four writes through one schema gave −9 % with ±10 %
  noise); residency 28 compressors (30-100 MiB) at `compKeep = 16`. Written, tested
  (`TestCompressorCache`), then reverted: no measurable gain, real memory held.
- **A cache of `weak.Pointer`** (proposal from Florent): a weak-only cache is emptied by the
  first collection, because nothing holds the compressors after `Close` — strictly worse than
  the `sync.Pool` in place, which keeps one victim generation. Weak pointers only make sense
  *on top of* a strong owner; the owner experiment is the one just above.

### 3. A persistent reader pool for scans (2026-11-25)

`pipeline.go` read each extent with a goroutine of its own and a semaphore token. Profile of
`gen16/scan_filter` 4 threads (after PR #72): `goready→wakep` 45 %,
`notewakeup→startm→pthread_cond_signal` 48 %, syscalls 20 %, GC 6 % — two thirds of the profile
in wakeups.

Replaced it with readers that live for the whole scan (one per core, a mutex queue and a
`cond`, a `stopped` flag broadcast by `close`), so a reader takes the next extent without a
goroutine started, woken and parked. All the scan, rows, filter and range tests passed.

Same-session A/B (`gen16`, min of 5):

| case | pool | base | delta |
|---|---|---|---|
| `scan_filter` 1 thread | 14.15 ms | 14.72 ms | −3.9 % |
| `scan_filter` 4 threads | 9.50 ms | 9.56 ms | −0.7 % |
| `scan_filter` mapped 1 thread | 13.73 ms | 11.84 ms | **+16 %** |
| `scan_range`, four variants | | | ±1 % |

Neutral exactly where it was meant to win (4 threads) and slower where the reads are
instantaneous (mapped), where the queue and the condition cost more than the goroutines they
replaced. Reverted.

*Lesson: the wakeups a profile attributes are mostly waiting time, not reclaimable CPU, and
the gap between the ideal parallel time and the measured one (9.5 ms against an ideal 3.7)
is not a budget that can be spent: converting one synchronisation into another moved nothing.*

### 4. A bigger compression queue (2026-11-24)

`minQueue` 16 → 64, to have fewer `drain()` barriers in the writer: ±5 % over the eight write
cases, i.e. noise. The barriers were therefore not the bottleneck, and the "compress the queue
while the rows of the next chunk are filled" refactor that this had motivated was **designed
but never written** — a risky refactor avoided by a two-line experiment.

### 5. `Compression = None` with no dictionary (2026-11-24)

`column.decide` chooses a dictionary even when the chunks are not compressed, which Rust's
`none` does not ("the compressor has no scheme, every column as it is"). Removing that branch,
same session, min of 5:

| case | 1 thread | 4 threads | file size |
|---|---|---|---|
| `tpch/write_none` | 19.56 → 16.90 ms (−11 %) | 7.27 → 9.73 ms (**+34 %**) | 6.27 → 11.20 MB (**+79 %**) |
| `clickbench/write_none` | 17.12 → 7.82 ms (−54 %) | 6.16 → 5.65 ms (−8 %) | 2.12 → 5.44 MB (**+157 %**) |
| `gen16/write_none` | 249.8 → 228.8 ms (−8 %) | 119.1 → 131.2 ms (**+10 %**) | 141.1 → 172.8 MB (+22 %) |

Rejected: the dictionary nearly halves a TPC-H file, it is pinned by
`TestDictionaryOfNumbersAndStrings` (« the probe without a compressor »), and the worst case
(2.09×) only improved by 11 % while the four-thread writes regressed. This is one of the two
open decisions below.

### 6. Micro-optimisations of the Pco coder (2026-11-24)

`internal/pco` is 21-31 % of a `write_compact` (on `tpch`: `sortLatents` 11.5 %, `train` 9.9 %,
`page`+`symbols` 15 %; on `gen16`: 5.9 %, 4.5 %, 11.4 %). Hoisted the slices of the training
dynamic program and of `bin`'s binary search — outputs byte-identical (golden tests green) —
and the end-to-end A/B was 64.41/64.56, 27.07/26.96, 40.11/40.43, 21.85/21.68 ms: **±1 %, below
the noise floor**. Reverted.

*Lesson: the win is not inside those kernels but in how many times they run — see the
time-aware encoding choice below.*

### 7. The memory of the scans in a slot instead of a pool (2026-11-24)

`File.mem` was a `sync.Pool`, which a collection empties (its victim set is dropped at the
second collection), so repeated scans of one file can each start cold. Replaced with an
`atomic.Pointer` slot: `gen16` scan times ±3 %, `B/op` unchanged (11.9 → 12.3 MiB for
`scan_full`) — the pool was not the problem for a file whose scan already reuses its memory
from batch to batch (the small files were the problem, and PR #72 fixed them). Reverted.

### 8. Larger read merging (2026-11-24)

`localGap`/`localMaxRead` 4 KiB / 1 MiB → 64 KiB / 4 MiB → 1 MiB / 16 MiB: ±3 %, and at
1 MiB `gen16/scan_range` 4 threads got 7.5 % *worse* (more bytes copied). Reverted.

Merging is already effective — counted with a wrapping `io.ReaderAt`: `gen16/scan_3col` is
98 preads of 510 KiB for 50 MiB, `scan_filter` 112 of 447 KiB, a whole `scan_full` 1,016 of
501 KiB. The syscall share of a profile is therefore the copy, not the number of calls.

## Where the time actually goes (profiles, 2026-11-24/25, M2 Pro)

- `gen16/scan_range` 1 thread (1.88× against Rust locally): 47 % `pread` (kernel copy),
  23 % `sysUsedOS` (page reclaim), about 30 % decode.
- `gen16/scan_3col` 1 thread (1.84×): 49 % `pread`, then `gatherRanges` 12.9 %,
  `unpackBlock` 12.9 %, `alpFloats` 8.6 %, `dict.take` 15.8 %.
- `gen16/scan_filter` 4 threads: wakeups 45 % + 48 % (attempt 3: measured not recoverable).
- `tpch/write_compact` 1 thread (1.78×): Pco ≈31 %, zstd ≈13 %, statistics and UTF-8 ≈15 %,
  GC ≈5 %.
- `gen16/write_compact` 1 thread: zstd ≈19 %, `memmove` 10.4 % (of which `growslice` 32 %,
  `appendRows` 27 %, zstd `encodeAll` 23 %), Pco ≈22 %, GC ≈10 %.
- `gen16/write_compact` allocations: `(*column).reserve` (the row buffers) about half, zstd
  `ensureHist` and encoder creation about a fifth.

## Open decisions, waiting on the owner

1. **What `None` promises.** Keeping dictionaries and zone statistics (small files) or dropping
   them for speed: `tpch/write_none` is 2.0× Rust and Rust writes plain. Attempt 5 measured the
   cost of dropping them.
2. **A time-aware encoding choice.** The choice optimises size only; a rule like "among the
   schemes within N % of the smallest, take the cheapest to encode" could touch every
   `write_*` case (1.1 to 2.15×), at the cost of file size — there is headroom today (0.98 to
   1.05 of the reference size).
3. **What the site leads with.** `rust.html` leads with the `OpenFile` ratios: 20 cases above
   1. With `OpenMapped` it is 7, because the Rust reader reads mapped.
4. **The zstd level of `Default`** (`SpeedFastest` instead of `SpeedDefault`): spends size
   headroom for time; the size tests would decide.

## Queued, not yet tried

- **The single-thread decode kernels of `gen16`** (ALP `alpFloats`, `gatherRanges`,
  `unpackBlock`): this is the 1.4-1.6× of the five scan cases still above 1 (`scan_filter`
  4 threads 1.51×, `scan_3col` 1.41×, `scan_filter` 1.28×, `scan_range` 1.13×, `scan_full`
  1.10×). No assembly is allowed, so the levers are bounds-check elimination, unrolling and
  batching inside `internal/array`.
- **The zstd experiment** of decision 4.
- `gen16/open` is the worst ratio of the published table (2.25× at 1 thread, 2.0× at 4
  threads): neither a scan nor a write, but public — footer, schema and zone parsing plus the
  allocations of `Open`, against 0.30 ms for Rust.

## Standing of the table (bench of `main` at `edbf7b3`, 2026-11-24 11:32)

60 cases are measured against Rust on the same runner: **20 are slower with `OpenFile`, 7 with
`OpenMapped`**. Before that day's pull requests it was 29 of 60, with 16 scan cases above 1
against 5 today. The 12 writes above 1 are `tpch_lineitem` (1.51-2.15×), `clickbench_hits`
`write_none` 1.77× and `gen16` (1.13-1.66×); the three `open` cases are `gen16` 2.25× and
2.0× and `clickbench` 1.12×.
