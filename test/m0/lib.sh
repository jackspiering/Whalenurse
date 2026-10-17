#!/usr/bin/env bash
# Shared helpers for the M0 experiments. Source this file; do not run it.
#
# SAFETY MODEL (fails closed). Every Docker command that changes anything goes
# through one of the wrappers below, and each one refuses to act unless the
# compose project of its target matches ^whalenurse-m0-[a-z0-9][a-z0-9_-]*$:
#   dc        docker compose, always with -p <project>; refuses -p/--project-name/down
#   m0_down   the ONLY place that runs `down -v`, and only on matching projects
#   dk_ctr    stop/start/restart/kill/rm/exec on a container whose project label matches
#   m0_tag    image tags, only under whalenurse-m0/
#   m0_rmi    removes tags, only under whalenurse-m0/
#   m0_pull   pulls only the base images in M0_BASE_IMAGES
# Reads (inspect, ps, events, logs, image inspect, info) use plain `docker`.
# shellcheck shell=bash

M0_RE='^whalenurse-m0-[a-z0-9][a-z0-9_-]*$'
M0_BASE_IMAGES=(busybox:1.37 alpine:3.22 gcr.io/distroless/static:latest gcr.io/distroless/static:nonroot)
M0_PROJECTS=()

m0_die() { printf 'm0: REFUSED: %s\n' "$*" >&2; exit 3; }

m0_require_project() {
  local p="${1-}"
  [[ -n "$p" && "$p" =~ $M0_RE ]] || m0_die "project '${p}' does not match $M0_RE"
}

# Environment that could redirect compose or docker at something else.
m0_sanitize_env() {
  unset COMPOSE_PROJECT_NAME COMPOSE_FILE COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR
  unset DOCKER_CONTEXT DOCKER_CONFIG
  case "${DOCKER_HOST-}" in
    "" | unix:///var/run/docker.sock | unix:///run/docker.sock) unset DOCKER_HOST ;;
    *) m0_die "DOCKER_HOST=$DOCKER_HOST: only the local socket is allowed" ;;
  esac
  local ctx
  ctx=$(docker context show 2>/dev/null) || m0_die "cannot read the docker context"
  [[ "$ctx" == default ]] || m0_die "docker context is '$ctx', expected 'default'"
}

m0_register() {
  local p
  for p in "${M0_PROJECTS[@]+"${M0_PROJECTS[@]}"}"; do [[ "$p" == "$1" ]] && return 0; done
  M0_PROJECTS+=("$1")
}

# dc <project> <compose args...>
dc() {
  local project="${1-}"
  m0_require_project "$project"
  shift
  local a
  for a in "$@"; do
    case "$a" in
      -p | -p=* | --project-name | --project-name=*) m0_die "dc: caller-supplied project name" ;;
      down | rm) m0_die "dc: '$a' is only allowed through m0_down" ;;
    esac
  done
  m0_register "$project"
  docker compose -p "$project" "$@"
}

# dcs <project> <stackdir> <compose args...>: dc with the stack's own files
# (and vars.env, if present; a plain .env would be gitignored).
dcs() {
  local project="$1" dir="$2" envf=()
  shift 2
  [[ -f "$dir/vars.env" ]] && envf=(--env-file "$dir/vars.env")
  dc "$project" --project-directory "$dir" -f "$dir/compose.yaml" "${envf[@]+"${envf[@]}"}" "$@"
}

# The only `down -v` in the whole harness.
m0_down() {
  local project="${1-}"
  m0_require_project "$project"
  docker compose -p "$project" down -v --timeout 2 --remove-orphans >/dev/null 2>&1 || true
}

# m0_project_of <container> -> prints the compose project label, fails if unreadable.
m0_project_of() {
  local p
  p=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$1" 2>/dev/null) || return 1
  printf '%s' "$p"
}

