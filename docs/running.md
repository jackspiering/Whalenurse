# Running whalenurse (M2a)

whalenurse watches Docker, and for opted-in containers that go unhealthy, crash-loop, or stop unexpectedly it runs rung 1 (a Docker API `restart` or `start`). M2a adds the persisted state, the full ladder (cooldown, recovery period, flap limit, SUSPENDED, PAUSED), the CLI, and shutdown handling. **Rung 2 (recreate via compose) does not exist until M2b**: where the ladder would recreate, it records "needs recreate (M2b)" and goes on to SUSPENDED. Notifications arrive in M3. Nothing is published yet; build the image yourself.

## Opt a container in

```yaml
services:
  web:
    labels:
      whalenurse.enable: "true"
```

`WHALENURSE_MODE=all` opts in everything except containers labelled `whalenurse.enable=false`. whalenurse never acts on itself, on `docker compose run` containers, or on swarm tasks.

Labels read: `whalenurse.enable`, `whalenurse.actions` (only `restart` runs; `recreate` is recorded as unavailable until M2b), `whalenurse.heal-exited` (`crashed` | `always` | `never`), `whalenurse.cooldown`, `whalenurse.flap-limit` (a positive integer replacing `WHALENURSE_FLAP_LIMIT` for this container). `whalenurse.heal.pull` is accepted and ignored until M4.

## Dry-run first

```
docker run --rm --read-only --group-add "$(stat -c %g /var/run/docker.sock)" \
  --tmpfs /run:uid=65532,gid=65532,mode=0700 \
  -v /var/run/docker.sock:/var/run/docker.sock whalenurse run --dry-run
```

Every decision is a JSON log line on stderr with a `type` such as `IncidentOpened`, `ActionResult` (with `attr.dry_run=true`), `Suspended`, `NeedsRecreate`, `WaitingForParent` or `NotifyOnly`. In dry-run the healer holds a no-op actor, so it cannot change a container, **and it stores nothing**: incidents, SUSPENDED and pauses of a dry run live in memory only, so they are not there for the next real run to find. A dry run does not need `/data`.

## Docker socket access

Mounting `docker.sock` is **root-equivalent on the host**. The image is `gcr.io/distroless/static:nonroot` (uid 65532, no shell), so it cannot open the socket by default and fails with `permission denied while trying to connect to the docker API`. Give it the socket's group:

- container: `group_add: ["<gid>"]` where `<gid>` is `stat -c %g /var/run/docker.sock`; or
- run as root with `user: "0"` if you accept that.

Run it with a read-only root filesystem. Writable paths: `/run` (a tmpfs for the control socket) and **`/data`, a volume for the state store**. The image creates `/data` owned by uid 65532 and declares it a volume, so a fresh named volume (`data:/data`) is writable by the nonroot user. A bind mount keeps the host directory's owner: `chown 65532:65532` it. `whalenurse run` (not dry-run) refuses to start if it cannot open the store.

Under systemd, run `whalenurse run` as a user in the `docker` group and set `WHALENURSE_CONTROL_SOCKET` to a path that user can write, and `WHALENURSE_DATA_DIR` to a directory it owns.

## The CLI

The commands talk to the running daemon over its unix socket (`WHALENURSE_CONTROL_SOCKET`, mode 0600). Run them where the socket is, so with a container, through `docker exec`:

```
docker exec whalenurse /whalenurse status [--json]
docker exec whalenurse /whalenurse pause <project>[/<service>] [--for 2h]   # default 2h; --for 0 = until resume; 30m, 1d work
docker exec whalenurse /whalenurse resume <project>[/<service>]
docker exec whalenurse /whalenurse unsuspend <project>[/<service>]
```

- **pause** stops all actions for the project or service. Events are still recorded. A pause expires (default 2h) so a forgotten one cannot disable healing for good; when it ends, or on `resume`, the containers are looked at again within a second. A container outside compose is addressed by its name.
- **unsuspend** lifts SUSPENDED so the ladder may run again. For a container suspended for *flapping* it also forgets its flap history; otherwise its next incident would suspend it again at once.
- **status** lists every container that has an incident, a pause, flap history, a manual-stop marker or a crash-loop count, the pauses, and the rate-limit budget.

There are no write endpoints on the TCP listener (`WHALENURSE_HTTP_ADDR` serves `/healthz` only): the commands exist only on the unix socket.

## Settings

