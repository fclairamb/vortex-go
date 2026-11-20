# vortex-go

A pure Go reader and writer for the [Vortex](https://vortex.dev) columnar file format.

**Status: design only. Nothing is implemented yet.**

<!-- compat:begin -->
Reads the fixtures of every published release, 28 of them, from 0.64.0 to 0.93.0, checked every night against the reference reader.
<!-- compat:end -->

## Goals

Ranked. The details and the "done when" lists are in [docs/goals.md](docs/goals.md).

1. **Easy to use.** A file to Go structs, or Go structs to a file, in a few lines and no configuration.
2. **Correct.** The Rust implementation is the judge, for what we read and for what we write.
3. **Complete.** Every encoding, layout and type of the upstream `core` edition.
4. **Fast, without allocation.** A running scan or writer allocates nothing.
5. **Self-contained.** No cgo. The standard library, plus `klauspost/compress` for zstd.
6. **100% statement coverage.** No exclusion.

## What it will look like

```go
f, err := vortex.OpenFile("trades.vortex")
if err != nil {
    return err
}
defer f.Close()

type Trade struct {
    ID    int64   `vortex:"id"`
    Price float64 `vortex:"price"`
}

q := vortex.Query{Filter: vortex.Col("price").Gt(100)}
for t, err := range vortex.Rows[Trade](ctx, f, q) {
    if err != nil {
        return err
    }
    use(t)
}
```

The full interface is in [docs/interface.md](docs/interface.md).

## How it is tested

[docs/testing.md](docs/testing.md) says how each goal is checked: the corpus of files
written by the reference implementation, the oracle that gives the expected values, the
check of our writer by the reference reader, the allocation tests and the gates.

```bash
make check
```

## Following upstream

`.vortex-version` pins the upstream release we follow. Renovate opens a PR on each new
release. See [docs/upstream-update.md](docs/upstream-update.md).

## License

MIT, see [LICENSE](LICENSE).
