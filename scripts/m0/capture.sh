#!/usr/bin/env bash
# Read-only. Usage: capture.sh <container> [--since 30m]
# Writes raw evidence into captures/<timestamp>-<name>/ (gitignored, mode 0700).
# Raw output is NEVER committed: pass it through scripts/scrub-fixture first.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-readonly.sh
source "$here/lib-readonly.sh"
root="$(cd "$here/../.." && pwd)"

since="30m"
name=""
while (($#)); do
  case "$1" in
    --since) since="${2:?--since needs a value}"; shift 2 ;;
    -*) ro_die "unknown option: $1" ;;
    *) [[ -z "$name" ]] || ro_die "only one container"; name="$1"; shift ;;
  esac
done
[[ -n "$name" ]] || ro_die "usage: capture.sh <container> [--since 30m]"
[[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || ro_die "bad container name: $name"
[[ "$since" =~ ^[0-9]+[smhd]$ ]] || ro_die "--since must look like 30m, 2h, 90s or 1d"

dest="$root/captures/$(date -u +%Y%m%dT%H%M%SZ)-$name"
umask 077
mkdir -p "$dest"

ro_docker inspect "$name" >"$dest/inspect.json"
# --until makes `docker events` return instead of streaming forever.
ro_docker events --since "$since" --until "$(date +%s)" \
  --filter "container=$name" --format '{{json .}}' >"$dest/events.ndjson"
ro_journal "$(ro_since_journal "$since")" >"$dest/journal.txt"
{
  ro_docker version
  ro_docker compose version
} >"$dest/versions.txt" 2>&1
ro_docker info --format '{{json .}}' >"$dest/info.json"

echo "wrote $dest"
ls -l "$dest"
