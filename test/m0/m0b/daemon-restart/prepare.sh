#!/usr/bin/env bash
# M0b scenario 2: daemon restart. Builds the boot stack (stacks/boot), records a baseline, installs the event
# listener and prints the one maintainer step (a daemon restart). Run collect.sh afterwards.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"
# shellcheck source=../boot-lib.sh
source "$here/../boot-lib.sh"

boot_prepare daemon-restart "restart the Docker daemon (no live-restore: every container is stopped, then restored)" \
  'sudo systemctl restart docker'
