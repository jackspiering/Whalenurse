# whalenurse — implementation plan v3

Status: v3, approved for implementation. Supersedes draft v2. Facts and versions are as of 16 Oct 2026.
Repo: `github.com/jackspiering/whalenurse` (Go module path, GHCR image and binary are all lowercase).

## 0. Instructions for Claude Code

- **One milestone at a time, in order:** H, M0, M1 … M6. At the end of each milestone, stop and report:
  - what you built;
  - what you verified, and how;
  - what is still uncertain;
  - the **[maintainer]** items that are still open.
  Wait for review before you start the next milestone.
- **[maintainer]** marks work only the maintainer can do: GitHub settings, and anything on the real Docker host. Don't attempt these items. List them in your milestone report.
- **Check sources first.** This plan dates from 16 Oct 2026, so some facts and versions may have changed. Before you rely on a Docker or Compose behavior, or add a dependency, check the current upstream source, docs or releases page. Use the latest stable Go and the latest release of each dependency. Record anything that differs from this plan.
- **Stop on conflicts.** If an M0 finding or a test contradicts a rule in this plan, report it. Don't quietly change the design.
- **Safety on any Docker host:**
  - Only act on containers in compose projects named `whalenurse-test-*` or `whalenurse-m0-*`. Never stop, restart, recreate, pull, prune or remove anything else.
  - Integration tests and M0 scripts must enforce this in code (a project-name filter that fails closed). Convention alone is not enough.
  - Don't run M0 experiments until the maintainer confirms which host to use.
- **Fixtures:** never commit raw `docker inspect`, `docker events` or `journalctl` output. Raw captures go in `/captures/`, which is gitignored. Fixtures in `testdata/` must be scrubbed first (§3.3).
- **Build rules:** use `CGO_ENABLED=0` (single static binary). `internal/engine` stays pure: no Docker calls, and the clock is injected.

## 1. Problem and positioning

Containers sometimes go `unhealthy`, or stop and stay stopped despite their restart policy. Today the fix is to SSH in and run `docker compose pull && down && up -d`.

Docker Engine does not restart unhealthy containers itself. Restart policies only act when the main process exits.

Prior art (checked 16 Oct 2026):

| Project | What it does | What it lacks |
|---|---|---|
| `willfarrell/autoheal` | Restarts unhealthy containers; alerts via `WEBHOOK_URL` | No recreate; not compose-aware |
| `qdm12/deunhealth` | Event-driven restart of labelled containers | Restart-triggers-other-restarts is still on its TODO list |
| `satya-sovan/docker-autoheal` | Python + React; dashboard, maintenance mode, persistent `/data` state, webhook alerts | Restart only; uses compose labels only as a stable ID; UI has no built-in auth |
| Watchtower | `containrrr/watchtower` was archived on 17 Dec 2025; `nicholas-fedor/watchtower` is the maintained fork (v1.22.3, 22 Sep 2026) | Updates containers; does not heal |

**Differentiator** (lead with this in the README and docs): whalenurse recreates from the stack's own compose files, handles `network_mode` sidecar dependents, and has a drift guard. Dashboard, pause and notifications are table stakes now.

## 2. Decisions

| Topic | Decision |
|---|---|
| Audience | Homelab first. Public repo from the start; announce once stable |
| Stack layout | Plain compose directories on the host |
| Failure modes | Unhealthy, and stopped-unexpectedly. Hosts already use `restart:` + `healthcheck:` |
| v1 scope | **Healing only**, plus the opt-in pull rung. Scheduled updates are post-v1 (R4) |
| Language / delivery | Go, single static binary; runs as a container (socket mounted) or under systemd |
| Detection | Docker events stream, plus a safety poll, plus a startup sweep |
| Selection | Opt-in per container via label; optional global `all` mode |
| Ladder | **One pass per incident** (restart → recreate → optional pull+recreate), then SUSPENDED. A **flap limit** suspends containers with repeated incidents |
| Compose binary (container mode) | **Host's compose plugin mounted `:ro` first; bundled copy as fallback** |
| Docker Go client | `github.com/moby/moby/client` + `github.com/moby/moby/api` (`github.com/docker/docker` no longer gets updates as of Engine v29) |
| Notifications | Shoutrrr; leaning `nicholas-fedor/shoutrrr` (vet in M3) |
| Store | SQLite via `modernc.org/sqlite` (recommended in review; confirm at M2) |
| Dashboard | Read-only, off by default, built on a versioned JSON API; default port 8484 |
| Control in v1 | CLI over a local unix socket (pause/resume/unsuspend/status). No network-exposed write endpoints |
| Multi-server | Post-v1 hub (R1). v1 already carries a host ID in every key, event and API record |
| Repo name | Lowercase `whalenurse` (rename is a **[maintainer]** task in H) |

