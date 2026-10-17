# M2a notes

Decisions, deviations and open questions for milestone M2a (state, the full ladder, the CLI and shutdown handling; no compose) of [PLAN.md](PLAN.md) v3.4. Where the plan is ambiguous this file says what was chosen and why, so the maintainer can overrule it. Nothing here changes a rule of §3; the open questions at the end are the ones that might.

## Open questions for the maintainer (most important first)

1. **whalenurse itself is killed by a daemon shutdown, and the plan's mechanisms do not cover it.** In container mode a daemon restart sends `kill` to whalenurse too. Its events stream then ends because of its **own** SIGTERM, which mechanism 1 excludes by name ("other than whalenurse's own shutdown"), and the boot id is unchanged, so mechanism 3 does not fire. The shutdown-kill markers it saw before dying were persisted. After the restart the restore's `start` follows a marker and **clears SUSPENDED**, which v3.3 forbids. Pinned by `TestKnownGapWhalenurseKilledByTheDaemonShutdownItself` (it passes by asserting the gap and skips itself once the gap is closed).
   - I implemented mechanisms 1, 3 and 4 exactly as written and did not patch this, because the obvious fix contradicts the plan text.
   - **Proposed fix:** persist a `last_seen` timestamp (a heartbeat, written at most every few seconds and on a clean stop). At startup treat the previous run's end as a stream loss: discard markers set in the 30 s before `last_seen` (mechanism 1, extended to our own stop), and open the restore window (mechanism 4) whenever the gap between `last_seen` and now exceeds the poll interval. The price is the same as for mechanism 1: a real manual stop in the 30 s before whalenurse itself is stopped is forgotten, which is the safe outcome (notify only). In systemd mode this also covers systemd stopping whalenurse before Docker.
2. **Rule 3 taken literally has a sharp edge** (pinned by `TestCrashLoopLiteralEdgeThreeQuickRestartsThenQuiet`). Three quick restarts followed by a long quiet count as a crash loop once `CRASHLOOP_AFTER` has passed since the first of them, and keep counting until `RECOVERY_PERIOD` has passed since the last. A stable container can then be restarted once, and (with no recreate rung until M2b) SUSPENDED. Candidate refinement, not applied: require the most recent increase to be within `CRASHLOOP_AFTER` too.
3. **When the flap limit fires.** §3.2 says "more than `FLAP_LIMIT` incidents within `FLAP_WINDOW` -> SUSPENDED, even if the last incident recovered". I read it as: the (limit+1)th incident to open is SUSPENDED at once, with nothing tried (so with the default 3, the 4th incident within 24 h). The alternative, healing the (limit+1)th and suspending when it closes, would make "SUSPENDED clears when the container returns to healthy" clear it again immediately.
4. **`unsuspend` forgets the flap history of a key that was suspended for flapping** (and only then). Without it the next incident re-suspends at once and `unsuspend` is a no-op. A `create` or a start-after-manual-stop that clears a flapping suspension does *not* clear the history.
5. **What "returns to healthy" means for clearing SUSPENDED.** I used §3.2's *Recovered means*: with a healthcheck, `healthy` (clears at once); without one, running with `RestartCount` unchanged for `RECOVERY_PERIOD`. The incident closes only after another `RECOVERY_PERIOD` of staying recovered (for a container without a healthcheck, that is the same moment). If the container fails again in between, it is the same incident: the ladder goes on where it was, so a spent ladder means SUSPENDED again without trying anything.
6. **The poll now inspects every opted-in container, every poll.** The container list carries no `RestartCount`, so otherwise a loop whose `die` events were missed is invisible, and the "same answer however often whalenurse inspects" requirement could not hold. Cost: one inspect per opted-in container per poll (60 s).
7. **A dry run stores nothing.** `run --dry-run` does not open the store and does not need `/data`; its incidents, SUSPENDED states and pauses are in memory. Otherwise a dry run would leave fake state for the next real run.

## Upstream check (17 Oct 2026)

