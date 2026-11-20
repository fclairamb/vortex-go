---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Tests prove that every identifier of the target edition is decoded, and was met in a real file

## Problem

The decoding specs each remove names from a "not decoded yet" list. Nothing proves the list
is empty for the right reason, or that it stays empty when `.vortex-version` moves.
`docs/testing.md` section 2 asks for two gates: every identifier of the edition has a
decoder, and every decoder has run on a file written by the reference writer.

## Proposal

1. **Registries.** One per kind, filled at package init by plain tables (no `init`
   function, the linter forbids it): array decoders, layout readers, extension types,
   statistics. Each maps an identifier to its implementation. An identifier met in a file
   and absent from the registry gives an error that names it, the column and the file
   offset: "unsupported encoding".
2. **Complete.** A test walks `internal/edition.Members()`: every `array`, `layout`,
   `dtype` and `aggregate` member is in its registry. The "not decoded yet" list of the
   earlier specs is deleted, with every test skip that used it.
3. **Seen.** The registries count, in test builds only (a hook, not a field on the hot
   path), each identifier met while scanning. A test scans the whole committed corpus
   (`testdata/fixtures/<version>/` and `extra/`), every column, then fails for each member
   of the edition that was never met. The message says which fixture is missing.
4. **The gaps.** For each identifier never met:
   - first try to produce a file with the reference writer (`tools/oracle/genfixtures.py`),
     as the oracle spec did for `onpair`, `masked`, `uuid` and the variants;
   - `vortex.stats` is the older name of the zoned layout: the current writer does not emit
     it. Take the smallest file of an older release that has it, from `make fixtures
     VERSION=<older>`, into `testdata/fixtures/compat/<older>/` with its `.expected.json`;
   - if `extra/MISSING.md` still lists an identifier that the reference writer cannot be
     made to emit, hand-build the file in a Go test from upstream's own encoder output
     (vectors produced by a few lines of Rust, committed with the command that made them).
     Never from our own encoder: there is none yet, and it would not be a judge.
   - when all else fails, the identifier is listed in the test as "not seen", with the
     reason, and the test fails if that list has an entry that is in fact seen. The list
     must be empty to close this spec unless the open question below says otherwise.
5. **Types.** One test per logical type of `docs/interface.md`, nullable and not nullable,
   alone and nested in a struct and in a list. Most exist: add the missing ones.
6. **The matrix.** `go generate` is not wired yet (a later spec). Leave a
   `Supported() []Support` function in `internal/edition`, built from the registries, that
   the generator will use.
7. `docs/testing.md` section 9: "Corpus against the oracle" and "Edition table complete
   and seen" become "in place".

## Tests

- The complete test and the seen test of steps 2 and 3, green.
- The registry lookup error: a file whose array table names `acme.unknown` (a fixture
  patched in the test) gives the "unsupported encoding" error with the name in it.
- The complete test fails when a member is removed from a registry, and the seen test
  fails when a fixture is removed: both run the check function on a doctored input.

## To verify

- Which fixture holds each identifier. The oracle's JSON may list the encodings met
  (`encodings`, `layouts`), if the Python API gave them.
- Whether `vortex.patched` or other identifiers outside the edition show up in the 0.93.0
  fixtures. An identifier that the reference writer emits by default but that the edition
  does not list must be decoded too: report it in the commit message.

## Open questions

- **An identifier of the edition that no tool can put in a file.** Recommended answer: the
  spec closes with that identifier in the "not seen" list and its reason, the decoder stays
  (tested with hand-built bytes taken from upstream's unit tests), and the README matrix
  marks it "read, not checked against a reference file".

## Resolved open questions

- **An identifier no tool can put in a file:** list it under "not seen" with its reason, keep the decoder (tested with hand-built bytes from upstream's unit tests), and mark it "read, not checked against a reference file" in the README matrix. (autopilot: recommended answer)