## 3. Design

### 3.1 Heal never upgrades by accident
- Healing never pulls and never builds. The one exception is the opt-in pull rung (§3.7).
- This is enforced in three places: `--pull never --no-build` on every compose `up`, the image-drift check (§3.6), and integration tests.

### 3.2 Incidents, the ladder and the flap limit (per container)

```
healthy --(trigger)--> wait GRACE --> inspect + rules (3.3) --> incident opens
  rung 1: restart (Docker API)               [+ dependents step, 3.5]
  rung 2: recreate (compose)                 [+ dependents step]
  rung 3: pull; recreate only if newer image [opt-in, 3.7]
  after each rung: wait cooldown (grows per rung) -> re-inspect -> stop if recovering
  all rungs failed -> notify "giving up" -> SUSPENDED
recovered for RECOVERY_PERIOD (default 10m) -> incident closes, notify "recovered"
more than FLAP_LIMIT incidents (default 3) within FLAP_WINDOW (default 24h)
  -> SUSPENDED ("flapping"), even if the last incident recovered
any state --(pause)--> PAUSED (no actions; events recorded) --(resume / expiry)--> re-evaluate
```

- **Rungs:** set per container with `whalenurse.actions` (default `restart,recreate`). Rung 3 is appended only with `whalenurse.heal.pull=true`. Each rung runs once per incident.
- **Recovered means:**
  - with a healthcheck: health is `healthy`;
  - without one: the container is running and `RestartCount` has not changed for `RECOVERY_PERIOD`.
- **Flap count:** closing an incident does not reset it. Only the window expiring does.
- **State key:** `host/project/service/container-number`, taken from the `com.docker.compose.project`, `.service` and `.container-number` labels. Never the container ID, because a recreate changes it.
  - `host` comes from `WHALENURSE_HOST_ID` (default: the hostname).
  - Non-compose containers fall back to the container name and only get rung 1.
- **Persisted from M2:** incident state, rung reached, SUSPENDED/PAUSED (with reason and expiry), flap history and manual-stop markers.
- **Action-in-flight window:** while whalenurse is acting on a key, events for that key are *recorded and attributed to the action*, not interpreted. They must not:
  - trigger the manual-stop heuristic;
  - clear SUSPENDED;
  - open a new incident.
- **SUSPENDED clears** when:
  - the container returns to healthy;
  - an external `start`/`create` arrives outside any in-flight window;
  - someone runs `whalenurse unsuspend`.
- **Pause:**
  - Syntax: `whalenurse pause <project>[/<service>] [--for 2h]` and `whalenurse resume ...`, run via `docker exec`. The CLI talks to the daemon over a unix socket.
  - Default duration is 2h; `--for 0` means indefinite. Pauses expire so a forgotten pause doesn't disable healing for good.
- **Global rate limit:** `WHALENURSE_RATE_LIMIT` actions per hour (default 10). This prevents action storms during host-wide outages.

### 3.3 Decision rules (inspect after the grace period)

**Events are only triggers.** The decision is made by inspecting the container after `GRACE`, because event order is unreliable: `docker stop` emits `kill` → `die` → `stop`.

**Health events are edge-triggered.** Docker emits `health_status` only when the status changes (verified in `daemon/health.go`), so a missed event never repeats. The poll (default 60s) is the level-triggered backstop and applies the same rules.

**Fields to inspect:** `State.Status`, `State.Running`, `State.Health`, `State.ExitCode`, `State.OOMKilled`, `State.Error`, `RestartCount`, `HostConfig.RestartPolicy`, `HostConfig.NetworkMode`, and labels.

**Rule precedence:**

0. **Skip entirely:**
   - whalenurse's own container;
   - `com.docker.compose.oneoff=True` (`compose run` containers carry the same project and service labels);
   - swarm tasks (`com.docker.swarm.task.id` present);
   - containers that are not opted in;
   - PAUSED or SUSPENDED keys.
