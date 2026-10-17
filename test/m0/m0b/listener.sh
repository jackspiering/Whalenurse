#!/usr/bin/env bash
# Records `docker events` for whalenurse-m0-b-* projects into a file, across daemon restarts.
# Read-only: it only runs `docker version` and `docker events`. Started as a user systemd unit by
# lib.sh (m0b_listener_install), so it survives the session that ran prepare.sh.
#
#   listener.sh <outfile> <stopfile> <project>...
#
# Why it exists: the daemon keeps events in an in-memory buffer (daemon/events/events.go, 256
# events) that starts empty in every daemon process, so `docker events --since` after a restart
# cannot show what the old daemon emitted during shutdown. Only a client that is already connected
# can. The file also gets marker lines ({"_marker":"connect|disconnect","t":<epoch>}), which show
# whether the shutdown events arrived before the stream closed.
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$here/lib.sh"

out="${1:?usage: listener.sh <outfile> <stopfile> <project>...}"
stop="${2:?stopfile}"
shift 2
(($#)) || m0_die "listener: no project"
filters=()
for p in "$@"; do
  m0b_require_project "$p"
  filters+=(--filter "label=com.docker.compose.project=$p")
done
umask 077

mark() { printf '{"_marker":"%s","t":%s}\n' "$1" "$EPOCHREALTIME" >>"$out"; }
last=$(date +%s)
while [[ ! -e "$stop" ]]; do
  until docker version --format '{{.Server.Version}}' >/dev/null 2>&1; do
    [[ -e "$stop" ]] && exit 0
    sleep 0.5
  done
  mark connect
  docker events --since "$last" --filter type=container "${filters[@]}" --format '{{json .}}' >>"$out" 2>>"$out.err"
  mark disconnect
  last=$(($(date +%s) - 1))
  sleep 0.5
done
