#!/usr/bin/env bash
# M0b scenario 2: collect after the daemon restart. Works from a fresh session; safe to run repeatedly.
#   collect.sh [--watch SECS] [state dir]      (default: the newest daemon-restart state)
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"
# shellcheck source=../boot-lib.sh
source "$here/../boot-lib.sh"

boot_collect daemon-restart "$@"
