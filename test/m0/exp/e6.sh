#!/usr/bin/env bash
# E6: does a recreate work from a container that sees the stacks directory read-only at the IDENTICAL path,
# with the host's compose plugin mounted read-only? Negative control: the same directory at a different path.
# shellcheck shell=bash
exp_e6() {
  begin E6 "recreate from a helper container with the stack mounted :ro"
  local P=whalenurse-m0-e6 H=whalenurse-m0-e6helper dir hdir old new bin
  dir=$(stage e6 e6)
  bin=$(compose_plugin_path)
  m0_require_project "$P"
  m0_require_project "$H"
  rec up.txt dcs "$P" "$dir" up -d --pull never --no-build
  old=$(cid "$P" svc)
  say "svc: $(brief "$old")"

  hdir="$SCRATCH/e6helper"
  mkdir -p "$hdir"
  # The inner project name is a validated literal; dc refuses -p on its own command line, so the inner
  # command lives in this generated file.
  cat >"$hdir/compose.yaml" <<YAML
services:
  roprobe:
    image: alpine:3.22
    volumes:
      - type: bind
        source: $SCRATCH
        target: $SCRATCH
        read_only: true
    command: ["sh", "-c", "touch $dir/probe 2>&1; echo write-rc=\$\$?; ls -ld $dir/data"]
  healer:
    image: alpine:3.22
    volumes:
      - type: bind
        source: $bin
        target: /usr/local/bin/docker-compose
        read_only: true
      - type: bind
        source: /var/run/docker.sock
        target: /var/run/docker.sock
      - type: bind
        source: $SCRATCH
        target: $SCRATCH
        read_only: true
    entrypoint: ["docker-compose"]
    command: ["-p", "$P", "--project-directory", "$dir", "-f", "$dir/compose.yaml", "--env-file", "$dir/vars.env",
              "up", "-d", "--force-recreate", "--no-deps", "--pull", "never", "--no-build", "svc"]
  healer-wrongpath:
    image: alpine:3.22
    volumes:
      - type: bind
        source: $bin
        target: /usr/local/bin/docker-compose
        read_only: true
      - type: bind
        source: /var/run/docker.sock
        target: /var/run/docker.sock
      - type: bind
        source: $SCRATCH
        target: /stacks
        read_only: true
    entrypoint: ["docker-compose"]
    command: ["-p", "$P", "--project-directory", "/stacks/e6", "-f", "/stacks/e6/compose.yaml", "--env-file", "/stacks/e6/vars.env",
              "up", "-d", "--force-recreate", "--no-deps", "--pull", "never", "--no-build", "svc"]
YAML

  say "--- is the mount really read-only?"
  rec roprobe.txt dcs "$H" "$hdir" run --rm -T roprobe
  say "$(tr '\n' ' ' <"$OUT/roprobe.txt")"
  say "--- recreate from the helper, identical path, :ro"
  rec healer.txt dcs "$H" "$hdir" run --rm -T healer
  say "helper rc=$REC_RC"
  new=$(cid "$P" svc)
  say "svc recreated: $([[ "$old" != "$new" ]] && echo yes || echo NO)  running: $(ins "$new" '.[0].State.Status')"
  say "bind mount works: $(dk_ctr exec "$new" cat /data/hello.txt 2>&1 || true)"
  # shellcheck disable=SC2016
  say "env_file + vars applied: $(dk_ctr exec "$new" sh -c 'echo $FROM_ENV_FILE/$FROM_VARS' 2>&1 || true)"
  say "labels kept: working_dir matches the host path: $([[ "$(ins "$new" '.[0].Config.Labels["com.docker.compose.project.working_dir"]')" == "$dir" ]] && echo yes || echo NO)"
  say "config-hash label unchanged by the helper: $([[ "$(ins "$old" '.[0].Config.Labels["com.docker.compose.config-hash"]' 2>/dev/null)" == "$(ins "$new" '.[0].Config.Labels["com.docker.compose.config-hash"]')" ]] && echo yes || echo 'n/a (old container gone)')"
  say "hash from the host vs label: $([[ "$(lcompose "$new" config --hash=svc | awk '{print $2}')" == "$(ins "$new" '.[0].Config.Labels["com.docker.compose.config-hash"]')" ]] && echo MATCH || echo MISMATCH)"
  sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g' "$OUT/healer.txt" | tail -8 | sed 's/^/    /' | tee -a "$OUT/summary.txt"

  say "--- NEGATIVE CONTROL: the stack mounted at /stacks (not the identical path)"
  rec healer-wrongpath.txt dcs "$H" "$hdir" run --rm -T healer-wrongpath
  say "helper rc=$REC_RC"
  sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g' "$OUT/healer-wrongpath.txt" | tail -8 | sed 's/^/    /' | tee -a "$OUT/summary.txt"
  new=$(cid "$P" svc)
  if [[ -n "$new" ]]; then
    say "svc after: $(brief "$new")"
    say "working_dir label: $(ins "$new" '.[0].Config.Labels["com.docker.compose.project.working_dir"]')"
    fixture e6-wrongpath-svc "$new"
  else
    say "svc after: no container"
  fi
  m0_down "$H"
  m0_down "$P"
}
