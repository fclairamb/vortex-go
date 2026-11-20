.PHONY: check test race cover nocgo deps lint fixtures edition oracle upstream fuzz bigfile oracle-all compat interop vectors api bench bench-compare bench-parquet bench-rust bench-json

# Every blocking gate. CI runs this, run it before every push.
check: nocgo deps cover race

test:
	CGO_ENABLED=0 go test ./...

# -race needs the cgo toolchain on Linux. The code itself has no cgo (see nocgo).
race:
	go test -race ./...

# 100% statement coverage, no exclusion. Lists every block that is not covered.
cover:
	CGO_ENABLED=0 go test -coverpkg=./... -coverprofile=coverage.txt ./...
	@awk -f scripts/coverage.awk coverage.txt

# No cgo in this module or in anything it imports. CGO_ENABLED=1 is needed for
# go list to report cgo files instead of ignoring them.
nocgo:
	@out="$$(CGO_ENABLED=1 go list -deps -f '{{if and (not .Standard) .CgoFiles}}{{.ImportPath}}{{end}}' ./...)"; \
	if [ -n "$$out" ]; then echo "cgo found in:"; echo "$$out"; exit 1; fi; \
	echo "nocgo: ok"

# The only module allowed is klauspost/compress (zstd). Adding another one is
# Florent's decision, then this line changes.
deps:
	@out="$$(go list -m all | awk 'NR > 1 { print $$1 }' | grep -v -x -F github.com/klauspost/compress || true)"; \
	if [ -n "$$out" ]; then echo "modules not allowed:"; echo "$$out"; exit 1; fi; \
	echo "deps: ok"

# Rewrites the generated files: the example and the support matrix of README.md, and api.txt
# (go doc -all). CI runs this, then git diff --exit-code. The output of go doc is only
# promised stable on one Go release: api.txt is generated with Go 1.27, the other legs skip it.
api:
	go generate ./...

lint:
	golangci-lint run ./...

# Fixtures of a release (default: .vortex-version). VERSION=x fetches another one into .cache/.
fixtures:
	scripts/fixtures.sh $(VERSION)

# The edition table of a release, generated into internal/edition/table.go.
edition:
	scripts/edition.sh $(VERSION)

# The expected values: generates the fixtures upstream lacks, then writes the
# <file>.expected.json of every fixture of the pinned version. Needs uv and network.
# Existing extra fixtures are kept, the JSON files are rewritten (byte-identical).
ORACLE = uv run --with vortex-data==$$(cat .vortex-version) --with pyarrow python
oracle:
	$(ORACLE) tools/oracle/genfixtures.py
	find testdata/fixtures/$$(cat .vortex-version) testdata/fixtures/compat -name '*.vortex' | sort | xargs $(ORACLE) tools/oracle/oracle.py expected

# The 2 million rows file of the peak memory test, in .cache/ (not committed). Needs uv.
bigfile:
	$(ORACLE) tools/oracle/genbig.py

# Open and scan arbitrary bytes for FUZZTIME. A crasher lands in testdata/fuzz/, commit it
# with its fix.
FUZZTIME ?= 30s
fuzz:
	go test -run '^$$' -fuzz '^FuzzOpenScan$$' -fuzztime $(FUZZTIME) -fuzzminimizetime 10s .

# The expected values of every release in .cache/fixtures, with the vortex-data of that
# release (the pinned one when it cannot be installed, see .cache/fixtures/<version>/ORACLE).
oracle-all:
	scripts/oracle-all.sh

# Reads the fixtures of every published release and compares them with the oracle. Needs uv
# and network, takes a few minutes the first time (.cache/ is reused). Not part of check.
# When it passes, the list of releases and the README line are rewritten.
compat:
	scripts/fixtures.sh --all
	$(MAKE) oracle-all
	go test -tags compat -run Compat .
	cp .cache/compat-passed.txt testdata/compat-releases.txt
	go generate ./...

