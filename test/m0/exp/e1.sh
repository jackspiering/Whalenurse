#!/usr/bin/env bash
# E1: after `up -d --force-recreate --no-deps <parent>`, are network_mode: service: dependents broken?
# Also: what happens after a plain parent restart (same ID)?
# shellcheck shell=bash
exp_e1() {
  begin E1 "network_mode dependents after the parent is recreated or restarted"
  local P=whalenurse-m0-e1 dir parent dep newparent t0 t1
  dir=$(stage net e1)
  rec up.txt dcs "$P" "$dir" up -d --pull never --no-build
  parent=$(cid "$P" parent)
  dep=$(cid "$P" dep)
  wait_for 30 is_healthy "$parent" || say "WARN parent never became healthy"

  say "--- baseline"
  say "parent: $(brief "$parent")"
  say "dep:    $(brief "$dep")"
  say "dep NetworkMode is container:<parent id>: $([[ "$(ins "$dep" '.[0].HostConfig.NetworkMode')" == "container:$parent" ]] && echo yes || echo NO)"
  say "netns parent=$(netns "$parent") dep=$(netns "$dep")"
  fixture e1-parent-baseline "$parent"
  fixture e1-dep-baseline "$dep"

  say "--- A: up -d --force-recreate --no-deps --pull never --no-build parent"
  t0=$(now)
  rec recreate-parent.txt dcs "$P" "$dir" up -d --force-recreate --no-deps --pull never --no-build parent
  newparent=$(cid "$P" parent)
  wait_for 30 is_healthy "$newparent" || say "WARN new parent never became healthy"
  sleep 3
  t1=$(now)
  events_window "$t0" "$t1" "$OUT/events-recreate.ndjson" --filter "label=com.docker.compose.project=$P"
  say "parent id changed: $([[ "$parent" != "$newparent" ]] && echo yes || echo NO)"
  say "parent: $(brief "$newparent")"
  say "dep:    $(brief "$dep")"
  say "netns new parent=$(netns "$newparent") dep=$(netns "$dep")"
  say "dep interfaces: $(dk_ctr exec "$dep" sh -c 'ip -o link | cut -d: -f2 | tr -d " " | paste -sd,' 2>&1 || true)"
  say "new parent interfaces: $(dk_ctr exec "$newparent" sh -c 'ip -o link | cut -d: -f2 | tr -d " " | paste -sd,' 2>&1 || true)"
  say "wget parent:8080 from dep over loopback: $(dk_ctr exec "$dep" wget -qO /dev/null -T 2 http://127.0.0.1:8080/ >/dev/null 2>&1 && echo works || echo FAILS)"
  say "dep NetworkMode still points at the OLD parent id: $([[ "$(ins "$dep" '.[0].HostConfig.NetworkMode')" == "container:$parent" ]] && echo yes || echo no)"
  say "StartedAt parent=$(ins "$newparent" '.[0].State.StartedAt') dep=$(ins "$dep" '.[0].State.StartedAt') (staleness rule: parent newer than dep)"
  say "event sequence: $(ev_seq "$OUT/events-recreate.ndjson")"
  fixture e1-parent-after-recreate "$newparent"
  fixture e1-dep-stale-after-parent-recreate "$dep"
  fixture_events e1-recreate-parent "$OUT/events-recreate.ndjson"

  say "--- A2: docker restart of the stale dependent (rung 1 for a dependent after a parent RECREATE)"
  rec restart-stale-dep.txt dk_ctr restart "$dep"
  say "restart rc=$REC_RC: $(head -c 300 "$OUT/restart-stale-dep.txt")"
  say "dep:    $(brief "$dep")"
  fixture e1-dep-stale-restart-failed "$dep"

  say "--- B: parent plain restart (same id), fresh stack"
  m0_down "$P"
  rec up2.txt dcs "$P" "$dir" up -d --pull never --no-build
  parent=$(cid "$P" parent)
  dep=$(cid "$P" dep)
  wait_for 30 is_healthy "$parent" || true
  say "before: netns parent=$(netns "$parent") dep=$(netns "$dep")"
  rec restart-parent.txt dk_ctr restart "$parent"
  wait_for 30 is_healthy "$parent" || true
  sleep 2
  say "after parent restart: parent id same: $([[ "$parent" == "$(cid "$P" parent)" ]] && echo yes || echo NO)"
  say "netns parent=$(netns "$parent") dep=$(netns "$dep")"
  say "dep: $(brief "$dep")"
  say "wget from dep: $(dk_ctr exec "$dep" wget -qO /dev/null -T 2 http://127.0.0.1:8080/ >/dev/null 2>&1 && echo works || echo FAILS)"
  rec restart-dep.txt dk_ctr restart "$dep"
  sleep 2
  say "after dep restart (API): netns parent=$(netns "$parent") dep=$(netns "$dep")"
  say "wget from dep: $(dk_ctr exec "$dep" wget -qO /dev/null -T 2 http://127.0.0.1:8080/ >/dev/null 2>&1 && echo works || echo FAILS)"
  m0_down "$P"
}
