#!/usr/bin/env bash
# E2: does a plain `up -d --no-deps --pull never --no-build <dep>` (no --force-recreate) recreate a
# network_mode: service: dependent (a) when nothing changed (compose #13878), (b) after the parent was
# recreated, (c) after the parent was only restarted?
# shellcheck shell=bash
exp_e2() {
  begin E2 "non-forced up of network_mode dependents"
  local P=whalenurse-m0-e2 dir parent dep newparent newdep
  dir=$(stage net e2)
  rec up.txt dcs "$P" "$dir" up -d --pull never --no-build
  parent=$(cid "$P" parent)
  dep=$(cid "$P" dep)
  wait_for 30 is_healthy "$parent" || say "WARN parent never became healthy"

  say "--- (a) baseline, nothing changed: up -d --no-deps ... dep"
  rec a-up-dep.txt dcs "$P" "$dir" up -d --no-deps --pull never --no-build dep
  say "dep id unchanged: $([[ "$dep" == "$(cid "$P" dep)" ]] && echo yes || echo 'NO (recreated)')"
  rec a-up-all.txt dcs "$P" "$dir" up -d --pull never --no-build
  say "after plain 'up -d' (all services): parent unchanged: $([[ "$parent" == "$(cid "$P" parent)" ]] && echo yes || echo NO), dep unchanged: $([[ "$dep" == "$(cid "$P" dep)" ]] && echo yes || echo 'NO (recreated)')"
  cat "$OUT/a-up-dep.txt" >>"$OUT/summary.txt"

  say "--- (b) parent recreated, then non-forced up of the dependent"
  rec b-recreate-parent.txt dcs "$P" "$dir" up -d --force-recreate --no-deps --pull never --no-build parent
  newparent=$(cid "$P" parent)
  wait_for 30 is_healthy "$newparent" || true
  say "before: dep $(brief "$dep")"
  say "netns parent=$(netns "$newparent") dep=$(netns "$dep")"
  rec b-up-dep.txt dcs "$P" "$dir" up -d --no-deps --pull never --no-build dep
  newdep=$(cid "$P" dep)
  sleep 2
  say "dep recreated by the non-forced up: $([[ "$dep" != "$newdep" ]] && echo yes || echo NO)"
  say "after: dep $(brief "$newdep")"
  say "dep NetworkMode now points at the new parent: $([[ "$(ins "$newdep" '.[0].HostConfig.NetworkMode')" == "container:$newparent" ]] && echo yes || echo NO)"
  say "netns parent=$(netns "$newparent") dep=$(netns "$newdep")"
  say "wget from dep: $(dk_ctr exec "$newdep" wget -qO /dev/null -T 2 http://127.0.0.1:8080/ >/dev/null 2>&1 && echo works || echo FAILS)"
  fixture e2-dep-recreated-by-plain-up "$newdep"

  say "--- (c) parent only RESTARTED (same id), then non-forced up of the dependent"
  dep="$newdep"
  rec c-restart-parent.txt dk_ctr restart "$newparent"
  wait_for 30 is_healthy "$newparent" || true
  say "before: netns parent=$(netns "$newparent") dep=$(netns "$dep")"
  rec c-up-dep.txt dcs "$P" "$dir" up -d --no-deps --pull never --no-build dep
  say "dep recreated by the non-forced up: $([[ "$dep" != "$(cid "$P" dep)" ]] && echo yes || echo NO)"
  say "after: netns parent=$(netns "$newparent") dep=$(netns "$(cid "$P" dep)")"
  say "(so after a parent restart, a non-forced up does not repair the dependent: rung 1 must restart it via the API)"

  say "--- (d) parent recreated, dependent already failed (rung 1 on a stale dependent), then non-forced up"
  rec d-recreate-parent.txt dcs "$P" "$dir" up -d --force-recreate --no-deps --pull never --no-build parent
  newparent=$(cid "$P" parent)
  wait_for 30 is_healthy "$newparent" || true
  dep=$(cid "$P" dep)
  rec d-restart-dep.txt dk_ctr restart "$dep"
  say "dep after the failed API restart: $(brief "$dep")"
  rec d-up-dep.txt dcs "$P" "$dir" up -d --no-deps --pull never --no-build dep
  newdep=$(cid "$P" dep)
  sleep 2
  say "dep recreated: $([[ "$dep" != "$newdep" ]] && echo yes || echo NO), now: $(brief "$newdep")"
  say "netns parent=$(netns "$newparent") dep=$(netns "$newdep")"
  m0_down "$P"
}
