#!/usr/bin/env bash
# Install the latest Docker Engine 29.x and Compose 5.6.0 on a GitHub-hosted Ubuntu runner, then select
# the image store and restart Docker. CI only: it uses sudo, rewrites /etc/docker/daemon.json and
# restarts the daemon, so never run it on a host you care about.
#
# The runner ships Engine 28.0.4 and compose 2.38.x. `docker image inspect --platform` needs Engine 28.1
# or later and the dry-run oracle is pinned to compose v5.x (plan section 6), so install what the plan's
# matrix names. The image-drift job and the integration job both call this script.
#
# usage: scripts/ci/setup-docker.sh classic|containerd
set -euo pipefail

store=${1:-}
case "$store" in
  classic | containerd) ;;
  *)
    echo "usage: $0 classic|containerd" >&2
    exit 2
    ;;
esac

if [ "${GITHUB_ACTIONS:-}" != "true" ]; then
  echo "refusing to run outside GitHub Actions: this rewrites the Docker install and daemon.json" >&2
  exit 2
fi

sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker-m0b.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker-m0b.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | sudo tee /etc/apt/sources.list.d/docker-m0b.list
sudo apt-get update
sudo apt-get install -y --allow-change-held-packages --allow-downgrades docker-ce='5:29.*' docker-ce-cli='5:29.*' containerd.io docker-compose-plugin='5.6.0*'
docker version --format 'engine {{.Server.Version}} (API {{.Server.APIVersion}})'
docker compose version

if [ "$store" = containerd ]; then
  echo '{"features":{"containerd-snapshotter":true}}' | sudo tee /etc/docker/daemon.json
else
  echo '{"features":{"containerd-snapshotter":false},"storage-driver":"overlay2"}' | sudo tee /etc/docker/daemon.json
fi
sudo systemctl restart docker
docker info --format 'storage driver: {{.Driver}} / {{json .DriverStatus}}'
