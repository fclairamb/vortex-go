---
model: haiku
effort: low
pr: batch
automerge: false
---

# The README has its badges

## Problem

`README.md` has no badge. A reader cannot see at a glance whether the build is green,
what the coverage is, which Go version is needed or where the reference documentation is
(Florent, 2026-11-20: "update the readme file to contain all the necessary badges").

## Proposal

1. Under the title of `README.md`, on one block, in this order (the order and the style
   of `fclairamb/solidping`):

   ```markdown
   [![Build](https://img.shields.io/github/actions/workflow/status/fclairamb/vortex-go/build.yml?branch=main&label=build&logo=github)](https://github.com/fclairamb/vortex-go/actions/workflows/build.yml)
   [![Coverage](https://codecov.io/gh/fclairamb/vortex-go/graph/badge.svg)](https://codecov.io/gh/fclairamb/vortex-go)
   [![Go Reference](https://pkg.go.dev/badge/github.com/fclairamb/vortex-go.svg)](https://pkg.go.dev/github.com/fclairamb/vortex-go)
   [![Go Report Card](https://goreportcard.com/badge/github.com/fclairamb/vortex-go)](https://goreportcard.com/report/github.com/fclairamb/vortex-go)
   [![Go](https://img.shields.io/github/go-mod/go-version/fclairamb/vortex-go?logo=go)](go.mod)
   [![License](https://img.shields.io/github/license/fclairamb/vortex-go?color=blue)](LICENSE)
   ```
2. The block sits outside the marker comments of the generated parts, so
   `go generate ./...` leaves it alone.
3. **No token in a badge URL.** The repository is private today: the shields.io, Codecov,
   pkg.go.dev and Go Report Card badges only show their value once it is public. They are
   written now and left as they are. Never add `?token=` to make one work.

## Tests

- `readme_test.go`: every badge names `fclairamb/vortex-go`, the module path of the
  pkg.go.dev and Go Report Card badges is the one of `go.mod`, the workflow file of the
  build badge exists in `.github/workflows/`, `LICENSE` exists, and no badge URL has a
  `token` parameter. Offline: it reads files only.
- `go generate ./...` then `git diff --exit-code README.md`: the block is untouched.

## Open questions

- **A release badge.** The repository has no tag. Recommended answer: leave it out. It is
  added with the first release.
- **Go Report Card.** It is not on solidping's README. Recommended answer: keep it, it is
  the usual badge of a Go library.

## Resolved open questions

- No release badge for now; add it with the first release. (autopilot: recommended answer)
- Keep the Go Report Card badge. (autopilot: recommended answer)
