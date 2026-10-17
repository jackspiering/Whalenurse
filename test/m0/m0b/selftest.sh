#!/usr/bin/env bash
# Self-test of the M0b helpers. Needs no Docker and no systemd: docker is a stub function, state goes to a temp dir.
#   test/m0/m0b/selftest.sh
# A && ok || bad is the test idiom here (ok/bad never fail); the single quotes hold literal text for the code under test;
# subshells keep each state test from leaking into the next; the stub docker is only called by the functions under test.
# shellcheck disable=SC2015,SC2016,SC2030,SC2031,SC2032,SC2154
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$here/lib.sh"
# shellcheck source=nurse-lib.sh
source "$here/nurse-lib.sh"
command -v jq >/dev/null || { echo "jq is required"; exit 2; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
M0B_DIR="$tmp/m0b"
fail=0
ok() { printf 'ok   %s\n' "$1"; }
bad() { printf 'FAIL %s\n' "$1"; fail=1; }
expect() { # expect <description> <command...>   (command must succeed)
  local d="$1"
  shift
  if ("$@") >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi
}
refuse() { # refuse <description> <command...>   (command must exit non-zero and say REFUSED)
  local d="$1" out
  shift
  if out=$( ("$@") 2>&1); then bad "$d was allowed"; elif grep -q REFUSED <<<"$out"; then ok "$d"; else bad "$d failed without REFUSED: $out"; fi
}

# ---- project guard: M0b is stricter than M0
for p in whalenurse-m0-b-boot whalenurse-m0-b-imgdrift; do expect "project $p accepted" m0b_require_project "$p"; done
for p in whalenurse-m0-e1 whalenurse-m0-bx whalenurse-m0-b- whalenurse-test-b-x immich "" 'whalenurse-m0-b-a b' 'whalenurse-m0-b-$(id)'; do
  refuse "project '$p' refused" m0b_require_project "$p"
done
refuse "listener refuses an M0 (not M0b) project" "$here/listener.sh" "$tmp/out" "$tmp/stop" whalenurse-m0-e1
refuse "listener refuses a real project" "$here/listener.sh" "$tmp/out" "$tmp/stop" immich
refuse "listener needs a project" "$here/listener.sh" "$tmp/out" "$tmp/stop"

# ---- state round trip, last value wins, newest dir wins
(
  m0b_state_new demo
  m0b_set k 'a value with spaces and $dollar'
  m0b_set k 'second'
  first="$STATE"
  sleep 1
  m0b_state_new demo
  m0b_set k 'newest'
  newest="$STATE"
  STATE=""
  m0b_state_find demo
  [[ "$STATE" == "$newest" && "$k" == newest ]] || exit 1
  m0b_state_find demo "$first"
  [[ "$k" == second ]] || exit 2
) && ok "state: set/find, last value wins, newest directory by default" || bad "state round trip"
refuse "state: unknown scenario" m0b_state_find nosuch

# ---- step script: stamps before and after, runs every line, quotes the state dir
(
  m0b_state_new steptest
  m0b_write_step "demo" 'echo first >"$0.out"' 'false' 'echo third >>"$0.out"' >/dev/null
  bash "$STATE/step.sh"
  [[ -s "$STATE/step-time" && -s "$STATE/step-done" ]] || exit 1
  awk -v a="$(cat "$STATE/step-time")" -v b="$(cat "$STATE/step-done")" 'BEGIN{exit !(b >= a && a > 1700000000)}' || exit 2
  [[ "$(cat "$STATE/step.sh.out")" == $'first\nthird' ]] || exit 3
  t_prepare=1.5
  [[ "$(m0b_step_time)" == "$(head -1 "$STATE/step-time")" ]] || exit 4
  rm "$STATE/step-time"
  [[ "$(m0b_step_time 2>/dev/null)" == 1.5 ]] || exit 5
) && ok "step script: stamps, continues after a failing line, falls back to the prepare time" || bad "step script"

# ---- events: dedupe, window, per-service sequence
cat >"$tmp/ev.ndjson" <<'JSON'
{"_marker":"connect","t":100.0}
{"Type":"container","Action":"kill","id":"aa","Actor":{"ID":"aa","Attributes":{"com.docker.compose.service":"web","signal":"15"}},"timeNano":101000000000}
{"Type":"container","Action":"kill","id":"aa","Actor":{"ID":"aa","Attributes":{"com.docker.compose.service":"web","signal":"15"}},"timeNano":101000000000}
{"Type":"container","Action":"exec_start: x","id":"aa","Actor":{"ID":"aa","Attributes":{"com.docker.compose.service":"web","execID":"e1"}},"timeNano":101500000000}
{"Type":"container","Action":"die","id":"aa","Actor":{"ID":"aa","Attributes":{"com.docker.compose.service":"web","exitCode":"143"}},"timeNano":102000000000}
{"Type":"container","Action":"kill","id":"bb","Actor":{"ID":"bb","Attributes":{"com.docker.compose.service":"db","signal":"9"}},"timeNano":102500000000}
{"_marker":"disconnect","t":103.0}
JSON
m0b_events_normalize "$tmp/ev.ndjson" >"$tmp/norm.ndjson"
[[ $(grep -c . "$tmp/norm.ndjson") -eq 6 ]] && ok "events: a replayed duplicate is dropped, markers kept" || bad "events dedupe ($(grep -c . "$tmp/norm.ndjson") lines)"
seq_web=$(m0b_events_by_service "$tmp/norm.ndjson" '^web$')
[[ "$seq_web" == "web:         kill[15] die(143)" ]] && ok "events: per-service sequence skips exec noise" || bad "events by service: '$seq_web'"
rel=$(m0b_events_rel "$tmp/norm.ndjson" 100 | paste -sd'|' -)
[[ "$rel" == *"+1s"$'\t'"web"$'\t'"kill[sig 15]"* && "$rel" == *"+3s"$'\t'"-"$'\t'"<listener disconnect>"* ]] && ok "events: relative timeline with markers" || bad "events rel: $rel"

# ---- expectation comparison
printf 'service\tid\tstatus\texit\trestarts\thealth\tstarted\tfinished\tnetmode\terror\nfail\tabc\texited\t137\t0\t-\ts\tf\tbridge\tfailed to fulfil mount request\nok\tabd\trunning\t0\t0\t-\ts\tf\tbridge\t\nwrong\tabe\trunning\t0\t0\t-\ts\tf\tbridge\t\n' >"$tmp/table.tsv"
printf 'dependent\tparent\tsame-netns\nok\tp\tsame\nwrong\tp\tDIFFERENT(a vs b)\n' >"$tmp/netns.tsv"
printf '# c\nfail\texited\t.+mount.+\t-\nok\trunning\t-\tsame\nwrong\trunning\t-\tsame\nmissing\tany\tany\tany\n' >"$tmp/expect.tsv"
m0b_compare "$tmp/expect.tsv" "$tmp/table.tsv" "$tmp/netns.tsv" >"$tmp/cmp.out"
rc=$?
[[ $rc -eq 2 ]] && grep -q $'^DIFF\twrong' "$tmp/cmp.out" && grep -q $'^DIFF\tmissing' "$tmp/cmp.out" && grep -q $'^ok\tfail' "$tmp/cmp.out" && grep -q $'^ok\tok' "$tmp/cmp.out" &&
  ok "compare: matches, a netns mismatch and a missing service are DIFFs" || bad "compare (rc=$rc): $(cat "$tmp/cmp.out")"

# ---- other containers preflight (docker is a stub)
# shellcheck disable=SC2317  # called by the functions under test
docker() {
  case "$1" in
    ps) printf '%b' "$STUB_PS" ;;
    *) return 0 ;;
  esac
}
STUB_PS='boot-a-parent-1\twhalenurse-m0-b-boot\tUp 2 minutes\nimmich\timmich\tUp 3 days\nstray\t\tExited (0) 1 hour ago\n'
others=$(m0b_other_containers)
[[ "$others" == *immich* && "$others" == *stray* && "$others" != *boot-a-parent* ]] && ok "preflight: lists containers outside whalenurse-m0-*" || bad "other containers: '$others'"
refuse "preflight refuses without M0B_ALLOW_OTHER_CONTAINERS" m0b_preflight_other
(M0B_ALLOW_OTHER_CONTAINERS=1 m0b_preflight_other) >/dev/null 2>&1 && ok "preflight: allowed with M0B_ALLOW_OTHER_CONTAINERS=1" || bad "preflight override"
STUB_PS='boot-a-parent-1\twhalenurse-m0-b-boot\tUp 2 minutes\n'
m0b_preflight_other >/dev/null 2>&1 && ok "preflight: nothing else on the host" || bad "preflight on a clean host"
unset -f docker

