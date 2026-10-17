#!/usr/bin/env bash
# M0b scenario 1: upgrade the Engine to the latest 29.x (M0 ran 29.9.0). Records the versions and prints the apt step.
# No stacks are needed; the upgrade restarts the daemon, so none may be running.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"

m0b_need jq docker apt-get apt-cache dpkg-query
m0_sanitize_env
left=$(docker ps -a --format '{{.Label "com.docker.compose.project"}}' | sort -u | grep -E '^whalenurse-m0-' || true)
[[ -z "$left" ]] || m0_die "whalenurse-m0 projects are still present ($left): run test/m0/m0b/teardown.sh and test/m0/run.sh cleanup first"
m0b_preflight_other

m0b_state_new upgrade
m0b_set t_prepare "$EPOCHREALTIME"
m0b_versions "$STATE/versions-before.txt"
m0b_set docker_before "$(docker version --format '{{.Server.Version}}')"
m0b_set containerd_before "$(dpkg-query -W -f '${Version}' containerd.io)"
m0b_set compose_before "$(docker compose version --short)"
m0b_set store_before "$(docker info --format '{{.Driver}}')"
{
  echo "# apt candidates before 'apt-get update' (the step updates the lists first)"
  apt-cache policy docker-ce docker-ce-cli containerd.io docker-compose-plugin docker-buildx-plugin | grep -E '^[a-z]|Installed|Candidate'
  echo "# simulated upgrade (apt-get -s; compose and buildx are not named, so they stay)"
  apt-get -s install --only-upgrade 'docker-ce=5:29.*' 'docker-ce-cli=5:29.*' containerd.io | grep -E '^(Inst|Conf)'
} >"$STATE/apt-before.txt" 2>&1
cat "$STATE/apt-before.txt"
echo
echo "latest Engine release upstream (GitHub, non-prerelease):"
gh api repos/moby/moby/releases --jq '[.[] | select(.prerelease == false and (.tag_name | startswith("docker-v")))][0] | "  " + .tag_name + " " + .published_at' 2>&1 | head -2 || true

m0b_write_step "upgrade the Engine packages to the latest 29.x (restarts the daemon)" \
  'sudo apt-get update' \
  "sudo apt-get install -y --only-upgrade 'docker-ce=5:29.*' 'docker-ce-cli=5:29.*' containerd.io" \
  'docker version --format "server={{.Server.Version}}"' \
  "sudo usermod -aG systemd-journal $USER  # optional here, needed for scenario 4: lets collect.sh read the journal"
cat <<EOF2
Notes: compose ($(docker compose version --short)) and buildx are deliberately not upgraded: compose v5.6.0 is the CI pin and the M0 baseline.
docker-ce-rootless-extras is pulled in by apt because it is installed; it is not used.
EOF2
m0b_print_collect upgrade
