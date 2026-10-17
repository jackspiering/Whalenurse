#!/usr/bin/env bash
# M0b scenario 4: journal evidence for E8. Re-runs E8 (the failed restart: `restartmanger wait error`), records its time
# window, and, if the journal is not readable yet, prints the one step the maintainer must do (join systemd-journal).
# The boot-time `failed to start container` lines come from scenarios 2 and 3; collect.sh reads them back from the
# persistent journal, so the order of the scenarios does not matter.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"

m0b_need jq docker journalctl
m0_sanitize_env
m0b_state_new journal
m0b_set t_prepare "$EPOCHREALTIME"
how=$(m0b_journal_access)
m0b_set access_before "$how"
echo "journal access now: $how"
echo "persistent journal: $(if [[ -d /var/log/journal ]]; then echo "yes (/var/log/journal exists)"; else echo "NO: boot-time evidence would be lost at a reboot"; fi)"
echo "groups: $(id -nG)"

echo "== re-running E8 (about 4 minutes) so its journal lines fall in a known window"
M0_RUN_ID=m0b-e8 "$M0B_ROOT/test/m0/run.sh" prepare >/dev/null
t0="$EPOCHREALTIME"
M0_RUN_ID=m0b-e8 "$M0B_ROOT/test/m0/run.sh" E8 >"$STATE/e8-run.log" 2>&1 || true
t1="$EPOCHREALTIME"
m0b_set t_e8_start "$t0"
m0b_set t_e8_end "$t1"
m0b_set e8_dir "$M0B_ROOT/captures/m0-m0b-e8"
tail -12 "$STATE/e8-run.log"

if [[ "$how" == none ]]; then
  m0b_write_step "let your user read the journal (join the systemd-journal group)" \
    "sudo usermod -aG systemd-journal $USER"
  echo "No re-login is needed for collect.sh: it falls back to 'sg systemd-journal' when this shell predates the group change."
else
  echo "The journal is already readable ($how); no maintainer step is needed for this scenario."
fi
m0b_print_collect journal