# The reference reader opens the files our writer makes, and gets the values we wrote. The
# Go test writes the files and the canonical hashes of its source data into .cache/interop,
# the oracle checks them with the pinned vortex-data and with the latest release. Needs uv
# and network.
INTEROP = .cache/interop
interop:
	rm -rf $(INTEROP)
	mkdir -p $(INTEROP)
	INTEROP_DIR=$(CURDIR)/$(INTEROP) go test -tags interop -run '^(TestInterop|TestInteropRows|TestSizeRatios)$$' -count=1 .
	$(ORACLE) tools/oracle/oracle.py check $(INTEROP)
	uv run --with vortex-data --with pyarrow python tools/oracle/oracle.py check $(INTEROP)

# The byte-exact vectors of the encoders (testdata/vectors), made by the encoders of the
# reference. Needs cargo and network; the tests never do. See tools/vectors/README.md.
vectors:
	cd tools/vectors && cargo run --release -- ../../testdata/vectors

# Everything a .vortex-version bump needs.
upstream: fixtures edition oracle

# Benchmarks of the root package. The data is generated once into .cache/, the TPC-H and
# ClickBench files are used when .cache/fixtures/ has them, and a benchmark skips without data.
BENCHFLAGS = -run '^$$' -bench . -benchmem -count 1
bench:
	go test $(BENCHFLAGS) ./...

# benchstat of this tree against main, on the same machine. benchstat is run with go run, so
# it is not a module of go.mod. BENCH_COUNT repeats each benchmark for the statistics.
BENCH_COUNT ?= 6
bench-compare:
	rm -rf .cache/bench-main && git worktree add --detach .cache/bench-main main
	cd .cache/bench-main && go test -run '^$$' -bench . -benchmem -count $(BENCH_COUNT) ./... > ../bench-main.txt
	git worktree remove --force .cache/bench-main
	go test -run '^$$' -bench . -benchmem -count $(BENCH_COUNT) ./... > .cache/bench-new.txt
	go run golang.org/x/perf/cmd/benchstat@latest .cache/bench-main.txt .cache/bench-new.txt

# The comparison with parquet-go, a separate module (bench/go.mod) that is not in make deps
# or in the coverage gate. It reads the file the root benchmarks generate, made first here.
bench-parquet:
	go test -run '^$$' -bench '^BenchmarkOpen$$' -benchtime 1x .
	cd bench && go test -run '^$$' -bench . -benchmem -count 1 ./...

# The Rust harness: the reference reader at the release of .vortex-version, built in release mode.
# It needs cargo and network, and is never part of check or of go test. See bench.yml.
bench-rust:
	cd tools/bench-rust && cargo build --release

# The three harnesses on the cases of bench/cases.json, merged into bench.json (one line, the
# median of BENCH_RUNS runs): the Go benchmarks, parquet-go, and the Rust harness, with 1 thread
# and with every core. Neither this nor bench-rust is part of check. VORTEX_BENCH_ROWS lowers the
# size of the generated file.
BENCH_RUNS ?= 5
PARQUET_RUNS ?= 3
BENCH_TMP = .cache/bench-json
bench-json: bench-rust
	mkdir -p $(BENCH_TMP)
	cores=$$(getconf _NPROCESSORS_ONLN); cpus=1; [ "$$cores" -gt 1 ] && cpus="1,$$cores"; \
	go test -run '^$$' -bench '^BenchmarkCases$$' -benchmem -count $(BENCH_RUNS) -benchtime 1x -cpu $$cpus . | tee $(BENCH_TMP)/go.txt && \
	(cd bench && go test -run '^$$' -bench '^BenchmarkCases$$/./.*/parquet-go' -benchmem -count $(PARQUET_RUNS) -benchtime 1x -cpu 1 . | tee ../$(BENCH_TMP)/parquet.txt) && \
	tools/bench-rust/target/release/bench-rust --cases bench/cases.json --root . --runs $(BENCH_RUNS) --threads $$cpus > $(BENCH_TMP)/rust.jsonl && \
	go -C bench run ./cmd/benchjson -cases cases.json -go ../$(BENCH_TMP)/go.txt -parquet ../$(BENCH_TMP)/parquet.txt \
		-rust ../$(BENCH_TMP)/rust.jsonl -commit "$$(git rev-parse HEAD)" -vortex-version "$$(cat .vortex-version)" \
		-go-version "$$(go env GOVERSION)" -rust-version "$$(rustc --version)" -cores $$cores > bench.json
	@echo "bench.json: $$(wc -c < bench.json) bytes"
