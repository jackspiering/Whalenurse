#!/usr/bin/env bash
# The boot stack shared by scenario 2 (daemon restart) and scenario 3 (host reboot).
# Source this file after lib.sh; do not run it. See stacks/boot/compose.yaml for what each pair tests.
# shellcheck shell=bash

M0B_BOOT_SERVICES_NOT_STARTED=(z-created) # created only: HasBeenStartedBefore is false

boot_p() { printf '%s' "$M0B_BOOT_PROJECT"; }

boot_slow_ports() { printf '%s' "${M0B_SLOW_PORTS:-240}"; } # ~7s start on the M0 VM: 200 ports took 6.1s, 60 took 2.3s

boot_check_ports_free() {
  local lo=41000 hi=$((41000 + $(boot_slow_ports) - 1)) used
  used=$(ss -ltnH 2>/dev/null | awk -v lo="$lo" -v hi="$hi" '{n = split($4, a, ":"); p = a[n] + 0; if (p >= lo && p <= hi) c++} END {print c + 0}')
  ((used == 0)) || m0_die "$used ports in $lo-$hi are already in use; the slow-start parent needs them (M0B_SLOW_PORTS=$(boot_slow_ports))"
}

# boot_prepare <scenario> <step description> <step command line>...
boot_prepare() {
  local sc="$1" desc="$2" P svc start_args=() stack dir
  shift 2
  P=$(boot_p)
  m0b_need jq docker awk systemctl ss
  m0_sanitize_env
  [[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$P")" ]] ||
    m0_die "project $P already exists. Run test/m0/m0b/teardown.sh first (this script never removes it unasked)"
  m0b_preflight_other
  boot_check_ports_free
  m0b_ensure_images

  m0b_state_new "$sc"
  m0b_set t_prepare "$EPOCHREALTIME"
  m0b_set project "$P"
  m0b_set boot_id "$(cat /proc/sys/kernel/random/boot_id)"
  m0b_versions "$STATE/versions-before.txt"
  stack="$STATE/stack"
  mkdir -p "$stack/f-src" "$stack/g-src"
  cp "$M0B_HERE/stacks/boot/compose.yaml" "$M0B_HERE/stacks/boot/expect.tsv" "$stack/"
  printf 'P_LO=41000\nP_HI=%s\n' "$((41000 + $(boot_slow_ports) - 1))" >"$stack/vars.env"
  m0b_set stack_dir "$stack"
  m0b_set slow_ports "$(boot_slow_ports)"
  dir="$stack"

  # The listener goes first, so it also records how the manual stops below look.
  m0b_listener_install "$sc" "$P"

  while IFS= read -r svc; do
    [[ " ${M0B_BOOT_SERVICES_NOT_STARTED[*]} " == *" $svc "* ]] || start_args+=("$svc")
  done < <(dcs "$P" "$dir" config --services | sort)
  echo "== bringing the stack up (the slow-start parent publishes $(boot_slow_ports) ports; this takes a while)"
  dcs "$P" "$dir" up -d --pull never --no-build "${start_args[@]}" >"$STATE/up.txt" 2>&1 || {
    tail -5 "$STATE/up.txt"
    m0_die "compose up failed (see $STATE/up.txt)"
  }
  dcs "$P" "$dir" create --pull never --no-build "${M0B_BOOT_SERVICES_NOT_STARTED[@]}" >>"$STATE/up.txt" 2>&1
  wait_for 90 is_healthy "$(cid "$P" a-parent)" || echo "WARN a-parent not healthy"
  wait_for 120 is_healthy "$(cid "$P" s-parent)" || echo "WARN s-parent not healthy after 120s"

  echo "== timing a start of the slow parent (stop both, time 'docker start' of p-parent, start the dependent again)"
  dk_ctr stop "$(cid "$P" p-dep)" -t 2 >/dev/null
  dk_ctr stop "$(cid "$P" p-parent)" -t 2 >/dev/null
  t_ps="$EPOCHREALTIME"
  dk_ctr start "$(cid "$P" p-parent)" >/dev/null
  m0b_set p_start_seconds "$(m0b_secs "$t_ps" "$EPOCHREALTIME")"
  dk_ctr start "$(cid "$P" p-dep)" >/dev/null
  echo "p-parent start took $(m0b_secs "$t_ps" "$EPOCHREALTIME")s with $(boot_slow_ports) ports (the daemon waits 5s for a container: parent)"

  echo "== manual stops (so the step finds stopped containers it must not restart)"
  dk_ctr stop "$(cid "$P" m-unless)" -t 2 >/dev/null
  dk_ctr stop "$(cid "$P" m-always)" -t 2 >/dev/null

  echo "== the bind sources of f-parent and g-dep are deleted now (as in M0 E8): their next start will fail"
  [[ "$stack" == "$STATE"/* ]] || m0_die "refusing to delete outside the state directory"
  rmdir "$stack/f-src" "$stack/g-src"

  mkdir -p "$STATE/baseline"
  m0b_table "$P" >"$STATE/baseline/table.tsv"
  m0b_netns "$P" >"$STATE/baseline/netns.tsv"
  m0b_inspect_dir "$P" "$STATE/baseline/inspect"
  {
    echo "baseline at $(date -u +%FT%TZ), boot_id $(cat /proc/sys/kernel/random/boot_id)"
    column -t -s$'\t' "$STATE/baseline/table.tsv"
    echo
    column -t -s$'\t' "$STATE/baseline/netns.tsv"
    echo
    echo "p-parent: 'docker start' took $(grep '^p_start_seconds=' "$STATE/state.env" | tail -1 | cut -d= -f2)s at $(boot_slow_ports) published ports (the daemon waits 5s for a container: parent)"
  } >"$STATE/baseline/summary.txt"
  cat "$STATE/baseline/summary.txt"

  echo "== persistence check (collect.sh must work after a reboot)"
  m0b_persistence_check "$sc" "$(m0b_listener_unit "$sc")" || m0_die "something collect.sh needs would not survive: fix it before the step"
  m0b_write_step "$desc" "$@"
  cat <<EOF
Notes:
  - Everything in $P and the listener unit stay in place until test/m0/m0b/teardown.sh.
  - The listener (a user systemd unit; Linger=yes) records docker events into
    $STATE/events-listener.ndjson across the step.
  - After the step, the first collect can run as soon as 'docker ps' answers. Run it again
    about 3 minutes later (or use --watch 180 once) to show that failed starts are not retried.

EOF
  m0b_print_collect "$(basename "$(dirname "$STATE")")" "--watch 180"
  echo "From a fresh session (also in $STATE/RESUME.txt):"
  sed 's/^/    /' "$STATE/RESUME.txt"
}

# boot_collect <scenario> [--watch SECS] [state dir]
boot_collect() {
  local sc="$1" watch=0 dir="" P svc t_step t_done t_daemon now boot_now out sum kind nbad i snap first last
  shift
  while (($#)); do
    case "$1" in
      --watch) watch="${2:?--watch SECS}"; shift 2 ;;
      *) dir="$1"; shift ;;
    esac
  done
  m0b_need jq docker awk systemctl column
  m0_sanitize_env
  m0b_state_find "$sc" "$dir"
  # shellcheck disable=SC2154  # project and boot_id come from state.env
  P="$project"
  m0b_require_project "$P"
  docker version --format '{{.Server.Version}}' >/dev/null 2>&1 || m0_die "the Docker daemon does not answer yet; run this again in a minute"
  out="$STATE/collected"
  mkdir -p "$out" "$STATE/snapshots"
  sum="$out/summary.txt"
  : >"$sum"
  csay() { printf '%s\n' "$*" | tee -a "$sum"; }

  t_step=$(m0b_step_time)
  t_done=$(m0b_step_done)
  now="$EPOCHREALTIME"
  t_daemon=$(date -d "$(systemctl show docker -p ActiveEnterTimestamp --value)" +%s.%N)
  boot_now=$(cat /proc/sys/kernel/random/boot_id)
  # shellcheck disable=SC2154
  if [[ "$boot_now" == "$boot_id" ]]; then kind="same boot (daemon restart)"; else kind="NEW boot (host reboot)"; fi
  csay "# $sc: collected $(date -u +%FT%TZ) from $STATE"
  csay "step started $(date -u -d "@${t_step%.*}" +%T)Z, step script finished $(date -u -d "@${t_done%.*}" +%T)Z, daemon (re)started $(date -u -d "@${t_daemon%.*}" +%T)Z, now +$(m0b_secs "$t_daemon" "$now")s after the daemon start"
  csay "boot id: $kind"
  m0b_versions "$out/versions-after.txt"
  csay "docker: $(docker version --format 'server={{.Server.Version}} api={{.Server.APIVersion}}'), store: $(docker info --format '{{.Driver}} {{json .DriverStatus}}')"
  csay ""

  # ---- sampling: failed starts must not be retried -------------------------------------------------
  take_snapshot() { m0b_table "$P" >"$STATE/snapshots/$(date +%s).tsv"; }
  take_snapshot
  for ((i = 0; i < watch; i += 10)); do
    sleep 10
    take_snapshot
  done

  csay "== containers now (health column omitted from the no-retry comparison)"
  m0b_table "$P" >"$out/table.tsv"
  m0b_netns "$P" >"$out/netns.tsv"
  column -t -s$'\t' "$out/table.tsv" | cut -c1-230 | tee -a "$sum"
  csay ""
  csay "== network namespaces"
  column -t -s$'\t' "$out/netns.tsv" | tee -a "$sum"
  csay ""
  csay "== observed vs the source-based expectation (stacks/boot/expect.tsv)"
  nbad=0
  m0b_compare "$STATE/stack/expect.tsv" "$out/table.tsv" "$out/netns.tsv" >"$out/compare.tsv" || nbad=$?
  column -t -s$'\t' "$out/compare.tsv" | tee -a "$sum"
  csay "unexpected: $nbad"
  csay ""

  # ---- no retry ------------------------------------------------------------------------------------
  first=""
  last=""
  for snap in "$STATE/snapshots/"*.tsv; do
    [[ "$(basename "$snap" .tsv)" -ge "${t_daemon%.*}" ]] || continue
    [[ -n "$first" ]] || first="$snap"
    last="$snap"
  done
  if [[ -n "$first" && "$first" != "$last" ]]; then
    csay "== no-retry check: first vs last snapshot after the daemon start ($(($(basename "$last" .tsv) - $(basename "$first" .tsv)))s apart)"
    if diff <(cut -f1,3,4,5,7,8,9,10 "$first") <(cut -f1,3,4,5,7,8,9,10 "$last") >"$out/snapshot.diff"; then
      csay "no container changed status, exit code, RestartCount, StartedAt, FinishedAt, network mode or State.Error: failed starts were not retried"
    else
      csay "CHANGED between the two snapshots:"
      sed 's/^/    /' "$out/snapshot.diff" | tee -a "$sum"
    fi
  else
    csay "== no-retry check: only one snapshot after the daemon start; run collect.sh again later or with --watch 180"
  fi
  csay ""

  # ---- events --------------------------------------------------------------------------------------
  boot_collect_events "$sc" "$t_step" "$t_daemon" "$out" "$sum"

  # ---- the 5s wait for container: parents ----------------------------------------------------------
  csay "== start order of every parent/dependent pair (seconds after the daemon start, from State.StartedAt)"
  boot_pairs "$P" "$t_daemon" | column -t -s$'\t' | tee -a "$sum"
  csay ""

  # ---- journal -------------------------------------------------------------------------------------
  local how
  how=$(m0b_journal "$(awk -v t="$t_step" 'BEGIN{printf "%d", t - 5}')" "${now%.*}" "$out/journal.raw.txt")
  m0b_journal_scrub "$out/journal.raw.txt" "$out/journal.scrubbed.txt"
  csay "== journal (docker and containerd units; read access: $how)"
  if [[ "$how" == none ]]; then
    csay "not readable: after 'sudo usermod -aG systemd-journal $USER' run this again; the journal is persistent, so nothing is lost"
  else
    csay "raw lines in the window: $(wc -l <"$out/journal.raw.txt")"
    for svc in 'failed to start container' 'failed to prepare mount points' 'restartmanger wait error' 'failed to restart container' 'Could not find dependent container'; do
      csay "  '$svc': $(grep -cF "$svc" "$out/journal.raw.txt") lines"
    done
    csay "scrubbed excerpt (journal.scrubbed.txt, first 40 lines):"
    head -40 "$out/journal.scrubbed.txt" | cut -c1-260 | sed 's/^/    /' | tee -a "$sum"
  fi
  csay ""

  # ---- fixtures (raw; promote with test/m0/promote-fixtures.sh) ------------------------------------
  m0b_inspect_dir "$P" "$out/inspect"
  mkdir -p "$STATE/fixtures-raw"
  for svc in f-parent f-dep g-dep n-dep p-dep s-dep m-always; do
    [[ -f "$out/inspect/$svc.json" ]] && cp "$out/inspect/$svc.json" "$STATE/fixtures-raw/m0b-$sc-$svc.json"
  done
  # exec_* events (healthchecks and this script's own netns probes) are noise in a fixture
  [[ -s "$out/events-shutdown.ndjson" ]] && jq -c 'select(.Action | test("^exec_") | not)' "$out/events-shutdown.ndjson" >"$STATE/fixtures-raw/m0b-$sc-shutdown.events.ndjson"
  [[ -s "$out/events-boot.ndjson" ]] && jq -c 'select(.Action | test("^exec_") | not)' "$out/events-boot.ndjson" >"$STATE/fixtures-raw/m0b-$sc-boot.events.ndjson"
  csay "raw fixtures in $STATE/fixtures-raw (never commit these); promote the useful ones with:"
  csay "    test/m0/promote-fixtures.sh $STATE <name>...   # names: $(cd "$STATE/fixtures-raw" && printf '%s\n' * | sed -E 's/\.events\.ndjson$/.events/; s/\.json$//' | tr '\n' ' ')"
  m0b_set collected_at "$(date -u +%FT%TZ)"
  csay ""
  csay "summary: $sum"
}

# boot_collect_events <scenario> <t_step> <t_daemon> <out dir> <summary file>
boot_collect_events() {
  local sc="$1" t_step="$2" t_daemon="$3" out="$4" sum="$5" a b dis
  csay() { printf '%s\n' "$*" | tee -a "$sum"; }
  if [[ -s "$STATE/events-listener.ndjson" ]]; then
    m0b_events_normalize "$STATE/events-listener.ndjson" >"$out/events-listener.norm.ndjson"
  else
    : >"$out/events-listener.norm.ndjson"
  fi
  # The first disconnect after the step ends the old daemon's stream.
  dis=$(jq -r --argjson t "$t_step" 'select(._marker == "disconnect" and .t >= $t) | .t' "$out/events-listener.norm.ndjson" | head -1)
  a=$(awk -v t="$t_step" 'BEGIN{printf "%.3f", t - 1}')
  # Without a disconnect marker the listener was stopped with the session (a reboot): the shutdown window is the first 30s.
  b=${dis:-$(awk -v t="$t_step" 'BEGIN{printf "%.3f", t + 30}')}
  jq -c --argjson a "$a" --argjson b "$b" 'select(has("Action") and ((.timeNano / 1000000000) >= $a and (.timeNano / 1000000000) <= ($b + 0.5)))' \
    "$out/events-listener.norm.ndjson" >"$out/events-shutdown.ndjson"
  jq -c --argjson b "$b" 'select(has("Action") and ((.timeNano / 1000000000) > $b))' "$out/events-listener.norm.ndjson" >"$out/events-boot.ndjson"
  # What the new daemon itself still has in its 256-event buffer (cross-check, and the only source if the listener missed it).
  docker events --since "${t_step%.*}" --until "$(date +%s)" --filter type=container \
    --filter "label=com.docker.compose.project=$project" --format '{{json .}}' >"$out/events-newdaemon.ndjson" 2>&1 || true

  csay "== events seen by the listener during the shutdown (old daemon; stream closed $(awk -v a="$t_step" -v b="${dis:-0}" 'BEGIN{ if (b > 0) printf "+%.1fs after the step started", b - a; else print "never: no disconnect marker" }'))"
  if [[ -z "$dis" ]]; then
    csay "NO disconnect marker after the step: the listener was stopped with the user session before the Docker stream closed (expected on a reboot), or the step never ran. Window used: the first 30s."
  fi
  if [[ -s "$out/events-shutdown.ndjson" ]]; then
    m0b_events_by_service "$out/events-shutdown.ndjson" | tee -a "$sum"
    csay "(kill[SIG] is the signal; die(N) the exit code. Services with no line were not running or not seen.)"
  else
    csay "none"
  fi
  csay ""
  csay "== events after the daemon came back, seen by the listener"
  if [[ -s "$out/events-boot.ndjson" ]]; then
    m0b_events_by_service "$out/events-boot.ndjson" | tee -a "$sum"
  else
    csay "none"
  fi
  csay ""
  csay "== the same, from the new daemon's own buffer (docker events --since the step started): $(grep -c . "$out/events-newdaemon.ndjson") lines"
  if [[ -s "$out/events-newdaemon.ndjson" ]] && jq -e . "$out/events-newdaemon.ndjson" >/dev/null 2>&1; then
    m0b_events_by_service "$out/events-newdaemon.ndjson" | tee -a "$sum"
  fi
  csay ""
  csay "== timeline (seconds after the step started; boot-time starts)"
  m0b_events_rel "$out/events-listener.norm.ndjson" "$t_step" . >"$out/timeline.txt" 2>/dev/null || true
  awk -F'\t' '$1 !~ /^-/ && ($3 ~ /^(start|die|kill|stop|oom|health_status|restart)/ || $2 == "-")' "$out/timeline.txt" | head -150 | column -t -s$'\t' | tee -a "$sum"
  csay "(full timeline: $out/timeline.txt)"
  csay ""
}

# boot_epoch <RFC3339 time from inspect> -> epoch seconds, or "never" for the zero time.
boot_epoch() {
  if [[ -z "${1-}" || "$1" == 0001-01-01* ]]; then echo never; else date -d "$1" +%s.%N; fi
}

boot_rel() { if [[ "$2" == never ]]; then echo never; else m0b_secs "$1" "$2"; fi; }

# boot_pairs <project> <t_daemon>: parent/dependent pairs with State.StartedAt relative to the daemon start.
boot_pairs() {
  local p="$1" td="$2" ids dep par ps ds st a b
  ids=$(docker ps -aq --no-trunc --filter "label=com.docker.compose.project=$p")
  printf 'dependent\tparent\tparent-started\tdep-started\tdep-minus-parent\tdep-status\n'
  while IFS=$'\t' read -r dep par ps ds st; do
    a=$(boot_epoch "$ps")
    b=$(boot_epoch "$ds")
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$dep" "$par" "$(boot_rel "$td" "$a")" "$(boot_rel "$td" "$b")" \
      "$(if [[ "$a" == never || "$b" == never ]]; then echo -; else m0b_secs "$a" "$b"; fi)" "$st"
  done < <(
    # shellcheck disable=SC2086  # one id per line
    docker inspect $ids | jq -r '. as $all | .[] | select(.HostConfig.NetworkMode | startswith("container:"))
      | (.HostConfig.NetworkMode | .[10:]) as $pid
      | ([$all[] | select(.Id == $pid)][0]) as $par
      | [.Config.Labels["com.docker.compose.service"], ($par.Config.Labels["com.docker.compose.service"] // "(gone)"),
         ($par.State.StartedAt // ""), .State.StartedAt, .State.Status] | @tsv'
  )
}