# ---- journal scrub: only the interesting lines, scrubbed
cat >"$tmp/j.raw" <<JOURNAL
Oct 17 13:00:00.100000 $(hostname) dockerd[1]: unrelated line about /home/someone/x
Oct 17 13:00:01.200000 $(hostname) dockerd[1]: time="2026-10-17T13:00:01Z" level=error msg="failed to start container" container=0123abcd error="open /home/someone/f-src: no such file (10.1.2.3)"
Oct 17 13:00:02.300000 $(hostname) dockerd[1]: level=error msg="restartmanger wait error: x"
JOURNAL
m0b_journal_scrub "$tmp/j.raw" "$tmp/j.out"
[[ $(wc -l <"$tmp/j.out") -eq 2 ]] && ! grep -qE "someone|10\.1\.2|$(hostname)" "$tmp/j.out" && grep -q 'container=0123abcd' "$tmp/j.out" &&
  "$M0B_ROOT/scripts/scrub-fixture" --check "$tmp/j.out" 2>/dev/null && ok "journal: filtered to the two interesting lines, scrubbed, passes --check" || bad "journal scrub: $(cat "$tmp/j.out")"

# ---- the boot stack is valid YAML with the pairs the expectations name
if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  svcs=$(cd "$tmp" && mkdir -p stack && cp "$here/stacks/boot/compose.yaml" stack/ && printf 'P_LO=41000\nP_HI=41009\n' >stack/vars.env &&
    docker compose --project-directory stack -f stack/compose.yaml --env-file stack/vars.env config --services 2>&1 | sort)
  want=$(grep -vE '^#|^$' "$here/stacks/boot/expect.tsv" | cut -f1 | sort)
  [[ "$svcs" == "$want" ]] && ok "boot stack: compose config lists exactly the services in expect.tsv" || bad "boot stack services differ from expect.tsv: $(diff <(echo "$svcs") <(echo "$want") | head -5)"
