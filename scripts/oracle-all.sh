#!/usr/bin/env bash
# Writes the <file>.expected.json of every file under .cache/fixtures/<version>/, with the
# vortex-data of that version. When it cannot be installed (or cannot read the file), the
# pinned version is used and .cache/fixtures/<version>/ORACLE says which one was used and why.
# A file whose JSON exists already for the same oracle is not recomputed.
set -uo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
pinned="$(tr -d '[:space:]' < .vortex-version)"

run() { # run VERSION FILE...
  local v="$1"; shift
  uv run --quiet --with "vortex-data==$v" --with pyarrow python tools/oracle/oracle.py expected "$@"
}

for dir in .cache/fixtures/*/; do
  version="$(basename "$dir")"
  files=()
  while IFS= read -r f; do files+=("$f"); done < <(find "$dir" -name '*.vortex' | sort)
  [ ${#files[@]} -gt 0 ] || continue
  if [ -f "$dir/ORACLE" ] && grep -q '^done' "$dir/ORACLE" && [ -z "$(find "$dir" -name '*.vortex' -newer "$dir/ORACLE")" ]; then
    continue
  fi
  used="$version"
  note=""
  if ! out="$(run "$version" "${files[@]}" 2>&1)"; then
    note="vortex-data $version failed: $(printf '%s' "$out" | tail -1)"
    used="$pinned"
    if ! out="$(run "$pinned" "${files[@]}" 2>&1)"; then
      echo "oracle $version: $out" >&2
      exit 1
    fi
  fi
  printf 'done\nvortex-data %s\n%s\n' "$used" "$note" > "$dir/ORACLE"
  echo "oracle $version: vortex-data $used"
done
