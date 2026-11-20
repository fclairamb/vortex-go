.PHONY: check test race cover nocgo deps lint fixtures edition oracle upstream fuzz bigfile

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

# Everything a .vortex-version bump needs.
upstream: fixtures edition oracle
