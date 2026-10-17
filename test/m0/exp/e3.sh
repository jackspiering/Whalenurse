#!/usr/bin/env bash
# E3: does --no-deps suppress depends_on: {restart: true}? With positive control.
# shellcheck shell=bash
exp_e3() {
  begin E3 "depends_on restart: true vs --no-deps"
  local P=whalenurse-m0-e3 dir parent plain netdep t0 t1 v
  dir=$(stage e3 e3)
  rec up.txt dcs "$P" "$dir" up -d --pull never --no-build
  parent=$(cid "$P" parent)
  wait_for 30 is_healthy "$parent" || say "WARN parent never became healthy"
  plain=$(cid "$P" plain)
  netdep=$(cid "$P" netdep)
  sleep 2

  step() { # step <label> <compose args...>: run, report what happened to the dependents
    local label="$1"
    shift
    local pb ppre npre
    ppre=$(ins "$plain" '.[0].State.StartedAt')
    npre=$(ins "$netdep" '.[0].State.StartedAt')
    pb=$(cid "$P" parent)
    t0=$(now)
    rec "step-$label.txt" dcs "$P" "$dir" "$@"
    wait_for 30 is_healthy "$(cid "$P" parent)" || true
    sleep 4
    t1=$(now)
    events_window "$t0" "$t1" "$OUT/events-$label.ndjson" --filter "label=com.docker.compose.project=$P"
    say "[$label] parent recreated: $([[ "$pb" != "$(cid "$P" parent)" ]] && echo yes || echo no)"
    say "[$label] plain   (depends_on restart:true): id same: $([[ "$plain" == "$(cid "$P" plain)" ]] && echo yes || echo NO), restarted: $([[ "$ppre" != "$(ins "$(cid "$P" plain)" '.[0].State.StartedAt')" ]] && echo YES || echo no)"
    say "[$label] netdep  (network_mode + restart:true): id same: $([[ "$netdep" == "$(cid "$P" netdep)" ]] && echo yes || echo 'NO (recreated)'), restarted/recreated: $([[ "$npre" != "$(ins "$(cid "$P" netdep)" '.[0].State.StartedAt')" ]] && echo YES || echo no)"
    say "[$label] netdep netns matches parent: $([[ "$(netns "$(cid "$P" parent)")" == "$(netns "$(cid "$P" netdep)")" ]] && echo yes || echo NO)"
    say "[$label] events: $(jq -r 'select(.Action|test("^exec_")|not) | (.Actor.Attributes["com.docker.compose.service"]) + ":" + .Action' "$OUT/events-$label.ndjson" | paste -sd' ' -)"
    plain=$(cid "$P" plain)
    netdep=$(cid "$P" netdep)
  }

  say "--- (i) force-recreate --no-deps parent (no config change)"
  step i up -d --force-recreate --no-deps --pull never --no-build parent
  say "--- (ii) parent config change (V=2), up -d --no-deps parent"
  printf 'V=2\n' >"$dir/vars.env"
  step ii up -d --no-deps --pull never --no-build parent
  say "--- (iii) POSITIVE CONTROL: parent config change (V=3), up -d with all services in scope"
  printf 'V=3\n' >"$dir/vars.env"
  step iii up -d --pull never --no-build
  say "--- (iv) POSITIVE CONTROL 2: parent config change (V=4), up -d parent plain (plain in scope, netdep not)"
  printf 'V=4\n' >"$dir/vars.env"
  step iv up -d --pull never --no-build parent plain
  v=$(ins "$(cid "$P" plain)" '.[0].State.Status')
  say "plain status at end: $v"
  m0_down "$P"
}