| Variable | Default |
|---|---|
| `WHALENURSE_DRY_RUN` | `false` (also `run --dry-run`) |
| `WHALENURSE_MODE` | `label` (or `all`) |
| `WHALENURSE_POLL_INTERVAL` | `60s` |
| `WHALENURSE_GRACE` | `30s` |
| `WHALENURSE_COOLDOWN` | `30s` (base; doubles per rung, capped at 5m) |
| `WHALENURSE_RECOVERY_PERIOD` | `10m` |
| `WHALENURSE_FLAP_LIMIT` | `3` |
| `WHALENURSE_FLAP_WINDOW` | `24h` |
| `WHALENURSE_CRASHLOOP_AFTER` | `5m` |
| `WHALENURSE_RATE_LIMIT` | `10` actions/hour |
| `WHALENURSE_STARTUP_SETTLE` | `2m` |
| `WHALENURSE_DATA_DIR` | `/data` |
| `WHALENURSE_HISTORY_RETENTION` | `30d` (closed incidents older than this are pruned hourly) |
| `WHALENURSE_HOST_ID` | hostname |
| `WHALENURSE_LOG_LEVEL` | `info` |
| `WHALENURSE_HTTP_ADDR` | empty (serves `/healthz` only when set) |
| `WHALENURSE_CONTROL_SOCKET` | `/run/whalenurse.sock` (`/healthz` and the CLI commands) |

Durations take `s`, `m`, `h` and also `d` (`30d`). Other `WHALENURSE_*` variables from the plan (`NOTIFY_URLS`, `DRIFT_GUARD`, `COMPOSE_BIN`, `HTTP_TOKEN`, `HEARTBEAT_URL`) are accepted with a warning and ignored until their milestone. An unknown `WHALENURSE_*` name logs a warning.

## What it does

- **The ladder, one pass per incident.** Rung 1 restarts (`restart` for running containers, `start` for created or exited ones), then the cooldown, then a re-inspect. If the container is still bad, rung 2 is recorded as "needs recreate (M2b)" (`RungUnavailable`) and the container is **SUSPENDED** (`Suspended`): nothing more is tried.
- **Recovered** means a healthcheck says `healthy`, or, with no healthcheck, the container is running and `RestartCount` has not changed for `RECOVERY_PERIOD`. SUSPENDED clears at that point. The incident itself closes (`Recovered`) only after the container has stayed recovered for `RECOVERY_PERIOD`; if it fails again before that, it is the same incident and the ladder goes on where it was, so a spent ladder means SUSPENDED again.
- **Flapping.** More than `FLAP_LIMIT` incidents within `FLAP_WINDOW` suspend the container the next time an incident opens (nothing is tried), even if every earlier one recovered. Closing an incident does not reset the count; only the window expiring does.
- **SUSPENDED also clears** on a `create` by someone else, or on a `start` that follows a manual stop (someone stopped it, someone started it). A `start` by the restart policy never clears it. Nor does a `start` inside a restore window, nor one that follows a quarantined shutdown kill (below).
- **Crash loops.** At least 3 `RestartCount` increases, the first at least `CRASHLOOP_AFTER` ago and the latest less than `CRASHLOOP_AFTER` ago (so a few quick restarts followed by a long quiet are not a loop); the count clears after `RECOVERY_PERIOD` without an increase. The poll reads `RestartCount` of every opted-in container, so a loop is found whether or not its `die` events were seen.
- **Shutdown kills.** A daemon restart or reboot sends `kill` to every container, whalenurse included. When the events stream is lost, every manual-stop marker set in the 30 s before is discarded; a changed boot id drops all markers; and whalenurse writes a `last_seen` heartbeat (every 5 s and when it stops cleanly), so that at its next start the markers set in the 30 s before the previous run's end are discarded too, and a gap longer than the poll interval opens a restore window. For `STARTUP_SETTLE` after a reconnect (or a new boot id, or such a gap) a `start` is a restore and a `kill` sets no marker. After every reconnect that follows a lost stream, everything is swept once the settle time is over. API errors while the daemon restores are "not ready", never an incident. The price: a real `docker stop` in the 30 s before whalenurse itself stops is forgotten.
- A container whose `network_mode: container:<id>` parent was recreated (the ID is gone) is **never** restarted: that fails and turns a running dependent into an exited one. whalenurse records `NeedsRecreate` and logs "needs recreate (M2b)". If the parent is only stopped, it acts on the parent alone and starts the dependent after the parent runs. A running dependent whose parent restarted later is recorded as `NeedsDependentRestart`; the dependents step is M2b.
- `/healthz` reports unhealthy when the Docker events stream has been down longer than twice the poll interval.

Decisions and open points: [m2a-notes.md](m2a-notes.md).
