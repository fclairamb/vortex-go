# Performance attempts that did not pay

What was tried on the writer and the reader to get closer to the Rust reference, how it was
measured, what it gave, and why it was dropped. It exists so that the effort can be continued —
by this AI or another — without repeating the attempts below, and with the measurement habits
that they cost to learn.

The successes are not the subject of this file: they are in `git log --grep '^perf'` (PR #72 is
the one of 2026-11-24) and in the numbers of [benchmarks.md](benchmarks.md). The session of
2026-11-26 added a third kind: the **candidates**, measured with a consistent direction but
not yet pushed, saved as patches and listed below the attempts. A second session the same day
found that the scans had lost a factor 2 on `main` the day before, which no local measurement
showed: it is told in [the regression of 0.94](#the-regression-of-094-found-in-the-ci-history-2026-11-26),
with the leads it opened.

Everything here was measured on the same machine (M2 Pro) with the protocol of the next
section; the ratios quoted against Rust come from the CI bench (`bench.json` on the
`bench-data` branch), where Go and Rust run on the same EPYC runner.

## How to measure, and how not to

- **Never compare across sessions or processes.** The benchmark's own `B/op` for the *same*
  `main` code moved from 358 to 425 MiB between two `go test` runs (a `sync.Pool` and the GC
  feed each other), and wall times drift ±5 % between sessions. The protocol that works is:
  change the code, run the bench, `git stash`, run the same bench, `git stash pop`, compare
  within that one session, `min` of 3 to 5 runs.
- **On a loaded machine, interleave prebuilt binaries.** On 2026-11-26 another project's
  tests held the load average between 7 and 30 for the whole session: consecutive runs of the
  *same* binary swung 2× (387 µs then 831 µs for `gen16/open`), and even the min-of-5 protocol
  misled. What worked: `go test -c` one binary per variant, then run them **alternately**
  (`base, new, base, new, …`), `-benchtime 2x` for the slow cases, and judge by the direction
  of the pairs, not by absolute times. A gain that shows in 4 of 5 pairs under that load is
  real direction; the CI benchstat on an idle runner decides the number.
- **Read the CI history before choosing a target.** `history.jsonl` on the `bench-data` branch
  has the Go and the Rust `ns/op` of every case for every commit of `main`. Print one case
  across the last commits: a step in the *Go* column on the same runner CPU is a regression
  of ours, whatever the ratio did. This is how the factor 2 of #73 was found, after a whole
  session of local A/B that could not see it.
- **The generated bench file is written once, by the writer of that day.**
  `.cache/bench/gen16-<rows>.vortex` is reused as long as it exists, so after a change of the
  writer the local scans read a file that `main` no longer writes. On 2026-11-26 the file was
  of 2026-11-21: FSST strings, where the writer had picked OnPair since #73, and every local
  scan number of the first session was measured on it. Delete it after any writer change (it
  is rewritten in 7 seconds), and fetch the fixtures of the pinned version (`make fixtures`):
  without them the `tpch_lineitem` and `clickbench_hits` cases skip in silence. The CI
  regenerates both on every run.
- **A case of less than a millisecond needs hundreds of iterations.** With `-benchtime 5x`,
  `clickbench_hits/scan_filter` gave 0.18 ms for one binary and 0.44 to 0.58 ms for another
  that ran the same code on that path; with `-benchtime 500x` both gave 0.17 to 0.18 ms.
- `-bench` splits its pattern on `/` before it compiles each part: an alternation that spans
  a slash (`Cases/(gen16/scan_full|tpch_lineitem/scan_full)$`) matches nothing, and prints
  nothing. Write `Cases/(gen16|tpch_lineitem)/scan_full$`.
- **Remove the instrumentation before measuring.** A counter added to `canRows` (a
  `sync.Map.Store` per call) alone moved `gen16/scan_filter` 1 thread from 15.61 to 16.78 ms.
- **A profiled run is about twice as slow**: compare percentages inside a profile, never its
  `ns/op` against an unprofiled run.
- `-bench 'Cases/<name>$'` runs the parent *and* its `/mapped` child, so such a profile mixes
  the mapped and the unmapped paths. Use `Cases/<name>/mapped$` for the mapped one.
- On macOS, `syscall.rawsyscalln` in a scan profile is the kernel copy — and, for a mapped
  file, the page faults — not an application call count. `runtime.madvise ←
  runtime.sysUsedOS` (up to 23 % of a profile) is the reclaim of recycled spans, i.e.
  allocation churn, not a bug (see [benchmarks.md](benchmarks.md), the traps section). It is
  also **darwin only**: `sysUsedOS` does nothing of the kind on Linux, where the ratios
  against Rust are measured. Do not spend a change on that share.
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

### 9. The `close()` of darwin, and the kqueue hypothesis (2026-11-26)

The profile of `gen16/open` (1 thread, `OpenFile` + `Close` in the loop) put **58 % of the
time in `syscall.Close`** and 16 % in `runtime.kevent` under `netpoll` at the start-the-world
of a GC. On darwin `os.Open` registers a regular file with the kqueue netpoller (on Linux
epoll refuses it), and a `close()` of a watched fd is known to be costly; the hypothesis was
that opening through `syscall.Open` + `os.NewFile` (which does not register a blocking fd)
would skip it.

Micro-benchmark of the three fd handlings, same session: `os.Open`+`Stat`+`Close` 12.6-15.4 µs,
`syscall.Open`+`Fstat`+`syscall.Close` 13.1-19.2 µs, `syscall.Open`+`os.NewFile`+`Close`
12.5-14.1 µs: **no difference**. The registration alone costs nothing measurable. The close
of the profile is slow only when the open's own allocation churn (380 KiB, 3,494 allocs per
open) drives GC after GC, each start-the-world calling `kevent`: with the layout arena below
(69 allocs per open) the close share fell from 58 % to 26 % of the profile without touching
the fd handling. *Lesson: a syscall's share of a profile can be the churn around it, not the
syscall; test the hypothesis alone before rewriting the path.* No fd change was made.

