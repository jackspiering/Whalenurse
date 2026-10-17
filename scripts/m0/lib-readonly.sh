#!/usr/bin/env bash
# Shared by scan.sh and capture.sh. Everything that talks to Docker goes
# through ro_docker, which allowlists read-only subcommands and fails closed.
# shellcheck shell=bash

ro_die() { printf 'whalenurse-m0: %s\n' "$*" >&2; exit 2; }

# Allowed: ps, inspect, events, version, info, and `compose version`.
# Anything else (stop, restart, rm, run, exec, compose up, ...) is refused.
ro_docker() {
  local verb="${1:-}"
  case "$verb" in
    ps | inspect | events | version | info) ;;
    compose)
      [[ "${2:-}" == "version" ]] || ro_die "refusing: docker compose ${2:-}"
      ;;
    *) ro_die "refusing non-read-only docker subcommand: ${verb:-<none>}" ;;
  esac
  docker "$@"
}

# journalctl needs privileges on most hosts. Try passwordless sudo, then plain
# journalctl, then give up with a note (never prompt for a password).
# $1: journalctl --since value (journalctl syntax, e.g. "-30m").
ro_journal() {
  local since="$1"
  local pat='failed to start container|restartmanger wait error|failed to restart container|failed to prepare mount points'
  local out
  if out=$(sudo -n journalctl -u docker --since "$since" --no-pager 2>/dev/null); then
    :
  elif out=$(journalctl -u docker --since "$since" --no-pager -q 2>/dev/null) && [[ -n "$out" ]]; then
    :
  else
    printf 'journal not readable: need passwordless sudo or membership in the adm/systemd-journal group\n'
    return 0
  fi
  grep -E "$pat" <<<"$out" || printf '(journal readable, no matching lines since %s)\n' "$since"
}

# Convert a Docker-style duration (30m, 2h, 90s, 1d) to journalctl's "-30m"
# form. Anything else is passed through unchanged.
ro_since_journal() {
  local s="$1"
  if [[ "$s" =~ ^[0-9]+[smhd]$ ]]; then
    case "${s: -1}" in
      s) printf -- '-%s sec ago' "${s%?}" ;;
      m) printf -- '-%s min ago' "${s%?}" ;;
      h) printf -- '-%s hour ago' "${s%?}" ;;
      d) printf -- '-%s day ago' "${s%?}" ;;
    esac
  else
    printf '%s' "$s"
  fi
}
