#!/usr/bin/env bash
# The whalenurse-restart scenario (PLAN v3.5 §3.3, mechanism d): whalenurse in a container, built from this branch,
# next to a crash-looper that it SUSPENDS. The maintainer step is `sudo systemctl restart docker`, which kills
# whalenurse together with everything else. collect.sh then checks that
#   - the looper is still SUSPENDED (the restore's `start` must not clear it), and
#   - a `docker compose stop` of a test project, after the restore window, is still a manual stop (the rejected burst
#     rule would have healed it).
# Source this file after lib.sh; do not run it. See stacks/nurse and stacks/nursestop.
#
# Safety: whalenurse acts on containers that carry whalenurse.enable=true, so prepare.sh refuses to continue if any
# container outside whalenurse-m0-b-* carries it. The projects are whalenurse-m0-b-nurse and whalenurse-m0-b-nursestop
# (both pass the guards of ../lib.sh and m0b_require_project). The image is assembled from the branch's binary on the
# distroless base the Dockerfile uses, so nothing is pulled but the allowlisted base.
# shellcheck shell=bash

NURSE_PROJECT=whalenurse-m0-b-nurse
NURSE_STOP_PROJECT=whalenurse-m0-b-nursestop
NURSE_HOST=m0b # WHALENURSE_HOST_ID in stacks/nurse
NURSE_LOOPER_KEY="$NURSE_HOST/$NURSE_PROJECT/looper/1"
NURSE_STOP_SERVICES=(s1 s2 s3)

nurse_key() { printf '%s/%s/%s/1' "$NURSE_HOST" "$1" "$2"; } # nurse_key <project> <service>

# ---- reading whalenurse ----------------------------------------------------------------------------------

# nurse_status <container> <outfile>: `whalenurse status --json` through docker exec. Returns non-zero if it does not answer.
nurse_status() { dk_ctr exec "$1" /whalenurse status --json >"$2" 2>/dev/null; }

# nurse_state <status.json> <key>: the engine's state of a key, or "-" if it is not listed.
nurse_state() { jq -r --arg k "$2" '(.engine.keys // []) | map(select(.key == $k)) | (.[0].state // "-")' "$1"; }
nurse_field() { jq -r --arg k "$2" --arg f "$3" '(.engine.keys // []) | map(select(.key == $k)) | (.[0][$f] // "-") | tostring' "$1"; }

# nurse_restore_open <status.json>: succeeds while the restore window is open.
nurse_restore_open() {
  jq -e '(.engine.restore_window_until // "") as $u | ($u != "" and ($u | sub("\\.[0-9]+"; "") | fromdateiso8601) > now)' "$1" >/dev/null 2>&1
}

# nurse_logs <container> <since epoch> <outfile>: whalenurse's JSON log lines since a time, one object per line.
# docker logs holds every run of the container; the time field of each line says which run it belongs to.
nurse_logs() {
  docker logs --since "$2" "$1" 2>&1 | jq -c -R 'fromjson? | select(type == "object")' >"$3" 2>/dev/null || true
}

# nurse_count <logfile> <type> <key>: how many domain events of that type the log has for a key.
nurse_count() { jq -s --arg t "$2" --arg k "$3" '[.[] | select(.type == $t and .key == $k)] | length' "$1"; }

# nurse_count_since <logfile> <type> <key> <epoch>: the same, for events at or after a time.
nurse_count_since() {
  jq -s --arg t "$2" --arg k "$3" --argjson since "$4" \
    '[.[] | select(.type == $t and .key == $k and ((.time | sub("\\.[0-9]+Z$"; "Z") | fromdateiso8601) >= $since))] | length' "$1"
}

# ---- prepare ---------------------------------------------------------------------------------------------

