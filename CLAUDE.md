# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository state

The design lives in `docs/PLAN.md` (v3.2, which is v3.1 plus the approved M1 verdicts, as of 17 Oct 2026). Milestones H (repo housekeeping), M0 (diagnose and verify) and M1 (restart-only heal MVP) are built; **M0b is next** (after the maintainer reviews M1), then M2. The M0 results are in `docs/m0-findings.md`, the M1 decisions and deviations in `docs/m1-notes.md`, and how to run the daemon in `docs/running.md`.

Layout:

- Go module `github.com/jackspiering/whalenurse` (Go 1.27.2). `cmd/whalenurse` has `run [--dry-run]`, `healthcheck` and `version`. `internal/` has `docker` (moby client behind interfaces, plus a `Fake`), `rules`, `engine`, `daemon` (wires events, poll, sweep, the pre-action guard and the actor), `config`, `clock`, `api` (`/healthz`) and `control` (unix socket). The planned packages `compose`, `store`, `notify` and `web` do not exist yet.
- `test/integration/`: tests against a real Docker daemon (build tag `integration`), with the fail-closed guard in `guard.go`.
- `scripts/m0/`, `scripts/scrub-fixture`, `test/m0/`, `testdata/inspect/`, `testdata/events/`: the M0 tooling and scrubbed fixtures.
- `Dockerfile` (distroless/static:nonroot), `.github/workflows/ci.yml`, `examples/compose.yaml`.

Commands:

```
go vet ./... && go test -race ./...                      # unit tests; touch no Docker
CGO_ENABLED=0 go build -trimpath -o whalenurse ./cmd/whalenurse
shellcheck -x $(grep -rIl --exclude-dir=testdata -E '^#!.*(ba)?sh' scripts test; find scripts test -name '*.sh')
scripts/m0/selftest.sh && scripts/scrub-fixture --self-test && test/m0/run.sh selftest   # M0 self-tests: no Docker needed
```

The integration tests (`WHALENURSE_INTEGRATION=1 go test -tags integration ./test/integration/`) and `test/m0/run.sh all` act on a real Docker daemon. The integration tests run in CI on GitHub-hosted runners; do not run them locally unless the maintainer has confirmed the host (see Docker host safety below).

whalenurse is a compose-aware self-healer for Docker hosts. For opted-in containers that go unhealthy or stop unexpectedly, it restarts them, then recreates them from the stack's own compose files, then optionally pulls and recreates. It notifies on giving up. It is a single static Go binary that runs as a container (socket mounted) or under systemd.

## Working protocol (from the plan)

- Work **one milestone at a time, in order: H, M0, M1, M0b, M2 … M6**. After each, stop and report what was built, what was verified and how, what is uncertain, and which **[maintainer]** items are open. Wait for review before starting the next.
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
  - A startup sweep runs after `STARTUP_SETTLE`.
- **One domain event stream** (`IncidentOpened`, `ActionStarted`, `ActionResult`, `Suspended`, `Recovered`, `Paused`, `TagMoved`, …). Store, notify and API/SSE all subscribe to it, so notifications and history can't disagree.
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
  - A non-empty `State.Error` means a failed start. It overrides everything else, and the exit code is never used for it. There are two shapes: a failed first start is `created` + exit 126/127/128, and a failed restart-policy restart is `exited` + the previous run's exit code + `RestartCount` incremented.
  - The cause splits three ways:
    - `No such container` (parent recreated): recreate the dependent.
    - `cannot join network namespace of a non running container` (parent stopped): heal the parent first, then restart the dependent.
    - "Other" causes (missing bind source, port in use, bad command): start, then recreate.
  - Then unhealthy, crash-loop, and exited handling via `whalenurse.heal-exited`.
  - Keep error-string matching in one place, backed by fixtures, because strings vary by Engine version.
- **Heal never upgrades by accident.**
  - Every compose `up` uses `--pull never --no-build`.
  - Only the opt-in pull rung may pull.
  - Never use `compose down`, because it removes the shared project network.
  - The drift guard (config drift, image drift, compose version skew) runs before recreate. Any failure skips the recreate, notifies, and sets SUSPENDED.
  - Config drift: the primary check is the dry-run oracle, `--dry-run --progress json up -d --no-deps --pull never --no-build <svc>`, where a `Recreate` text for the container means drift. `config --hash` is the fallback when that output can't be parsed.
  - Both are skipped for services that reference another service or container (`network_mode`/`ipc`/`pid`/`volumes_from`), because compose hashes those after resolving them.
  - Image drift on the containerd store: compare the `com.docker.compose.image` label with `docker image inspect --platform linux/<arch>` `.Descriptor.digest`. Never use `.Id` or the container's `.Image`: they are index digests, so every container would look drifted. The classic store is still to be verified (M0b, CI).
- **Dependents (`network_mode: container:<parent>` sidecars) break whenever the parent restarts or recreates.**
  - On a parent restart (same ID, parent started later), restart dependents via the API, one at a time.
  - On a parent recreate (parent ID gone), recreate the dependents with the non-forced `up -d --no-deps --pull never --no-build <dep>`. Use `--force-recreate` only if a post-check still finds a stale `NetworkMode` (M0 E1/E2).
  - **Never API-restart a dependent whose parent ID is gone.** The restart fails and turns a running-but-disconnected dependent into an exited one. M1 logs it as "needs recreate (M2)".
  - A stale dependent stays `running` with only a loopback interface, so only the staleness sweep (or the parent's `start`/`create` event) finds it. The sweep also covers the boot-time race.
  - While a parent has an open incident or an in-flight action, suppress its dependents' ladders.
  - A dependent never triggers an action on its parent.
- **Compose binary lookup order:** `WHALENURSE_COMPOSE_BIN`, then the host plugin mounted `:ro` at a fixed path, then the bundled copy. In container mode the stacks root must be mounted at the **identical path** as on the host.
- **Dashboard and API:** read-only, off by default, built on the versioned `/api/v1` JSON API. The CLI controls the daemon over a local unix socket, and there are no network-exposed write endpoints in v1. Never render env vars or full inspect output.
- The moby client (`github.com/moby/moby/client` + `/api`, not `github.com/docker/docker`) is pre-1.0, so keep it behind `internal/docker`.
- Minimum supported Docker Engine is v25.

## Configuration surface

All env vars are prefixed `WHALENURSE_` (poll interval, grace, cooldown, flap limit, rate limit, drift guard, host ID, data dir, HTTP addr/token, and others). Container labels use the `whalenurse.*` namespace (`enable`, `actions`, `heal-exited`, `heal.pull`, `cooldown`, `flap-limit`). Plan §4 holds the full tables with defaults. Update that table (and later the docs) when adding or changing a variable or label.

## Testing approach

- Engine unit tests use a fake clock and fake Docker.
- Rules are tested against scrubbed `docker inspect` fixtures, including M0 captures.
- API endpoints get golden JSON tests, which become the future hub contract.
- A test must prove env values never appear in dashboard output.
- Integration tests run in CI against real Docker, and only in `whalenurse-test-*` projects.
- The CI matrix covers the classic and containerd image stores, and compose v5.6.0 (the M0 version), the latest release, and v5.2.0 (the only release with compose bug #13878).
