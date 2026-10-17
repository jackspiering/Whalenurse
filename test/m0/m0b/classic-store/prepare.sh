#!/usr/bin/env bash
# M0b scenario 5: switch the VM to the classic (overlay2) image store and run E5's image-drift part there.
# prepare.sh runs the same check on the containerd store first (the reference), then prints the daemon.json step and the
# steps to switch back. Switching hides every image and container of the other store until you switch back (docs: "Switching
# storage backends temporarily hides images and containers created with the other backend"), so nothing may be running.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"

m0b_need jq docker
m0_sanitize_env
left=$(docker ps -a --format '{{.Label "com.docker.compose.project"}}' | sort -u | grep -E '^whalenurse-m0-' || true)
[[ -z "$left" ]] || m0_die "whalenurse-m0 projects are still present ($left): run test/m0/m0b/teardown.sh and test/m0/run.sh cleanup first"
m0b_preflight_other
[[ ! -e /etc/docker/daemon.json ]] || m0_die "/etc/docker/daemon.json exists; merge the two keys by hand (see the printed JSON) instead of using this step"
[[ "$(docker info --format '{{.Driver}}')" != overlay2 ]] || m0_die "the daemon already uses overlay2; nothing to switch (is this the second run?)"

m0b_state_new classic-store
m0b_set t_prepare "$EPOCHREALTIME"
m0b_versions "$STATE/versions-before.txt"
m0b_set store_before "$(docker info --format '{{.Driver}}')"
docker images --format '{{.Repository}}:{{.Tag}} {{.ID}}' | sort >"$STATE/images-before.txt"
docker ps -a --format '{{.Names}}' | sort >"$STATE/containers-before.txt"
echo "== reference run on the current store ($(docker info --format '{{.Driver}}'))"
"$M0B_HERE/image-drift.sh" --out "$STATE/imgdrift-before" || echo "(reference run reported a problem; see $STATE/imgdrift-before/summary.txt)"

cfg='{
  "features": { "containerd-snapshotter": false },
  "storage-driver": "overlay2"
}'
# Removing daemon.json is NOT enough: once the data root holds an overlay2 store the daemon keeps it ("graphdriver-prior").
m0b_write_script switch-back "SWITCH BACK to the containerd image store, explicitly (run only after collect.sh and any run.sh all; there was no daemon.json before)" \
  "sudo tee /etc/docker/daemon.json >/dev/null <<'JSON'
{
  \"features\": { \"containerd-snapshotter\": true }
}
JSON" \
  'sudo systemctl restart docker' \
  "docker info --format '{{.Driver}} {{json .DriverStatus}}'  # expect: overlayfs [[driver-type io.containerd.snapshotter.v1]]" \
  'docker images  # the M0 images reappear'
m0b_write_step "switch to the classic image store: write /etc/docker/daemon.json and restart the daemon" \
  "sudo tee /etc/docker/daemon.json >/dev/null <<'JSON'
$cfg
JSON" \
  'sudo systemctl restart docker' \
  "docker info --format '{{.Driver}} {{json .DriverStatus}}'  # expect: overlay2 [[Backing Filesystem extfs] ...]"
cat <<EOF2
Why these keys: moby (daemon/image_store_choice.go, docker-v29.9.1) treats "containerd-snapshotter": false as an explicit
graphdriver choice, and a set "storage-driver" is honoured too. The docs only describe how to ENABLE the containerd store
(https://docs.docker.com/engine/storage/containerd/); they say the old store's images and containers stay on disk, hidden.

The step above is NOT the way back: $STATE/switch-back.sh was written for that (it is not run by anything).
After the switch, collect.sh re-pulls the four base images (network needed), re-tags whalenurse-m0/busybox:1 and runs the
check on overlay2. Then run  M0_RUN_ID=m0b-s5 test/m0/run.sh all  here, and tell me; I print the switch-back block again.

EOF2
m0b_print_collect classic-store