### 10. `MADV_WILLNEED` when mapping (2026-11-26)

An uncommitted edit in `mapped_unix.go` called `syscall.Madvise(data, MADV_WILLNEED)` after
`Mmap`. It does not compile on darwin (`syscall.Madvise` is Linux-only; it would take
`syscall.Syscall(SYS_MADVISE, …)` there), and it cannot pay on the bench anyway: every case
reads with a **warm page cache**, where the pages are already present and `WILLNEED` does
nothing — the minor faults that remain are PTE installs, which only `MAP_POPULATE` (Linux
only) would skip. On a cold cache it would read the whole file ahead, helping `scan_full`
and hurting the selective scans. Left in `git stash` (`wip: madvise willneed`), not pursued.

### 11. A profile-guided build (2026-11-26)

PGO cannot ship with a library (the profile belongs to the build of the main package), but
it tells how much inlining and devirtualisation the compiler leaves on the table. A profile
of six cases (`gen16` and `tpch_lineitem`, scans and `write_default`), then
`go test -c -pgo=<profile>`, interleaved with the plain binary, 3 pairs:
`gen16/scan_full` 355/359/367 against 416/358/368 ms, `scan_3col` 24.5/22.2/22.5 against
22.5/22.5/23.7, `scan_filter` 17.0/16.0/16.4 against 15.5/15.7/16.1, `gen16/write_default`
956/970/997 against 1067/979/1031, `tpch/write_default` 53.7/52.6/52.3 against
52.4/52.6/51.8: **±2 %, no direction**. The hot paths are kernels over slices, already
generic-specialised and free of interface calls: there is nothing for PGO to inline.
*Lesson: no need to tell users to build with a profile of ours.*

### 12. The verdict of the reference on a dictionary of bytes (2026-11-26)

The reference makes no trial of a dictionary of integers: `IntDictScheme` returns a ratio
computed from the exact distinct count of the whole array (the values, plus codes of the
bits of their number, bit-packed or in runs of 32 more bits). We measure the dictionary on
the sample, and build it on all the rows when the sample says it wins. The hypothesis for
the UUID columns of the dbbat archive: a sample of a few UUIDs shows a few byte values, so
the dictionary wins there, is built over 1.6 M bytes, comes out no smaller, and is dropped.
A synthetic column agreed (random bytes, and 50 UUIDs in runs, both end `plain`).

