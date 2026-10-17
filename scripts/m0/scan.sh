#!/usr/bin/env bash
# Read-only. Lists containers that are exited with a restart policy, or that
# have a non-empty State.Error. Shows exit code, OOMKilled, State.Error,
# FinishedAt and restart policy. Never changes anything.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-readonly.sh
source "$here/lib-readonly.sh"

fmt='{{.Name}}|{{.State.Status}}|{{.State.ExitCode}}|{{.State.OOMKilled}}|{{.HostConfig.RestartPolicy.Name}}|{{.State.FinishedAt}}|{{.State.Error}}|{{index .Config.Labels "com.docker.compose.project"}}'

ids=$(ro_docker ps -aq --no-trunc)
printf '%-28s %-10s %-5s %-5s %-15s %-30s %s\n' NAME STATUS EXIT OOM RESTART FINISHED_AT STATE_ERROR
found=0
while IFS= read -r id; do
  [[ -n "$id" ]] || continue
  IFS='|' read -r name status code oom policy finished err _project < <(ro_docker inspect --format "$fmt" "$id")
  name="${name#/}"
  hit=0
  [[ "$status" == "exited" && "$policy" != "no" && "$policy" != "" ]] && hit=1
  [[ -n "$err" ]] && hit=1
  if ((hit)); then
    found=1
    printf '%-28s %-10s %-5s %-5s %-15s %-30s %s\n' "$name" "$status" "$code" "$oom" "${policy:-no}" "$finished" "${err:--}"
  fi
done <<<"$ids"
((found)) || echo "(no matching containers)"
