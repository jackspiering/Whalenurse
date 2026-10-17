#!/usr/bin/env bash
# Shared helpers for the M0b scenarios (plan §7 M0b). Source this file; do not run it.
#
# Every scenario is prepare.sh -> [maintainer step] -> collect.sh:
#   prepare.sh  sets up the stacks, records a baseline in a state directory, installs the event
#               listener if needed, and prints the exact step (also written to <state>/step.sh).
#   the step    is run by the maintainer (sudo, daemon restart, reboot). step.sh writes the epoch
#               time before and after it to <state>/step-time and <state>/step-done.
#   collect.sh  reads only <state>/state.env (and Docker, the journal, the listener's file), so it
#               works from a fresh session after the daemon restart or a reboot. It can be run
#               again and again: it never changes the stacks.
#
# State lives in captures/m0b/<scenario>/<UTC id>/ (gitignored, on the persistent disk: /tmp is a
# tmpfs here and would not survive a reboot). Raw output stays there; only scrubbed fixtures are
# promoted into testdata/ (test/m0/promote-fixtures.sh works on <state>/fixtures-raw).
#
# Safety: everything that changes a container goes through the guards of ../lib.sh, and M0b
# projects are further restricted to whalenurse-m0-b-*. The maintainer step itself is the only
# thing that can touch other containers (a daemon restart stops all of them): prepare.sh lists
# them and refuses to continue unless M0B_ALLOW_OTHER_CONTAINERS=1.
# shellcheck shell=bash

export LC_ALL=C # EPOCHREALTIME and awk must print a decimal point, whatever the login locale
M0B_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null  # shellcheck would resolve ../lib.sh relative to the sourcing script and loop on itself
source "$M0B_HERE/../lib.sh"
M0B_ROOT="$(cd "$M0B_HERE/../../.." && pwd)"
M0B_DIR="${M0B_DIR:-$M0B_ROOT/captures/m0b}"
M0B_PROJECT_RE='^whalenurse-m0-b-[a-z0-9][a-z0-9_-]*$'
# shellcheck disable=SC2034  # used by boot-lib.sh and the scenarios
M0B_BOOT_PROJECT=whalenurse-m0-b-boot
M0B_UNIT_PREFIX=whalenurse-m0b-listener
# Daemon and runtime lines worth keeping from the journal.
M0B_JOURNAL_PATTERN='failed to start container|restartmanger wait error|failed to restart container|failed to prepare mount points|Could not find dependent container|Daemon has completed initialization|Processing signal|Daemon shutdown complete|Loading containers: done|dockerd.*level=(error|warning)'

m0b_require_project() {
  m0_require_project "${1-}"
  [[ "$1" =~ $M0B_PROJECT_RE ]] || m0_die "project '$1' is not an M0b project ($M0B_PROJECT_RE)"
}

m0b_need() {
  local t
  for t in "$@"; do command -v "$t" >/dev/null || m0_die "$t is required"; done
}

# ---- state directory ------------------------------------------------------

m0b_state_new() { # m0b_state_new <scenario> -> sets STATE
  local sc="${1-}"
  [[ "$sc" =~ ^[a-z0-9-]+$ ]] || m0_die "bad scenario name '$sc'"
  umask 077
  STATE="$M0B_DIR/$sc/$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$STATE"
  : >"$STATE/state.env"
  m0b_set scenario "$sc"
  m0b_set state_dir "$STATE"
}

# Append-only: when a key repeats, the last value wins (the file is sourced).
m0b_set() { printf '%s=%q\n' "$1" "$2" >>"$STATE/state.env"; }