Prototype: at the top of a chunk, for 8-bit integers, count the distinct values in a table
of 256 and leave the dictionary out of the candidates when the estimate of the reference
says it cannot be smaller (`.cache/perf2/attempt-byte-dict-verdict.patch`). Every test
passed, the files did not change, and **the time did not either**: `dbbat_narrow/write_default`
60.7 and 57.7 ms against 59.7 and 55.7 without it. The profile showed why: `Dict[uint8]` was
still there (11 %), and the new count cost 9 %. On the real rows the dictionary is
legitimate for the UUIDs that repeat (the connection and the query): their bytes come in
long runs and the estimate is above 1. Only the random row UUID was a dictionary built for
nothing, and counting its values costs what building it did.

*Lesson: a synthetic column is not the rows of the bench. Print the tree of the real file
first.* What may still pay is the reference's whole rule, not this prune: with the exact
count, no trial of the dictionary on the sample either, for every width (lead 1).

## Candidates of 2026-11-26, measured, waiting for a PR

> **Read with the second session in mind.** The scan numbers of this section (candidate C,
> and the `open` of candidate A) were measured on the bench file of 2026-11-21, written before
> #73: its strings were FSST, where `main` writes OnPair. Candidate C must be measured again
> on a fresh file before it is believed, and after #81: the batch is the granularity of the
> string decode too, which was the slow part. The fresh file has 2,647 allocations per open,
> not 3,494. Candidate B (a write) does not read that file and stands.

Three changes came out of the session with a consistent direction under the load. Each is
saved as a patch in `.cache/perf/` (the tree was left clean), each kept the root package at
100 % statement coverage, and each needs its own PR and the CI benchstat against `main`
before it is believed. The numbers below are interleaved pairs on the loaded M2 Pro, not
the protocol's clean min-of-5.

### A. An arena for the layout tree at open (`candidate-layout-arena.patch`): pushed

The allocation profile of `gen16/open`: `layoutNode` was **85 % of the objects** (3,494
allocs, 380 KiB per open) — one heap node, one `[]uint32` of segment indices and one
`append` growth of the children per node, 1,562 nodes for 16 columns × 10 M rows. The patch
adds `AppendUint32s`/`AppendSegments` beside the existing `AppendUint16s`, and a
`layoutArena` in `footer.go`: the nodes, the child lists and the segment indices come from a
few doubling blocks (64, 128, …), never moved, kept alive by the tree; the sub-slices are
capped so no list can append into the next.

- allocs **3,494 → 69** (−98 %), `B/op` 380 → 403 KiB (+6 %, the rounding of the last block);
- time: 5/5 then 4/5 interleaved pairs faster, −5 to −20 % (one +35 % outlier at load 18);
- the GC churn it removes also cut the `close()` share of the profile from 58 % to 26 %
  (attempt 9);
- what remains in the profile is the flatbuffer walk itself (`Children`, `List.At`, the
  field accessors): ~48 % of the open, the irreducible parse.

**Pushed on 2026-11-28**, written again rather than from the patch: the blocks are of 64
elements, and a list of 16 or more is allocated on its own, so that what is left of a block
is not lost (the doubling blocks cost +6 % of bytes). Interleaved, 12 runs each, on the file
of after #73: `gen16/open` 381 → 300 µs (**−21 %**, p=0.000), mapped 488 → 398 µs (−18 %),
243 → 213 KiB (−12.5 %), 2,643 → 92 allocations. `TestOpenAllocatesByBlocks` pins it.


### B. zstd at level 1 for the trials only (`candidate-zstd-trial-fast.patch`)

Decision 4 measured, in three variants (gen16 `write_compact`, 1 thread, interleaved pairs;
the size gate run against the 0.93.0 fixtures — the pinned 0.94.0 ones were not on this
machine, the source data is the same):

