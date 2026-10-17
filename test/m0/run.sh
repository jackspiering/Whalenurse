#!/usr/bin/env bash
# M0 experiment runner. Only ever touches compose projects named whalenurse-m0-*
# (see the safety model in lib.sh). Raw output goes to captures/ (gitignored).
#
#   run.sh selftest            prove the guards fail closed (no Docker needed)
#   run.sh prepare             pull the base images, tag whalenurse-m0/busybox:1, snapshot host state
#   run.sh E1 [E2 ...]         run experiments (prepare must have run)
#   run.sh all                 prepare + E1..E9 + host-state comparison
#   run.sh cleanup             `down -v` on any leftover whalenurse-m0-* project (not the M0b ones: m0b/teardown.sh)
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$here/lib.sh"

selftest() {
  local calls=() fail=0
  # Stub docker: records every call, answers the lookups the guards make.
  # It is called by the guard functions, which shellcheck cannot follow.
  # shellcheck disable=SC2317
  docker() {
    calls+=("$*")
    case "$1" in
      inspect)
        case "$*" in
          *"index .Config.Labels"*)
            case "${*: -1}" in
              m0ctr) printf 'whalenurse-m0-e1' ;;
              realctr) printf 'immich' ;;
              testctr) printf 'whalenurse-test-x' ;;
              nolabel) printf '' ;;
              *) return 1 ;;
            esac ;;
          *".Id"*) printf 'abc123' ;;
        esac ;;
      ps) printf 'whalenurse-m0-e1\nimmich\nwhalenurse-test-x\n\nwhalenurse-m0-e2\nwhalenurse-m0-b-boot\n' ;;
    esac
    return 0
  }
  local out
  expect_refused() { # expect_refused <description> <cmd...>
    local d="$1"
    shift
    if out=$( ("$@") 2>&1); then echo "FAIL: $d was allowed"; fail=1; else
      grep -q REFUSED <<<"$out" || { echo "FAIL: $d failed without REFUSED: $out"; fail=1; }
    fi
  }
  expect_ok() {
    local d="$1"
    shift
    ("$@") >/dev/null 2>&1 || { echo "FAIL: $d was refused"; fail=1; }
  }
  local bad
  # shellcheck disable=SC2016  # the $(id) is a deliberate literal
  for bad in "" default whalenurse-m0- whalenurse-test-x whalenurse-m0 WHALENURSE-M0-x whalenurse-m0-X 'whalenurse-m0-a b' \
    xwhalenurse-m0-a 'whalenurse-m0-a/../b' 'whalenurse-m0-a$(id)' 'whalenurse-m0-a;b' $'whalenurse-m0-a\nb' immich; do
    expect_refused "project '$bad'" m0_require_project "$bad"
    expect_refused "dc project '$bad'" dc "$bad" ps
    expect_refused "m0_down project '$bad'" m0_down "$bad"
  done
  for ok in whalenurse-m0-e1 whalenurse-m0-e6-helper whalenurse-m0-a_b; do
    expect_ok "project '$ok'" m0_require_project "$ok"
  done
  expect_refused "dc -p" dc whalenurse-m0-e1 -p other ps
  expect_refused "dc -pother (attached)" dc whalenurse-m0-e1 -pother ps
  expect_refused "dc -p=other" dc whalenurse-m0-e1 -p=other ps
  expect_ok "dc --pull never is not a project flag" dc whalenurse-m0-e1 up --pull never
  expect_ok "dc --progress json is not a project flag" dc whalenurse-m0-e1 --progress json ps
  expect_refused "dc --project-name" dc whalenurse-m0-e1 --project-name other ps
  expect_refused "dc --project-name=" dc whalenurse-m0-e1 --project-name=other ps
  expect_refused "dc down" dc whalenurse-m0-e1 down -v
  expect_refused "dc rm" dc whalenurse-m0-e1 rm -f
  expect_refused "dk_ctr on a real container" dk_ctr stop realctr
  expect_refused "dk_ctr on a whalenurse-test container" dk_ctr rm testctr
  expect_refused "dk_ctr on an unlabeled container" dk_ctr kill nolabel
  expect_refused "dk_ctr on an unknown container" dk_ctr restart nosuchctr
  expect_refused "dk_ctr prune verb" dk_ctr prune m0ctr
  expect_refused "dk_ctr run verb" dk_ctr run m0ctr
  expect_ok "dk_ctr stop on an m0 container" dk_ctr stop m0ctr -t 1
  expect_refused "m0_tag outside whalenurse-m0/" m0_tag busybox:1.37 library/busybox:evil
  expect_refused "m0_tag over a base image" m0_tag alpine:3.22 busybox:1.37
  expect_refused "m0_rmi of a real image" m0_rmi busybox:1.37
  expect_refused "m0_pull of an arbitrary image" m0_pull evil/image:latest
  calls=()
  m0_down_leftovers
  local c downs=0
  for c in "${calls[@]}"; do
    case "$c" in
      "compose -p whalenurse-m0-e1 down -v"* | "compose -p whalenurse-m0-e2 down -v"*) downs=$((downs + 1)) ;;
      "ps "*) ;;
      *) echo "FAIL: leftover cleanup ran unexpected docker call: $c"; fail=1 ;;
    esac
  done
  ((downs == 2)) || { echo "FAIL: leftover cleanup should down exactly e1 and e2 (and leave the M0b stacks), ran $downs"; fail=1; }
  # No guard failure may have reached a mutating docker call.
  calls=()
  expect_refused "dc down (no docker call)" dc whalenurse-m0-e1 down
  expect_refused "dk_ctr stop real (no docker call)" dk_ctr stop realctr
  for c in "${calls[@]}"; do
    case "$c" in
      inspect*) ;;
      *) echo "FAIL: refused request still called docker: $c"; fail=1 ;;
    esac
  done
  unset -f docker
  if ((fail == 0)); then echo "selftest OK: guards fail closed"; else echo "selftest FAILED"; return 1; fi
}

