#!/usr/bin/env bash
# E8: hypothesis A. restart: unless-stopped + a bind mount (create_host_path: false) whose source is removed
# after the first start + a process that exits 1. Expect: failed restart, State.Error set, no retry.
# Plus the other failed-start shapes that feed §3.3 rule 1 (127/126/ENOTDIR, port in use, stale network ref).
# shellcheck shell=bash
exp_e8() {
  begin E8 "failed restart (hypothesis A) and State.Error shapes"
  local P=whalenurse-m0-e8 dir stuck control t0 t1 i wait="${M0_E8_WAIT:-150}"
  dir=$(stage e8 e8)
  mkdir -p "$dir/src"
  t0=$(now)
  rec up.txt dcs "$P" "$dir" up -d --pull never --no-build
  stuck=$(cid "$P" stuck)
  control=$(cid "$P" control)
  sleep 1
  rmdir "$dir/src"
  say "bind source removed 1s after the first start; observing for ${wait}s"
  printf 't\tstuck:status\texit\trestarting\trestarts\terror\tcontrol:status\texit\trestarts\n' >"$OUT/poll.tsv"
  for ((i = 0; i <= wait; i += 5)); do
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$i" \
      "$(ins "$stuck" '.[0].State.Status')" "$(ins "$stuck" '.[0].State.ExitCode')" "$(ins "$stuck" '.[0].State.Restarting')" \
      "$(ins "$stuck" '.[0].RestartCount')" "$(ins "$stuck" '.[0].State.Error' | cut -c1-90)" \
      "$(ins "$control" '.[0].State.Status')" "$(ins "$control" '.[0].State.ExitCode')" "$(ins "$control" '.[0].RestartCount')" >>"$OUT/poll.tsv"
    sleep 5
  done
  t1=$(now)
  say "poll (every 5s):"
  awk 'NR==1 || NR<=9 || (NR % 6 == 0)' "$OUT/poll.tsv" | sed 's/^/    /' | tee -a "$OUT/summary.txt"
  events_window "$t0" "$t1" "$OUT/events-stuck.ndjson" --filter "label=com.docker.compose.project=$P" --filter "label=com.docker.compose.service=stuck"
  events_window "$t0" "$t1" "$OUT/events-control.ndjson" --filter "label=com.docker.compose.project=$P" --filter "label=com.docker.compose.service=control"
  say "stuck events:   $(ev_seq "$OUT/events-stuck.ndjson")"
  say "control events (first 40): $(ev_seq "$OUT/events-control.ndjson" | cut -d' ' -f1-40)"
  say "docker ps -a: $(docker ps -a --filter "id=$stuck" --format '{{.Names}}: {{.Status}} | {{.State}}')"
  say "last 'stuck' die event exit code / error: $(ins "$stuck" '.[0].State.ExitCode') / $(ins "$stuck" '.[0].State.Error')"
  say "no die event after the failed restart (started=$(ins "$stuck" '.[0].State.StartedAt') finished=$(ins "$stuck" '.[0].State.FinishedAt'))"
  fixture e8-stuck-failed-restart "$stuck"
  fixture e8-control-restarting "$control"
  fixture_events e8-stuck-failed-restart "$OUT/events-stuck.ndjson"
  m0_down "$P"

  say "--- E8c: failed FIRST start (127 / 126 / ENOTDIR), restart: unless-stopped"
  P=whalenurse-m0-e8c
  dir=$(stage e8c e8c)
  rec c-up.txt dcs "$P" "$dir" up -d --pull never --no-build
  say "compose up rc=$REC_RC: $(sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g' "$OUT/c-up.txt" | grep -iE 'error|fail|exec|permission' | head -3 | cut -c1-220 | tr '\n' '|')"
  sleep 15
  for s in nocmd noexec enotdir; do
    i=$(cid "$P" "$s")
    say "$s: status=$(ins "$i" '.[0].State.Status') exit=$(ins "$i" '.[0].State.ExitCode') restarts=$(ins "$i" '.[0].RestartCount') error=$(ins "$i" '.[0].State.Error' | cut -c1-160)"
    fixture "e8c-$s" "$i"
  done
  m0_down "$P"

  say "--- E8d: port already allocated"
  P=whalenurse-m0-e8d
  dir=$(stage e8d e8d)
  rec d-up.txt dcs "$P" "$dir" up -d --pull never --no-build
  say "compose up rc=$REC_RC: $(sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g' "$OUT/d-up.txt" | grep -iE 'error|allocated' | head -2 | cut -c1-220 | tr '\n' '|')"
  i=$(cid "$P" b)
  say "b: status=$(ins "$i" '.[0].State.Status') exit=$(ins "$i" '.[0].State.ExitCode') error=$(ins "$i" '.[0].State.Error' | cut -c1-200)"
  fixture e8d-port-allocated "$i"
  m0_down "$P"

  say "--- E8e: stale network reference (parent removed / parent stopped), dependent started via the API"
  P=whalenurse-m0-e8e
  dir=$(stage net e8e)
  rec e-up.txt dcs "$P" "$dir" up -d --pull never --no-build
  wait_for 30 is_healthy "$(cid "$P" parent)" || true
  local dep parent
  dep=$(cid "$P" dep)
  parent=$(cid "$P" parent)
  say "(1) parent STOPPED (not removed), then docker restart dep:"
  rec e-stop-parent.txt dk_ctr stop "$parent"
  rec e-restart-dep-1.txt dk_ctr restart "$dep"
  say "rc=$REC_RC: $(head -c 300 "$OUT/e-restart-dep-1.txt")"
  say "dep: status=$(ins "$dep" '.[0].State.Status') exit=$(ins "$dep" '.[0].State.ExitCode') error=$(ins "$dep" '.[0].State.Error' | cut -c1-200)"
  fixture e8e-dep-parent-stopped "$dep"
  say "(2) parent REMOVED, then docker start dep:"
  rec e-rm-parent.txt dk_ctr rm "$parent" -f
  rec e-start-dep-2.txt dk_ctr start "$dep"
  say "rc=$REC_RC: $(head -c 300 "$OUT/e-start-dep-2.txt")"
  say "dep: status=$(ins "$dep" '.[0].State.Status') exit=$(ins "$dep" '.[0].State.ExitCode') error=$(ins "$dep" '.[0].State.Error' | cut -c1-200)"
  fixture e8e-dep-parent-removed "$dep"
  m0_down "$P"
}