| variant | time (pairs) | B/op | gen16 file | size gate |
|---|---|---|---|---|
| `SpeedFastest` everywhere | 3/4 faster, −10 % | **−19 %** | −0.15 % | **FAIL**: clickbench compact 0.894 > 0.881 (+3.6 %) |
| fast for numbers, level 3 for strings | ±3 %, noise | −0.25 % | identical | pass |
| fast when `e.measure` (the trial encoder), level 3 for the frames kept | 3/4 faster, −7 to −10 % | −3.4 % | +0.25 % | pass (clickbench 0.862) |

The middle variant is the diagnosis: **the zstd time of `gen16` is string trials that lose** —
the file bytes are identical when only the numbers change level, and the whole-fast variant
changes the gen16 file by only 0.15 % while cutting 19 % of the memory. The compressor
already measures the candidates on a sample in a separate encoder marked `Measure()`; the
patch gives that encoder `SpeedFastest` (one `if e.measure` in `zstdEncoder`), so the ranking
is cheaper and every frame that is kept is still cut at level 3 as the reference cuts them.
The ranking sees slightly larger zstd samples, so zstd wins marginally less often: the gen16
file grew 0.25 %, the gate datasets did not move (tpch identical, clickbench −80 bytes).
`TestDBBatSize` passes in all three variants (the whole-fast file was even 2.1 % smaller).

### C. Batches of 32,768 rows instead of 8,192 (one line: `maxBatchRows`)

The scanner decodes through the rows path a batch at a time, so the batch is the granularity
of the decode kernels: 4× fewer calls, 4× longer loops. Interleaved pairs, gen16:

| case | 1 thread | 4 threads | mapped 1 thread |
|---|---|---|---|
| `scan_full` | 2/2 faster, −4 to −13 % | 2/3 faster, −15 to −18 % | ±3 % |
| `scan_3col` | 2/2 faster, −2.5 to −34 % | 2/2 faster, −4 to −49 % | not run |
| `scan_filter` | ±2 %, but `B/op` +60 to +90 % | not run | not run |
| `scan_range` | mixed (−25 %, +3 %) | not run | mixed, `B/op` +130 % |

65,536 was also built and measured: **worse than 8,192 everywhere** (scan_full +2 to +10 %,
scan_3col +8 to +11 %, more bytes) — the working set of a batch (64 Ki rows × 16 columns)
leaves the cache. 32,768 is the sweet spot of the three. All scan, rows, query and range
tests pass at 32,768.

The catch is not technical: **8,192 is public** — `api.txt`, `docs/interface.md` and the
`Scan` doc comment promise it, and the filter/range paths pay for the larger batches in
memory (the selection buffers, +0.6 to +6 MiB per scan, still small). It is the owner's
call, and the docs change with the constant.

## The regression of 0.94, found in the CI history (2026-11-26)

The question of the second session was "what other approaches are there". The answer came
from the CI history, not from a profile: printing a few cases across the last commits of
`history.jsonl` showed the **Go** side stepping up at `df838b7` (#73, the update to vortex
0.94.0), on the same EPYC 7763 runner as the commit before it.

| case | `884b840` (0.93) | `df838b7` (0.94) | Rust, both |
|---|---|---|---|
| `gen16/scan_full` 1 thread | 516 ms | 1,022 ms | 430 ms |
| `gen16/scan_range` 1 thread | 58 ms | 111 ms | 46 ms |
| `tpch_lineitem/scan_range` 1 thread | 1.0 ms | 3.6 ms | 1.0 ms |
| `gen16/write_default` 1 thread | 1,141 ms | 1,546 ms | 1,115 → 1,139 ms |
| `tpch_lineitem/write_compact` 4 threads | 78 ms | 128 ms | 42 → 50 ms |
| `dbbat_narrow/write_default` 4 threads | 147 ms | 144 ms | **300 → 39 ms** |

Three different things are in that table.