| Item | Version | Source |
|---|---|---|
| Go | 1.27.2, still the latest stable | go.dev/dl |
| `modernc.org/sqlite` | **v1.60.1** (29 Sep 2026), `go 1.26.0` in its go.mod; pulls `modernc.org/libc` v1.77.1 | proxy.golang.org; answers plan §9 question 7: confirmed |
| `github.com/moby/moby/client` / `api` | v0.6.2 (8 Oct) / v1.56.1 (1 Oct), unchanged | proxy.golang.org |
| Docker Engine | 29.9.1 (14 Oct), the latest `docker-v29.*` release; CI installs `5:29.*` from the apt repo | GitHub releases |
| Docker Compose | v5.6.0 (2 Oct), still the latest release | GitHub releases |
| Actions | checkout v7.0.1, setup-go v7.0.0, unchanged | releases |

The static binary is about 19 MB with the SQLite driver (`CGO_ENABLED=0`, no cgo).

## What M2a built

- **CI.** `scripts/ci/setup-docker.sh <classic|containerd>` is the `image-drift` job's install step, moved into a script (it refuses to run outside GitHub Actions). The `image-drift` job and the `integration` job (now a two-store matrix) both call it: Engine 29.x, compose 5.6.0. **The full compose matrix (v5.2.0, v5.6.0, latest) belongs in M2b**: M2a never runs compose itself (the integration tests use it only to set up their stacks), so the other two versions would test nothing. Plan §6 says so.
- **`internal/store`** (SQLite, WAL, one connection). Tables: `meta` (boot id), `key_state` (name, marker, crash-loop counters), `flaps`, `incidents` (the open incident is the row with `closed_at IS NULL`, at most one per key; closed rows are the history), `pauses`, `actions` (the rate-limit ledger). Schema version in `PRAGMA user_version`; a database from a newer whalenurse is refused. A failed write is logged and counted and the engine carries on from memory; failing to *open* the store at start is fatal. `Prune` applies `WHALENURSE_HISTORY_RETENTION` (hourly).
- **`internal/engine`** (still pure; the Persister is an interface). Incident state with RECOVERY_PERIOD, flap limit (`whalenurse.flap-limit` overrides it), SUSPENDED, PAUSED by `project[/service]` scope with expiry, the crash-loop tracker (`Observe`), the restore window, `QuarantineMarkers`/`DropAllMarkers`, status. The engine is now safe for concurrent use (one mutex), because the control socket's HTTP goroutines call it. Events are published under the lock, so subscribers must not call back into the engine.
- **`internal/rules`**: `Input.Loop` replaces `LoopSince`/`CrashloopAfter`; the verdict comes from the engine's `RestartCount` tracker. `Tracker` now knows when a marker was set, `Quarantine`/`Drop`, and `Summarize` understands a `stream_lost` pseudo-event and a new verdict `shutdown-killed`.
- **`internal/daemon`**: boot id check, `StreamLost`/`StreamConnected`, the sweep after every reconnect that follows a loss, "docker API not ready" instead of errors, pause expiry and re-evaluation on resume/unsuspend, `Status`.
- **Control socket and CLI**: `GET /v1/status`, `POST /v1/pause|resume|unsuspend` on the unix socket only; `whalenurse pause|resume|unsuspend|status`. `main.handlers` builds the TCP handler (read-only `/healthz`) and the socket handler separately; a test asserts the TCP one rejects every command. `control.Get` no longer truncates at 512 bytes.
- **Image**: `/data` exists, owned by 65532, and is a `VOLUME`.
- **Strings**: "needs recreate (M2)" is now "(M2b)" everywhere; `GaveUp` is `Suspended`; `Paused`, `Resumed`, `MarkersQuarantined`, `RestoreWindowOpened` and `RestoreEvent` are new domain events.

## Behaviour changes from M1

- `Recovered` now fires after `RECOVERY_PERIOD` of staying healthy, not at the first healthy re-inspect. `Unsuspended` ("returned to healthy") fires at the first healthy one when there is a healthcheck.
- A container `restarting` with `RestartCount` never rising is no longer a crash loop (M1 counted time spent `restarting`). `TestCrashLoopMeasuredFromFirstSightingAndRestartRung` was replaced by `TestRestartingWithoutRisingCountIsNotACrashLoop` on purpose.
- Busy: an incident that is SUSPENDED or recovering (healthy) no longer holds back its dependents' ladders; one still being worked does.
- Shutdown fixtures: `multiKeyEventVerdicts` now expects `shutdown-killed` for the two shutdown fixtures (after the stream loss that follows them), and `TestShutdownFixturesAreManualStopsWithoutAStreamLoss` keeps the old reading for a stream that stays up.

