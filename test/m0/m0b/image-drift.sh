#!/usr/bin/env bash
# Image-drift check of plan §3.6 on whatever image store the daemon uses (M0 E5's image part, made store-aware).
# Standalone, so CI can run it on both stores:   test/m0/m0b/image-drift.sh [--out DIR]
#
# It creates one container from a re-taggable image, then moves the tag to another image, and at each point prints
# every candidate comparator next to the `com.docker.compose.image` label that compose recorded:
#   a comparator is USABLE if it equals the label before the re-tag and differs from it after.
# It also records what `compose --dry-run --progress json up` says (the oracle): no `Recreate` before, `Recreate` after.
# Exit 0 only if at least one comparator is usable and the oracle behaves. Project: whalenurse-m0-b-imgdrift (guarded).
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$here/lib.sh"

out=""
while (($#)); do
  case "$1" in
    --out) out="${2:?--out DIR}"; shift 2 ;;
    *) m0_die "usage: image-drift.sh [--out DIR]" ;;
  esac
done
m0b_need jq docker
m0_sanitize_env
P=whalenurse-m0-b-imgdrift
m0b_require_project "$P"
[[ -z "$(docker ps -aq --filter "label=com.docker.compose.project=$P")" ]] || m0_die "project $P already exists; run test/m0/m0b/teardown.sh"
store="$(docker info --format '{{.Driver}}')"
[[ -n "$out" ]] || out="$M0B_DIR/imgdrift-$store-$(date -u +%Y%m%dT%H%M%SZ)"
umask 077
mkdir -p "$out"
dir="$out/stack"
mkdir -p "$dir"
cat >"$dir/compose.yaml" <<'YAML'
services:
  tagged:
    image: whalenurse-m0/imgdrift:latest
    command: ["sh", "-c", "while true; do sleep 1; done"]
    stop_grace_period: 1s
YAML
sum="$out/summary.txt"
: >"$sum"
say() { printf '%s\n' "$*" | tee -a "$sum"; }
cleanup() { m0_down "$P"; m0_rmi whalenurse-m0/imgdrift:latest; }
trap cleanup EXIT

for b in busybox:1.37 alpine:3.22; do docker image inspect "$b" >/dev/null 2>&1 || m0_pull "$b"; done
arch=$(docker version --format '{{.Server.Arch}}')
m0_tag busybox:1.37 whalenurse-m0/imgdrift:latest
ref=whalenurse-m0/imgdrift:latest
dcs "$P" "$dir" up -d --pull never --no-build >"$out/up.txt" 2>&1 || { cat "$out/up.txt"; m0_die "compose up failed"; }
c=$(cid "$P" tagged)

short() { printf '%.19s' "$1"; }
# Each comparator prints the value to compare with the label, or "ERR: <message>".
cmp_ids=(ref.Id ref.RepoDigest ref.Descriptor.digest ref.Descriptor.digest@platform ref.Id@platform)
val() {
  case "$1" in
    ref.Id) docker image inspect --format '{{.Id}}' "$ref" 2>&1 ;;
    ref.RepoDigest) docker image inspect --format '{{if .RepoDigests}}{{index .RepoDigests 0}}{{end}}' "$ref" 2>&1 | sed 's/.*@//' ;;
    ref.Descriptor.digest) docker image inspect --format '{{if .Descriptor}}{{.Descriptor.digest}}{{end}}' "$ref" 2>&1 ;;
    ref.Descriptor.digest@platform) docker image inspect --platform "linux/$arch" --format '{{if .Descriptor}}{{.Descriptor.digest}}{{end}}' "$ref" 2>&1 ;;
    ref.Id@platform) docker image inspect --platform "linux/$arch" --format '{{.Id}}' "$ref" 2>&1 ;;
  esac | head -1 | sed 's/^Error.*/ERR: &/' | cut -c1-120
}
oracle() { # texts compose prints for the container, sorted unique
  dcs "$P" "$dir" --dry-run --progress json up -d --no-deps --pull never --no-build tagged 2>&1 |
    jq -R -r 'fromjson? | select(.id | test("-tagged-1$")) | .text' | sort -u | paste -sd' ' -
}

