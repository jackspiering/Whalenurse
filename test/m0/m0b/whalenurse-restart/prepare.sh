#!/usr/bin/env bash
# M0b scenario 6 (whalenurse-restart): whalenurse in a container, built from this branch, with a SUSPENDED crash-looper
# and a stack for the `docker compose stop` check. Prints the one maintainer step (a daemon restart). Run collect.sh afterwards.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"
# shellcheck source=../nurse-lib.sh
source "$here/../nurse-lib.sh"

nurse_prepare
