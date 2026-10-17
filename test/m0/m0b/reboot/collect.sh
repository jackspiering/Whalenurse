#!/usr/bin/env bash
# M0b scenario 3: collect after the reboot. Works from a fresh session; safe to run repeatedly.
#   collect.sh [--watch SECS] [state dir]      (default: the newest reboot state)
# Boot-time events: the daemon keeps only 256 in memory, so collect soon after the boot; the listener
# unit (started at boot by the user manager) and the inspect timestamps cover the rest.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"
# shellcheck source=../boot-lib.sh
source "$here/../boot-lib.sh"

boot_collect reboot "$@"