# m0b_state_find <scenario> [dir]: sets STATE and loads state.env. Without a dir, the newest one.
m0b_state_find() {
  local sc="${1-}" dir="${2-}" d
  if [[ -n "$dir" ]]; then
    [[ -f "$dir/state.env" ]] || m0_die "no state.env in $dir"
    STATE="$(cd "$dir" && pwd)"
  else
    STATE=""
    for d in "$M0B_DIR/$sc"/*/; do
      [[ -f "${d}state.env" ]] && STATE="${d%/}"
    done
    [[ -n "$STATE" ]] || m0_die "no $sc state under $M0B_DIR/$sc: run $sc/prepare.sh first"
  fi
  # shellcheck source=/dev/null
  source "$STATE/state.env"
}

# ---- the maintainer step --------------------------------------------------

# m0b_write_script <name> <description> <command line>...: writes <state>/<name>.sh, which stamps the
# epoch time before and after the commands into <state>/<name>-time and <name>-done, and prints it.
# collect.sh uses the stamps to know when the step happened even if the session that ran prepare.sh is
# gone. Every command line runs even if the previous one failed.
m0b_write_script() {
  local name="$1" desc="$2" line
  shift 2
  {
    printf '#!/usr/bin/env bash\n# %s\n' "$desc"
    printf 'date +%%s.%%N >%q\n' "$STATE/$name-time"
    for line in "$@"; do printf '%s\n' "$line"; done
    printf 'date +%%s.%%N >%q\n' "$STATE/$name-done"
  } >"$STATE/$name.sh"
  chmod 700 "$STATE/$name.sh"
  m0b_set "${name//-/_}_script" "$STATE/$name.sh"
  printf '\n==================== MAINTAINER STEP: %s ====================\n' "$desc"
  printf 'Read it first, then run:\n\n    bash %s\n\nwhich contains:\n\n' "$STATE/$name.sh"
  while IFS= read -r line; do printf '    %s\n' "$line"; done <"$STATE/$name.sh"
  printf '\n'
}
m0b_write_step() { m0b_write_script step "$@"; }

m0b_print_collect() { # m0b_print_collect <scenario> [extra]
  printf 'Afterwards (any session, any time, repeatable):\n\n    test/m0/m0b/%s/collect.sh %s\n\n' "$1" "${2-}"
}

# Epoch (with fractions) of the step, falling back to the baseline time with a warning.
m0b_step_time() {
  if [[ -s "$STATE/step-time" ]]; then
    head -1 "$STATE/step-time"
  else
    echo "WARNING: $STATE/step-time is missing (step not run, or run without step.sh); using the prepare time" >&2
    printf '%s' "${t_prepare:?}"
  fi
}
m0b_step_done() { if [[ -s "$STATE/step-done" ]]; then head -1 "$STATE/step-done"; else printf '%s' "$EPOCHREALTIME"; fi; }

m0b_secs() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.1f", b-a}'; } # m0b_secs <t0> <t1>

# m0b_persistence_check <scenario> <unit>: everything collect.sh needs after a reboot must be on a persistent disk or
# start by itself. Prints one line per check and fails if any fails. Also writes <state>/RESUME.txt.
m0b_persistence_check() {
  local sc="$1" unit="$2" bad=0 fs
  chk() { # chk <description> <ok|fail> [detail]
    if [[ "$2" == ok ]]; then printf 'ok    %s %s\n' "$1" "${3-}"; else printf 'FAIL  %s %s\n' "$1" "${3-}"; bad=1; fi
  }
  fs=$(findmnt -no FSTYPE -T "$STATE")
  chk "state directory is on a persistent filesystem" "$([[ "$fs" != tmpfs && "$fs" != ramfs ]] && echo ok || echo fail)" "($fs: $STATE)"
  chk "state.env, stack and baseline are present" "$([[ -s "$STATE/state.env" && -d "$STATE/stack" && -s "$STATE/baseline/table.tsv" ]] && echo ok || echo fail)"
  chk "listener unit is enabled (starts at boot)" "$([[ "$(systemctl --user is-enabled "$unit" 2>/dev/null)" == enabled ]] && echo ok || echo fail)" "($unit)"
  chk "user manager lingers (runs without a login)" "$([[ "$(loginctl show-user "$USER" -p Linger --value 2>/dev/null)" == yes ]] && echo ok || echo fail)"
  chk "docker.service is enabled" "$([[ "$(systemctl is-enabled docker 2>/dev/null)" == enabled ]] && echo ok || echo fail)"
  chk "journal is persistent" "$([[ -d /var/log/journal ]] && echo ok || echo fail)"
  chk "no tmpfs path in the stack" "$(grep -q '/tmp\|/run/user' "$STATE/stack/compose.yaml" && echo fail || echo ok)"
  chk "the scripts are committed (a fresh checkout has them)" "$([[ -z "$(git -C "$M0B_ROOT" status --porcelain -- test/m0/m0b scripts 2>/dev/null)" ]] && echo ok || echo fail)" "(git status of test/m0/m0b and scripts)"
  cat >"$STATE/RESUME.txt" <<RESUME
Resume $sc from any fresh session (the worktree and captures/ are on the persistent disk):

    cd $M0B_ROOT && test/m0/m0b/$sc/collect.sh --watch 180

From a different checkout of branch m0b-scenarios, point it at this state:

    M0B_DIR=$M0B_DIR test/m0/m0b/$sc/collect.sh --watch 180 $STATE
RESUME
  return "$bad"
}

# ---- host checks ----------------------------------------------------------

# Containers outside whalenurse-m0-* projects that a daemon restart or a reboot would stop.
m0b_other_containers() {
  docker ps -a --format '{{.Names}}\t{{.Label "com.docker.compose.project"}}\t{{.Status}}' |
    awk -F'\t' '$2 !~ /^whalenurse-m0-/ {print}'
}

m0b_preflight_other() { # prints them; refuses unless M0B_ALLOW_OTHER_CONTAINERS=1
  local others
  others=$(m0b_other_containers)
  if [[ -z "$others" ]]; then
    echo "other containers on this host (outside whalenurse-m0-*): none"
    return 0
  fi
  echo "containers outside whalenurse-m0-* that the step will stop or restart:"
  printf '    %s\n' "${others//$'\n'/$'\n'    }"
  [[ "${M0B_ALLOW_OTHER_CONTAINERS-}" == 1 ]] ||
    m0_die "those containers are not mine to bounce. If this host is the throwaway VM and you accept it, rerun with M0B_ALLOW_OTHER_CONTAINERS=1"
}

m0b_versions() { # m0b_versions <outfile>
  {
    echo "# docker version"
    docker version --format 'client={{.Client.Version}} server={{.Server.Version}} api={{.Server.APIVersion}} min-api={{.Server.MinAPIVersion}} os={{.Server.Os}}/{{.Server.Arch}}'
    docker version --format '{{range .Server.Components}}{{.Name}}={{.Version}} {{end}}'
    echo "# compose / buildx"
    docker compose version --short
    docker buildx version 2>/dev/null | head -1
    echo "# image store"
    docker info --format 'driver={{.Driver}} status={{json .DriverStatus}} live-restore={{.LiveRestoreEnabled}} cgroup={{.CgroupVersion}}/{{.CgroupDriver}}'
    echo "# packages"
    dpkg-query -W -f '${Package} ${Version}\n' docker-ce docker-ce-cli containerd.io docker-compose-plugin docker-buildx-plugin 2>/dev/null
    echo "# daemon"
    systemctl show docker -p ActiveEnterTimestamp -p MainPID -p NRestarts -p TimeoutStopUSec 2>/dev/null
    echo "kernel=$(uname -r) boot_id=$(cat /proc/sys/kernel/random/boot_id)"
  } >"$1" 2>&1
}

m0b_ensure_images() { # the base images the stacks use (pulls only allowed base images)
  local b
  for b in busybox:1.37 alpine:3.22; do
    docker image inspect "$b" >/dev/null 2>&1 || m0_pull "$b"
  done
  docker image inspect whalenurse-m0/busybox:1 >/dev/null 2>&1 || m0_tag busybox:1.37 whalenurse-m0/busybox:1
}

# ---- container tables -----------------------------------------------------

# m0b_table <project>: one TSV row per container, from docker inspect.
m0b_table() {
  local p="$1" ids
  m0b_require_project "$p"
  ids=$(docker ps -aq --no-trunc --filter "label=com.docker.compose.project=$p")
  printf 'service\tid\tstatus\texit\trestarts\thealth\tstarted\tfinished\tnetmode\terror\n'
  [[ -n "$ids" ]] || return 0
  # shellcheck disable=SC2086  # one id per line, no spaces
  docker inspect $ids | jq -r 'sort_by(.Config.Labels["com.docker.compose.service"]) | .[]
    | [ .Config.Labels["com.docker.compose.service"], .Id[0:12], .State.Status, (.State.ExitCode | tostring),
        (.RestartCount | tostring), (.State.Health.Status // "-"), .State.StartedAt, .State.FinishedAt,
        (.HostConfig.NetworkMode | if startswith("container:") then "c:" + .[10:22] else . end),
        ((.State.Error // "") | .[0:130]) ] | @tsv'
}

# m0b_inspect_dir <project> <dir>: raw inspect, one file per container (raw: stays in captures/).
m0b_inspect_dir() {
  local p="$1" d="$2" id svc
  m0b_require_project "$p"
  mkdir -p "$d"
  while IFS= read -r id; do
    svc=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$id")
    docker inspect "$id" >"$d/$svc.json"
  done < <(docker ps -aq --no-trunc --filter "label=com.docker.compose.project=$p")
}

# m0b_netns <project>: for every container whose network_mode is container:<id> of a container in the
# project, say whether both are running and share a network namespace.
m0b_netns() {
  local p="$1" dep parent svc pid_svc ns1 ns2 res
  m0b_require_project "$p"
  printf 'dependent\tparent\tsame-netns\n'
  while IFS=$'\t' read -r svc dep parent; do
    pid_svc=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$parent" 2>/dev/null) || pid_svc="(gone: ${parent:0:12})"
    res=n/a
    if is_running "$dep" && is_running "$parent" 2>/dev/null; then
      ns1=$(dk_ctr exec "$dep" readlink /proc/self/ns/net 2>/dev/null || true)
      ns2=$(dk_ctr exec "$parent" readlink /proc/self/ns/net 2>/dev/null || true)
      [[ -n "$ns1" && "$ns1" == "$ns2" ]] && res=same || res="DIFFERENT($ns1 vs $ns2)"
    fi
    printf '%s\t%s\t%s\n' "$svc" "$pid_svc" "$res"
  done < <(docker ps -aq --no-trunc --filter "label=com.docker.compose.project=$p" |
    xargs -r docker inspect |
    jq -r '.[] | select(.HostConfig.NetworkMode | startswith("container:"))
      | [.Config.Labels["com.docker.compose.service"], .Id, (.HostConfig.NetworkMode | .[10:])] | @tsv')
}

# ---- events ---------------------------------------------------------------

# m0b_events_normalize <in.ndjson> -> stdout. Keeps listener markers, drops duplicates (a reconnect with --since
# replays), sorts by time.
m0b_events_normalize() {
  [[ -s "$1" ]] || return 0
  jq -c -s '(map(select(has("_marker"))))
      + (map(select(has("Action"))) | unique_by([.timeNano, .id, .Action, (.Actor.Attributes.execID // "")]))
    | sort_by(.timeNano // (.t * 1000000000)) | .[]' "$1"
}

# m0b_events_rel <normalized.ndjson> <t0 epoch> [service-regex]: "+secs  service  action" lines.
m0b_events_rel() {
  jq -r --argjson t0 "$2" --arg re "${3:-.}" '
    select(has("_marker") or (.Action | test("^exec_") | not))
    | (if has("_marker") then [(.t - $t0), "-", "<listener " + ._marker + ">"]
       else [((.timeNano / 1000000000) - $t0), (.Actor.Attributes["com.docker.compose.service"] // .Actor.Attributes.name // "?"),
             (.Action + (if .Actor.Attributes.exitCode then "(" + .Actor.Attributes.exitCode + ")" else "" end)
                      + (if .Actor.Attributes.signal then "[sig " + .Actor.Attributes.signal + "]" else "" end))] end)
    | select(.[1] | test($re))
    | (if .[0] >= 0 then "+" else "" end) + ((.[0] * 100 | round) / 100 | tostring) + "s\t" + .[1] + "\t" + .[2]' "$1"
}

# m0b_events_by_service <normalized.ndjson> [service-regex]: "service: kill[sig 15] stop die(143) ..." (one line each).
m0b_events_by_service() {
  jq -r --arg re "${2:-.}" '
    select(has("Action") and (.Action | test("^exec_") | not))
    | [(.Actor.Attributes["com.docker.compose.service"] // .Actor.Attributes.name // "?"),
       (.Action + (if .Actor.Attributes.exitCode then "(" + .Actor.Attributes.exitCode + ")" else "" end)
                + (if .Actor.Attributes.signal then "[" + .Actor.Attributes.signal + "]" else "" end))]
    | select(.[0] | test($re)) | @tsv' "$1" |
    awk -F'\t' '{a[$1] = a[$1] (a[$1] == "" ? "" : " ") $2; if (!($1 in o)) {o[$1] = ++n; k[n] = $1}}
      END {for (i = 1; i <= n; i++) printf "%-12s %s\n", k[i] ":", a[k[i]]}'
}

# ---- the event listener (a user systemd unit; Linger=yes keeps it across logout and reboot) -----

m0b_listener_unit() { printf '%s-%s.service' "$M0B_UNIT_PREFIX" "$1"; }

# m0b_listener_install <scenario> <project>...: records docker events for the projects into <state>/events-listener.ndjson
m0b_listener_install() {
  local sc="$1" unit dir
  shift
  local p
  for p in "$@"; do m0b_require_project "$p"; done
  unit=$(m0b_listener_unit "$sc")
  dir="$HOME/.config/systemd/user"
  mkdir -p "$dir"
  rm -f "$STATE/listener.stop"
  cat >"$dir/$unit" <<UNIT
[Unit]
Description=whalenurse M0b: record docker events for $* (read-only)

[Service]
ExecStart=$M0B_HERE/listener.sh $STATE/events-listener.ndjson $STATE/listener.stop $*
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
UNIT
  systemctl --user daemon-reload
  systemctl --user enable --now "$unit" >/dev/null 2>&1
  m0b_set listener_unit "$unit"
  m0b_set listener_file "$STATE/events-listener.ndjson"
  sleep 1
  systemctl --user is-active "$unit" >/dev/null || m0_die "the listener unit $unit is not active"
  echo "listener: $unit active, recording to $STATE/events-listener.ndjson"
}

m0b_listener_remove() { # all M0b listener units
  local f u
  for f in "$HOME/.config/systemd/user/$M0B_UNIT_PREFIX"-*.service; do
    [[ -e "$f" ]] || continue
    u=$(basename "$f")
    systemctl --user disable --now "$u" >/dev/null 2>&1 || true
    rm -f "$f"
    echo "removed listener unit $u"
  done
  systemctl --user daemon-reload 2>/dev/null || true
}

# ---- journal ---------------------------------------------------------------

# m0b_journal_access -> prints plain|sg|sudo|none. Without permission journalctl exits 0 and only hints on stderr
# that it is not showing other users' messages, so a readable journal means: output, and no such hint.
m0b_journal_access() {
  local out
  if out=$(journalctl -u docker -n 1 --no-pager -o cat 2>&1 </dev/null) && [[ -n "$out" && "$out" != *'not seeing messages'* ]]; then
    echo plain
  elif out=$(timeout 5 sg systemd-journal -c 'journalctl -u docker -n 1 --no-pager -o cat' 2>&1 </dev/null) &&
    [[ -n "$out" && "$out" != *'not seeing messages'* && "$out" != *assword* && "$out" != sg:* ]]; then
    echo sg # in the group (usermod -aG) but this login session predates it
  elif out=$(sudo -n journalctl -u docker -n 1 --no-pager -o cat 2>&1 </dev/null) && [[ -n "$out" ]]; then
    echo sudo
  else
    echo none
  fi
}

# m0b_journal <since epoch> <until epoch> <raw out>: the docker and containerd units for the window.
# Writes <raw out> (full window, stays in captures/) and prints how it was read.
m0b_journal() {
  local since="${1%.*}" until_="${2%.*}" out="$3" how args
  args=(-u docker -u containerd --since "@$since" --until "@$until_" --no-pager -q -o short-precise)
  how=$(m0b_journal_access)
  # shellcheck disable=SC2024  # sudo only reads the journal; the redirect is our own file
  case "$how" in
    plain) journalctl "${args[@]}" >"$out" 2>&1 </dev/null ;;
    sg) timeout 30 sg systemd-journal -c "journalctl ${args[*]}" >"$out" 2>&1 </dev/null ;;
    sudo) sudo -n journalctl "${args[@]}" >"$out" 2>&1 </dev/null ;;
    none) printf 'journal not readable by this user: add it to systemd-journal (sudo usermod -aG systemd-journal %s)\n' "$USER" >"$out" ;;
  esac
  printf '%s' "$how"
}

# m0b_journal_scrub <raw> <scrubbed out>: the interesting lines only, through scrub-fixture --journal.
m0b_journal_scrub() {
  { grep -E "$M0B_JOURNAL_PATTERN" "$1" || true; } | "$M0B_ROOT/scripts/scrub-fixture" --journal >"$2"
}

# ---- expectations ----------------------------------------------------------

# m0b_compare <expect.tsv> <table.tsv> <netns.tsv>: prints "ok|DIFF  service  detail" lines; returns the number of DIFFs.
# expect.tsv columns: service, status|any, error-regex|-|any, netns same|-|any. '#' lines are comments.
m0b_compare() {
  local exp="$1" table="$2" netns="$3" svc st er ns row st_a er_a ns_a bad=0 res detail
  while IFS=$'\t' read -r svc st er ns; do
    [[ -z "$svc" || "$svc" == \#* ]] && continue
    row=$(awk -F'\t' -v s="$svc" '$1 == s' "$table")
    if [[ -z "$row" ]]; then
      printf 'DIFF\t%s\tnot found\n' "$svc"
      bad=$((bad + 1))
      continue
    fi
    st_a=$(cut -f3 <<<"$row")
    er_a=$(cut -f10 <<<"$row")
    ns_a=$(awk -F'\t' -v s="$svc" '$1 == s {print $3}' "$netns")
    res=ok
    detail="status=$st_a"
    [[ "$st" == any || "$st" == "$st_a" ]] || res=DIFF
    case "$er" in
      any) ;;
      -) [[ -z "$er_a" ]] || res=DIFF ;;
      *) [[ "$er_a" =~ $er ]] || res=DIFF ;;
    esac
    case "$ns" in
      any) ;;
      -) ;;
      same) [[ "$ns_a" == same ]] || res=DIFF ;;
    esac
    [[ -z "$er_a" ]] || detail="$detail error=${er_a:0:80}"
    [[ -z "$ns_a" ]] || detail="$detail netns=$ns_a"
    [[ "$res" == ok ]] || { detail="$detail (expected status=$st error=$er netns=$ns)"; bad=$((bad + 1)); }
    printf '%s\t%s\t%s\n' "$res" "$svc" "$detail"
  done <"$exp"
  return "$bad"
}