1. **Non-empty `State.Error` means a failed start.** This rule overrides everything below.
   - This is hypothesis A, now confirmed in moby source:
     - A start that fails during a restart-policy restart is **not retried** (`daemon/monitor.go`). Docker logs `failed to restart container` at debug level and `restartmanger wait error` at error level (the misspelling is in the source).
     - A start that fails during boot-time restore is not retried either; Docker logs `failed to start container` (`daemon.go`).
   - A failed start sets `State.Error` and the exit code: **126** (EACCES), **127** (command not found / ENOTDIR) or **128** (anything else). Don't expect a `die` event; the container never ran.
   - Split by cause. Do the matching in one place, backed by fixtures, because error strings vary by Engine version:
     - **Stale reference** (`No such container`, joining a network namespace, `network ... not found`): recreate (skip restart).
     - **Other** (missing bind-mount source, port already allocated, NFS or device not ready): start first, then recreate.
2. `running` + `unhealthy` → ladder.
3. `restarting` for longer than `CRASHLOOP_AFTER` (default 5m), or `on-failure:N` exhausted → ladder. A container that was `created` but never started → recreate.
4. `exited` with no `State.Error` → apply `whalenurse.heal-exited` (`crashed` | `always` | `never`; default `crashed`):
   - **Intended exit:** with restart policy `no` or `on-failure` and exit code 0, the exit was intended. Leave it alone in `crashed` and `never` modes.
   - **`crashed`:** heal if whalenurse saw a `die` **without a preceding `kill`**, or `OOMKilled=true` (also watch the `oom` event). If whalenurse missed the events (it was down):
     - heal on a non-zero exit code other than 137/143;
     - 137 with `OOMKilled=true` is a crash, so heal;
     - 137 or 143 without OOM: notify only, don't heal.
   - **`always`:** heal any exited container.
   - **`never`:** notify only.

**Manual-stop markers:**
- A marker is set on a `kill` event outside any in-flight window, and cleared on the next `start` event.
- A daemon shutdown or reboot emits `kill` for every container. Docker's boot restart (a `start` event) clears those markers. Containers that fail to start at boot have `State.Error`, so rule 1 wins.

**Startup sweep:** after whalenurse starts (including at host boot), wait `STARTUP_SETTLE` (default 2m), then apply the rules to everything.

**Exit-code reading guide** (for M0, docs and the dashboard):

| Code | Meaning |
|---|---|
| 0 | Clean exit |
| 126 / 127 / 128 + `State.Error` | Failed start |
| 137 | SIGKILL: OOM if `OOMKilled`, otherwise `docker kill` or a stop that hit its timeout |
| 143 | SIGTERM (usually a stop) |

**Fixtures:** every rule gets a unit test with scrubbed `docker inspect` fixtures. The scrubber (`scripts/scrub-fixture`):
- removes `Config.Env`;
- replaces label values except `com.docker.compose.*` and `whalenurse.*`;
- replaces hostnames, IPs, MACs and host mount sources with placeholders.

### 3.4 Recreate via compose

**Compose binary lookup** (first match wins):
1. `WHALENURSE_COMPOSE_BIN`
2. The host plugin, mounted read-only at a fixed path (e.g. `/host/docker-compose`). The host path is typically `/usr/libexec/docker/cli-plugins/docker-compose`; `docker info` shows it. The compose plugin runs standalone.
3. The bundled copy.

Log which binary and version is in use at startup, and expose it in `/api/v1/status`. In systemd mode, use the host's `docker compose`.

**Command:**
```
<compose> -p <project> --project-directory <working_dir> -f <config_files...> [--env-file <f>...] \
  up -d --force-recreate --no-deps --pull never --no-build <service>
```
- Inputs come from these labels: `com.docker.compose.project`, `.project.working_dir`, `.project.config_files`, `.project.environment_file` and `.service`.
- `--pull never`: without it, `pull_policy: always` (or a missing image) would make compose pull during a heal.
- `--no-build`: a heal never builds.
- Never use `down`. It removes the project network and affects other services.

**Mounts (container mode):**
- Mount the stacks root at the **identical path**, read-only, e.g. `/opt/stacks:/opt/stacks:ro`.
- Also mount anything compose reads from outside it: `env_file:` paths with `../`, `include:`/`extends:` files, and file-based `secrets:`/`configs:`.
- That `:ro` works is still to be confirmed in M0.

**Fallbacks and restarts:**
- If compose labels or files are missing or unreadable, skip rung 2 and notify.
- Rung 1 (restart) uses only the Docker API.

### 3.5 Dependents (network_mode sidecars)

Dependents break whenever their parent is restarted or recreated, by anything: Docker's restart policy, whalenurse, or a manual `compose up`.