else
  echo "skip boot stack config check (no docker compose)"
fi

# ---- the whalenurse-restart scenario: projects, status and log readers
for p in "$NURSE_PROJECT" "$NURSE_STOP_PROJECT"; do expect "project $p accepted" m0b_require_project "$p"; done
cat >"$tmp/status.json" <<'JSON'
{"host":"m0b","settled":true,"engine":{"restore_window_until":"2020-01-01T00:00:00Z","keys":[
 {"key":"m0b/whalenurse-m0-b-nurse/looper/1","state":"SUSPENDED","suspend_reason":"ladder","flaps":1},
 {"key":"m0b/whalenurse-m0-b-nursestop/s1/1","state":"ok","manual_stop":true,"flaps":0}]}}
JSON
[[ "$(nurse_state "$tmp/status.json" "$NURSE_LOOPER_KEY")" == SUSPENDED && "$(nurse_state "$tmp/status.json" nope)" == "-" ]] && ok "nurse: state of a key" || bad "nurse_state"
[[ "$(nurse_field "$tmp/status.json" "$(nurse_key "$NURSE_STOP_PROJECT" s1)" manual_stop)" == true ]] && ok "nurse: field of a key" || bad "nurse_field"
nurse_restore_open "$tmp/status.json" && bad "a restore window in the past reported open" || ok "nurse: a past restore window is closed"
sed 's/2020-01-01T00:00:00Z/2099-01-01T00:00:00.123456789Z/' "$tmp/status.json" >"$tmp/status-open.json"
nurse_restore_open "$tmp/status-open.json" && ok "nurse: a future restore window is open" || bad "future restore window"
cat >"$tmp/nurse.ndjson" <<'JSON'
{"time":"2026-10-17T12:00:00.5Z","type":"ManualStopMarked","key":"m0b/whalenurse-m0-b-nursestop/s1/1"}
{"time":"2026-10-17T12:00:10Z","type":"ManualStopMarked","key":"m0b/whalenurse-m0-b-nursestop/s1/1"}
{"time":"2026-10-17T12:00:10Z","type":"ActionStarted","key":"m0b/whalenurse-m0-b-nursestop/s2/1"}
JSON
k1=$(nurse_key "$NURSE_STOP_PROJECT" s1)
since=$(date -u -d 2026-10-17T12:00:05Z +%s)
[[ "$(nurse_count "$tmp/nurse.ndjson" ManualStopMarked "$k1")" == 2 && "$(nurse_count_since "$tmp/nurse.ndjson" ManualStopMarked "$k1" "$since")" == 1 &&
  "$(nurse_count_since "$tmp/nurse.ndjson" ActionStarted "$k1" "$since")" == 0 ]] && ok "nurse: event counts, total and since a time" || bad "nurse counts"
if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  for st in nurse nursestop; do
    svcs=$(cd "$tmp" && rm -rf "stack-$st" && mkdir -p "stack-$st" && cp "$here/stacks/$st/compose.yaml" "stack-$st/" &&
      printf 'NURSE_IMAGE=whalenurse-m0/whalenurse:x\nDOCKER_GID=999\n' >"stack-$st/vars.env" &&
      docker compose --project-directory "stack-$st" -f "stack-$st/compose.yaml" --env-file "stack-$st/vars.env" config --services 2>&1 | sort | paste -sd' ')
    case "$st" in nurse) want="looper whalenurse" ;; *) want="s1 s2 s3" ;; esac
    [[ "$svcs" == "$want" ]] && ok "stack $st: compose config lists $want" || bad "stack $st services: '$svcs'"
  done
  # Only opted-in M0b containers: the nurse stack opts in the looper and nothing else, the stop stack its three.
  [[ "$(grep -c 'whalenurse.enable: "true"' "$here/stacks/nurse/compose.yaml")" == 1 && "$(grep -c 'whalenurse.enable: "true"' "$here/stacks/nursestop/compose.yaml")" == 1 ]] &&
    ok "stacks: the opt-in labels are where they should be" || bad "opt-in labels"
fi

((fail == 0)) && echo "m0b selftest OK" || { echo "m0b selftest FAILED"; exit 1; }
