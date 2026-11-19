#!/usr/bin/env bash
# Downloads the upstream compat fixtures of a Vortex release and checks every SHA-256.
# Usage: scripts/fixtures.sh [version]   (defaults to .vortex-version)
# Synthetic files go to testdata/fixtures/<version>/ (committed) when the version is the
# pinned one, else to .cache/fixtures/<version>/. Dataset files (tpch_*, clickbench_*)
# always go to .cache/fixtures/<version>/.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
pinned="$(tr -d '[:space:]' < "$root/.vortex-version")"
version="${1:-$pinned}"
base="https://vortex-compat-fixtures.s3.amazonaws.com/v${version}/arrays"

if [ "$version" = "$pinned" ]; then
  synth="$root/testdata/fixtures/$version"
else
  synth="$root/.cache/fixtures/$version"
fi
data="$root/.cache/fixtures/$version"
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
  curl -fsSL "$base/$name" -o "$dir/$name"
  got="$(shasum -a 256 "$dir/$name" | awk '{ print $1 }')"
  if [ "$got" != "$sum" ]; then
    echo "sha256 mismatch for $name: want $sum, got $got" >&2
    exit 1
  fi
done
echo "fixtures $version: ok"
