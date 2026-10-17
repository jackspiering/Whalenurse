#!/usr/bin/env bash
# M0b scenario 5: on the classic store, re-pull the base images and run the image-drift check. After this, optionally
#   M0_RUN_ID=m0b-s5 test/m0/run.sh all
# and then switch back (switch-back.sh in the state directory).
#   collect.sh [state dir]
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"

m0b_need jq docker
m0_sanitize_env
m0b_state_find classic-store "${1-}"
docker version --format '{{.Server.Version}}' >/dev/null 2>&1 || m0_die "the Docker daemon does not answer yet"
out="$STATE/collected"
mkdir -p "$out"
sum="$out/summary.txt"
: >"$sum"
say() { printf '%s\n' "$*" | tee -a "$sum"; }
m0b_versions "$out/versions-after.txt"
store="$(docker info --format '{{.Driver}}')"
say "# classic store: collected $(date -u +%FT%TZ)"
say "store now: $store $(docker info --format '{{json .DriverStatus}}'); engine $(docker version --format '{{.Server.Version}}')"
say "images visible now: $(docker images -q | wc -l) (before the switch: $(wc -l <"$STATE/images-before.txt"))"
if [[ "$store" != overlay2 ]]; then
  say "the daemon is not on overlay2 yet: run the step from prepare.sh ($STATE/step.sh) first"
  exit 1
fi
say "== pulling the base images into the classic store (the only pulls)"
for b in "${M0_BASE_IMAGES[@]}"; do m0_pull "$b" && say "pulled $b"; done
docker image inspect busybox:1.37 >/dev/null 2>&1 || m0_pull busybox:1.37
docker image inspect alpine:3.22 >/dev/null 2>&1 || m0_pull alpine:3.22
m0_tag busybox:1.37 whalenurse-m0/busybox:1
say "== image-drift check on overlay2"
rc=0
"$M0B_HERE/image-drift.sh" --out "$out/imgdrift-classic" | tee -a "$sum" || rc=$?
say ""
say "image-drift exit code: $rc"
say "reference (containerd) summary: $STATE/imgdrift-before/summary.txt"
say ""
say "Next (me): M0_RUN_ID=m0b-s5 test/m0/run.sh all   # optional, about 8 minutes"
say "Then (you): bash $STATE/switch-back.sh   # contents are printed by prepare.sh"
exit "$rc"
