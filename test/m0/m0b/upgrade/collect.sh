#!/usr/bin/env bash
# M0b scenario 1: confirm the Engine upgrade. Then (separately) run: M0_RUN_ID=m0b-s1 test/m0/run.sh all
#   collect.sh [state dir]      (default: the newest upgrade state)
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../lib.sh
source "$here/../lib.sh"

m0b_need jq docker apt-cache dpkg-query
m0_sanitize_env
m0b_state_find upgrade "${1-}"
docker version --format '{{.Server.Version}}' >/dev/null 2>&1 || m0_die "the Docker daemon does not answer yet"
out="$STATE/collected"
mkdir -p "$out"
sum="$out/summary.txt"
: >"$sum"
fails=0
say() { printf '%s\n' "$*" | tee -a "$sum"; }
check() { # check <description> <ok|fail> <detail>
  if [[ "$2" == ok ]]; then say "PASS  $1: $3"; else say "FAIL  $1: $3"; fails=$((fails + 1)); fi
}
yes_no() { if "$@"; then echo ok; else echo fail; fi; }

m0b_versions "$out/versions-after.txt"
now_docker=$(docker version --format '{{.Server.Version}}')
now_containerd=$(dpkg-query -W -f '${Version}' containerd.io)
runtime_containerd=$(docker version --format '{{range .Server.Components}}{{if eq .Name "containerd"}}{{.Version}}{{end}}{{end}}')
latest_pkg=$(apt-cache madison docker-ce | awk '{print $3}' | grep '^5:29\.' | sort -V | tail -1)
latest_ctr=$(apt-cache madison containerd.io | awk '{print $3}' | sort -V | tail -1)
latest_up=$(gh api repos/moby/moby/releases --jq '[.[] | select(.prerelease == false and (.tag_name | startswith("docker-v29.")))][0].tag_name' 2>/dev/null || echo "?")

say "# upgrade: collected $(date -u +%FT%TZ) from $STATE"
# shellcheck disable=SC2154  # from state.env
say "before: engine $docker_before, containerd.io $containerd_before, compose $compose_before, store $store_before"
say "after:  engine $now_docker, containerd.io $now_containerd (runtime reports $runtime_containerd), compose $(docker compose version --short), store $(docker info --format '{{.Driver}}')"
say "latest 29.x in the apt lists: ${latest_pkg:-?}; latest containerd.io: ${latest_ctr:-?}; latest 29.x release upstream: $latest_up"
say ""
check "Engine is the latest 29.x package" "$(yes_no test "5:$now_docker" = "${latest_pkg%%-*}")" "installed $now_docker vs ${latest_pkg:-?}"
check "Engine matches the latest upstream release" "$(yes_no test "docker-v$now_docker" = "$latest_up")" "installed docker-v$now_docker vs $latest_up"
check "containerd.io is the latest packaged" "$(yes_no test "$now_containerd" = "$latest_ctr")" "$now_containerd vs ${latest_ctr:-?}"
# shellcheck disable=SC2154
check "compose unchanged" "$(yes_no test "$(docker compose version --short)" = "$compose_before")" "$compose_before -> $(docker compose version --short)"
check "image store unchanged" "$(yes_no test "$(docker info --format '{{.Driver}}')" = "$store_before")" "$store_before -> $(docker info --format '{{.Driver}}')"
t_step=$(m0b_step_time)
t_daemon=$(date -d "$(systemctl show docker -p ActiveEnterTimestamp --value)" +%s.%N)
check "daemon restarted after the step started" "$(yes_no awk -v a="$t_daemon" -v b="$t_step" 'BEGIN{exit !(a > b)}')" "daemon active since $(date -u -d "@${t_daemon%.*}" +%T)Z, step began $(date -u -d "@${t_step%.*}" +%T)Z"
check "no whalenurse-m0 container exists" "$(yes_no test -z "$(docker ps -a --format '{{.Label "com.docker.compose.project"}}' | grep -E '^whalenurse-m0-' || true)")" "after the daemon restart"

how=$(m0b_journal "${t_step%.*}" "$(date +%s)" "$out/journal.raw.txt")
m0b_journal_scrub "$out/journal.raw.txt" "$out/journal.scrubbed.txt"
say ""
say "journal (read access: $how): $(wc -l <"$out/journal.raw.txt") raw lines since the step; scrubbed excerpt $out/journal.scrubbed.txt"
say ""
if ((fails == 0)); then
  say "versions confirmed. Next: M0_RUN_ID=m0b-s1 test/m0/run.sh all"
else
  say "$fails check(s) failed; do not run the suite until this is understood."
  exit 1
fi
