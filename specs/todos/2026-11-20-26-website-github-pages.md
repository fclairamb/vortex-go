---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# A small site on GitHub Pages says how to use the library and how it compares with the Rust implementation

## Problem

The documentation is written for the people who build the library: `docs/goals.md`,
`docs/interface.md`, `docs/testing.md`. A user has the README and pkg.go.dev. The numbers
that answer "should I use this or the Rust one" exist, but in files nobody browses:
`bench.json` and `history.jsonl` on the `bench-data` branch, `testdata/size-ratios.json`,
the support matrix of the README.

Florent, 2026-11-20: "create a small website on github pages to explain how to use the
library and how it compares, especially in terms of performance, to the rust version".

## Proposal

1. `website/`: static pages written by hand, one `style.css`. No site generator, no npm,
   no framework, no JavaScript, no external script, font or image. Light and dark with
   `prefers-color-scheme`. Readable at phone width. Every link is relative: the site is
   served under `/vortex-go/`.
2. Three pages, with the same header and navigation:
   - `index.html`: what the library is in 3 lines, `go get`, the README example, the
     status (the edition, the pinned upstream release, the releases it reads), links to
     pkg.go.dev and to the repository.
   - `usage.html`: one section per use case, the same list as the `Example` functions:
     read rows, read columns, project, filter and take a row range, write rows, write
     columns, the three compression modes, the errors. Then the table of Vortex types and
     their Go types, from `docs/interface.md`.
   - `rust.html`, the comparison:
     - what is the same: the file format, the edition, and who judges (the Rust reader
       reads every file we write, our reader is checked against it on every file);
     - what differs: the support matrix (read, written, per identifier), what our writer
       does not emit, the non-goals of `docs/goals.md`;
     - speed: per case, our number, the Rust one, parquet-go's and the ratio, with 1
       thread and with all cores, as a table and as bars. Under it the machine, the
       commit and the date. Then the ratio to Rust over the commits of `history.jsonl`;
     - file size: the ratios of `testdata/size-ratios.json`;
     - "which one to use", in 4 lines.
     Every ratio is shown, the ones where we lose first (`docs/goals.md` goal 4:
     "published, whatever it is").
3. **Nothing is typed twice.**
   - The code blocks and the support matrix are written between marker comments by the
     generator of the examples spec (`internal/gen`, `go generate ./...`), from the
     `Example` functions and the registries. CI fails when a page is stale, like the
     README.
   - The numbers are not committed in the pages. `bench/cmd/benchsite` (in the `bench/`
     module, `html/template`, standard library) fills the tables and draws the bars and
     the history line as inline SVG when the site is built. With no data, the section
     says "no measurement yet".
4. `data-testid` on the navigation links, each table, each chart and each code block.
5. `.github/workflows/pages.yml`:
   - build: copy `website/` to `_site/`, fetch `bench.json` and `history.jsonl` from
     `bench-data`, run `benchsite`. On a pull request it stops there and uploads `_site`
     as an artifact;
   - deploy (`actions/upload-pages-artifact`, `actions/deploy-pages`): on push to `main`
     and after a run of `bench.yml`, and only when the repository variable `PAGES` is
     `on`. Without it the job is skipped, not red.
6. **Turning Pages on is not in this spec.** It puts the site on the public Internet, for
   a repository that is private today. Florent does it: Pages with "GitHub Actions" as
   the source, then the variable `PAGES=on`.
7. `README.md`: a link to the site, outside the generated blocks. `make site` builds
   `_site/` locally. `_site/` goes in `.gitignore`.

## Tests

- `website_test.go`: for every page, each internal link and each asset exists, the
  navigation lists the three pages, there is a `<title>`, no `http://` link, no external
  `src` or stylesheet, no `<script>`, and no `data-testid` is used twice in a page.
- The generator: the pages' blocks, fresh and stale, on a temp tree.
- `bench/cmd/benchsite`: a `bench.json` of 3 cases gives 3 rows and 3 groups of bars, a
  ratio above 1 is marked as slower, a case with no Rust number shows a dash, no data
  gives "no measurement yet", a damaged `history.jsonl` line is an error naming the line.
- The workflow on the PR: a green build with the `_site` artifact, the deploy job skipped.
- `make check` still passes: nothing new in the root `go.mod`.

## To verify

- What GitHub Pages needs on a private repository (the plan), and that the site is public
  whatever the visibility of the repository.
- The permissions and the environment `actions/deploy-pages` needs (`pages: write`,
  `id-token: write`, `github-pages`).
- That a `workflow_run` trigger on `bench.yml` fires for runs on the default branch.
- That the `bench-data` branch can be missing (first run): the build must not fail.

## Open questions

- **Deploying while the repository is private.** Nobody could `go get` what the site
  describes. Recommended answer: build everything, leave the deploy off. Florent turns it
  on when he makes the repository public.
- **A custom domain.** Recommended answer: none, `https://fclairamb.github.io/vortex-go/`.
