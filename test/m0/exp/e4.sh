#!/usr/bin/env bash
# E4: `config --hash` vs the com.docker.compose.config-hash label, for plain services and for services that
# reference another service (network_mode, ipc, pid, volumes_from). Invocation is built from the container's
# own labels and run from / (plan §3.4).
# shellcheck shell=bash
exp_e4() {
  begin E4 "config --hash vs config-hash label"
  local P=whalenurse-m0-e4 dir svc c label h
  dir=$(stage e4 e4)
  rec up.txt dcs "$P" "$dir" up -d --pull never --no-build
  wait_for 30 is_healthy "$(cid "$P" parent)" || say "WARN parent never became healthy"

  table() { # table <title>
    say "$1"
    say "$(printf '%-8s %-16s %-16s %s' service label config--hash match)"
    for svc in parent plain netdep ipcdep piddep vfdep; do
      c=$(cid "$P" "$svc")
      label=$(ins "$c" '.[0].Config.Labels["com.docker.compose.config-hash"]')
      h=$(lcompose "$c" config --hash="$svc" 2>"$OUT/hash-$svc.err" | awk '{print $2}')
      say "$(printf '%-8s %-16.16s %-16.16s %s' "$svc" "$label" "${h:-<error>}" "$([[ "$label" == "$h" ]] && echo MATCH || echo MISMATCH)")"
    done
  }
  table "--- 1. fresh stack"
  fixture e4-parent-labels "$(cid "$P" parent)"
  fixture e4-netdep-labels "$(cid "$P" netdep)"

  say "--- 2. syntax (run from / with label-derived arguments)"
  c=$(cid "$P" parent)
  rec hash-star.txt lcompose "$c" config --hash='*'
  say "--hash='*' ($(wc -l <"$OUT/hash-star.txt") lines):"
  sed -E 's/^([a-z]+) ([0-9a-f]{8}).*/  \1 \2.../' "$OUT/hash-star.txt" | tee -a "$OUT/summary.txt"
  say "sorted by service name: $([[ "$(cut -d' ' -f1 "$OUT/hash-star.txt")" == "$(cut -d' ' -f1 "$OUT/hash-star.txt" | sort)" ]] && echo yes || echo no)"
  rec hash-two.txt lcompose "$c" config --hash='parent,plain'
  say "--hash='parent,plain': rc=$REC_RC lines=$(wc -l <"$OUT/hash-two.txt")"
  rec hash-space.txt lcompose "$c" config --hash='parent, plain'
  say "--hash='parent, plain' (space after comma): rc=$REC_RC lines=$(wc -l <"$OUT/hash-space.txt") first: $(head -c 160 "$OUT/hash-space.txt" | head -1)"

  say "--- 3. POSITIVE CONTROL: edit plain's config (A=2); container untouched"
  printf 'A=2\n' >"$dir/vars.env"
  # Labels carry the original env file path, which is the same file we just edited.
  say "plain: label=$(ins "$(cid "$P" plain)" '.[0].Config.Labels["com.docker.compose.config-hash"]' | cut -c1-16) config--hash=$(lcompose "$(cid "$P" plain)" config --hash=plain | awk '{print $2}' | cut -c1-16)  (a drift must show as MISMATCH)"
  printf 'A=1\n' >"$dir/vars.env"

  say "--- 4. after the parent is recreated (dependents still stale)"
  rec recreate-parent.txt dcs "$P" "$dir" up -d --force-recreate --no-deps --pull never --no-build parent
  wait_for 30 is_healthy "$(cid "$P" parent)" || true
  table "(parent recreated; dependents not yet)"
  say "--- 5. after the dependents are recreated through compose"
  rec recreate-deps.txt dcs "$P" "$dir" up -d --no-deps --force-recreate --pull never --no-build netdep ipcdep piddep vfdep
  sleep 3
  table "(everything recreated; hashes are now freshly written by compose)"
  m0_down "$P"
}
