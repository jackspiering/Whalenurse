#!/usr/bin/env bash
# Usage: promote-fixtures.sh <captures/m0-RUN> <name>...   (<name>.events picks the events file when an inspect fixture has the same name)
# Scrubs raw fixtures (captures/.../fixtures-raw/<name>.json or <name>.events.ndjson) into testdata/
# and refuses to keep anything that fails scripts/scrub-fixture --check.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
run="${1:?usage: promote-fixtures.sh <captures/m0-RUN> <name>...}"
shift
scrub="$root/scripts/scrub-fixture"
for name in "$@"; do
  [[ "$name" =~ ^[a-z0-9._-]+$ ]] || { echo "bad name: $name" >&2; exit 2; }
  if [[ "$name" == *.events ]]; then # events-only, for names that also have an inspect fixture
    base="${name%.events}"
    out="$root/testdata/events/$base.ndjson"
    src="$run/fixtures-raw/$base.events.ndjson"
    [[ -f "$src" ]] || { echo "no raw events fixture named $base in $run" >&2; exit 2; }
  elif [[ -f "$run/fixtures-raw/$name.json" ]]; then
    out="$root/testdata/inspect/$name.json"
    src="$run/fixtures-raw/$name.json"
  elif [[ -f "$run/fixtures-raw/$name.events.ndjson" ]]; then
    out="$root/testdata/events/$name.ndjson"
    src="$run/fixtures-raw/$name.events.ndjson"
  else
    echo "no raw fixture named $name in $run" >&2
    exit 2
  fi
  mkdir -p "$(dirname "$out")"
  "$scrub" "$src" >"$out.tmp"
  if "$scrub" --check "$out.tmp"; then
    mv "$out.tmp" "$out"
    echo "promoted $name -> ${out#"$root"/}"
  else
    rm -f "$out.tmp"
    echo "REFUSED $name: leak check failed" >&2
    exit 1
  fi
done
