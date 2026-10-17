#!/usr/bin/env bash
# E5: `--dry-run up -d --no-deps --pull never --no-build <svc>`: what does it print for an unchanged service,
# a changed config, a dependent after its parent was recreated, and a re-tagged image? Is it parseable?
# Also: is it free of side effects, and how do the image labels behave on re-tag (image drift check)?
# shellcheck shell=bash
exp_e5() {
  begin E5 "compose --dry-run up as a recreate oracle"
  local P=whalenurse-m0-e5 dir before after svc c
  dir=$(stage e5 e5)
  m0_tag busybox:1.37 whalenurse-m0/e5img:latest
  rec up.txt dcs "$P" "$dir" up -d --pull never --no-build
  wait_for 30 is_healthy "$(cid "$P" parent)" || say "WARN parent never became healthy"

  ids() { for svc in parent dep plain tagged; do printf '%s=%s ' "$svc" "$(cid "$P" "$svc")"; done; }

  dry() { # dry <case> <service>: plain and json progress
    local name="$1" s="$2"
    before=$(ids)
    rec "dry-$name.plain.txt" dcs "$P" "$dir" --dry-run --progress plain up -d --no-deps --pull never --no-build "$s"
    rec "dry-$name.json.txt" dcs "$P" "$dir" --dry-run --progress json up -d --no-deps --pull never --no-build "$s"
    rec "dry-$name.default.txt" dcs "$P" "$dir" --dry-run up -d --no-deps --pull never --no-build "$s"
    after=$(ids)
    say "[$name] side effects (container ids changed): $([[ "$before" == "$after" ]] && echo none || echo YES)"
    say "[$name] --progress plain:"
    sed 's/^/    /' "$OUT/dry-$name.plain.txt" | tee -a "$OUT/summary.txt"
    say "[$name] --progress json (first lines):"
    head -c 700 "$OUT/dry-$name.json.txt" | sed 's/^/    /' | tee -a "$OUT/summary.txt"
    echo | tee -a "$OUT/summary.txt" >/dev/null
  }

  say "--- A. unchanged service"
  dry A plain
  say "--- B. changed config (P=2)"
  printf 'P=2\n' >"$dir/vars.env"
  dry B plain
  printf 'P=1\n' >"$dir/vars.env"
  say "--- C. dependent after its parent was recreated"
  rec recreate-parent.txt dcs "$P" "$dir" up -d --force-recreate --no-deps --pull never --no-build parent
  wait_for 30 is_healthy "$(cid "$P" parent)" || true
  dry C dep
  say "--- C2. parent itself, unchanged after its recreate"
  dry C2 parent
  say "--- D. re-tagged image: whalenurse-m0/e5img:latest moved from busybox to alpine"
  c=$(cid "$P" tagged)
  local arch lbl
  arch=$(docker version --format '{{.Server.Arch}}')
  lbl=$(ins "$c" '.[0].Config.Labels["com.docker.compose.image"]')
  imgstate() { # what each candidate comparator says about the ref right now
    say "  ref .Id (index digest on containerd) = $(docker image inspect --format '{{.Id}}' whalenurse-m0/e5img:latest | cut -c1-26)"
    say "  ref --platform linux/$arch .Descriptor.digest = $(docker image inspect --platform "linux/$arch" --format '{{.Descriptor.digest}}' whalenurse-m0/e5img:latest | cut -c1-26)"
  }
  say "container: label com.docker.compose.image=${lbl:0:26}  .Image=$(ins "$c" '.[0].Image' | cut -c1-26)"
  say "before retag:"
  imgstate
  m0_tag alpine:3.22 whalenurse-m0/e5img:latest
  say "after retag:"
  imgstate
  say "naive check (label == ref .Id): $([[ "$lbl" == "$(docker image inspect --format '{{.Id}}' whalenurse-m0/e5img:latest)" ]] && echo equal || echo 'differs (also differs BEFORE the retag, so useless)')"
  say "platform check (label == ref --platform .Descriptor.digest): $([[ "$lbl" == "$(docker image inspect --platform "linux/$arch" --format '{{.Descriptor.digest}}' whalenurse-m0/e5img:latest)" ]] && echo 'no drift' || echo 'DRIFT detected')"
  dry D tagged
  say "--- F. stopped container, unchanged config"
  rec stop-plain.txt dk_ctr stop "$(cid "$P" plain)"
  dry F plain
  m0_down "$P"
  m0_rmi whalenurse-m0/e5img:latest
}