## Decisions the plan leaves open

- **Pause scope** is `project` or `project/service`; a container outside compose is addressed by its name. Matching is on the state key's parts, so `p` does not reach `pp`.
- **The quarantine reaches 30 s back from the moment the loss is noticed**, compared with each marker's event time.
- **A restore window opens** after a reconnect that follows a lost stream, and at start when the boot id changed. It does not open at every process start (the plan says "after connecting to a daemon that was down"; see open question 1).
- **A `kill` inside the restore window** sets no marker and a `start` inside it unsuspends nothing; both are recorded as `RestoreEvent`.
- **The rate-limit ledger is persisted** (not on the plan's list) so a restart does not refill the budget.
- **A subscription counts as established only after the first event or 2 s open.** `Events()` returns before the daemon has accepted the request, so during an outage every failed retry would otherwise look like a reconnect (a restore window, a scheduled sweep and log lines each time). A retry that never established is not a loss and not a reconnect (`TestFailedRetriesDuringAnOutageAreNotReconnects`).
- **Crash-loop timestamps.** An increase is stamped with the previous run's `FinishedAt` when that lies between the two inspects, otherwise with the inspect time. Both only delay the verdict, never bring it forward; detection is within `CRASHLOOP_AFTER + 2 x (POLL_INTERVAL + GRACE)` of the loop's start whatever those are (tested over 3 polls x 4 graces x with/without `die` events).
- **A count that goes down** (the container was recreated) resets the crash-loop tracker.
- **`status` shows** only keys with something to show.

## Tests

- Unit: `engine` (ladder, cooldown growth, recovery, flapping, per-container limit, unsuspend, pause and expiry, scope matching, persistence round-trip, loop tracker incl. the rate-independence table), `store` (round-trip across reopen, history, retention, one open incident per key, schema guard, write errors), `daemon` (shutdown fixtures with a stream loss, older real manual stop survives, the burst negative test, boot id, restore window, SUSPENDED survives a shutdown, sweep after reconnect, API not ready, loop at every poll/GRACE, pause/resume/unsuspend, state survives a restart, concurrency under `-race`), `control` (commands over a real unix socket, no truncation), `cmd` (TCP has no write endpoints, status output, flags after the positional), `config`, `rules`.
- The negative burst test uses a container that exits 1 on SIGTERM, so the marker is what keeps it down (137 would be notify-only anyway); its control case shows the same container is started once the markers are gone.
- Integration (CI only, behind the guard): the ladder ends at SUSPENDED after one restart and "needs recreate (M2b)"; pause/resume/unsuspend over the socket against a real daemon; the binary's state survives SIGTERM and a restart; the image runs with `/data` as a named volume and the CLI works through `docker exec`. Existing tests now run on Engine 29.x / compose 5.6.0 on both stores.

## Not verified

- **No real daemon shutdown or reboot with whalenurse running.** The shutdown handling is tested against the M0b fixtures and fake streams; whether the real sequence at shutdown matches (and open question 1) needs a **[maintainer]** run on the throwaway VM: run whalenurse in a container, suspend a crash-looper, `systemctl restart docker`, and check it is still SUSPENDED.
- The integration tests were not run locally (the plan forbids it); CI is their only run. The first run (`39050052452`) passed everything except the two tests that call the new CLI through the built binary, which failed because those calls did not get `WHALENURSE_CONTROL_SOCKET` (a test bug, fixed). The old tests, the ladder test, the control-socket test and `TestImage` (with `/data` as a named volume and the CLI through `docker exec`) passed on both stores on Engine 29.x / compose 5.6.0. The run that counts is the last one on the PR, which the PR description links.
- A real `docker compose stop` of a project with whalenurse running is covered only by the fixture-based negative test.
- SQLite under a bind-mounted `/data` with another owner, and on NFS, was not tried.

## [maintainer]

- Review the open questions above (1 and 2 may change §3.3).
- The throwaway-VM shutdown run described under *Not verified*.
