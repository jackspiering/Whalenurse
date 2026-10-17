#!/usr/bin/env bash
# E9: event sequences. The daemon-restart scenario is intentionally NOT run (maintainer item).
# shellcheck shell=bash
exp_e9() {
  begin E9 "docker event sequences"
  local P=whalenurse-m0-e9 dir t0 t1 c
  dir=$(stage e9 e9)

  scenario() { # scenario <name> <settle seconds> <command...>: run, capture the container events of the project
    local name="$1" settle="$2"
    shift 2
    t0=$(now)
    "$@" >"$OUT/$name.cmd.txt" 2>&1 || true
    sleep "$settle"
    t1=$(now)
    events_window "$t0" "$t1" "$OUT/events-$name.ndjson" --filter "label=com.docker.compose.project=$P"
    say "[$name] $(ev_seq "$OUT/events-$name.ndjson")"
    fixture_events "e9-$name" "$OUT/events-$name.ndjson"
  }

  rec up-sleeper.txt dcs "$P" "$dir" up -d --pull never --no-build sleeper
  c=$(cid "$P" sleeper)
  sleep 2
  scenario stop 2 dk_ctr stop "$c"
  say "    sleeper after stop: exit=$(ins "$c" '.[0].State.ExitCode') oom=$(ins "$c" '.[0].State.OOMKilled') error='$(ins "$c" '.[0].State.Error')'"
  fixture e9-sleeper-stopped "$c"
  scenario start 2 dk_ctr start "$c"
  scenario kill 2 dk_ctr kill "$c"
  say "    sleeper after docker kill: exit=$(ins "$c" '.[0].State.ExitCode')"
  fixture e9-sleeper-killed "$c"
  scenario start2 2 dk_ctr start "$c"
  scenario restart 3 dk_ctr restart "$c"
  scenario recreate 4 dcs "$P" "$dir" up -d --force-recreate --no-deps --pull never --no-build sleeper

  rec up-stubborn.txt dcs "$P" "$dir" up -d --pull never --no-build stubborn
  c=$(cid "$P" stubborn)
  sleep 2
  scenario stop-timeout 2 dk_ctr stop "$c" -t 2
  say "    stubborn after stop -t 2: exit=$(ins "$c" '.[0].State.ExitCode') oom=$(ins "$c" '.[0].State.OOMKilled')"
  fixture e9-stubborn-stop-timeout "$c"

  scenario crash 0 dcs "$P" "$dir" up -d --pull never --no-build crash
  c=$(cid "$P" crash)
  wait_for 20 bash -c "[[ \"\$(docker inspect --format '{{.State.Status}}' $c)\" == exited ]]" || true
  sleep 1
  t1=$(now)
  events_window "$t0" "$t1" "$OUT/events-crash.ndjson" --filter "label=com.docker.compose.project=$P" --filter "label=com.docker.compose.service=crash"
  say "[crash] $(ev_seq "$OUT/events-crash.ndjson")"
  say "    crash: status=$(ins "$c" '.[0].State.Status') exit=$(ins "$c" '.[0].State.ExitCode') policy=$(ins "$c" '.[0].HostConfig.RestartPolicy.Name')"
  fixture e9-crash-exited "$c"
  fixture_events e9-crash "$OUT/events-crash.ndjson"

  scenario oom 0 dcs "$P" "$dir" up -d --pull never --no-build oom
  c=$(cid "$P" oom)
  wait_for 30 bash -c "[[ \"\$(docker inspect --format '{{.State.Status}}' $c)\" == exited ]]" || true
  sleep 1
  t1=$(now)
  events_window "$t0" "$t1" "$OUT/events-oom.ndjson" --filter "label=com.docker.compose.project=$P" --filter "label=com.docker.compose.service=oom"
  say "[oom] $(ev_seq "$OUT/events-oom.ndjson")"
  say "    oom: status=$(ins "$c" '.[0].State.Status') exit=$(ins "$c" '.[0].State.ExitCode') OOMKilled=$(ins "$c" '.[0].State.OOMKilled')"
  fixture e9-oom-killed "$c"
  fixture_events e9-oom "$OUT/events-oom.ndjson"

  scenario loop-restart-policy 12 dcs "$P" "$dir" up -d --pull never --no-build loop
  c=$(cid "$P" loop)
  say "    loop after 12s: status=$(ins "$c" '.[0].State.Status') restarts=$(ins "$c" '.[0].RestartCount')"
  fixture e9-loop-restarting "$c"
  rec stop-loop.txt dk_ctr stop "$c"

  rec up-hc.txt dcs "$P" "$dir" up -d --pull never --no-build hc
  c=$(cid "$P" hc)
  wait_for 20 is_healthy "$c" || true
  scenario unhealthy 6 dk_ctr exec "$c" rm /tmp/ok
  say "    hc: status=$(ins "$c" '.[0].State.Status') health=$(ins "$c" '.[0].State.Health.Status') failing=$(ins "$c" '.[0].State.Health.FailingStreak')"
  fixture e9-hc-unhealthy "$c"
  say "daemon restart / host reboot scenario: NOT RUN (maintainer item)"
  m0_down "$P"
}