1. **The scans read OnPair, and the OnPair decoder was the first draft.** #73 made the writer
   rank the string schemes as 0.94 does, and OnPair now wins most string columns (the
   reference writes it too: the TPC-H fixture has it). FSST had three pull requests of
   decoding work (#33, #47, #56); `decodeOnPair` had none: it allocated its bytes and its
   ranges outside the arena (**794 MB per `gen16/scan_full`**, against 12 MiB), widened its
   four integer children to `[]int`, tested every token, and had no rows path, so a range of
   10 % of the rows decoded every chunk whole (`tpch/scan_range` took as long as
   `scan_full`). **Fixed by #81**: one pass over the codes with a 16-byte copy per token, as
   `onpair::decompress_into` does, the arena, and a rows path.
2. **The writes do more, as the reference's do** (OnPair trains on half of a sample, and a
   dictionary is measured on the whole chunk): the commit message of #73 says so, 25 to 70 %.
   This one is the price of the format, and it is where the time-aware choice (decision 2)
   would act.
3. **The reference got 7.5 times faster on `dbbat_narrow/write_default`** between 0.93 and
   0.94, and we did not move: 3.8× at 4 threads, the worst ratio of the table. Its profile
   on our side led to the second pull request below. What the reference stopped doing is
   still to be found (lead 1 below).

### Pushed

**#81, the OnPair decoder.** Interleaved pairs, 1 thread, 3 of 3 in the same direction:

| case | `main` | #81 |
|---|---|---|
| `gen16/scan_full` | 580 ms, 794 MB/op | 340 ms, 11 MB/op |
| `gen16/scan_range` | 65 ms, 89 MB/op | 41 ms, 7 MB/op |
| `tpch_lineitem/scan_full` | 2.7 ms | 1.1 ms |
| `tpch_lineitem/scan_range` | 2.6 ms | 0.7 ms (1.1 ms without the rows path) |
| `clickbench_hits/scan_3col`, `scan_filter` | 0.33 ms | 0.17 ms |
| `clickbench_hits/scan_full` | 2.0 ms | 1.5 ms |

The file of before #73 (FSST strings) still scans faster with the same binary: 295 ms
against 340 to 370 for `scan_full`, 35 against 41 ms for `scan_range`. That is what is left
between the two encodings on this machine (lead 3).

**#82, the dictionaries of numbers and the statistics of integers.** The profile of
`dbbat_narrow/write_default` had **53 % of the write in the bytes of the three UUID
columns**, compressed as 8-bit integers (1.6 M values a column): `statsOfInts[uint8]` 19 %
(three null tests and two guarded comparisons per value), and `encode.Dict[uint8]` 22 %,
whose `classify` hashed every one-byte value with `maphash.Bytes` and compared it with
`bytes.Equal`, the path of the strings. Three changes, the files **byte-identical**:

- `classify` of numbers reads them as words: one multiplication for the slot, one integer
  comparison, and integers of 8 or 16 bits that have fewer values than the table has slots
  get a slot each (a table of 256 or 65,536, no probing);
- `statsOfInts` without nulls is one comparison with the row before, a `min` and a `max`;
- the codes of a dictionary are written by `storeWords`, and `putWidth` is one store of the
  width instead of a loop over the bytes (it is called per row by FoR, zigzag, run-end,
  sparse and the children of OnPair).

| case, 1 thread | `main` | first two | all three |
|---|---|---|---|
| `dbbat_narrow/write_default` | 97-103 ms | 61-64 ms | 57-59 ms (**−41 %**) |
| `dbbat_wide/write_default` | 261-300 ms | 222-254 ms | 224-241 ms (−14 %) |
| `dbbat_narrow/write_compact` | 57-61 ms | 52-54 ms | 54-57 ms (−8 %) |
| `tpch_lineitem/write_default` | 53-58 ms | 48-54 ms | 47-53 ms (−9 %) |
| `tpch_lineitem/write_compact` | 97-110 ms | 91-104 ms | 92-99 ms (−6 %, noisy) |
| `clickbench_hits/write_default` | 100-112 ms | 95-96 ms | not run |
| `gen16/write_default`, `write_compact` | 966-1004, 1127-1150 ms | ±5 % | ±5 %: no direction |

### Leads it opened, not yet tried, by expected value

