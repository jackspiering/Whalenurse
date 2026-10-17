# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository state

The design lives in `docs/PLAN.md` (v3.5, which is v3.4 plus the two approved M2a verdicts (the `last_seen` heartbeat, the stricter crash-loop rule); v3.4 split M2 into M2a and M2b; v3.3 holds the approved M0b verdicts; as of 17 Oct 2026). Milestones H (repo housekeeping), M0 (diagnose and verify), M1 (restart-only heal MVP) and M0b (the scenarios M0 could not run) are built, and **M2a is built** (plan §7: CI on Docker 29, the SQLite store, the full ladder, the CLI, shutdown handling; no compose). **M2b is next**, after the maintainer reviews M2a: `internal/compose`, recreate, the drift guard with the version gate, dependents, and the full compose CI matrix. Rung 2 does not exist until M2b: where the ladder would recreate it records "needs recreate (M2b)" and goes on to SUSPENDED. The M0 results are in `docs/m0-findings.md`, the M0b results in `docs/m0b-findings.md`, the M1 decisions and deviations in `docs/m1-notes.md`, the M2a decisions and open questions in `docs/m2a-notes.md`, and how to run the daemon and the CLI in `docs/running.md`. The M0b verdicts are in the plan as v3.3 ("Changes in v3.3"); `docs/m0b-findings.md` has the evidence behind them.

Layout:

- Go module `github.com/jackspiering/whalenurse` (Go 1.27.2). `cmd/whalenurse` has `run [--dry-run]`, `pause`, `resume`, `unsuspend`, `status` (over the control socket), `healthcheck` and `version`. `internal/` has `docker` (moby client behind interfaces, plus a `Fake`), `rules`, `engine`, `daemon` (wires events, poll, sweep, the pre-action guard and the actor), `config`, `clock`, `api` (`/healthz`), `control` (unix socket: `/healthz` and the CLI commands) and `store` (SQLite via `modernc.org/sqlite`). The planned packages `compose`, `notify` and `web` do not exist yet.
- `test/integration/`: tests against a real Docker daemon (build tag `integration`), with the fail-closed guard in `guard.go`.
- `scripts/m0/`, `scripts/scrub-fixture` (inspect, events and `--journal` modes), `test/m0/`, `testdata/inspect/`, `testdata/events/`, `testdata/journal/`: the M0 tooling and scrubbed fixtures.
- `test/m0/m0b/`: the M0b scenarios (and `whalenurse-restart/`, the M2a check with whalenurse in a container), each `prepare.sh` → maintainer step (`step.sh`) → `collect.sh`, plus `image-drift.sh` (store-aware, for CI), `teardown.sh` and `selftest.sh`. State goes to `captures/m0b/` (gitignored). `image-drift.sh` also runs in CI (`image-drift` job in `ci.yml`, both image stores).
- `Dockerfile` (distroless/static:nonroot; `/data` is a volume owned by 65532), `.github/workflows/ci.yml`, `scripts/ci/setup-docker.sh` (CI's Engine 29.x + compose 5.6.0 install and image-store switch; refuses to run outside GitHub Actions), `examples/compose.yaml`.

Commands:

```
go vet ./... && go test -race ./...                      # unit tests; touch no Docker
CGO_ENABLED=0 go build -trimpath -o whalenurse ./cmd/whalenurse
shellcheck -x $(grep -rIl --exclude-dir=testdata -E '^#!.*(ba)?sh' scripts test; find scripts test -name '*.sh')
scripts/m0/selftest.sh && scripts/scrub-fixture --self-test && test/m0/run.sh selftest && test/m0/m0b/selftest.sh   # self-tests: no Docker needed
```

The integration tests (`WHALENURSE_INTEGRATION=1 go test -tags integration ./test/integration/`), `test/m0/run.sh all` and the `test/m0/m0b/` scenarios act on a real Docker daemon; the M0b steps restart the daemon, reboot or switch the image store, so only the maintainer runs them. Never run a probe outside the agreed scenarios, or anything that can exhaust CPU, memory, disk or ports, without asking (M0b's 4000-port probe nearly exhausted the VM). The integration tests run in CI on GitHub-hosted runners; do not run them locally unless the maintainer has confirmed the host (see Docker host safety below).

whalenurse is a compose-aware self-healer for Docker hosts. For opted-in containers that go unhealthy or stop unexpectedly, it restarts them, then recreates them from the stack's own compose files, then optionally pulls and recreates. It notifies on giving up. It is a single static Go binary that runs as a container (socket mounted) or under systemd.

## Working protocol (from the plan)

- Work **one milestone at a time, in order: H, M0, M1, M0b, M2a, M2b, M3 … M6**. After each, stop and report what was built, what was verified and how, what is uncertain, and which **[maintainer]** items are open. Wait for review before starting the next.
- **[maintainer]** items (GitHub settings, anything on the real Docker host) are not yours to do. List them in the report.
- **When you change `docs/PLAN.md`, update `CLAUDE.md` to match in the same PR.**
- **Check upstream before relying on facts.** Verify Docker/Compose behavior and dependency versions against current source, docs or releases. Use the latest stable Go and the latest release of each dependency. Record differences from the plan.
- **If a finding or test contradicts a plan rule, stop and report it.** Don't quietly change the design.
- **Docker host safety:**
  - Only act on containers in compose projects named `whalenurse-test-*` or `whalenurse-m0-*`.
  - Never stop, restart, recreate, pull, prune or remove anything else.
  - Integration tests and M0 scripts must enforce this in code with a project-name filter that fails closed.
  - Don't run M0 experiments (or the M0b scenarios) until the maintainer confirms the host.
- **Fixtures:** never commit raw `docker inspect`, `docker events` or `journalctl` output. Raw captures go in `/captures/` (gitignored). Only scrubbed fixtures (via `scripts/scrub-fixture`) go in `testdata/`.
- Build with `CGO_ENABLED=0`.
- Lowercase `whalenurse` everywhere: Go module path, GHCR image (`ghcr.io/jackspiering/whalenurse`) and binary.

## Architecture (planned)

```
cmd/whalenurse/      main + subcommands (run, pause, resume, unsuspend, status)
internal/docker/     interface over github.com/moby/moby/client (events, inspect, restart, list) + fake
internal/compose/    compose binary resolution, command builder, hash/dry-run helpers
internal/engine/     incidents, ladder, flap limit (pure; injected clock; no Docker calls)
internal/rules/      inspect classification (State.Error, exit codes, heal-exited), fixture-tested
internal/store/      SQLite (modernc.org/sqlite) persistence + retention
internal/notify/     Shoutrrr wrapper, templates, dedupe, heartbeat
internal/api/        /api/v1 JSON + SSE, /healthz, /metrics
internal/web/        read-only dashboard (html/template, embedded assets, no Node build)
internal/control/    unix-socket control server for the CLI
internal/config/     env + label parsing
```

Cross-cutting design points that span several packages:

- **Events are only triggers.**
  - Decisions come from inspecting the container after `GRACE`, because Docker event order is unreliable. `docker stop` emits `kill`, `stop`, `die` on Engine 29.9.0, and the order differs between operations and Engine versions.
  - A failed restart-policy restart emits **no event**, so the poll detects it. The poll also re-inspects containers still `restarting` after `GRACE`.
  - `health_status` events are edge-triggered, so the safety poll (default 60s) is the level-triggered backstop.
  - A startup sweep runs after `STARTUP_SETTLE`, and again after every reconnect that follows a lost events stream. The Docker API is unavailable while the daemon restores containers (API errors then mean "not ready", not an incident), and `since=` replay holds only the new daemon's last 256 events, so replay never replaces the sweep.
  - **Shutdown kills:** a daemon shutdown or reboot sends `kill` to every running container. Those kills must not set manual-stop markers or clear SUSPENDED. Mechanisms: (a) discard markers created in the 30 s before an events-stream loss, (b) persist `/proc/sys/kernel/random/boot_id` with the markers and drop them when it changes, (c) a restore window of `STARTUP_SETTLE` after reconnecting to a daemon that was down, in which `start` events are restores, (d) a persisted `last_seen` heartbeat: whalenurse is itself killed by a daemon shutdown (no stream loss, same boot id), so at startup the previous run's end counts as a stream loss (markers from the 30 s before `last_seen` are discarded; a gap over `POLL_INTERVAL` opens the restore window). Do **not** add a rule that recognises a burst of `kill[15]`: `docker compose stop`/`down` send the same burst, so deliberate stops would be healed.
- **One domain event stream** (`IncidentOpened`, `ActionStarted`, `ActionResult`, `Suspended`, `Recovered`, `Paused`, `TagMoved`, …). Store, notify and API/SSE all subscribe to it, so notifications and history can't disagree.
- **State and control (M2a).**
  - The engine persists through an interface (`engine.Persister`), implemented by `internal/store`; it stays pure and is safe for concurrent use (one mutex), because the control socket calls it from HTTP goroutines. Events are published under that lock, so subscribers must not call back into the engine.
  - **Dry-run stores nothing** (it does not open the store or need `/data`); `run` fails fast if it cannot open the store.
  - The CLI commands (`pause`, `resume`, `unsuspend`, `status`) are served on the unix socket only. The TCP listener gets the read-only `/healthz` handler; a test in `cmd/whalenurse` asserts it has no write endpoints. Keep it that way.
  - The poll inspects every opted-in container, because only an inspect carries `RestartCount` (the crash-loop rule, §3.3 rule 3, lives in `engine.Observe`).
  - The crash-loop rule (§3.3 rule 3, `engine.Observe`) needs ≥3 `RestartCount` increases, the first ≥ `CRASHLOOP_AFTER` ago **and the latest < `CRASHLOOP_AFTER` ago**.
  - `test/m0/m0b/whalenurse-restart/` is the throwaway-VM check of the shutdown handling with whalenurse itself in a container (prepare → `sudo systemctl restart docker` → collect); only the maintainer runs the step.
- **Ladder: one pass per incident.**
  - The rungs are restart (Docker API), recreate (compose), then optional pull+recreate.
  - After each rung comes a cooldown, then a re-inspect.
  - All rungs failing leads to SUSPENDED.
  - More than `FLAP_LIMIT` incidents within `FLAP_WINDOW` also suspends, and closing an incident does not reset the flap count.
  - PAUSED is separate and expires (default 2h).
- **State key is `host/project/service/container-number`**, from compose labels, never the container ID, because a recreate changes the ID. Non-compose containers fall back to the container name and only get rung 1. The host ID is carried in every key, event and API record for the future multi-server hub.
- **Action-in-flight window:** events for a key that whalenurse is acting on are recorded and attributed to the action, not interpreted. They must not trigger manual-stop markers, clear SUSPENDED or open an incident.
- **Rule precedence** (`internal/rules`, plan §3.3):
  - Skip rules come first: own container, `compose.oneoff=True`, swarm tasks, not opted in, PAUSED/SUSPENDED.
  - A non-empty `State.Error` means a failed start. It overrides everything else, and the exit code is never used for it. There are three shapes: a failed first start is `created` + exit 126/127/128; a failed restart-policy restart is `exited` + the previous run's exit code + `RestartCount` incremented; a failed boot-time restore is `exited` + exit 126/127/128 mapped from the error + `RestartCount` unchanged.
  - The cause splits three ways:
    - `No such container` (parent recreated): recreate the dependent.
    - `cannot join network namespace of a non running container` (parent stopped): heal the parent first, then restart the dependent.
    - "Other" causes (missing bind source, port in use, bad command): start, then recreate.
  - Then unhealthy, crash-loop, and exited handling via `whalenurse.heal-exited`.
  - Keep error-string matching in one place, backed by fixtures, because strings vary by Engine version. The port-conflict string appears as `Bind for 0.0.0.0:80` and `Bind for :::80`; match on `port is already allocated`.
  - A `restart: "no"` container stopped by a host shutdown respects its policy: it and its `network_mode` dependents stay down and whalenurse notifies once per boot. Only `heal-exited=always` starts them.
- **Heal never upgrades by accident.**
  - Every compose `up` uses `--pull never --no-build`.
  - Only the opt-in pull rung may pull.
  - Never use `compose down`, because it removes the shared project network.
  - The drift guard (config drift, image drift, compose version skew) runs before recreate. Any failure skips the recreate, notifies, and sets SUSPENDED.
  - Config drift: the primary check is the dry-run oracle, `--dry-run --progress json up -d --no-deps --pull never --no-build <svc>`, where a `Recreate` text for the container means drift. `config --hash` is the fallback when that output can't be parsed.
  - Both are skipped for services that reference another service or container (`network_mode`/`ipc`/`pid`/`volumes_from`), because compose hashes those after resolving them.
  - Image drift, on both image stores: compare the `com.docker.compose.image` label with `docker image inspect --platform linux/<arch> --format '{{.Id}}' <ref>`. **Never use `.Descriptor.digest`** (empty on the classic store, so every container would look drifted), and never plain `.Id`, `.RepoDigests` or the container's `.Image` (index digests on containerd). `image inspect --platform` needs Engine 28.1 or later; older Engines have no fallback: rungs 2 and 3 are disabled below Engine 28.1 / Compose v5.2.0 (plan §3.6, *Minimum versions per rung*).
- **Dependents (`network_mode: container:<parent>` sidecars) break whenever the parent restarts or recreates.**
  - On a parent restart (same ID, parent started later), restart dependents via the API, one at a time.
  - On a parent recreate (parent ID gone), recreate the dependents with the non-forced `up -d --no-deps --pull never --no-build <dep>`. Use `--force-recreate` only if a post-check still finds a stale `NetworkMode` (M0 E1/E2).
  - **Never API-restart a dependent whose parent ID is gone.** The restart fails and turns a running-but-disconnected dependent into an exited one. M1 logs it as "needs recreate (M2b)".
  - A stale dependent stays `running` with only a loopback interface, so only the staleness sweep (or the parent's `start`/`create` event) finds it. The sweep also covers the boot-time race. At boot Docker restores a dependent after its parent, also behind a slow parent start; a parent that is not restored takes its dependents down (M0b).
  - While a parent has an open incident or an in-flight action, suppress its dependents' ladders.
  - A dependent never triggers an action on its parent.
- **Compose binary lookup order:** `WHALENURSE_COMPOSE_BIN`, then the host plugin mounted `:ro` at a fixed path, then the bundled copy. In container mode the stacks root must be mounted at the **identical path** as on the host.
- **Dashboard and API:** read-only, off by default, built on the versioned `/api/v1` JSON API. The CLI controls the daemon over a local unix socket, and there are no network-exposed write endpoints in v1. Never render env vars or full inspect output.
- The moby client (`github.com/moby/moby/client` + `/api`, not `github.com/docker/docker`) is pre-1.0, so keep it behind `internal/docker`.
- Minimum supported Docker Engine is v25, for rung 1 (restart) only. Rungs 2 and 3 (recreate, pull) and the drift guard require Engine ≥ 28.1 and Compose ≥ v5.2.0, checked at startup: below that, one warning, rungs 2 and 3 disabled, and the ladder ends with notify + SUSPENDED after rung 1. No store-guessing fallback (plan §3.6).

## Configuration surface

All env vars are prefixed `WHALENURSE_` (poll interval, grace, cooldown, flap limit, rate limit, drift guard, host ID, data dir, HTTP addr/token, and others). Container labels use the `whalenurse.*` namespace (`enable`, `actions`, `heal-exited`, `heal.pull`, `cooldown`, `flap-limit`). Plan §4 holds the full tables with defaults. Update that table (and later the docs) when adding or changing a variable or label.

## Testing approach

- Engine unit tests use a fake clock and fake Docker.
- Rules are tested against scrubbed `docker inspect` fixtures, including M0 captures.
- API endpoints get golden JSON tests, which become the future hub contract.
- A test must prove env values never appear in dashboard output.
- Integration tests run in CI against real Docker, and only in `whalenurse-test-*` projects.
- The CI matrix covers the classic and containerd image stores, and compose v5.6.0 (the M0 version), the latest release, and v5.2.0 (the only release with compose bug #13878). The `image-drift` job (M0b) already runs `test/m0/m0b/image-drift.sh` on both stores.