- **Discovery:** a dependent is any container whose `HostConfig.NetworkMode` (or IPC/PID mode) is `container:<id-or-name>` pointing at the parent. Compose resolves `service:X` to `container:<id>` at create time.
- **Parent restart** (same ID): restart dependents via the Docker API, one at a time, with a health wait between each.
- **Parent recreate** (new ID): recreate the dependents.
  - Preferred, to be verified in M0: `<compose> ... up -d --no-deps --pull never --no-build <dep...>` **without** `--force-recreate`. Current compose (v5.x source) hashes the *resolved* `container:<id>`, so the new parent ID causes a mismatch and compose recreates the dependents itself.
  - Fallback: `--force-recreate`.
- **Trigger:** a parent `start` or `create` event outside the in-flight window. When the action is whalenurse's own, it runs the dependents step explicitly.
- **Staleness sweep** (part of the poll and startup sweep). A dependent is stale if either:
  - its `NetworkMode` is `container:<id>` and `<id>` is not the parent's current ID; or
  - the parent's `State.StartedAt` is newer than the dependent's.
- **At boot:** Docker waits for `container:` network parents using a single shared 5s timer, so dependents can come up before their parent is ready. The startup sweep catches this.
- **Ordering:** while a parent has an open incident or an in-flight action, suppress its dependents' ladders. Heal the parent first, wait for it to be healthy, then handle the dependents in order.
- **Reverse case:** a dependent failing on its own never triggers an action on its parent.
- **Compose behaviors to keep in mind:**
  - `depends_on: {X: {restart: true}}` only applies to explicit compose commands, not to restarts by Docker's restart policy.
  - From reading the source, `up --no-deps` narrows the project and therefore suppresses `depends_on.restart` (verify in M0).
  - Compose bug #13878 (open): since v5.2.0, `network_mode: service:` dependents are recreated on *every* `up`. Expect external `create` events when the maintainer runs `compose up`, and treat them as external recreates.

### 3.6 Drift guard (before rung 2, and before rung 3's `up`)

A recreate deploys whatever is on disk and in the local image store *now*. The guard runs three checks:

1. **Config drift:**
   - Compare `<compose> config --hash=<svc>` with the container's `com.docker.compose.config-hash` label.
   - Syntax: comma-separated service names, or `'*'` for all; commas are not trimmed. Output: one `<service> <hash>` line per service, sorted.
   - **Skip this check for services whose `network_mode`/`ipc`/`pid`/`volumes_from` references another service or container.** Compose hashes those *after* resolving the reference to `container:<id>`, but `config --hash` hashes the unresolved form, so they never match (verified in compose source: `execCreateContainer` → `resolveServiceReferences` → `ServiceHash`). For these services, rely on checks 2–3 and on the parent's own check.
   - M0 evaluates `<compose> --dry-run up -d --no-deps --pull never --no-build <svc>` as a replacement for this check, since it gives compose's own recreate decision. Adopt it only if the output is stable and parseable.
2. **Image drift:**
   - Compare the image the container runs with what its image reference resolves to locally now. Prefer compose's `com.docker.compose.image` label, the digest compose recorded and uses for its own recreate decision.
   - Must be correct on both image stores. Fresh Engine v29 installs default to the containerd store, where IDs are index digests.
   - Allowed only for rung 3.
3. **Compose version skew** (only when running the bundled compose fallback):
   - Compare the container's `com.docker.compose.version` label with the bundled version.
   - If both the versions and the hashes differ, report "can't verify drift (compose X on host vs Y in whalenurse)".
   - This is a real risk: v5.5.0 changed image-digest reconciliation and v5.6.0 pinned the hash byte layout.

**On any failure:** skip the recreate, notify with the specific cause, and set SUSPENDED.
- If whalenurse itself moved a shared tag (via rung 3 on another service), the message says which service did it and when.

**Strictness:** `WHALENURSE_DRIFT_GUARD=strict|warn` (default `strict`). `warn` recreates anyway and says so.

### 3.7 Pull rung (opt-in, `whalenurse.heal.pull=true`)

Runs only after restart and recreate have failed.
1. Run `<compose> pull <svc>`.
2. If the digest is unchanged, skip to notify + SUSPENDED.
3. If it changed, run drift checks 1 and 3, then `up ... --pull never`, then the dependents step.

The notification includes:
- the old digest → new digest;
- the exact rollback command, e.g. `docker image tag <old-id> <ref> && <compose> ... up -d --no-deps --pull never <svc>` (the old image stays local until pruned).

Record a "tag moved by whalenurse" entry, because other stacks sharing the tag will now see image drift.