host_snapshot() { # state that must be identical before and after, minus anything m0
  {
    echo '# containers'
    docker ps -a --format '{{.Names}} {{.Image}} {{.Label "com.docker.compose.project"}}' | sort
    echo '# volumes'
    docker volume ls -q | sort
    echo '# networks'
    docker network ls --format '{{.Name}}' | sort
    echo '# images'
    docker images --format '{{.Repository}}:{{.Tag}}' | sort
  } | grep -v 'whalenurse-m0' >"$1"
}

prepare() {
  begin PREP "pull base images, tag whalenurse-m0/busybox:1"
  local b
  for b in "${M0_BASE_IMAGES[@]}"; do
    if m0_pull "$b"; then say "pulled $b"; else say "could not pull $b"; fi
  done
  m0_tag busybox:1.37 whalenurse-m0/busybox:1
  host_snapshot "$CAP/host-before.txt"
  { docker version; docker compose version; docker info --format '{{json .}}' | jq '{ServerVersion, Driver, DriverStatus, CgroupVersion, OperatingSystem, KernelVersion, Plugins: .ClientInfo.Plugins}'; } >"$OUT/versions.txt" 2>&1
  say "host snapshot written to host-before.txt"
}

usage() { sed -n '2,10p' "$0"; exit 1; }

cmd="${1:-}"
[[ -n "$cmd" ]] || usage
if [[ "$cmd" == selftest ]]; then selftest; exit $?; fi

command -v jq >/dev/null || m0_die "jq is required"
m0_sanitize_env
m0_init_run
trap 'm0_cleanup' EXIT

for f in "$here"/exp/e*.sh; do
  # shellcheck source=/dev/null
  source "$f"
done

run_one() {
  local e="${1,,}"
  declare -F "exp_$e" >/dev/null || m0_die "unknown experiment: $1"
  "exp_$e"
}

case "$cmd" in
  cleanup) m0_down_leftovers; echo "cleanup done" ;;
  prepare) prepare ;;
  all)
    prepare
    for e in e1 e2 e3 e4 e5 e6 e7 e8 e9; do run_one "$e"; done
    host_snapshot "$CAP/host-after.txt"
    diff -u "$CAP/host-before.txt" "$CAP/host-after.txt" && echo "host state unchanged outside whalenurse-m0" ;;
  *)
    for e in "$@"; do run_one "$e"; done ;;
esac
