#!/usr/bin/env bash
# M0b scenario 3: host reboot with the same boot stack as scenario 2. If scenario 2's stack is still up, run it
# with M0B_RESET=1 to take it down first (only the whalenurse-m0-b-boot project and the listener units).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"
# shellcheck source=../boot-lib.sh
source "$here/../boot-lib.sh"

if [[ "${M0B_RESET-}" == 1 ]]; then
  m0_sanitize_env
  m0_down "$M0B_BOOT_PROJECT"
  m0b_listener_remove
fi
boot_prepare reboot "reboot the host (the listener is a user unit with lingering; it restarts at boot)" \
  'sudo systemctl reboot'
