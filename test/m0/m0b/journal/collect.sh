#!/usr/bin/env bash
# M0b scenario 4: collect the journal evidence: the E8 window, plus the daemon-restart, reboot and upgrade windows of the other
# scenarios if their state exists. Scrubbed excerpts only leave captures/ through scripts/scrub-fixture --journal.
#   collect.sh [state dir]
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"

m0b_need jq journalctl
m0b_state_find journal "${1-}"
out="$STATE/collected"
mkdir -p "$out"
sum="$out/summary.txt"
: >"$sum"
say() { printf '%s\n' "$*" | tee -a "$sum"; }
how=$(m0b_journal_access)
say "# journal evidence, collected $(date -u +%FT%TZ); read access: $how"
if [[ "$how" == none ]]; then
  say "the journal is not readable by $USER: run the step from prepare.sh (sudo usermod -aG systemd-journal $USER) and collect again"
  exit 1
fi
say "journal storage: $(if [[ -d /var/log/journal ]]; then echo persistent; else echo volatile; fi); boots: $(if [[ "$how" == plain ]]; then journalctl --list-boots -q 2>/dev/null | wc -l; else echo "(not listed via $how)"; fi)"
say ""

# window <label> <since> <until>
window() {
  local label="$1" since="$2" until_="$3" raw="$out/journal-$1.raw.txt" scrubbed="$out/journal-$1.scrubbed.txt" pat n
  m0b_journal "$since" "$until_" "$raw" >/dev/null
  m0b_journal_scrub "$raw" "$scrubbed"
  say "== $label: $(date -u -d "@${since%.*}" +%T)Z to $(date -u -d "@${until_%.*}" +%T)Z, $(wc -l <"$raw") raw lines"
  for pat in 'failed to start container' 'restartmanger wait error' 'failed to restart container' 'failed to prepare mount points' 'Could not find dependent container'; do
    n=$(grep -cF "$pat" "$raw" || true)
    say "   $(printf '%-40s' "'$pat'") $n"
  done
  if [[ -s "$scrubbed" ]]; then
    say "   excerpt (scrubbed):"
    head -12 "$scrubbed" | cut -c1-300 | sed 's/^/     /' | tee -a "$sum"
  fi
  say ""
}

# shellcheck disable=SC2154  # from state.env
window e8 "$(awk -v t="$t_e8_start" 'BEGIN{printf "%d", t - 5}')" "$(awk -v t="$t_e8_end" 'BEGIN{printf "%d", t + 5}')"

for sc in upgrade daemon-restart reboot; do
  d=""
  for x in "$M0B_DIR/$sc"/*/; do [[ -f "${x}step-time" ]] && d="${x%/}"; done
  [[ -n "$d" ]] || { say "== $sc: no state with a step time; skipped"; say ""; continue; }
  s=$(head -1 "$d/step-time")
  if [[ -s "$d/step-done" ]]; then e=$(awk -v t="$(head -1 "$d/step-done")" 'BEGIN{printf "%d", t + 900}'); else e=$(date +%s); fi
  now=$(date +%s)
  ((e > now)) && e=$now
  window "$sc" "$(awk -v t="$s" 'BEGIN{printf "%d", t - 5}')" "$e"
done
say "Scrubbed excerpts: $out/journal-*.scrubbed.txt (commit only after reading them; scripts/scrub-fixture --check)"
