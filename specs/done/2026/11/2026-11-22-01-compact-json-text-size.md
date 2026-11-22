---
model: sonnet
effort: high
pr: batch
automerge: false
---

# `Compact` writes a JSON text column 1.5–2.5x larger than parquet-go + zstd

## Problem

dbbat (`~/code/fclairamb/dbbat`, `internal/archive/`) archives captured result rows as one
Parquet file per closed connection, zstd, row groups of 50,000 rows. It considered moving to
vortex-go, since nothing else reads those files. On its schema, vortex-go is 4–5x faster to read
back, but its files are much bigger, even with `Compact`. Size is the reason that archive exists
(the rows are 96% of dbbat's store), so this gap is what rules vortex-go out:

| Capture | parquet-go 0.32.1, zstd | vortex-go `Default` | vortex-go `Compact` |
|---|---|---|---|
| 3 rows | 2 KB | 8 KB | 8 KB |
| 100k rows, narrow JSON | 2,240 KB | 5,643 KB | 4,981 KB |
| 100k rows, wide JSON | 5,455 KB | 9,055 KB | 8,264 KB |
| 1M rows, wide JSON | 54,572 KB | 90,582 KB | 82,635 KB |

Measured on 2026-11-22 at `bc792df`. Writing takes about twice as long as parquet-go.

`Compact` is documented as "favors a small file over the time to write it: zstd is a
candidate too, for the numbers and the strings" (`writer.go:26`). Each string chunk is about
1 MiB (`wcol.go:18`), and a Parquet zstd page is no bigger than that, so with zstd on the table
`Compact` should land near parquet-go on this column, not 1.5x above it. Either zstd is not
picked for these chunks (`internal/compress/types.go:95-117`: dictionary, FSST, OnPair, zstd
compete through `pick`), or it is picked but compresses worse than parquet-go's.

The schema (one row per captured result row):

| Column | Go type | Content |
|---|---|---|
| `connection_uid` | `[16]byte` uuid | constant in a file |
| `query_uid` | `[16]byte` uuid | runs: n/stmts consecutive rows each |
| `row_uid` | `[16]byte` uuid | random v4, incompressible (16 B/row in both formats) |
| `row_number` | int32 | 1..n |
| `row_data` | string | JSON text, e.g. `{"id":1,"name":"user-1","ok":false}` |
| `row_size_bytes` | int64 | `len(row_data)` |

## Proposal

1. Add the dbbat schema to the benchmark set: a generated data set in `bench/cases.json` (or a
   generator beside the existing generated ones), with `write_default`, `write_compact` and a
   size record. Its generator is the one below (`gen`), seeded the same way, so the numbers
   above can be reproduced. The case covers the narrow JSON, the wide JSON, and a 3-row file.
2. Find where the bytes go: the size of each column's segments and the scheme `pick` chose
   for every `row_data` chunk, under `Compact`. Compare `row_data` alone against
   `zstd.EncodeAll` of the same chunk (the text joined, then the lengths) at the level parquet-go
   uses.
3. Fix what that shows. Some places to look, none decided yet:
   - zstd loses the comparison in `pick` because the sample it is judged on is too small for
     zstd's window, while FSST/OnPair win on the sample and lose on the whole chunk;
   - the zstd encoder level used for strings;
   - the zstd array compressing each value or a small frame instead of the whole chunk's bytes;
   - FSST/OnPair kept, then their own outputs (codes, offsets) not cascaded into zstd;
   - the offsets/lengths of the strings (`row_size_bytes` already says them) and the uuid
     columns: `query_uid` is runs and should cost almost nothing.
4. The 3-row file is 8 KB against 2 KB: check what the footer, layout and statistics cost on
   a tiny file and trim what a file of a few rows does not need. dbbat writes one file per
   connection, and most connections capture few rows.
5. Record the before/after numbers in the PR description and `docs/benchmarks.md`.

## Tests

- New benchmark case(s) from step 1, recorded on `bench-data` like the others.
- A size test on the generated dbbat data: under `Compact`, `row_data`'s 100k-row wide set
  stays within 10% of a plain `zstd.EncodeAll` of the same chunks (the bound is the target; if
  it proves out of reach, the spec's PR says why and sets the bound it reached).
- The round trip: every row read back with `vortex.Rows` equals the row written (uuids and the
  text), under `Default` and `Compact`, the 3-row file included.
- The Rust reference reads the new files (the interop tests), so a smaller file stays a valid
  one. Negative case: a file for an edition without `vortex.zstd` (`Limits.NoZstd`) still
  writes and reads, without zstd.
- `Default`'s sizes on the existing cases do not grow.

## The benchmark (as run in dbbat's evaluation)

A standalone module (`go.mod`: vortex-go with a `replace` to this checkout,
`github.com/parquet-go/parquet-go v0.32.1`, `github.com/google/uuid v1.6.0`):

```go
type PRow struct {
	ConnectionUID uuid.UUID `parquet:"connection_uid"`
	QueryUID      uuid.UUID `parquet:"query_uid"`
	RowUID        uuid.UUID `parquet:"row_uid"`
	RowNumber     int32     `parquet:"row_number"`
	RowData       string    `parquet:"row_data,json"`
	RowSizeBytes  int64     `parquet:"row_size_bytes"`
}

type VRow struct {
	ConnectionUID [16]byte `vortex:"connection_uid"`
	QueryUID      [16]byte `vortex:"query_uid"`
	RowUID        [16]byte `vortex:"row_uid"`
	RowNumber     int32    `vortex:"row_number"`
	RowData       string   `vortex:"row_data"`
	RowSizeBytes  int64    `vortex:"row_size_bytes"`
}

func gen(n, stmts int, wide bool) []PRow {
	r := rand.New(rand.NewPCG(1, 2))
	conn := uuid.New()
	out := make([]PRow, n)
	for i := range n {
		var m map[string]any
		if wide {
			m = map[string]any{"id": i, "email": fmt.Sprintf("user%d@example.com", r.IntN(50000)),
				"created_at": time.Unix(1.7e9+int64(r.IntN(1e7)), 0).UTC().Format(time.RFC3339),
				"status": []string{"active", "pending", "closed"}[r.IntN(3)], "amount": r.Float64() * 1000,
				"note": fmt.Sprintf("order %d for customer %d", r.IntN(1e6), r.IntN(5000))}
		} else {
			m = map[string]any{"id": i, "name": fmt.Sprintf("user-%d", i%977), "ok": i%2 == 0}
		}
		b, _ := json.Marshal(m)
		q := uuid.NewSHA1(conn, []byte{byte(i * stmts / n)})
		out[i] = PRow{conn, q, uuid.New(), int32(i + 1), string(b), int64(len(b))}
	}
	return out
}
```

Cases `(n, stmts, wide)`: `(3, 2, false)`, `(100_000, 40, false)`, `(100_000, 40, true)`,
`(1_000_000, 200, true)`. Parquet is written with
`parquet.NewGenericWriter[PRow](w, parquet.Compression(&zstd.Codec{}), parquet.MaxRowsPerRowGroup(50_000))`
(dbbat's settings, `internal/archive/archiver.go`), vortex with
`vortex.NewRowWriter[VRow](w, vortex.WriteOptions{Compression: vortex.Compact})`; the sizes are
the buffers' lengths. `row_uid` is `uuid.New()`, so the files differ by a few bytes from run
to run; the ratios do not.

## To verify

- Which zstd level the writer encodes with, and whether parquet-go's `zstd.Codec{}` default
  (its level) is higher: the comparison must be at the same level, or the spec says which.
- What `pick` judges a candidate on (a sample or the whole chunk), and how large the sample is.
- Whether the upstream Rust writer, at its most compact setting, has the same gap on this data
  (`~/code/other/vortex`): if it does, the fix may be outside what "the reference is the judge"
  allows for the scheme choice, and the spec's PR says so.

## Resolved open questions

- Zstd level (autopilot: recommended answer): both are klauspost `SpeedDefault`, the level
  parquet-go's `zstd.Codec{}` uses, so the comparison is at the same level.
- What `pick` judges on (autopilot: recommended answer): a sample of the chunk. It was not the
  cause: `row_data` under `Compact` was already within 3% of plain zstd. The bytes went to the
  uuid columns (`[16]byte`, written as plain `u8` with no compression, 1.6 MB each for 100k rows).
- The Rust writer on this data (autopilot: recommended answer): the reference is not in this
  checkout and no Python reader is installed, so it is not measured; the fix does not change a
  scheme choice, it only lets the compressor see the bytes of a uuid as it sees any `u8` array.
- Tiny files (autopilot: recommended answer): a zone table per column (about 600 bytes each) was
  3 of the 8 KB of the 3-row file. Under `Compact` only, a column of one zone has none; `Default`
  keeps the layouts of the reference. The file is 5 KB, not 2 KB: the rest is the dtype, the file
  statistics and one segment per column.