# dk_ctr <verb> <container> [args...]
#   stop/kill/restart/start/rm: docker <verb> [args...] <id>
#   exec:                       docker exec <id> [args...]
dk_ctr() {
  local verb="${1-}" ctr="${2-}" project id
  [[ -n "$ctr" ]] || m0_die "dk_ctr: no container"
  case "$verb" in stop | kill | restart | start | rm | exec) ;; *) m0_die "dk_ctr: verb '$verb' not allowed" ;; esac
  project=$(m0_project_of "$ctr") || m0_die "dk_ctr: cannot determine the project of '$ctr'"
  m0_require_project "$project"
  id=$(docker inspect --format '{{.Id}}' "$ctr") || m0_die "dk_ctr: cannot resolve '$ctr'"
  shift 2
  if [[ "$verb" == exec ]]; then docker exec "$id" "$@"; else docker "$verb" "$@" "$id"; fi
}

m0_tag() { # m0_tag <source image> <whalenurse-m0/name:tag>
  [[ "${2-}" == whalenurse-m0/* ]] || m0_die "m0_tag: destination '${2-}' is not under whalenurse-m0/"
  docker tag "$1" "$2"
}

m0_rmi() { # removes tags only
  [[ "${1-}" == whalenurse-m0/* ]] || m0_die "m0_rmi: '${1-}' is not under whalenurse-m0/"
  docker rmi "$1" >/dev/null 2>&1 || true
}

m0_pull() {
  local ref="${1-}" ok=0 b
  for b in "${M0_BASE_IMAGES[@]}"; do [[ "$b" == "$ref" ]] && ok=1; done
  ((ok)) || m0_die "m0_pull: '$ref' is not an allowed base image"
  docker pull -q "$ref" >/dev/null
}

# Downs every m0 project this run touched, then any m0 project still on the host.
m0_cleanup() {
  local p
  for p in "${M0_PROJECTS[@]+"${M0_PROJECTS[@]}"}"; do m0_down "$p"; done
  m0_down_leftovers
}

m0_down_leftovers() {
  local p
  while IFS= read -r p; do
    if [[ "$p" =~ $M0_RE ]]; then m0_down "$p"; fi
  done < <(docker ps -a --format '{{.Label "com.docker.compose.project"}}' | sort -u)
  return 0
}

# ---- scratch + evidence --------------------------------------------------

m0_init_run() { # sets ROOT RUN CAP SCRATCH
  ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  RUN="${M0_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
  CAP="$ROOT/captures/m0-$RUN"
  SCRATCH="$CAP/scratch"
  umask 077
  mkdir -p "$CAP" "$SCRATCH"
}

# stage <stack> [name] -> copies test/m0/stacks/<stack> to $SCRATCH/<name>, prints the path.
stage() {
  local src="$1" dst="${2:-$1}"
  [[ "$src" =~ ^[a-z0-9_-]+$ && "$dst" =~ ^[a-z0-9_-]+$ ]] || m0_die "stage: bad name"
  rm -rf "${SCRATCH:?}/$dst"
  cp -r "$ROOT/test/m0/stacks/$src" "$SCRATCH/$dst"
  printf '%s' "$SCRATCH/$dst"
}

begin() { # begin <E1> -> sets OUT
  OUT="$CAP/$1"
  mkdir -p "$OUT"
  : >"$OUT/summary.txt"
  printf '\n=== %s: %s\n' "$1" "${2-}"
}

say() { printf '%s\n' "$*" | tee -a "$OUT/summary.txt"; }

# rec <file> <cmd...>: run, save stdout+stderr to $OUT/<file>, note the exit code in REC_RC.
# Never fails the run: a failing command is evidence, not an error.
rec() {
  local f="$1" rc=0
  shift
  "$@" >"$OUT/$f" 2>&1 || rc=$?
  printf '[%s rc=%s]\n' "$f" "$rc" | tee -a "$OUT/summary.txt"
  # shellcheck disable=SC2034
  REC_RC=$rc
  return 0
}

ins() { docker inspect "$1" | jq -r "$2"; } # ins <container> <jq path on .[0]>
cid() { # cid <project> <service> -> container id (non-oneoff)
  docker ps -aq --no-trunc --filter "label=com.docker.compose.project=$1" \
    --filter "label=com.docker.compose.service=$2" --filter 'label=com.docker.compose.oneoff=False' | head -1
}
netns() { dk_ctr exec "$1" readlink /proc/self/ns/net 2>&1 || true; }

wait_for() { # wait_for <seconds> <cmd...>
  local t="$1" i
  shift
  for ((i = 0; i < t; i++)); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}
is_healthy() { [[ "$(docker inspect --format '{{.State.Health.Status}}' "$1" 2>/dev/null)" == healthy ]]; }
is_running() { [[ "$(docker inspect --format '{{.State.Status}}' "$1" 2>/dev/null)" == running ]]; }

# events <t0> <t1> <outfile> <docker events filters...>; t0/t1 are `date +%s.%N` values.
events_window() {
  local t0="$1" t1="$2" f="$3"
  shift 3
  docker events --since "$t0" --until "$t1" --filter type=container "$@" --format '{{json .}}' >"$f" 2>&1 || true
}
now() { date +%s.%N; }

# Compact "Action(exitCode)" sequence for the findings; drops exec_* noise.
ev_seq() {
  jq -r 'select(.Action | test("^exec_") | not)
    | .Action + (if .Actor.Attributes.exitCode then "(" + .Actor.Attributes.exitCode + ")" else "" end)
      + (if .Actor.Attributes.signal then "[sig " + .Actor.Attributes.signal + "]" else "" end)' "$1" | paste -sd' ' -
}

# save a raw inspect for later scrubbing/promotion: fixture <name> <container>
fixture() {
  mkdir -p "$CAP/fixtures-raw"
  docker inspect "$2" >"$CAP/fixtures-raw/$1.json"
}
fixture_events() { # fixture_events <name> <file>
  mkdir -p "$CAP/fixtures-raw"
  cp "$2" "$CAP/fixtures-raw/$1.events.ndjson"
}

compose_plugin_path() {
  docker info --format '{{json .ClientInfo.Plugins}}' | jq -r '.[] | select(.Name == "compose") | .Path'
}

# Build the label-derived compose invocation for a container (plan §3.4).
# lcompose <container> <compose args...>; runs from / to prove it does not depend on the cwd.
lcompose() {
  local c="$1" proj wd files envf
  shift
  proj=$(ins "$c" '.[0].Config.Labels["com.docker.compose.project"]')
  m0_require_project "$proj"
  wd=$(ins "$c" '.[0].Config.Labels["com.docker.compose.project.working_dir"]')
  files=$(ins "$c" '.[0].Config.Labels["com.docker.compose.project.config_files"]')
  envf=$(ins "$c" '.[0].Config.Labels["com.docker.compose.project.environment_file"] // empty')
  local args=(--project-directory "$wd") f
  IFS=',' read -ra fa <<<"$files"
  for f in "${fa[@]}"; do args+=(-f "$f"); done
  if [[ -n "$envf" ]]; then
    IFS=',' read -ra ea <<<"$envf"
    for f in "${ea[@]}"; do args+=(--env-file "$f"); done
  fi
  (cd / && dc "$proj" "${args[@]}" "$@")
}

# brief <container>: one evidence line (ids shortened).
brief() {
  docker inspect --format '{{.Name}} id={{printf "%.12s" .Id}} status={{.State.Status}} exit={{.State.ExitCode}} started={{.State.StartedAt}} restarts={{.RestartCount}} netmode={{.HostConfig.NetworkMode}} err={{.State.Error}}' "$1" \
    | sed -E 's/container:([0-9a-f]{12})[0-9a-f]{52}/container:\1/'
}
short() { printf '%.12s' "$1"; }
