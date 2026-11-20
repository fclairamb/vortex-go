#!/usr/bin/env bash
# Downloads the upstream compat fixtures of a Vortex release and checks every SHA-256.
# Usage: scripts/fixtures.sh [version]   (defaults to .vortex-version)
#        scripts/fixtures.sh --all       (every release of versions.json, into .cache/)
# Synthetic files go to testdata/fixtures/<version>/ (committed) when the version is the
# pinned one, else to .cache/fixtures/<version>/. Dataset files (tpch_*, clickbench_*)
# always go to .cache/fixtures/<version>/. With --all, every file of every release goes to
# .cache/fixtures/<version>/ (the pinned one too), and a file already there with the right
# hash is not downloaded again.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
pinned="$(tr -d '[:space:]' < "$root/.vortex-version")"
bucket="https://vortex-compat-fixtures.s3.amazonaws.com"

sha() { shasum -a 256 "$1" | awk '{ print $1 }'; }

# fetch VERSION SYNTH_DIR DATA_DIR
fetch() {
  local version="$1" synth="$2" data="$3"
  local base="$bucket/v${version}/arrays"
  mkdir -p "$synth" "$data"
  curl -fsSL "$base/manifest.json" -o "$synth/manifest.json"
  # The manifest is pretty-printed: one "name" line then its "sha256" line per fixture.
  awk '
    /"name":/   { gsub(/.*"name": *"|".*/, ""); name = $0 }
    /"sha256":/ { gsub(/.*"sha256": *"|".*/, ""); print name, $0 }
  ' "$synth/manifest.json" | while read -r name sum; do
    case "$name" in
      tpch_* | clickbench_*) dir="$data" ;;
      *) dir="$synth" ;;
    esac
    if [ -f "$dir/$name" ] && [ "$(sha "$dir/$name")" = "$sum" ]; then
      continue
    fi
    curl -fsSL "$base/$name" -o "$dir/$name"
    got="$(sha "$dir/$name")"
    if [ "$got" != "$sum" ]; then
      echo "sha256 mismatch for $version/$name: want $sum, got $got" >&2
      exit 1
    fi
  done
  echo "fixtures $version: ok"
}

if [ "${1:-}" = "--all" ]; then
  mkdir -p "$root/.cache"
  curl -fsSL "$bucket/versions.json" -o "$root/.cache/versions.json"
  for version in $(grep -o '"[0-9][^"]*"' "$root/.cache/versions.json" | tr -d '"'); do
    fetch "$version" "$root/.cache/fixtures/$version" "$root/.cache/fixtures/$version"
  done
  exit 0
fi

version="${1:-$pinned}"
if [ "$version" = "$pinned" ]; then
  synth="$root/testdata/fixtures/$version"
else
  synth="$root/.cache/fixtures/$version"
fi
fetch "$version" "$synth" "$root/.cache/fixtures/$version"