### 3.8 Guardrails
- Opt-in label `whalenurse.enable=true`, or `WHALENURSE_MODE=all`.
- Never act on itself. In container mode, resolve the own container ID from `/proc/self/mountinfo` (the hostname can be overridden).
- At startup, warn if whalenurse's own `NetworkMode` is `container:<id>`. **Never run whalenurse with `network_mode: service:<x>`**: it would become a dependent of a container it may heal.
- Rate limit, flap limit, cooldown backoff, `--dry-run` (log and notify only, with a dashboard banner).
- Events-stream reconnect: resubscribe with `since=<last event time>`; the poll re-syncs state.
- `/healthz` fails when the events stream has been down longer than a threshold. Use it as whalenurse's own healthcheck.
- Optional heartbeat: ping `WHALENURSE_HEARTBEAT_URL` every few minutes.
- Structured JSON logs.

### 3.9 Security
- Mounting `docker.sock` is root-equivalent on the host. Say so in the README.
- Minimal image with a read-only root filesystem. Only `/data` (store) and `/run` (control socket) are writable.
- **Dashboard:**
  - Off by default, read-only, no write endpoints (so no CSRF surface).
  - Never render env vars or full inspect output; show only `com.docker.compose.*` and `whalenurse.*` labels.
  - Optional bearer token.
  - Recommended exposure: `127.0.0.1:8484:8484`, a reverse proxy, or a Tailscale container on a **shared bridge network** forwarding with `tailscale serve`. Not `network_mode`.
- A socket proxy is roadmap (R3). Compose needs broad API access, so it adds little in v1.

### 3.10 Store
- SQLite via `modernc.org/sqlite` in `WHALENURSE_DATA_DIR` (default `/data`).
- Tables: per-key state, incidents and flap history, events (the autoincrement ID doubles as the `since` cursor), and notifications sent.
- Retention: `WHALENURSE_HISTORY_RETENTION` (default 30d).

### 3.11 API and dashboard (read-only)
- **API:** `/api/v1/status`, `/api/v1/containers`, `/api/v1/containers/{key}`, `/api/v1/events?since=<cursor>`, plus an SSE stream. Alongside it: `/healthz` and an optional Prometheus `/metrics`.
- The dashboard uses only this API, and the hub (R1) will reuse it as the agent contract.
- **Pages:**
  - Overview: counts, healer status, compose binary and version, dry-run banner, rate-limit budget.
  - Containers: state, health, incident and rung, cooldown, flap count, badges, effective config.
  - Container detail: timeline, inspect summary, drift status, parent and dependents.
  - History.
- **Tech:** `html/template`, a little vanilla JS (or vendored htmx), assets embedded with `embed`. No Node build.
- **Hub readiness:** `host` in every record, a versioned API, monotonic per-host event IDs, UTC timestamps.

## 4. Configuration (v1)

Initial defaults; tune them after M0.

**Environment variables:**

| Variable | Default |
|---|---|
| `WHALENURSE_NOTIFY_URLS` | — (Shoutrrr URLs, comma-separated) |
| `WHALENURSE_DRY_RUN` | `false` |
| `WHALENURSE_MODE` | `label` (or `all`) |
| `WHALENURSE_POLL_INTERVAL` | `60s` |
| `WHALENURSE_GRACE` | `30s` |
| `WHALENURSE_COOLDOWN` | `30s` (base; doubles per rung, capped at 5m) |
| `WHALENURSE_RECOVERY_PERIOD` | `10m` |
| `WHALENURSE_FLAP_LIMIT` | `3` |
| `WHALENURSE_FLAP_WINDOW` | `24h` |
| `WHALENURSE_CRASHLOOP_AFTER` | `5m` |
| `WHALENURSE_RATE_LIMIT` | `10` (actions/hour) |
| `WHALENURSE_STARTUP_SETTLE` | `2m` |
| `WHALENURSE_DRIFT_GUARD` | `strict` (or `warn`) |
| `WHALENURSE_COMPOSE_BIN` | — (see §3.4) |
| `WHALENURSE_HOST_ID` | hostname |
| `WHALENURSE_DATA_DIR` | `/data` |
| `WHALENURSE_HTTP_ADDR` | empty (dashboard off; `:8484` in examples) |
| `WHALENURSE_HTTP_TOKEN` | — |
| `WHALENURSE_HISTORY_RETENTION` | `30d` |
| `WHALENURSE_HEARTBEAT_URL` | — |
| `WHALENURSE_LOG_LEVEL` | `info` |

**Labels:**

