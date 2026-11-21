# vortex-go

Pure Go reader and writer for the Vortex columnar file format. Module
`github.com/fclairamb/vortex-go`, package `vortex`.

Read these before working here:

- [docs/goals.md](docs/goals.md): the ranked goals, the non-goals, the open decisions.
- [docs/interface.md](docs/interface.md): the public API we are building.
- [docs/testing.md](docs/testing.md): how each goal is checked, and the list of gates.
- [docs/upstream-update.md](docs/upstream-update.md): what to do on a Renovate PR that changes `.vortex-version`.

## Hard rules

- **No cgo.** Not here, not in a dependency. `make nocgo` checks it.
- **Standard library, plus `github.com/klauspost/compress` for zstd.** Never add another
  module without Florent's explicit go. `make deps` checks it.
- **100% statement coverage, no exclusion.** `make cover` checks it. Code that no test can
  reach is deleted, not excused.
- **The Rust implementation is the judge.** Expected values come from the reference reader
  (the oracle), never from our own code. Never edit an expected value by hand.
- **`go test ./...` is offline.** No network, no Python, no Rust.
- **No assembly**, and `unsafe` only in the one internal package meant for it.
- **Every API change updates `api.txt`** and, when it changes the design, `docs/interface.md`.

## Specs

- To do: `specs/todos/`. Done: `specs/done/YYYY/MM/`. File name: `YYYY-MM-DD-NN-title.md`.
- They are implemented one at a time, in file name order. Each one builds on the ones
  before it. Never skip one.
- **One spec, one push.** When a spec is done and archived, push, then read the CI run of
  that commit. Start the next spec only when it is green. A red CI is fixed first.
- A spec's `## To verify` items are checked first, against upstream at the pinned tag. When
  upstream says otherwise, upstream wins: fix the spec in the same commit.

## Releases

release-please keeps a release pull request open (`.github/workflows/release-please.yml`,
`release-please-config.json`). Its changelog is the titles of the merged pull requests, so a
title is a conventional commit: `feat:`, `fix:` and `perf:` are in the changelog, the others
are not. The first release is v0.1.0, and before v1 a breaking change is a minor version.
Never tag by hand, and never edit `.release-please-manifest.json`: merging the release pull
request does both.

## Before every push

```bash
make check
```

```bash
~/go/bin/golangci-lint run ./...
```

## References on this Mac

- `~/code/other/vortex`: the upstream Rust implementation (shallow clone). The spec is
  thin, the Rust code is the real reference.
- `~/code/other/vortex-go`: `mprammer/vortex-go`, a scalar-only Go reader (Apache-2.0).
  Good to read for the footer parsing and the Pco decoder. Do not copy code without
  keeping its license notice.
- Upstream fixtures: `https://vortex-compat-fixtures.s3.amazonaws.com` (`versions.json`,
  then `v<version>/arrays/manifest.json`).
