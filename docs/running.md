# Running whalenurse (M1)

M1 is the restart-only heal MVP: it watches Docker, and for opted-in containers that go unhealthy, crash-loop, or stop unexpectedly it runs rung 1 (a Docker API `restart` or `start`). Recreate via compose, persistence, the CLI and notifications arrive in M2 and M3. Nothing is published yet; build the image yourself.

## Opt a container in

```yaml
services:
  web:
    labels:
      whalenurse.enable: "true"
```

`WHALENURSE_MODE=all` opts in everything except containers labelled `whalenurse.enable=false`. whalenurse never acts on itself, on `docker compose run` containers, or on swarm tasks.

Labels read in M1: `whalenurse.enable`, `whalenurse.actions` (only `restart` runs; `recreate` is recorded as unavailable until M2), `whalenurse.heal-exited` (`crashed` | `always` | `never`), `whalenurse.cooldown`. `whalenurse.heal.pull` and `whalenurse.flap-limit` are accepted and ignored until M4 and M2.

## Dry-run first

```
docker run --rm --read-only --group-add "$(stat -c %g /var/run/docker.sock)" \
  --tmpfs /run:uid=65532,gid=65532,mode=0700 \
  -v /var/run/docker.sock:/var/run/docker.sock whalenurse run --dry-run
```

Every decision is a JSON log line on stderr with a `type` such as `IncidentOpened`, `ActionResult` (with `attr.dry_run=true`), `GaveUp`, `NeedsRecreate`, `WaitingForParent` or `NotifyOnly`. In dry-run the healer holds a no-op actor, so it cannot change a container.

## Docker socket access

Mounting `docker.sock` is **root-equivalent on the host**. The image is `gcr.io/distroless/static:nonroot` (uid 65532, no shell), so it cannot open the socket by default and fails with `permission denied while trying to connect to the docker API`. Give it the socket's group:

- container: `group_add: ["<gid>"]` where `<gid>` is `stat -c %g /var/run/docker.sock`; or
- run as root with `user: "0"` if you accept that.

Run it with a read-only root filesystem. Writable paths: `/run` (a tmpfs for the control socket the image healthcheck uses). `/data` becomes writable-required in M2 with the store.

Under systemd, run `whalenurse run` as a user in the `docker` group and set `WHALENURSE_CONTROL_SOCKET` to a path that user can write.

## Settings used in M1

| Variable | Default |
|---|---|
| `WHALENURSE_DRY_RUN` | `false` (also `run --dry-run`) |
| `WHALENURSE_MODE` | `label` (or `all`) |
| `WHALENURSE_POLL_INTERVAL` | `60s` |
| `WHALENURSE_GRACE` | `30s` |
| `WHALENURSE_COOLDOWN` | `30s` |
| `WHALENURSE_CRASHLOOP_AFTER` | `5m` |
| `WHALENURSE_RATE_LIMIT` | `10` actions/hour |
| `WHALENURSE_STARTUP_SETTLE` | `2m` |
| `WHALENURSE_HOST_ID` | hostname |
| `WHALENURSE_LOG_LEVEL` | `info` |
| `WHALENURSE_HTTP_ADDR` | empty (serves `/healthz` only when set) |
| `WHALENURSE_CONTROL_SOCKET` | `/run/whalenurse.sock` (serves `/healthz`; M2 adds the CLI commands) |

Other `WHALENURSE_*` variables from the plan are accepted with a warning and ignored until their milestone. An unknown `WHALENURSE_*` name logs a warning.

## What M1 does and does not do

- One restart per incident (`restart` for running containers, `start` for created or exited ones), then the cooldown, then a re-inspect. If the container is still bad it logs `GaveUp` and does nothing more until the container recovers, is recreated by someone else, or is started by hand after a manual stop. This is a stand-in for M2's SUSPENDED.
- A container whose `network_mode: container:<id>` parent was recreated (the ID is gone) is **never** restarted: that fails and turns a running dependent into an exited one. M1 records `NeedsRecreate` and logs "needs recreate (M2)". If the parent is only stopped, M1 acts on the parent alone and starts the dependent after the parent runs.
- A running dependent whose parent restarted later is recorded as `NeedsDependentRestart`; the dependents step is M2.
- `/healthz` reports unhealthy when the Docker events stream has been down longer than twice the poll interval.
