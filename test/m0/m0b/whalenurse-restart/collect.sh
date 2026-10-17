#!/usr/bin/env bash
# M0b scenario 6 (whalenurse-restart): collect after the daemon restart. Works from a fresh session; safe to run repeatedly
# (the compose stop of the second project is done once; later runs evaluate it again).
#   collect.sh [--watch SECS] [state dir]      (default: the newest whalenurse-restart state; waits up to 240 s)
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"
# shellcheck source=../nurse-lib.sh
source "$here/../nurse-lib.sh"

nurse_collect "$@"