| Label | Values / default |
|---|---|
| `whalenurse.enable` | `true` to opt in |
| `whalenurse.actions` | default `restart,recreate` |
| `whalenurse.heal-exited` | `crashed` \| `always` \| `never` |
| `whalenurse.heal.pull` | `true` \| `false` |
| `whalenurse.cooldown` | per-container override |
| `whalenurse.flap-limit` | per-container override |

**CLI:** `whalenurse run` (default), `pause`, `resume`, `unsuspend`, `status`.

## 5. Architecture and dependencies

```
cmd/whalenurse/      main + subcommands
internal/docker/     interface over the moby client (events, inspect, restart, list) + fake
internal/compose/    binary resolution, command builder, hash/dry-run helpers
internal/engine/     incidents, ladder, flap limit, rules (pure, injected clock); emits domain events
internal/rules/      inspect classification (State.Error, exit codes, heal-exited), fixture-tested
internal/store/      SQLite persistence + retention
internal/notify/     Shoutrrr wrapper, templates, dedupe, heartbeat
internal/api/        /api/v1 JSON + SSE, /healthz, /metrics
internal/web/        dashboard templates + embedded assets
internal/control/    unix-socket control server for the CLI
internal/config/     env + label parsing
```

**Domain event stream:** one stream (`IncidentOpened`, `ActionStarted`, `ActionResult`, `Suspended`, `Recovered`, `Paused`, `TagMoved`, …). Store, notify and API/SSE all subscribe to it, so notifications and history can't disagree.

**Dependencies** (check the latest version at implementation time):
- **`github.com/moby/moby/client` + `/api`.** The client was at v0.6.2 on 8 Oct 2026. It is pre-1.0, so minor versions can break; keep it behind `internal/docker`. API negotiation is the default (`WithAPIVersionNegotiation` is deprecated), and `Events()` returns a single `EventsResult`.
- **Minimum Docker Engine v25.** Engine v29 rejects API versions below 1.44.
- **`modernc.org/sqlite`.**
- **`github.com/nicholas-fedor/shoutrrr`.** Was at v0.17.0 in Aug 2026; vet it in M3.
- No cron library in v1. If R4 needs one, `robfig/cron` is unmaintained (last release Jan 2020); use `go-co-op/gocron/v2` or a cron-expression parser plus your own timer.

## 6. Testing

**Unit tests** (engine with a fake clock and fake Docker):
- the ladder: restart → recreate → SUSPENDED;
- recovery closes the incident;
- the flap limit suspends after N incidents in the window;
- pause and pause expiry;
- the action-in-flight window suppresses interpretation;
- a manual stop is ignored;
- daemon-shutdown kill markers are cleared on start;
- `on-failure` + exit 0 is left alone;
- oneoff containers and swarm tasks are skipped;
- OOM 137 vs stop 137;
- failed start (126/127/128 + `State.Error`) and stale-reference classification;
- dependents are suppressed while the parent's incident is open.

**Other tests:**
- **Rules:** scrubbed `docker inspect` fixtures, including captures from M0.
- **Store:** state survives a restart; retention pruning.
- **API:** golden JSON per endpoint (this is the future hub contract); dashboard pages render against a fixture store, with a test that env values never appear in output.

**Integration tests** (CI, real Docker, projects `whalenurse-test-*` only):
- toggled healthcheck → restart, then recreate, then SUSPENDED;
- heal never pulls (a service with `pull_policy: always`);
- the config-drift guard blocks a recreate after a compose edit;
- the image-drift guard blocks a recreate after a re-tag;
- a ref-holding dependent skips the hash check and is recreated after its parent is;
- notifications reach a local webhook receiver;
- dry-run performs no actions;
- the pull rung, against a local `registry:2`.

**CI matrix:** latest Docker Engine on both the classic and the containerd image store; compose pinned to the version from M0 plus the latest release.

## 7. Milestones

### H — Repo housekeeping
- [maintainer] Rename the repo to `whalenurse` (GitHub redirects the old URL), then run `git remote set-url origin https://github.com/jackspiering/whalenurse.git` in local clones.
- [maintainer] Set the About text and topics (Appendix C).
- Replace `README.md` with Appendix A.
- Add the Appendix B lines to `.gitignore`.
- Commit this plan as `docs/PLAN.md`.
- **Done when:** the README, `.gitignore` and plan are committed, and the [maintainer] items are listed in the report.

### M0 — Diagnose and verify (no app code)