label=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.image"}}' "$c")
say "# image-drift: store=$store driver=$(docker info --format '{{json .DriverStatus}}') engine=$(docker version --format '{{.Server.Version}}') compose=$(docker compose version --short) arch=$arch"
say "container .Image        = $(short "$(docker inspect --format '{{.Image}}' "$c")")"
say "container .Config.Image = $(docker inspect --format '{{.Config.Image}}' "$c")"
say "label com.docker.compose.image = $(short "$label")"
declare -A before after
for k in "${cmp_ids[@]}"; do before[$k]=$(val "$k" || true); done
before[container.Image@ref.Id]=$([[ "$(docker inspect --format '{{.Image}}' "$c")" == "$(docker image inspect --format '{{.Id}}' "$ref")" ]] && echo equal || echo differs)
oracle_before=$(oracle || true)
say "before the re-tag:"
for k in "${cmp_ids[@]}"; do say "  $(printf '%-34s' "$k") $(short "${before[$k]}")   $([[ "${before[$k]}" == "$label" ]] && echo '== label' || echo '!= label')"; done
say "  $(printf '%-34s' 'container.Image == ref.Id') ${before[container.Image@ref.Id]}"
say "  oracle: $oracle_before"

m0_tag alpine:3.22 "$ref"
for k in "${cmp_ids[@]}"; do after[$k]=$(val "$k" || true); done
after[container.Image@ref.Id]=$([[ "$(docker inspect --format '{{.Image}}' "$c")" == "$(docker image inspect --format '{{.Id}}' "$ref")" ]] && echo equal || echo differs)
oracle_after=$(oracle || true)
say "after moving the tag to alpine:"
for k in "${cmp_ids[@]}"; do say "  $(printf '%-34s' "$k") $(short "${after[$k]}")   $([[ "${after[$k]}" == "$label" ]] && echo '== label' || echo '!= label')"; done
say "  $(printf '%-34s' 'container.Image == ref.Id') ${after[container.Image@ref.Id]}"
say "  oracle: $oracle_after"

usable=()
for k in "${cmp_ids[@]}"; do
  [[ "${before[$k]}" != ERR* && -n "${before[$k]}" && "${before[$k]}" == "$label" && "${after[$k]}" != "$label" ]] && usable+=("$k")
done
[[ "${before[container.Image@ref.Id]}" == equal && "${after[container.Image@ref.Id]}" == differs ]] && usable+=("container.Image==ref.Id")
say ""
say "usable comparators (equal to the label before, different after): ${usable[*]:-none}"
oracle_ok=no
if [[ " $oracle_before " != *" Recreate "* && " $oracle_after " == *" Recreate "* ]]; then oracle_ok=yes; fi
say "dry-run oracle: no Recreate before, Recreate after: $oracle_ok"

jq -n --arg store "$store" --arg driver "$(docker info --format '{{json .DriverStatus}}')" --arg engine "$(docker version --format '{{.Server.Version}}')" \
  --arg compose "$(docker compose version --short)" --arg label "$label" --arg ob "$oracle_before" --arg oa "$oracle_after" \
  --argjson usable "$(printf '%s\n' "${usable[@]:-}" | jq -R . | jq -s 'map(select(. != ""))')" --arg oracle_ok "$oracle_ok" \
  '{store: $store, driver: $driver, engine: $engine, compose: $compose, label: $label, oracle_before: $ob, oracle_after: $oa, usable: $usable, oracle_ok: ($oracle_ok == "yes")}' >"$out/verdict.json"
say "verdict: $out/verdict.json"
((${#usable[@]} > 0)) && [[ "$oracle_ok" == yes ]]
