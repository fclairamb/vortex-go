#!/usr/bin/env bash
# Benchmarks two checkouts of vortex-go against each other, interleaved: the test binaries of
# both are built first, then every round runs each package once on each side, the order
# swapped from one round to the next. A shared runner drifts during the run (noisy neighbours,
# throttling); run back to back, main then the PR, that drift showed up as a change.
# Usage: scripts/bench-pr.sh OLD_DIR NEW_DIR OUT_DIR   (BENCH_COUNT rounds, default 6; BENCH_TIME
# per benchmark, default 1s)
# Writes OUT_DIR/old.txt and OUT_DIR/new.txt, the input of benchstat. A side that does not
# build gives an empty file.
set -euo pipefail

old="$(cd "$1" && pwd)" new="$(cd "$2" && pwd)"
mkdir -p "$3"
out="$(cd "$3" && pwd)"
count="${BENCH_COUNT:-6}" benchtime="${BENCH_TIME:-1s}"
: > "$out/old.txt"
: > "$out/new.txt"

# build SIDE DIR: one test binary per package with benchmarks, $out/bin/SIDE/<package>/bench.test,
# and the list of the packages that built in $out/bin/SIDE/list.
build() {
  local side="$1" dir="$2" pkg
  mkdir -p "$out/bin/$side"
  : > "$out/bin/$side/list"
  for pkg in $(cd "$dir" && git grep -l '^func Benchmark' -- '*_test.go' ':!bench/' | xargs -n 1 dirname | sort -u); do
    if (cd "$dir" && go test -c -o "$out/bin/$side/$pkg/bench.test" "./$pkg"); then
      echo "$pkg" >> "$out/bin/$side/list"
    else
      echo "bench-pr: $side: ./$pkg does not build" >&2
    fi
  done
}

# bench SIDE DIR FILE: runs every binary of SIDE once, in its package directory.
bench() {
  local side="$1" dir="$2" file="$3" pkg
  while read -r pkg; do
    (cd "$dir/$pkg" && "$out/bin/$side/$pkg/bench.test" -test.run '^$' -test.bench . -test.benchmem -test.benchtime "$benchtime" -test.count 1 < /dev/null >> "$file") ||
      echo "bench-pr: $side: ./$pkg failed" >&2
  done < "$out/bin/$side/list"
}

build old "$old"
build new "$new"
for i in $(seq "$count"); do
  echo "bench-pr: round $i of $count" >&2
  if [ $((i % 2)) -eq 1 ]; then
    bench old "$old" "$out/old.txt"
    bench new "$new" "$out/new.txt"
  else
    bench new "$new" "$out/new.txt"
    bench old "$old" "$out/old.txt"
  fi
done