**Read-only scripts** (refuse to modify anything):
- `scripts/m0/scan.sh`: lists containers that are exited with a restart policy, or have a non-empty `State.Error`. Shows exit code, `OOMKilled`, `State.Error`, `FinishedAt` and restart policy.
- `scripts/m0/capture.sh <container> [--since 30m]`: writes into `captures/<timestamp>-<name>/`:
  - `docker inspect`;
  - `docker events --since/--until --filter container=<name> --format '{{json .}}'`;
  - `journalctl -u docker --since ...` filtered with `failed to start container|restartmanger wait error|failed to restart container|failed to prepare mount points`;
  - `docker version`, `docker compose version` and `docker info --format '{{json .}}'` (image store).
- `scripts/scrub-fixture`: the scrubber from §3.3.

**Experiments:** throwaway stacks in `test/m0/` (projects `whalenurse-m0-*` only), plus `test/m0/run.sh`. The script runs each experiment, prints evidence and cleans up with `down -v` for its own projects only.

| # | Question |
|---|---|
| E1 | After `up -d --force-recreate --no-deps <parent>`, are `network_mode: service:` dependents broken (exited, or running with no network)? |
| E2 | Does a plain `up -d --no-deps --pull never --no-build <dep>` (no force) then recreate them? |
| E3 | Does `--no-deps` suppress `depends_on: {restart: true}`? |
| E4 | `config --hash` vs label for the parent (expect a match) and for the dependent (expect a mismatch) |
| E5 | `--dry-run up -d --no-deps ...` output for: an unchanged service, a changed config, a dependent after its parent was recreated, and a re-tagged image. Is it parseable? |
| E6 | Does recreate work from a container with the stacks dir mounted `:ro` at the identical path? |
| E7 | Does the host compose plugin run inside the candidate base image (distroless/static or alpine)? |
| E8 | Hypothesis A reproduction: `restart: unless-stopped`, a bind mount with `create_host_path: false` whose source is removed after the first start, and a process that exits 1. Expect `Exited (128?)`, a non-empty `State.Error` and no retry after 2 minutes |
| E9 | Event sequences for: `docker stop`, a crash, an OOM (tiny memory limit), a restart-policy restart, a compose recreate, and daemon restart (if allowed). Capture these as fixtures |

- [maintainer] Confirm the host for E1–E9. Run `scan.sh` and `capture.sh` on the real host when a stuck container appears.
- Record the results in `docs/m0-findings.md`. Move scrubbed captures into `testdata/`.
- **Done when:** every row above has an answer with evidence, and §3.3, §3.5 and §3.6 are each marked "confirmed" or "needs change" in the findings.

### M1 — Heal MVP (restart only)
- `go mod init github.com/jackspiering/whalenurse` with the latest stable Go.
- Config parsing for the M1 subset; `internal/docker` with a fake; events stream with reconnect.
- Poll and startup sweep; rules from §3.3, with every heal action mapped to restart/start in M1.
- Skips: own container, oneoff, swarm, not opted in. Also opt-in labels, the action-in-flight window, the state key, rate limit, dry-run, the domain event stream (in memory), `/healthz` and JSON logs.
- Dockerfile: static binary, minimal image, read-only rootfs. Document socket access (group or root).
- CI: build, vet, unit tests, integration test (unhealthy container gets restarted; dry-run does nothing).
- **Done when:** the unit and integration tests pass, and `whalenurse run --dry-run` against a test stack logs the expected decisions.

### M2 — Compose recreate, escalation and persistence
- `internal/compose`: binary lookup (§3.4), command builder, `config --hash`, and the dry-run helper if M0 adopted it.
- The full ladder: cooldown backoff, recovery period, flap limit, SUSPENDED, PAUSED.
- SQLite store; control socket and CLI (pause/resume/unsuspend/status).
- `State.Error` classification and the `heal-exited` modes; manual-stop markers.
- Drift guard (§3.6), including the ref-holding skip.
- Dependents (§3.5): discovery, ordering, staleness sweep, and the recreate path chosen in M0.
- **Done when:** the M2 integration tests in §6 pass, and state survives a whalenurse restart.

### M3 — Notifications
- Vet the Shoutrrr fork at the source: latest release, maintenance, licence.
- Templates: action taken, recovered, giving up (with cause), flapping, drift blocked, upgraded during heal (with rollback command).
- Dedupe and the optional heartbeat.
- **Done when:** notifications are verified against a local webhook receiver in CI.

### M4 — Pull rung
- §3.7 end to end, including the `TagMoved` record and the dependents step.
- **Done when:** the integration test against a local `registry:2` passes; with no newer image, the pull rung goes straight to SUSPENDED.

