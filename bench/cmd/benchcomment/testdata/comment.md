<!-- vortex-go:bench -->
### Benchmarks against `main`

PR at [`0123456`](https://github.com/o/r/commit/0123456789abcdef), `main` at [`fedcba9`](https://github.com/o/r/commit/fedcba9876543210). Shared runner (AMD EPYC 9V45 96-Core Processor), the numbers are for information. [Run](https://github.com/o/r/actions/runs/1).

| Metric | Geomean | Better | Worse | Unchanged |
|---|---:|---:|---:|---:|
| sec/op | 🟢 -3.79% | **1** | **1** | 2 |
| B/op | 🔴 +3.23% | 0 | **1** | 3 |
| allocs/op | +0.00% | 0 | 0 | 4 |
| decoded-B/s | 🟢 +34.43% | **1** | 0 | 0 |
| rows/s | 🟢 +34.25% | **1** | 0 | 0 |
| file-bytes | 🟢 -1.92% | **1** | 0 | 0 |
| internal/encode: sec/op | 🔴 +1.74% | 0 | 0 | 1 |
| internal/encode: B/op | +0.00% | 0 | 0 | 1 |
| internal/encode: allocs/op | +0.00% | 0 | 0 | 1 |
| internal/encode: values/s | +0.00% | 0 | 0 | 1 |

#### Worse (2)

| Benchmark | Metric | main | PR | Change |
|---|---|---:|---:|---:|
| `WriteRows` | sec/op | 1.10s ±3% | 1.30s ±4% | 🔴 +18.66% |
| `WriteRows` | B/op | 286MiB | 315MiB | 🔴 +10.00% |

#### Better (2)

| Benchmark | Metric | main | PR | Change |
|---|---|---:|---:|---:|
| `Decode/alp` | sec/op | 131µs ±4% | 97.7µs ±2% | 🟢 -25.52% |
| `WriteRows` | file-bytes | 49.6MiB | 48.6MiB | 🟢 -1.92% |

<details><summary>Every result</summary>

#### sec/op

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Open` | 59.8µs ±3% | 60.3µs ±2% | ~ | p=0.420 n=6 |
| `Decode/alp` | 131µs ±4% | 97.7µs ±2% | 🟢 -25.52% | p=0.002 n=6 |
| `WriteRows` | 1.10s ±3% | 1.30s ±4% | 🔴 +18.66% | p=0.002 n=6 |
| `NewOne` | — | 884ns ±1% | new |  |

#### B/op

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Open` | 113KiB | 113KiB | ~ | p=1.000 n=6 |
| `Decode/alp` | 2.00KiB | 2.00KiB | ~ | p=1.000 n=6 |
| `WriteRows` | 286MiB | 315MiB | 🔴 +10.00% | p=0.002 n=6 |
| `NewOne` | — | 64B | new |  |

#### allocs/op

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Open` | 120 | 120 | ~ | p=1.000 n=6 |
| `Decode/alp` | 12 | 12 | ~ | p=1.000 n=6 |
| `WriteRows` | 12.0k | 12.0k | ~ | p=1.000 n=6 |
| `NewOne` | — | 1 | new |  |

#### decoded-B/s

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Decode/alp` | 61.0MB/s ±3% | 82.0MB/s ±2% | 🟢 +34.43% | p=0.002 n=6 |

#### rows/s

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Decode/alp` | 7.63G ±4% | 10.2G ±2% | 🟢 +34.25% | p=0.002 n=6 |

#### file-bytes

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `WriteRows` | 49.6MiB | 48.6MiB | 🟢 -1.92% | p=0.002 n=6 |

#### internal/encode: sec/op

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Encode/bitpacked` | 44.9µs ±3% | 45.7µs ±3% | ~ | p=0.589 n=6 |

#### internal/encode: B/op

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Encode/bitpacked` | 0B | 0B | ~ | p=1.000 n=6 |

#### internal/encode: allocs/op

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Encode/bitpacked` | 0 | 0 | ~ | p=1.000 n=6 |

#### internal/encode: values/s

| Benchmark | main | PR | Change | p |
|---|---:|---:|---:|---|
| `Encode/bitpacked` | 20.0G | 20.0G | ~ | p=1.000 n=6 |

</details>
