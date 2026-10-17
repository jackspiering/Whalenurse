#!/usr/bin/env bash
# Compares two test/m0/run.sh result directories (captures/m0-<id>): the summary.txt of every experiment, after removing the
# parts that differ between any two runs (container and image ids, timestamps, namespace numbers, durations).
#   compare-run.sh <baseline run dir> <new run dir>
set -euo pipefail
a="${1:?usage: compare-run.sh <baseline run dir> <new run dir>}"
b="${2:?usage: compare-run.sh <baseline run dir> <new run dir>}"
norm() {
  sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g;
    s/sha256:[0-9a-f]+/<digest>/g;
    s/[0-9a-f]{12,64}/<id>/g;
    s/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z/<ts>/g;
    s/net:\[[0-9]+\]/net:[N]/g;
    s/[0-9]+(\.[0-9]+)?s\b/<N>s/g;
    s/\[[a-z0-9._-]+ rc=[0-9]+\]/[<file> rc=N]/g;
    s#(/[A-Za-z0-9._-]+)*/captures/m0-[A-Za-z0-9._-]+/scratch#<scratch>#g;
    s/whalenurse-m0-[a-z0-9]+-(parent|dep|plain|netdep|ipcdep|piddep|vfdep|tagged|stuck|control|svc)-[0-9]+/<container>/g' "$1"
}
same=0
differ=0
for e in E1 E2 E3 E4 E5 E6 E7 E8 E9; do
  fa="$a/$e/summary.txt"
  fb="$b/$e/summary.txt"
  if [[ ! -f "$fa" || ! -f "$fb" ]]; then echo "$e: missing summary ($fa / $fb)"; differ=$((differ + 1)); continue; fi
  if diff -q <(norm "$fa") <(norm "$fb") >/dev/null; then
    echo "$e: same after normalising"
    same=$((same + 1))
  else
    echo "$e: DIFFERENT"
    { diff -u <(norm "$fa") <(norm "$fb") || true; } | head -40 | sed "s/^/    /"
    differ=$((differ + 1))
  fi
done
echo "same: $same, different: $differ"
