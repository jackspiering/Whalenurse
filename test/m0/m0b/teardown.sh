#!/usr/bin/env bash
# Removes what the M0b scenarios leave running: `down -v` for whalenurse-m0-b-* projects (only those) and the
# user systemd listener units. The state under captures/m0b is kept. Never touches daemon.json, images or
# anything outside the M0b projects.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$here/lib.sh"
m0_sanitize_env
while IFS= read -r p; do
  if [[ "$p" =~ $M0B_PROJECT_RE ]]; then
    echo "down -v $p"
    m0_down "$p"
  fi
done < <(docker ps -a --format '{{.Label "com.docker.compose.project"}}' | sort -u)
m0b_listener_remove
# The images prepare.sh built for the whalenurse-restart scenario (tags under whalenurse-m0/ only).
while IFS= read -r img; do
  echo "rmi $img"
  m0_rmi "$img"
done < <(docker images --format '{{.Repository}}:{{.Tag}}' | grep -E '^whalenurse-m0/whalenurse:' || true)
echo "teardown done"