# nurse_build_image: sets NURSE_IMAGE. The binary comes from the working tree (which must be committed), the image from the
# runtime stage of the repository's Dockerfile with its two build-stage COPYs pointed at the local files.
nurse_build_image() {
  local ctx="$STATE/image" sha tag dirty
  sha=$(git -C "$M0B_ROOT" rev-parse --short=12 HEAD)
  dirty=$(git -C "$M0B_ROOT" status --porcelain -- cmd internal go.mod go.sum Dockerfile)
  if [[ -n "$dirty" && "${M0B_ALLOW_DIRTY-}" != 1 ]]; then
    m0_die "uncommitted changes in the code that would go into the image (commit them, or M0B_ALLOW_DIRTY=1):
$dirty"
  fi
  m0b_set commit "$sha"
  mkdir -p "$ctx/data"
  echo "== building the binary from commit $sha (CGO_ENABLED=0)"
  (cd "$M0B_ROOT" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=m0b-$sha" -o "$ctx/whalenurse" ./cmd/whalenurse)
  docker image inspect gcr.io/distroless/static:nonroot >/dev/null 2>&1 || m0_pull gcr.io/distroless/static:nonroot
  sed -n '/^FROM gcr.io\/distroless\/static:nonroot$/,$p' "$M0B_ROOT/Dockerfile" |
    sed -e 's|^COPY --from=build /out/whalenurse /whalenurse$|COPY whalenurse /whalenurse|' \
      -e 's|^COPY --from=build --chown=65532:65532 /out/data /data$|COPY --chown=65532:65532 data /data|' >"$ctx/Dockerfile"
  if ! grep -q '^COPY whalenurse /whalenurse$' "$ctx/Dockerfile" || ! grep -q '^COPY --chown=65532:65532 data /data$' "$ctx/Dockerfile" ||
    grep -q -- '--from' "$ctx/Dockerfile"; then
    m0_die "the runtime stage of the Dockerfile changed; update nurse_build_image ($ctx/Dockerfile)"
  fi
  tag="whalenurse-m0/whalenurse:$sha-$(date +%s)"
  [[ "$tag" == whalenurse-m0/* ]] || m0_die "image tag '$tag' is not under whalenurse-m0/"
  docker build -q -t "$tag" "$ctx" >"$STATE/image-build.txt" 2>&1 || {
    tail -5 "$STATE/image-build.txt"
    m0_die "docker build failed (see $STATE/image-build.txt)"
  }
  NURSE_IMAGE="$tag"
  m0b_set nurse_image "$tag"
  echo "image $tag"
}

# nurse_preflight_strangers: whalenurse would act on any opted-in container; refuse unless all of them are M0b ones.
nurse_preflight_strangers() {
  local bad
  bad=$(docker ps -a --filter 'label=whalenurse.enable=true' --format '{{.Names}}\t{{.Label "com.docker.compose.project"}}' |
    awk -F'\t' '$2 !~ /^whalenurse-m0-b-/ {print}')
  [[ -z "$bad" ]] || m0_die "these containers carry whalenurse.enable=true but are not in a whalenurse-m0-b-* project, and the whalenurse under test would act on them:
$bad"
}

nurse_prepare() {
  local sc=whalenurse-restart P="$NURSE_PROJECT" SP="$NURSE_STOP_PROJECT" gid nc i state_json wn
  m0b_need jq docker awk systemctl go git
  m0_sanitize_env
  m0b_require_project "$P"
  m0b_require_project "$SP"
  for i in "$P" "$SP"; do
    [[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$i")" ]] ||
      m0_die "project $i already exists. Run test/m0/m0b/teardown.sh first (this script never removes it unasked)"
  done
  m0b_preflight_other
  nurse_preflight_strangers
  m0b_ensure_images

  m0b_state_new "$sc"
  m0b_set t_prepare "$EPOCHREALTIME"
  m0b_set project "$P"
  m0b_set stop_project "$SP"
  m0b_set boot_id "$(cat /proc/sys/kernel/random/boot_id)"
  m0b_versions "$STATE/versions-before.txt"
  nurse_build_image

  gid=$(stat -c %g /var/run/docker.sock)
  nc="$STATE/stack"
  mkdir -p "$nc" "$STATE/stack-stop"
  cp "$M0B_HERE/stacks/nurse/compose.yaml" "$nc/"
  printf 'NURSE_IMAGE=%s\nDOCKER_GID=%s\n' "$NURSE_IMAGE" "$gid" >"$nc/vars.env"
  cp "$M0B_HERE/stacks/nursestop/compose.yaml" "$STATE/stack-stop/"
  m0b_set stack_dir "$nc"
  m0b_set stop_stack_dir "$STATE/stack-stop"

  m0b_listener_install "$sc" "$P" "$SP"

  echo "== starting the three stoppable containers, then whalenurse and the crash-looper"
  dcs "$SP" "$STATE/stack-stop" up -d --pull never --no-build >"$STATE/up-stop.txt" 2>&1 || {
    tail -5 "$STATE/up-stop.txt"
    m0_die "compose up failed (see $STATE/up-stop.txt)"
  }
  dcs "$P" "$nc" up -d --pull never --no-build >"$STATE/up.txt" 2>&1 || {
    tail -5 "$STATE/up.txt"
    m0_die "compose up failed (see $STATE/up.txt)"
  }
  wn=$(cid "$P" whalenurse)
  wait_for 90 is_running "$wn" || m0_die "whalenurse is not running: docker logs $wn"

  echo "== waiting for whalenurse to SUSPEND the looper (a crash loop needs 3 restarts over 60 s, then one restart rung)"
  state_json="$STATE/baseline-status.json"
  for ((i = 0; i < 150; i++)); do
    if nurse_status "$wn" "$state_json" && [[ "$(nurse_state "$state_json" "$NURSE_LOOPER_KEY")" == SUSPENDED ]]; then break; fi
    sleep 2
  done
  if [[ ! -s "$state_json" || "$(nurse_state "$state_json" "$NURSE_LOOPER_KEY")" != SUSPENDED ]]; then
    docker logs --tail 40 "$wn" 2>&1 | tail -40
    m0_die "the looper is not SUSPENDED after 5 minutes (status in $state_json)"
  fi
  echo "looper: $(nurse_state "$state_json" "$NURSE_LOOPER_KEY"), suspend reason $(nurse_field "$state_json" "$NURSE_LOOPER_KEY" suspend_reason)"

  mkdir -p "$STATE/baseline"
  { m0b_table "$P"; m0b_table "$SP" | tail -n +2; } >"$STATE/baseline/table.tsv"
  m0b_inspect_dir "$P" "$STATE/baseline/inspect"
  m0b_inspect_dir "$SP" "$STATE/baseline/inspect-stop"
  nurse_logs "$wn" 0 "$STATE/baseline/whalenurse-log.ndjson"
  m0b_set nurse_container "$wn"
  m0b_set nurse_started "$(docker inspect --format '{{.State.StartedAt}}' "$wn")"
  {
    echo "baseline at $(date -u +%FT%TZ), boot_id $(cat /proc/sys/kernel/random/boot_id), image $NURSE_IMAGE"
    column -t -s$'\t' "$STATE/baseline/table.tsv"
    echo
    echo "looper key $NURSE_LOOPER_KEY: $(nurse_state "$state_json" "$NURSE_LOOPER_KEY")"
  } >"$STATE/baseline/summary.txt"
  cat "$STATE/baseline/summary.txt"

  echo "== persistence check (collect.sh must work from a fresh session)"
  m0b_persistence_check "$sc" "$(m0b_listener_unit "$sc")" || m0_die "something collect.sh needs would not survive: fix it before the step"
  m0b_write_step "restart the Docker daemon: whalenurse is killed with everything else, then restored; the SUSPENDED crash-looper must stay SUSPENDED" \
    'sudo systemctl restart docker'
  printf '%s\n' \
    'Notes:' \
    "  - Everything in $P and $SP, and the listener unit, stay in place until test/m0/m0b/teardown.sh." \
    "  - whalenurse's state is the named volume of project $P (its SQLite store); teardown.sh removes it with the project." \
    '  - collect.sh waits (up to --watch seconds, default 240) for whalenurse to be back and for its restore window to close,' \
    "    and only then runs 'docker compose stop' on $SP (once) to see whether it is still treated as a manual stop." \
    ''
  m0b_print_collect "$sc" "--watch 240"
  echo "From a fresh session (also in $STATE/RESUME.txt):"
  sed 's/^/    /' "$STATE/RESUME.txt"
}

# ---- collect -----------------------------------------------------------------------------------------------

# nurse_collect [--watch SECS] [state dir]
nurse_collect() {
  local sc=whalenurse-restart watch=240 dir="" P SP wn out sum t_step t_done t_daemon i t_stop svc key s st exit_code
  local fails=0 warns=0
  while (($#)); do
    case "$1" in
      --watch)
        watch="${2:?--watch SECS}"
        shift 2
        ;;
      *)
        dir="$1"
        shift
        ;;
    esac
  done
  m0b_need jq docker awk systemctl
  m0_sanitize_env
  m0b_state_find "$sc" "$dir"
  # shellcheck disable=SC2154  # these come from state.env
  P="$project"
  # shellcheck disable=SC2154
  SP="$stop_project"
  m0b_require_project "$P"
  m0b_require_project "$SP"
  docker version --format '{{.Server.Version}}' >/dev/null 2>&1 || m0_die "the Docker daemon does not answer yet; run this again in a minute"
  out="$STATE/collected"
  mkdir -p "$out"
  sum="$out/summary.txt"
  : >"$sum"
  csay() { printf '%s\n' "$*" | tee -a "$sum"; }
  pass() { csay "PASS  $*"; }
  fail() {
    csay "FAIL  $*"
    fails=$((fails + 1))
  }
  warn() {
    csay "WARN  $*"
    warns=$((warns + 1))
  }

  t_step=$(m0b_step_time)
  t_done=$(m0b_step_done)
  t_daemon=$(date -d "$(systemctl show docker -p ActiveEnterTimestamp --value)" +%s.%N)
  csay "# $sc: collected $(date -u +%FT%TZ) from $STATE"
  csay "step started $(date -u -d "@${t_step%.*}" +%T)Z, finished $(date -u -d "@${t_done%.*}" +%T)Z, daemon (re)started $(date -u -d "@${t_daemon%.*}" +%T)Z"
  # shellcheck disable=SC2154
  if [[ "$(cat /proc/sys/kernel/random/boot_id)" != "$boot_id" ]]; then
    warn "the host rebooted; this scenario is about a daemon restart (the boot id, mechanism b, covers a reboot)"
  fi
  m0b_versions "$out/versions-after.txt"

  wn=$(cid "$P" whalenurse)
  if [[ -z "$wn" ]]; then
    fail "the whalenurse container is gone"
    csay "RESULT: FAIL"
    return 1
  fi

  csay "== waiting up to ${watch}s for whalenurse to be back with its restore window closed"
  for ((i = 0; i < watch; i += 5)); do
    if is_running "$wn" && nurse_status "$wn" "$out/status-after.json" &&
      [[ "$(jq -r '.settled' "$out/status-after.json")" == true ]] && ! nurse_restore_open "$out/status-after.json"; then break; fi
    sleep 5
  done
  if ! nurse_status "$wn" "$out/status-after.json"; then
    fail "whalenurse does not answer on its control socket"
    csay "RESULT: FAIL"
    return 1
  fi
  docker inspect "$wn" >"$out/whalenurse-inspect.json"
  nurse_logs "$wn" 0 "$out/whalenurse-log-all.ndjson"
  nurse_logs "$wn" "$t_step" "$out/whalenurse-log-since-step.ndjson"

  # ---- 1. it came back, with its state
  st=$(docker inspect --format '{{.State.StartedAt}}' "$wn")
  # shellcheck disable=SC2154
  if [[ "$st" != "$nurse_started" ]] && awk -v a="$(date -d "$st" +%s)" -v b="${t_step%.*}" 'BEGIN{exit !(a >= b)}'; then
    pass "whalenurse was restarted by the daemon's restore (StartedAt $st)"
  else
    fail "whalenurse's StartedAt is $st; it was not restarted after the step (did the step run?)"
  fi
  if jq -e 'select(.msg == "state store opened" and .keys >= 1)' "$out/whalenurse-log-since-step.ndjson" >/dev/null 2>&1; then
    pass "the new run opened the state store and loaded $(jq -r 'select(.msg == "state store opened") | .keys' "$out/whalenurse-log-since-step.ndjson" | head -1) keys"
  else
    fail "the new run did not log 'state store opened' with keys >= 1"
  fi

  # ---- 2. the looper is still SUSPENDED
  s=$(nurse_state "$out/status-after.json" "$NURSE_LOOPER_KEY")
  if [[ "$s" == SUSPENDED ]]; then
    pass "the crash-looper is still SUSPENDED after the daemon restart"
  else
    fail "the crash-looper is $s after the daemon restart (expected SUSPENDED)"
  fi
  if [[ "$(nurse_count_since "$out/whalenurse-log-since-step.ndjson" Unsuspended "$NURSE_LOOPER_KEY" "${t_step%.*}")" == 0 ]]; then
    pass "no Unsuspended event for the looper since the step"
  else
    fail "an Unsuspended event for the looper since the step (the restore cleared SUSPENDED)"
  fi
  if [[ "$(nurse_count_since "$out/whalenurse-log-since-step.ndjson" ActionStarted "$NURSE_LOOPER_KEY" "${t_step%.*}")" == 0 ]]; then
    pass "whalenurse did not start another ladder pass on the looper"
  else
    fail "whalenurse acted on the looper after the step (ActionStarted)"
  fi

  # ---- 3. was the dangerous sequence really exercised? (information, not a verdict)
  if [[ "$(nurse_count_since "$out/whalenurse-log-since-step.ndjson" ManualStopMarked "$NURSE_LOOPER_KEY" "${t_step%.*}")" -ge 1 ]]; then
    csay "info  whalenurse saw the shutdown kill of the looper before it died (a marker was set): the case mechanism d exists for"
    if jq -e 'select(.type == "MarkersQuarantined")' "$out/whalenurse-log-since-step.ndjson" >/dev/null 2>&1; then
      pass "the new run discarded those markers at startup: $(jq -r 'select(.type == "MarkersQuarantined") | .msg' "$out/whalenurse-log-since-step.ndjson" | head -1)"
    else
      fail "a shutdown-kill marker was set but the new run did not discard it (no MarkersQuarantined)"
    fi
  else
    warn "whalenurse died before it saw the looper's kill, so this run did not exercise the persisted-marker case (run it again: prepare.sh, step, collect.sh)"
  fi
  if jq -e 'select(.msg | tostring | test("whalenurse was not running for a while"))' "$out/whalenurse-log-since-step.ndjson" >/dev/null 2>&1; then
    csay "info  the gap since last_seen exceeded the poll interval: a restore window was opened"
  else
    csay "info  the gap since last_seen was within the poll interval: no restore window from the heartbeat"
  fi
  m0b_events_normalize "$STATE/events-listener.ndjson" >"$out/events.ndjson" 2>/dev/null || true
  {
    echo "# docker events around the step, by service (kill[sig] stop die(exit) start ...)"
    m0b_events_by_service "$out/events.ndjson" '^(whalenurse|looper)$' || true
  } >"$out/events-by-service.txt"
  tee -a "$sum" <"$out/events-by-service.txt" >/dev/null
  m0b_journal "$t_step" "$EPOCHREALTIME" "$out/journal-raw.txt" >"$out/journal-how.txt" 2>&1 || true

  # ---- 4. docker compose stop of a test project is still a manual stop
  if nurse_restore_open "$out/status-after.json"; then
    warn "the restore window is still open (until $(jq -r '.engine.restore_window_until' "$out/status-after.json")); run collect.sh again later for the compose stop check"
    csay "RESULT: INCOMPLETE ($fails FAIL, $warns WARN)"
    return 1
  fi
  # shellcheck disable=SC2154
  t_stop="${t_compose_stop-}"
  if [[ -z "$t_stop" ]]; then
    t_stop="$EPOCHREALTIME"
    m0b_set t_compose_stop "$t_stop"
    csay "== docker compose stop of $SP (the burst of kill[15] that the rejected rule would have mistaken for a shutdown)"
    # shellcheck disable=SC2154
    dcs "$SP" "$stop_stack_dir" stop >"$out/compose-stop.txt" 2>&1 || true
    sleep 45 # GRACE 5s + poll 15s + the cooldown, with room to spare
  else
    csay "== the compose stop was done at $(date -u -d "@${t_stop%.*}" +%T)Z (an earlier collect); evaluating again"
  fi
  nurse_status "$wn" "$out/status-after-stop.json" || fail "whalenurse stopped answering after the compose stop"
  nurse_logs "$wn" "$t_stop" "$out/whalenurse-log-since-stop.ndjson"
  for svc in "${NURSE_STOP_SERVICES[@]}"; do
    key=$(nurse_key "$SP" "$svc")
    st=$(docker inspect --format '{{.State.Status}}' "$(cid "$SP" "$svc")")
    exit_code=$(docker inspect --format '{{.State.ExitCode}}' "$(cid "$SP" "$svc")")
    if [[ "$st" == exited && "$exit_code" == 1 ]]; then
      pass "$svc: stopped by compose (exited, exit code 1) and still stopped"
    else
      fail "$svc: status=$st exit=$exit_code (expected exited with 1: a healed container is the failure this checks for)"
    fi
    if [[ "$(nurse_count_since "$out/whalenurse-log-since-stop.ndjson" ManualStopMarked "$key" "${t_stop%.*}")" -ge 1 ]]; then
      pass "$svc: whalenurse set a manual-stop marker"
    else
      fail "$svc: no ManualStopMarked event (the kill was not read as a manual stop)"
    fi
    if [[ "$(nurse_count_since "$out/whalenurse-log-since-stop.ndjson" ActionStarted "$key" "${t_stop%.*}")" == 0 ]]; then
      pass "$svc: whalenurse did not start it"
    else
      fail "$svc: whalenurse started it after the compose stop"
    fi
    if [[ "$(nurse_field "$out/status-after-stop.json" "$key" manual_stop)" == true ]]; then
      pass "$svc: status shows the manual-stop marker"
    else
      fail "$svc: status shows no manual-stop marker"
    fi
  done

  if ((fails == 0)); then csay "RESULT: PASS ($warns WARN)"; else csay "RESULT: FAIL ($fails FAIL, $warns WARN)"; fi
  csay "evidence: $out (raw: stays in captures/, never committed)"
  ((fails == 0))
}
