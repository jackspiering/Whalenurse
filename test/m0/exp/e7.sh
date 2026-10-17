#!/usr/bin/env bash
# E7: does the host compose plugin binary run inside the candidate base images (alpine, distroless/static)?
# shellcheck shell=bash
exp_e7() {
  begin E7 "host compose plugin inside candidate base images"
  local P=whalenurse-m0-e7 dir bin svc gid
  dir="$SCRATCH/e7"
  mkdir -p "$dir"
  bin=$(compose_plugin_path)
  m0_require_project "$P"
  say "plugin: $bin"
  say "file: $(file -b "$bin" | cut -c1-120)"
  say "ldd: $(ldd "$bin" 2>&1 | head -1)"
  say "version (host): $("$bin" version 2>&1 | head -1)"
  gid=$(stat -c %g /var/run/docker.sock)
  cat >"$dir/compose.yaml" <<YAML
x-common: &common
  volumes:
    - type: bind
      source: $bin
      target: /usr/local/bin/docker-compose
      read_only: true
    - type: bind
      source: /var/run/docker.sock
      target: /var/run/docker.sock
  entrypoint: ["/usr/local/bin/docker-compose"]
services:
  alpine-version:
    <<: *common
    image: alpine:3.22
    command: ["version"]
  alpine-ps:
    <<: *common
    image: alpine:3.22
    command: ["-p", "$P", "ps", "-a"]
  distroless-version:
    <<: *common
    image: gcr.io/distroless/static:latest
    command: ["version"]
  distroless-ps:
    <<: *common
    image: gcr.io/distroless/static:latest
    command: ["-p", "$P", "ps", "-a"]
  nonroot-version:
    <<: *common
    image: gcr.io/distroless/static:nonroot
    group_add: ["$gid"]
    command: ["version"]
  nonroot-ps:
    <<: *common
    image: gcr.io/distroless/static:nonroot
    group_add: ["$gid"]
    command: ["-p", "$P", "ps", "-a"]
  nonroot-nogroup-ps:
    <<: *common
    image: gcr.io/distroless/static:nonroot
    command: ["-p", "$P", "ps", "-a"]
YAML
  for svc in alpine-version alpine-ps distroless-version distroless-ps nonroot-version nonroot-ps nonroot-nogroup-ps; do
    rec "$svc.txt" dcs "$P" "$dir" run --rm -T "$svc"
    say "$svc: rc=$REC_RC :: $(sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g' "$OUT/$svc.txt" | head -2 | tr '\n' '|' | cut -c1-200)"
  done
  m0_down "$P"
}
