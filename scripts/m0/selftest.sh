#!/usr/bin/env bash
# Proves scan.sh and capture.sh are read-only: runs them against a fake
# `docker` (and fake sudo/journalctl) on PATH that logs argv, then asserts that
# only allowlisted subcommands were called. Needs no real Docker.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
export FAKE_LOG="$tmp/calls.log"
: >"$FAKE_LOG"

cat >"$tmp/bin/docker" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_LOG"
case "$1" in
  ps) printf 'aaa\nbbb\nccc\n' ;;
  inspect)
    if [[ "$*" == *--format* ]]; then
      case "${@: -1}" in
        aaa) echo '/web|exited|137|true|always|2026-10-17T10:00:00Z||proj' ;;
        bbb) echo '/db|created|128|false|unless-stopped|0001-01-01T00:00:00Z|mount source missing|proj' ;;
        *)   echo '/ok|running|0|false|always|0001-01-01T00:00:00Z||proj' ;;
      esac
    else echo '[{"Name":"/x"}]'; fi ;;
  events | version | info) echo '{}' ;;
  compose) echo 'Docker Compose version vX' ;;
esac
FAKE
for tool in sudo journalctl; do
  cat >"$tmp/bin/$tool" <<'STUB'
#!/usr/bin/env bash
echo "$(basename "$0") $*" >>"$FAKE_LOG"
exit 1
STUB
done
chmod +x "$tmp/bin/"*
export PATH="$tmp/bin:$PATH"

fail() { echo "FAIL: $*" >&2; exit 1; }

out=$("$here/scan.sh")
grep -q '^/\?web ' <<<"$out" || fail "scan.sh missed exited+restart-policy container"
grep -q 'mount source missing' <<<"$out" || fail "scan.sh missed State.Error container"
grep -q '^ok ' <<<"$out" && fail "scan.sh listed a healthy container"

root="$(cd "$here/../.." && pwd)"
before=$(find "$root/captures" -mindepth 1 -maxdepth 1 2>/dev/null | wc -l)
"$here/capture.sh" web --since 30m >/dev/null
after=$(find "$root/captures" -mindepth 1 -maxdepth 1 | wc -l)
((after == before + 1)) || fail "capture.sh did not write exactly one directory"
newest=$(find "$root/captures" -mindepth 1 -maxdepth 1 -name '*-web' | sort | tail -1)
grep -q 'journal not readable' "$newest/journal.txt" || fail "journal fallback note missing"
rm -rf "$newest"

# Only allowlisted docker subcommands may appear in the call log.
bad=$(grep -vE '^(ps|inspect|events|version|info|compose version|sudo|journalctl)( |$)' "$FAKE_LOG" || true)
[[ -z "$bad" ]] || fail "non-allowlisted calls: $bad"
calls=$(awk '{print $1}' "$FAKE_LOG" | sort -u | tr '\n' ' ')
grep -q '^sudo -n ' "$FAKE_LOG" || fail "sudo must be called with -n (never prompt)"

# The wrapper itself must refuse mutating verbs.
# shellcheck source=lib-readonly.sh
source "$here/lib-readonly.sh"
for v in "stop x" "restart x" "rm x" "kill x" "start x" "run alpine" "exec x ls" "compose up" "compose down" "pull alpine" "system prune" "network rm x"; do
  # shellcheck disable=SC2086
  if (ro_docker $v) 2>/dev/null; then fail "ro_docker allowed: $v"; fi
done
# Bad input to capture.sh is refused before touching Docker.
: >"$FAKE_LOG"
"$here/capture.sh" '../etc' 2>/dev/null && fail "capture.sh accepted a path-like name"
"$here/capture.sh" web --since 'x;rm' 2>/dev/null && fail "capture.sh accepted a bad --since"
[[ ! -s "$FAKE_LOG" ]] || fail "docker was called despite bad input"

echo "selftest OK: scan.sh/capture.sh only called: ${calls}; ro_docker refuses mutating verbs"