### M5 — Dashboard (read-only)
- §3.11: API, SSE, pages, token, optional `/metrics`, retention.
- **Done when:** golden JSON tests pass, pages render against a fixture store, and the no-secrets-rendered test passes.

### M6 — Polish and public release
- Full README; docs (env and label reference, security, exit-code guide, deployment examples).
- Example compose files: host compose plugin mount, stacks mount, dashboard via a Tailscale container on a shared bridge network.
- `SECURITY.md` and `CHANGELOG.md`.
- GoReleaser: multi-arch images on `ghcr.io/jackspiering/whalenurse` plus binaries; optional systemd unit.
- [maintainer] Announce.

## 8. Roadmap (post-v1)
- **R1 — Multi-server hub:**
  - One autonomous agent per host, plus `whalenurse hub`, which pulls each agent's `/api/v1` over the tailnet with per-agent tokens and a `since` cursor.
  - The hub never talks to Docker.
  - Rejected: a central controller driving remote Docker sockets.
- **R2 — Dashboard controls:** unsuspend, pause/resume, heal now. Needs auth and CSRF protection; every action goes into the history.
- **R3 — Hardening:** socket proxy, update rollback.
- **R4 — Scheduled updates:**
  - Per-service schedule, `apply` or `notify` mode, drift guard first, dependents step, Updates page.
  - Revisit this against the Watchtower fork's feature set and the state of compose #13878 before building it.

## 9. Open questions
1. What makes the maintainer's stuck containers sit at `Exited (N)`? Most likely hypothesis A; M0 confirms with real captures.
2. `:ro` stack mounts (E6).
3. Does the host compose plugin run in the image (E7)?
4. Should the dry-run plan replace the config-hash check (E5)?
5. Does compose recreate dependents on a non-forced `up` (E2)?
6. Final defaults for grace, cooldown, crash-loop and rate limits (after M0).
7. Confirm SQLite at the start of M2.

---

## Appendix A — README.md (milestone H)

```markdown
# whalenurse

**Compose-aware self-healing for Docker hosts.**

When an opted-in container turns unhealthy, or stops and stays stopped, whalenurse restarts it. If that doesn't help, it recreates it from the stack's own compose files, together with any `network_mode: service:` sidecars that depend on it. If that fails too, it stops trying and tells you why.

> **Status: design phase.** There is no code or release yet; nothing below works today. The design and milestones are in [docs/PLAN.md](docs/PLAN.md).

## Why

Docker doesn't restart unhealthy containers. A restart policy also gives up for good when a start fails, which is common at boot when a mount or network isn't ready yet. Existing tools restart containers, but a restart isn't enough when a container has to be recreated, and it leaves sidecars that share its network namespace broken. whalenurse targets that gap:

- **Recreates from your compose files.** It uses the project, files and env file recorded on the container, and never runs `compose down`.
- **Handles sidecars.** After their parent is restarted or recreated, it brings `network_mode` dependents back in order.
- **Never upgrades by accident.** A drift guard refuses to recreate when the compose files or the image tag changed since the container was created.

## Planned features

- Opt-in per container with the `whalenurse.enable=true` label
- Escalation: restart → recreate → optional pull and recreate → notify and suspend
- Backoff, flap limit, global rate limit, pause (maintenance) mode and dry-run
- Notifications through Shoutrrr (ntfy, Gotify, Discord, Telegram and more)
- Optional read-only dashboard and JSON API
- Single static Go binary that runs as a container or under systemd

## Security

whalenurse needs access to the Docker socket, which is root-equivalent on the host. The dashboard will be off by default and read-only; expose it only on localhost, behind a reverse proxy or on a tailnet.

## Requirements (planned)

- Docker Engine v25 or later
- The Docker Compose plugin on the host
- Compose stacks in plain directories on the host

## License

MIT
```

## Appendix B — `.gitignore` additions (milestone H)

```gitignore
# Build output
/whalenurse
/dist/

# Local runtime state
/data/
*.db
*.sqlite*

# Raw M0 captures (docker inspect/events/journal). Scrub before moving into testdata/.
/captures/
```

## Appendix C — GitHub About and topics ([maintainer], milestone H)

- **About:** Compose-aware self-healing for Docker: restarts unhealthy or stopped containers, then recreates them from their own compose files, including network_mode sidecars. Opt-in via labels. Work in progress.
- **Topics:** `docker`, `docker-compose`, `self-healing`, `healthcheck`, `autoheal`, `homelab`, `self-hosted`, `golang`