1. **What the reference stopped doing on the dbbat archive.** 300 → 39 ms between two
   releases is not a kernel, it is a path not taken any more. After #82 our
   `dbbat_narrow/write_default` is still 39 % UUID bytes (`BitPackBest` 16 %, of which
   `Histogram` 7 %; `Dict` 16 %), 24 % OnPair, 13 % filling the rows. Read so far: the
   `Canonical::FixedSizeList` arm of `vortex-compressor/src/compressor/cascade.rs` sends the
   elements through the generic `compress`, as we do, so the reference does not skip the
   UUID bytes; and its `IntDictScheme` decides from the exact distinct count with no trial
   (attempt 12 tried the narrow version of that rule, for nothing). To do, in this order:
   write the same rows with both (`tools/bench-rust` has the generator), print the encoding
   tree of each column of the two files, and time the reference per column. Then the choice
   is between its whole rule for the dictionaries of integers (a change of what we write,
   judged by the size gate and the vectors) and whatever the trees show on the JSON column.
2. **A CPU profile from the CI runner.** Everything here is profiled on darwin/arm64 and
   judged on linux/amd64. Two shares that dominate the Mac profiles do not exist there in
   that form: `runtime.madvise` (22 % of `gen16/scan_full` after #81) is darwin only, and
   `pread` (37 %) is a different kernel. `bench.yml` could run the five worst cases once
   more with `-cpuprofile` and upload the profiles as an artifact: until then the ranking of
   the scan leads below is a guess.
3. **The OnPair decode, what is left.** After #81, `onPairBulk` is 14 % of `gen16/scan_full`
   (two loads of the offsets table and two bounds-checked reads of the dictionary per
   token: a table of `offset<<8 | length`, one load, and one slice of 16 bytes would halve
   the loads); the rows path reads the dictionary offsets again at every batch (they could
   stay on the node, like `rowKids`); and codes of a width other than 8 or 16 bits fall to
   the row-by-row path. Measure each on a micro-benchmark of the kernel (ns per token): the
   end-to-end noise of this machine hides anything under 5 %.
4. **Filters on strings in the compressed domain.** The reference compares and matches
   `LIKE` on OnPair without decoding (`encodings/onpair/src/compute/compare.rs`, `like.rs`):
   an equality is a search of the token sequence of the literal. We decode every row of the
   column, then compare. No bench case filters on a string today, so it would need its case
   first (`bench/cases.json`, on the three harnesses).
5. **The layout tree on demand at open.** Candidate A removes the allocations of
   `gen16/open`; the 48 % that remain are the walk of 1,562 flatbuffer nodes, of which a
   scan of 3 columns needs 3 subtrees. The reference builds the children of a layout when
   they are asked for. Cost: a malformed layout is then reported by the scan that reaches
   it, not by `OpenFile`, and `open_test.go` pins the errors of a malformed layout at open:
   it is a decision before it is a patch.
6. **One pass for the statistics and the histogram of integers.** `ints` runs `statsOfInts`
   (min, max, runs), then `spread` (distinct, top) on a trial, then `BitPackBest` runs
   `Histogram` (bit lengths) over the same values. For 8-bit values the three are one table
   of 256 counters filled once: min, max, distinct, top and the histogram all come from it.
7. **The ranges of the strings as 32 bits.** A decoded string column is `Data` plus
   `Starts` and `Ends`, two `[]int`: 16 bytes of ranges per row, more than the bytes of a
   short string (the `city` of `gen16`). One `[]uint32` of `rows + 1` offsets is 4 bytes per
   row when the rows follow each other, which is every decode but `take`. It touches
   `Column.Bytes` and every string decoder: large, and to be measured first on a prototype
   of one decoder.
8. **`GOAMD64=v3` on the bench runner**, as an information: it lets the compiler use BMI2
   and the three-operand forms in the bit-unpacking and the shifts. It is the user's build
   flag, not ours, but if it gives 5 % on the scans it belongs in the README next to
   `OpenMapped`.

## Where the time actually goes (profiles, 2026-11-24/25/26, M2 Pro)

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
- `gen16/scan_full` 1 thread on the file `main` writes since #73, before #81: `pread` 44 %,
  `onPairDict.decode` 23 %, the widening of its children to ints 10 %, `madvise` 6 %. After
  #81: `pread` 37 %, `madvise` 22 % (darwin only), `onPairBulk` 14 %, the first read of each
  segment (`array.open`, a cache miss) 6 %, `unpackBlock` 6 %.
- `tpch_lineitem/scan_range` before #81: `pread` 39 % and `kevent` under a start-the-world
  36 %, the collections that 4 MB of garbage per scan trigger.
- `dbbat_narrow/write_default` 1 thread: the bytes of the UUID columns as 8-bit integers
  53 % (after #82: 39 %), OnPair 16 % (24 %), filling the rows 8 % (13 %).
- `gen16/open` 1 thread (2026-11-26): `syscall.Close` 58 % and `kevent` 16 % — both the GC
  churn of the open's 3,494 allocs, not the fd handling (attempt 9); `layoutNode` 13 % of the
  time but 85 % of the objects. With the arena (candidate A): the flatbuffer walk ~48 %,
  close 26 %, the rest the reads.

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
4. **The zstd level of `Default`** (`SpeedFastest` instead of `SpeedDefault`): **measured on
   2026-11-26, see candidate B.** Everywhere-fast fails the size gate (clickbench +3.6 %);
   fast for the trials only passes it and keeps most of the time and memory gain. What
   remains for the owner is whether +0.25 % of gen16 file size is worth −7 to −10 % of
   `write_compact`.

## Queued, not yet tried

The leads of the second session are [above](#leads-it-opened-not-yet-tried-by-expected-value);
these two are the ones of the first.

- **The single-thread decode kernels of `gen16`** (ALP `alpFloats`, `gatherRanges`,
  `unpackBlock`): this is the 1.4-1.6× of the five scan cases still above 1 (`scan_filter`
  4 threads 1.51×, `scan_3col` 1.41×, `scan_filter` 1.28×, `scan_range` 1.13×, `scan_full`
  1.10×). Read on 2026-11-26: the levers named here are **already in place** — `alpFloats`
  reslices once so the loop has no bounds check, `gatherRanges` looks up a stack table of
  pairs for a small dictionary, `unpackBlock` works 8 bytes at a time with one shift and one
  mask per lane, `gatherValidity` builds a byte from 8 codes with no test. What is left is
  the SIMD the reference has and we cannot (no assembly). The structural lever found instead
  is the batch size (candidate C): fewer, longer calls to these same kernels.
- `gen16/open` is the worst ratio of the published table (2.25× at 1 thread, 2.0× at 4
  threads): candidate A removes 98 % of its allocations; what remains is the flatbuffer
  walk, which is the same work the reference does.

## Standing of the table (bench of `main` at `3bf77a5`, 2026-11-25 13:00, vortex 0.94.0)

60 cases are measured against Rust on the same runner: **33 are slower with `OpenFile`**,
where it was 20 the day before, at `edbf7b3` on 0.93.0 (and 29 before the pull requests of
2026-11-24). The ten worst, before #81 and #82:

| case | threads | Go | Rust | ratio |
|---|---|---|---|---|
| `dbbat_narrow/write_default` | 4 | 131 ms | 35 ms | 3.79 |
| `tpch_lineitem/scan_range` | 1 | 4.1 ms | 1.2 ms | 3.27 |
| `gen16/scan_full` | 1 | 1,206 ms | 440 ms | 2.74 |
| `tpch_lineitem/scan_range` | 4 | 3.9 ms | 1.4 ms | 2.74 |
| `gen16/scan_range` | 1 | 135 ms | 50 ms | 2.68 |
| `dbbat_wide/write_default` | 4 | 227 ms | 91 ms | 2.50 |
| `tpch_lineitem/write_compact` | 4 | 126 ms | 53 ms | 2.40 |
| `gen16/open` | 4 | 0.42 ms | 0.19 ms | 2.29 |
| `dbbat_wide/write_default` | 1 | 423 ms | 189 ms | 2.24 |
| `tpch_lineitem/write_default` | 4 | 61 ms | 29 ms | 2.11 |

Rows 2 to 5 are the OnPair decoder (#81), rows 1, 6 and 9 the UUID bytes (#82 and lead
1). The runner changes from one run to the next (EPYC 7763, EPYC 9V45, Xeon 6975P, Xeon
8573C in two days): compare the Go column of two commits only when the CPU is the same.
