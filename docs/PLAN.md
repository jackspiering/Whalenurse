# whalenurse — implementation plan v3.5

Status: v3.5, approved for implementation. It is v3.4 plus the two M2a verdicts below (the `last_seen` heartbeat and the stricter crash-loop rule), which the maintainer approved on 17 Oct 2026. v3.4 was v3.3 plus a split of milestone M2 into M2a and M2b. v3.3 was v3.2 plus the M0b verdicts ([m0b-findings.md](m0b-findings.md) §4), which the maintainer approved on 17 Oct 2026. v3.2 was v3.1 plus the two M1 verdicts ([m1-notes.md](m1-notes.md) decisions 2 and 11), and v3.1 was v3 plus the M0 verdicts ([m0-findings.md](m0-findings.md) §4). Supersedes v3.4. Facts and versions are as of 17 Oct 2026 (16 Oct for the prior-art survey).
Repo: `github.com/jackspiering/whalenurse` (Go module path, GHCR image and binary are all lowercase).

**Changes in v3.5** (from the M2a review of PR #8; [m2a-notes.md](m2a-notes.md) open questions 1 and 2):
- §3.3 *Manual-stop markers*: a fourth mechanism, **(d) the heartbeat**. whalenurse is itself one of the containers a daemon shutdown kills, so its events stream ends by its own SIGTERM and (a) never sees a loss, while the boot id (b) is unchanged: the persisted shutdown-kill markers then let the restore's `start` clear SUSPENDED. Fix: persist `last_seen`; at startup treat the previous run's end as a stream loss (discard markers set in the 30 s before it), and open the restore window when the gap exceeds `POLL_INTERVAL`. The burst rule stays rejected. In M2a this was a known gap, pinned by a test that asserted the opposite; that test is now a normal one.
- §3.3 rule 3: a crash loop also needs the **latest** `RestartCount` increase to be less than `CRASHLOOP_AFTER` ago. Under the v3.4 text three quick restarts followed by a long quiet were a loop from `CRASHLOOP_AFTER` after the first until `RECOVERY_PERIOD` after the last, and a stable container could be restarted and SUSPENDED.
- §3.2 *Persisted*: the heartbeat is persisted. §6: unit tests for both.

**Changes in v3.4** (a process change; no design rule changes):
- §7: milestone M2 is split in two, because it bundled two different kinds of risk. Every M2 bullet is kept, each in exactly one of the two.
  - **M2a** (state and control): CI on Docker Engine 29.x, the SQLite store, the full ladder with the crash-loop rule, the CLI over the control socket, the manual-stop markers, and shutdown handling. Rung 2 does not exist yet: where the ladder would recreate, it records "needs recreate (M2b)", as M1 does, and never restarts a dependent whose parent ID is gone.
  - **M2b** (compose): `internal/compose`, the recreate rung, the drift guard with the startup version gate, dependents, and the rule-1 third-shape / image-drift-comparator bullet. The last bullet mixes rules work with the comparator; it stays whole in M2b, and the third shape is already pinned by the M0b fixtures (`testdata/inspect/m0b-daemon-restart-*`).
- §0 order: H, M0, M1, M0b, **M2a, M2b**, M3 … M6.
- §6: the integration list and the CI matrix say which milestone owns each item. M2a drives no compose binary (it only sets up test stacks), so it runs compose v5.6.0 on both image stores. The full compose matrix (v5.2.0, v5.6.0, latest) belongs to **M2b**, where whalenurse runs compose and the dry-run oracle's output has to be pinned across versions.
- §3.2, §3.3, §4, §9: references to "M2" now name M2a (persistence, the crash-loop rule, the CLI, SQLite).

**Changes in v3.3** (from the M0b review of PR #6; findings references are to [m0b-findings.md](m0b-findings.md) §4):
- §3.3 *Manual-stop markers*: kills seen during a daemon shutdown or reboot must not set manual-stop markers or clear SUSPENDED. Three mechanisms are adopted: (a) quarantine on stream loss, (b) persist the boot id, (c) a restore window after reconnecting to a daemon that was down. A fourth, recognising a burst of `kill[15]`, is **rejected**: `docker compose stop` and `down` send the same burst, so deliberate stops would be healed. The reason is recorded in the section.
- §3.3 rule 4 and §3.5: a `restart: "no"` container stopped by a host shutdown respects its policy. It and its `network_mode` dependents stay down and whalenurse notifies once; only `heal-exited=always` starts them.
- §3.3 rule 1: the third failed-start shape, a failed boot-time restore (`exited`, exit 126/127/128 mapped from the error, `RestartCount` unchanged). The port-conflict matcher accepts `0.0.0.0:` and `:::` (and `[::]:`). `failed to start container` is confirmed at runtime for boot restore, and the journal lines of E8 are confirmed.
- §3.3: the Docker API is unavailable while the daemon restores containers, and `since=` replay holds only the new daemon's last 256 events and never the old daemon's shutdown, so the startup sweep is mandatory after every reconnect that follows a lost stream, not only at process start.
- §3.5: the boot-time wait for `container:` parents is confirmed (dependents come up after their parent, also behind a parent whose start takes longer than 5s); a parent that is not restored takes its dependents down.
- §3.6 check 2: image drift compares the `com.docker.compose.image` label with `docker image inspect --platform linux/<arch> --format '{{.Id}}' <ref>`, on both image stores. `.Descriptor.digest` must never be used: it is empty on the classic store.
- §6: unit tests for the above; the CI image-drift job runs on both stores.
- §3.6, §3.2, §5, README requirements: **minimum versions per rung.** Rung 1 works from Engine v25; rungs 2 and 3 and the drift guard require Engine ≥ 28.1 and Compose ≥ v5.2.0. whalenurse checks at startup, and below that it logs one warning, disables rungs 2 and 3, and the ladder ends with notify + SUSPENDED after rung 1. No store-guessing fallback (answers §9 question 8, found by the CI `image-drift` job).
- §7: M0b is done; M2 carries the items above.

**Changes in v3.2** (from the M1 review of PR #5):
- §3.2: SUSPENDED clears on recovery, on an external `create`, or on a `start` that follows a manual-stop marker. A restart-policy `start` never clears it, because it would re-arm the ladder on every cycle of a loop.
- §3.3 rule 3: a crash loop is signalled by `RestartCount` rises and `die` events, not by `restarting`. The restart manager resets its backoff after a 10s run (moby `restartmanager.go` at docker-v29.9.1), so a loop that crashes every ~15s is almost always `running`. M2 defines the rule independent of how often whalenurse inspects.
- §7 M2: the ladder bullet carries the new loop rule and the recovery period for it.
- M1 keeps its simpler implementation (see [m1-notes.md](m1-notes.md) decision 11); M2 replaces it.

**Changes in v3.1** (E-numbers are the M0 experiments in [m0-findings.md](m0-findings.md) §3):
- §3.3: the event-order example is `kill`, `stop`, `die`, and it differs between operations and Engine versions (E9).
- §3.3 rule 1: a failed first start (`created` + 126/127/128) and a failed restart-policy restart (`exited` + the previous run's exit code, `RestartCount` incremented) are different cases. Rule 1 tests `State.Error` in both and never uses the exit code (E8, E8c, E8d).
- §3.3 rule 1: a failed restart emits no event, so the poll owns detecting it and re-inspects containers still `restarting` after GRACE (E8, E9).
- §3.3 rule 1: the stale-reference matching is split by cause. `No such container` (parent recreated) → recreate the dependent. `cannot join network namespace of a non running container` (parent stopped) → heal the parent first, then restart the dependent. The "other" strings are added (E8e, E8c, E8d, E8).
- §3.3: the exit-code table is corrected, and the scrubber wording describes what `scripts/scrub-fixture` does (E8; findings §4 item 7).
- §3.5: remedy mapping by staleness condition. A dependent whose parent ID is gone is recreated with the non-forced `up`, and never API-restarted (E1, E2). `--no-deps` is confirmed to suppress `depends_on.restart` (E3).
- §3.5: compose #13878 is closed, fixed by compose PR #13880, and absent in v5.6.0 (E2a; findings §1).
- §3.6: the `--dry-run --progress json` oracle is the primary check for non-reference services and parents. `config --hash` is the fallback, and the skip rule stays for all four reference types (E4, E5).
- §3.6: on the containerd store, image drift compares `com.docker.compose.image` with `docker image inspect --platform linux/<arch>` `.Descriptor.digest`, never `.Id` (E5). The classic store is to be verified in M0b and CI.
- §3.9: the base image is `gcr.io/distroless/static:nonroot` with `group_add` for the socket's group (E7).
- §3.7: drift checks 1 and 3 run before the pull; after it, only the digest change is checked (E5: the oracle reports `Recreate` once the image has changed).
- §6: the CI compose matrix is v5.6.0, the latest release and v5.2.0, the only release with #13878 (findings §1). It runs on both image stores.
- §7: new milestone M0b between M1 and M2, for the scenarios M0 could not run (findings §6). M1 never applies rung 1 to a dependent whose parent ID is gone (E1).
- §9: questions 2–5 are answered (E6, E7, E5, E2). Question 1 stays open.
- Consistency edits with no new decisions: §0 (milestone order), §3.4 (`:ro` confirmed by E6), §6 (unit-test list), §7 M2 (wording).

## 0. Instructions for Claude Code

- **One milestone at a time, in order:** H, M0, M1, M0b, M2a, M2b, M3 … M6. At the end of each milestone, stop and report:
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
| Store | SQLite via `modernc.org/sqlite` (recommended in review; confirmed in M2a, v1.60.1) |
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

- **Rungs:** set per container with `whalenurse.actions` (default `restart,recreate`). Rung 3 is appended only with `whalenurse.heal.pull=true`. Each rung runs once per incident. Rungs 2 and 3 are available only with Engine ≥ 28.1 and Compose ≥ v5.2.0; otherwise they are disabled at startup and the ladder ends after rung 1 (§3.6, *Minimum versions per rung*).
- **Recovered means:**
  - with a healthcheck: health is `healthy`;
  - without one: the container is running and `RestartCount` has not changed for `RECOVERY_PERIOD`.
- **Flap count:** closing an incident does not reset it. Only the window expiring does.
- **State key:** `host/project/service/container-number`, taken from the `com.docker.compose.project`, `.service` and `.container-number` labels. Never the container ID, because a recreate changes it.
  - `host` comes from `WHALENURSE_HOST_ID` (default: the hostname).
  - Non-compose containers fall back to the container name and only get rung 1.
- **Persisted from M2a:** incident state, rung reached, SUSPENDED/PAUSED (with reason and expiry), flap history, manual-stop markers and the `last_seen` heartbeat.
- **Action-in-flight window:** while whalenurse is acting on a key, events for that key are *recorded and attributed to the action*, not interpreted. They must not:
  - trigger the manual-stop heuristic;
  - clear SUSPENDED;
  - open a new incident.
- **SUSPENDED clears** when:
  - the container returns to healthy;
  - an external `create` arrives outside any in-flight window;
  - a `start` arrives outside any in-flight window **and follows a manual-stop marker** (someone stopped it, someone started it). A `start` by the restart policy never clears SUSPENDED: it is also a `start` event, and clearing on it would re-arm the ladder on every cycle of a crash loop. Neither does a `start` inside a restore window, nor one that follows a quarantined shutdown kill (§3.3, *Manual-stop markers*): the restore of a daemon restart or reboot would otherwise unsuspend every container;
  - someone runs `whalenurse unsuspend`.
- **Pause:**
  - Syntax: `whalenurse pause <project>[/<service>] [--for 2h]` and `whalenurse resume ...`, run via `docker exec`. The CLI talks to the daemon over a unix socket.
  - Default duration is 2h; `--for 0` means indefinite. Pauses expire so a forgotten pause doesn't disable healing for good.
- **Global rate limit:** `WHALENURSE_RATE_LIMIT` actions per hour (default 10). This prevents action storms during host-wide outages.

### 3.3 Decision rules (inspect after the grace period)

**Events are only triggers.** The decision is made by inspecting the container after `GRACE`, because event order is unreliable. For example, `docker stop` emits `kill` → `stop` → `die` on Engine 29.9.0. The order differs between operations and Engine versions.

**Health events are edge-triggered.** Docker emits `health_status` only when the status changes (verified in `daemon/health.go`), so a missed event never repeats. The poll (default 60s) is the level-triggered backstop and applies the same rules.

**The daemon restarts containers after a restart or a reboot, and that has consequences for the observer** (M0b):
- **The Docker API is unavailable while the daemon restores containers** (9s after a daemon restart; about 48s after a reboot, of which 7s was one slow start). An API error or a closed events stream during that time means "daemon not ready": reconnect with backoff and open no incident.
- **`since=` replay is not a substitute for the sweep.** After a reconnect, `since=<last event time>` returns only the new daemon's buffered events (the restore's `start` events), the buffer holds 256 events, and the old daemon's shutdown events are never available. A `stop` or `die` may never be emitted at all: three of twelve containers were force-killed at the end of a 15s shutdown and had neither. So **the startup sweep is mandatory after every reconnect that follows a lost stream**, not only at process start.

**Fields to inspect:** `State.Status`, `State.Running`, `State.Health`, `State.ExitCode`, `State.OOMKilled`, `State.Error`, `RestartCount`, `HostConfig.RestartPolicy`, `HostConfig.NetworkMode`, and labels.

**Rule precedence:**

0. **Skip entirely:**
   - whalenurse's own container;
   - `com.docker.compose.oneoff=True` (`compose run` containers carry the same project and service labels);
   - swarm tasks (`com.docker.swarm.task.id` present);
   - containers that are not opted in;
   - PAUSED or SUSPENDED keys.
1. **Non-empty `State.Error` means a failed start.** This rule overrides everything below. Test `State.Error != ""` on both `created` and `exited` containers, and never use the exit code to decide.
   - This is hypothesis A, confirmed in moby source and reproduced on Engine 29.9.0 (E8):
     - A start that fails during a restart-policy restart is **not retried** (`daemon/monitor.go`). Docker logs `failed to restart container` at debug level and `restartmanger wait error` at error level (the misspelling is in the source).
     - A start that fails during boot-time restore is not retried either; Docker logs `failed to start container` at error level, once per container (`daemon.go`). Confirmed in M0b after a daemon restart and a reboot (4 containers each, no retry over 183s). The E8 line `restartmanger wait error` is confirmed in the journal; `failed to restart container` is `debug` level and is not expected there.
   - There are three shapes:
     - **Failed first start** (`docker start`, `compose up`): status `created`, `RestartCount` 0, and an exit code of **126** (EACCES), **127** (command not found / ENOTDIR) or **128** (anything else), with `State.Error` set (E8c, E8d). There is no `die` event; the container never ran.
     - **Failed restart by the restart policy:** status `exited`, `State.Error` set, `RestartCount` incremented, and the exit code is the **previous run's** (for example 1), not 126–128 (E8).
     - **Failed boot-time restore** (daemon restart or reboot): status `exited`, `State.Error` set, exit code **126/127/128 mapped from the error** (127 for a bind source that is gone), `RestartCount` **unchanged** (0), `StartedAt` of the previous run and `FinishedAt` of the shutdown. It emits no `die`. The dependents of a parent that fails this way fail with `cannot join network namespace of a non running container` (M0b).
   - **A failed restart emits no event** (no `die`, no `start`). The only event is the `die` of the earlier run. So the poll owns this detection: every poll applies rule 1, and it re-inspects containers still `restarting` after GRACE, because a retry that fails later is silent (E8, E9).
   - Split by cause. Do the matching in one place, backed by fixtures, because error strings vary by Engine version:
     - **Stale reference:**
       - `No such container` (`joining network namespace of container: No such container: <old id>`: the parent was recreated or removed) → recreate the dependent (skip restart; §3.5 gives the command). Restarting it cannot work (E1, E8e).
       - `cannot join network namespace of a non running container` (the parent is stopped, not removed) → recreating the dependent cannot help until the parent runs. Heal the parent first (§3.5 already suppresses the dependent's ladder while the parent has an incident), then restart the dependent (E8e). **If the parent is left down by its policy** (a `restart: "no"` parent stopped by a host shutdown; rule 4), nothing heals it, so the dependent stays down too: no ladder, and one notification (§3.5).
       - `network ... not found` → recreate. Not reproduced in M0; kept from v3.
     - **Other** (missing bind-mount source, port already allocated, NFS or device not ready, command not executable): start first, then recreate. Strings seen in M0:
       - `failed to fulfil mount request: open <path>: no such file or directory` (bind source gone at restart; E8)
       - `invalid mount config for type "bind": bind source path does not exist: <path>` (at create, with `create_host_path: false`; also seen in the E6 negative control)
       - `Bind for <addr> failed: port is already allocated` (E8d). `<addr>` is `0.0.0.0:<port>` or `:::<port>` (seen on the classic-store run) and may be `[::]:<port>`; match on the address-independent part, `port is already allocated`.
       - `exec: "<cmd>": stat <cmd>: no such file or directory` (E8c)
       - `exec: "<cmd>": permission denied` (E8c)
       - `stat <cmd>: not a directory` (E8c). Docker adds a misleading hint about mounting a directory onto a file; ignore it.
2. `running` + `unhealthy` → ladder.
3. A **crash loop**, or `on-failure:N` exhausted → ladder. (A container still `restarting` after GRACE is re-inspected on each poll; rule 1 applies when it fails.) A container that was `created` but never started → recreate.
   - **Crash loop:** at least 3 `RestartCount` increases, the first at least `CRASHLOOP_AFTER` (default 5m) ago **and the latest less than `CRASHLOOP_AFTER` ago** (three quick restarts followed by a long quiet are not a loop). `RestartCount` and `die` events are the signal, not the `restarting` status: the engine's restart manager resets its backoff after a run of 10s or more, so a container that crashes every ~15s is almost always `running` when inspected, and a rule that needs `restarting` never fires. The rule must give the same answer however often whalenurse inspects, so it counts increases between inspects rather than comparing two snapshots. The loop clears after `RECOVERY_PERIOD` (default 10m) without an increase.
   - M1 ships a simpler form: the clock starts on `restarting` or a rising `RestartCount` and clears after GRACE of a steady count ([m1-notes.md](m1-notes.md) decision 11). M2a replaces it with the rule above.
4. `exited` with no `State.Error` → apply `whalenurse.heal-exited` (`crashed` | `always` | `never`; default `crashed`):
   - **Intended exit:** with restart policy `no` or `on-failure` and exit code 0, the exit was intended. Leave it alone in `crashed` and `never` modes.
   - **`crashed`:** heal if whalenurse saw a `die` **without a preceding `kill`**, or `OOMKilled=true` (also watch the `oom` event). If whalenurse missed the events (it was down):
     - heal on a non-zero exit code other than 137/143;
     - 137 with `OOMKilled=true` is a crash, so heal;
     - 137 or 143 without OOM: notify only, don't heal.
   - **`always`:** heal any exited container.
   - **A `restart: "no"` container stopped by a host shutdown** (killed by a daemon restart or reboot: exit 137 or 143, no `State.Error`; M0b `n-parent`) **respects its policy.** Docker does not restore it, and whalenurse does not either, in `crashed` and `never` modes: the shutdown kill is not a crash (see *Manual-stop markers*). Its `network_mode` dependents, which fail at boot with `cannot join network namespace of a non running container`, stay down with it: no ladder runs for them. whalenurse sends **one** notification per boot that names the parent and its dependents (deduplicated, not repeated every poll). With `heal-exited=always` the parent is started, and then its dependents are restarted (the heal-the-parent-first path of rule 1).
   - **`never`:** notify only.

**Manual-stop markers:**
- A marker is set on a `kill` event outside any in-flight window, and cleared on the next `start` event.
- **A daemon shutdown or reboot sends `kill` to every running container** (M0b: 12 of 12 within 20ms at a daemon restart, `kill[15]` then `kill[9]`; at a reboot the observer is cut off after `kill[15]`, before any `stop` or `die`). Taken literally, the rules above would mark every container as manually stopped, and the `start` events of the restore would then clear SUSPENDED on all of them (§3.2). **Kills seen during a daemon shutdown or reboot must not set manual-stop markers or clear SUSPENDED.** Four mechanisms, all required:
  - **(a) Quarantine on stream loss.** When the events stream ends for a reason other than whalenurse's own shutdown (EOF or an error; for whalenurse's own end see (d)), discard every marker created in the 30 s before the loss: the daemon's shutdown limit is 15 s, plus the stop timeouts. A real manual stop in that window is lost too. That container is then judged by rule 4 without evidence (137/143 without OOM: notify only), which is the safe outcome.
  - **(b) Persist the boot id.** Store `/proc/sys/kernel/random/boot_id` with the markers. If it differs at startup, drop all markers and treat the first `STARTUP_SETTLE` as a restore window. A reboot stops whalenurse in the same second as the containers, so a marker can exist without any matching event in the next run.
  - **(c) Restore window.** After connecting to a daemon that was down, until `STARTUP_SETTLE` has passed, a `start` event is a restore: it does not clear SUSPENDED, and a `kill` in the window does not set a marker.
  - **(d) Heartbeat: whalenurse's own end.** In container mode a daemon shutdown kills whalenurse too. Its stream then ends by its own SIGTERM, which (a) excludes, and the boot id is unchanged, so (b) does not fire; the markers it saw and persisted would let the restore's `start` clear SUSPENDED. So persist `last_seen` (a heartbeat, written at most every 5 s, and on a clean stop). At startup treat the previous run's end as a stream loss: discard every marker set in the 30 s before `last_seen` (as in (a)), and if the gap between `last_seen` and now exceeds `POLL_INTERVAL`, open the restore window of (c) (the daemon may be restoring containers). The price is the same as (a): a real manual stop in the 30 s before whalenurse is itself stopped is forgotten, which is the safe outcome. In systemd mode this also covers systemd stopping whalenurse before Docker.
- **Rejected: recognising the shutdown by its burst of `kill[15]`** (two or more containers within a second, tagged as a shutdown). `docker compose stop` and `docker compose down` send the same burst, and so does stopping several containers by hand. A burst rule would label those deliberate stops as shutdowns, drop their markers, and heal the stopped containers. The stream loss in (a), the boot id in (b) and the heartbeat in (d) are the only reliable signals; do not add a burst heuristic.
- Containers that fail to start at boot have `State.Error`, so rule 1 wins. The restored containers' markers never matter again, because the restore starts them.
- A container stopped by hand before the shutdown (`unless-stopped`) stays down after the restore and keeps its marker; that case needs nothing.

**Startup sweep:** after whalenurse starts (including at host boot), and **after every reconnect that follows a lost events stream**, wait `STARTUP_SETTLE` (default 2m), then apply the rules to everything. Replay with `since=` does not replace it (see above).

**Exit-code reading guide** (for M0, docs and the dashboard):

| Code | Meaning |
|---|---|
| 0 | Clean exit |
| 126 / 127 / 128 + `State.Error`, status `created` | Failed **first** start (126 EACCES, 127 command not found / ENOTDIR, 128 anything else) |
| 126 / 127 / 128 + `State.Error`, status `exited`, `RestartCount` unchanged | Failed **boot-time restore** (the code is mapped from the error, not the previous run's) |
| any code + `State.Error`, status `exited` | Failed **restart** by the restart policy. The code is the previous run's, so don't read it; show `State.Error` |
| 137 | SIGKILL: OOM if `OOMKilled`, otherwise `docker kill` or a stop that hit its timeout |
| 143 | SIGTERM (usually a stop) |

**Fixtures:** every rule gets a unit test with scrubbed `docker inspect` fixtures. The scrubber (`scripts/scrub-fixture`):
- removes `Config.Env`;
- replaces label values except `com.docker.compose.*` and `whalenurse.*`; other label keys are kept;
- rewrites the path-valued compose labels (`working_dir`, `config_files`, `environment_file`) to `/srv/stacks/<project>[/<file>]`;
- replaces `Config.Cmd`, `Entrypoint`, `Args`, healthcheck test arguments and health-check log output with `<scrubbed>`, because a raw capture can hold secrets there;
- replaces hostnames, MACs and host mount sources with placeholders, and every IP except `0.0.0.0` and `127.0.0.1` with `192.0.2.99`. Those two stay so that strings like `Bind for 0.0.0.0:38080 failed` still match;
- keeps container IDs, so `container:<id>` references still match the parent;
- has an events mode (whitelisted attributes only, times rebased to 0) and a `--check` leak check.

### 3.4 Recreate via compose

**Compose binary lookup** (first match wins):
1. `WHALENURSE_COMPOSE_BIN`
2. The host plugin, mounted read-only at a fixed path (e.g. `/host/docker-compose`). The host path is typically `/usr/libexec/docker/cli-plugins/docker-compose`; `docker info` shows it. The compose plugin runs standalone.
3. The bundled copy.

Log which binary and version is in use at startup, and expose it in `/api/v1/status`. Below Compose v5.2.0 (or Engine 28.1) rungs 2 and 3 are disabled (§3.6). In systemd mode, use the host's `docker compose`.

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
- `:ro` works (E6). Mounting the stacks elsewhere fails, because compose passes the host path to the daemon for relative binds: that is why the path must be identical.

**Fallbacks and restarts:**
- If compose labels or files are missing or unreadable, skip rung 2 and notify.
- Rung 1 (restart) uses only the Docker API.

### 3.5 Dependents (network_mode sidecars)

Dependents break whenever their parent is restarted or recreated, by anything: Docker's restart policy, whalenurse, or a manual `compose up`.

- **Discovery:** a dependent is any container whose `HostConfig.NetworkMode` (or IPC/PID mode) is `container:<id-or-name>` pointing at the parent. Compose resolves `service:X` to `container:<id>` at create time.
- **Parent restart** (same ID): restart dependents via the Docker API, one at a time, with a health wait between each. The dependent keeps the parent's old namespace until it is restarted (E1 part B). A non-forced `up` does not repair it (E2c).
- **Parent recreate** (new ID): recreate the dependents.
  - Command: `<compose> ... up -d --no-deps --pull never --no-build <dep...>` **without** `--force-recreate`. Compose hashes the *resolved* `container:<id>`, so the new parent ID causes a mismatch and compose recreates the dependents itself. Confirmed for a running-but-stale dependent and for one that already exited (E2b, E2d).
  - Use `--force-recreate` only if a post-check still finds a stale `NetworkMode` afterwards.
- **Remedy by staleness condition** (the two conditions are under *Staleness sweep* below):
  - *Parent ID gone* (the dependent's `NetworkMode` targets an ID that is not the parent's current ID, or no longer exists) → **recreate** the dependent as above.
  - *Same ID, parent started later* → **restart** the dependent via the API.
  - **Never API-restart a dependent whose parent ID is gone.** The restart fails (`joining network namespace of container: No such container`) and turns a running-but-disconnected dependent into an exited one (E1).
- **Trigger:** a parent `start` or `create` event outside the in-flight window. When the action is whalenurse's own, it runs the dependents step explicitly.
- **Staleness sweep** (part of the poll and startup sweep). A dependent is stale if either:
  - its `NetworkMode` is `container:<id>` and `<id>` is not the parent's current ID; or
  - the parent's `State.StartedAt` is newer than the dependent's.

  A stale dependent usually stays `running` with only a loopback interface (E1), so no state-based rule notices it. Only this sweep and the parent's `start`/`create` event find it.
- **At boot** (confirmed in M0b after a daemon restart and a reboot): Docker restores a `container:` dependent after its parent, with the same network namespace, also behind a parent whose start takes longer than the 5s cap (8s; the dependent started after the parent's `start` event) and behind a parent that only becomes healthy 25s later (the daemon does not wait for readiness). A parent that is **not restored** (`restart: "no"`) or whose start fails takes its dependents down: they are attempted at once, fail with `cannot join network namespace of a non running container`, and are not retried. The "single shared 5s timer" covers a container's dependencies together, which with one `container:` parent is just a 5s cap. A dependent restored before its parent was not reproduced; the startup sweep stays as the backstop.
- **A parent left down by policy leaves its dependents down** (`restart: "no"` parent stopped by a host shutdown, `heal-exited` not `always`; §3.3 rule 4): no ladder runs for the dependents, and one notification covers the parent and them.
- **Ordering:** while a parent has an open incident or an in-flight action, suppress its dependents' ladders. Heal the parent first, wait for it to be healthy, then handle the dependents in order.
- **Reverse case:** a dependent failing on its own never triggers an action on its parent.
- **Compose behaviors to keep in mind:**
  - `depends_on: {X: {restart: true}}` only applies to explicit compose commands, not to restarts by Docker's restart policy.
  - `up --no-deps` narrows the project and therefore suppresses `depends_on.restart` (E3, with positive controls). So the explicit dependents step is needed, and there is no double restart. When dependents are in scope, compose restarts or recreates them *before* the parent is recreated.
  - Compose bug #13878 (closed): in v5.2.0, `network_mode: service:` dependents were recreated on *every* `up`. It was fixed by compose PR #13880 (29 Jun 2026), which v5.3.0 and later contain, and v5.6.0 does not recreate unchanged dependents (E2a). On v5.2.0, expect external `create` events when the maintainer runs `compose up`, and treat them as external recreates.

### 3.6 Drift guard (before rung 2, and before rung 3's `up`)

A recreate deploys whatever is on disk and in the local image store *now*. The guard runs three checks:

1. **Config drift:**
   - **Primary: the dry-run oracle (E5).** For non-reference services and for parents, run `<compose> ... --dry-run --progress json up -d --no-deps --pull never --no-build <svc>`.
     - Output is one JSON object per line: `{"id":"Container <name>","status":"Working|Done","text":"<action>"}`.
     - A `Recreate` text for the service's container means drift. The oracle is compose's own recreate decision, so it covers config drift and image drift together. It does not say why.
     - It has no side effects: container IDs were unchanged after every dry run in E5.
     - A stopped container with unchanged config shows `Starting`/`Started` and no `Recreate`, so that is not drift.
     - The JSON format is a compose implementation detail. The CI compose matrix pins it (§6).
   - **Fallback: `config --hash`,** used when the oracle's output can't be parsed. Compare `<compose> config --hash=<svc>` with the container's `com.docker.compose.config-hash` label (E4).
     - Syntax: comma-separated service names, or `'*'` for all; commas are not trimmed. Output: one `<service> <hash>` line per service, sorted.
   - **Skip this check, oracle and hash both, for services whose `network_mode`/`ipc`/`pid`/`volumes_from` references another service or container.** Compose hashes those *after* resolving the reference to `container:<id>`, but `config --hash` hashes the unresolved form, so they never match, even on a freshly created container (E4: all four reference types; compose source: `execCreateContainer` → `resolveServiceReferences` → `ServiceHash`). The oracle reports `Recreate` for a dependent after its parent was recreated, which is expected rather than drift (E5). For these services, rely on checks 2–3 and on the parent's own check.
2. **Image drift:**
   - Compare the container's `com.docker.compose.image` label, the digest compose recorded and uses for its own recreate decision, with what the image reference resolves to locally now:
     ```
     docker image inspect --platform linux/<arch> --format '{{.Id}}' <ref>
     ```
     `<arch>` is the server's (`docker version --format '{{.Server.Arch}}'`). This is **one comparison that is correct on both image stores** (M0b, `test/m0/m0b/image-drift.sh`): it equals the label before a re-tag and differs after. On the containerd store the label is the platform manifest digest and `--platform … .Id` returns it; on the classic store the label is the image ID.
   - **Never use `.Descriptor.digest`**, with or without `--platform`: it is empty on the classic store, so the guard would report drift for every container and SUSPEND them all. Never compare the label with plain `<ref>` `.Id`, with `.RepoDigests` or with the container's `.Image` either: on containerd those are index digests and never equal the label, and on classic plain `.Id` happens to work, which is the trap.
   - If the inspect fails (the reference is no longer present locally, or has no image for that platform), that is a failure: skip the recreate and say why.
   - **`image inspect --platform` needs Engine 28.1 or later** (API 1.49; docker/cli added the flag after v28.0.4, and the GitHub runner's Engine 28.0.4 answered `unknown flag: --platform` in the first CI run of the `image-drift` job). There is **no fallback and no store guessing** for older Engines: see *Minimum versions per rung* below.
   - The oracle already covers image drift for the services it checks. Run this explicit comparison for the services the oracle skips and whenever the hash fallback is used.
   - Fresh Engine v29 installs default to the containerd store, and a host that has run the classic store stays on it unless `containerd-snapshotter` is set explicitly. The CI `image-drift` job runs the check on both stores.
   - Allowed only for rung 3.
3. **Compose version skew** (only when running the bundled compose fallback):
   - Compare the container's `com.docker.compose.version` label (present; `5.6.0` in the fixtures) with the bundled version.
   - If both the versions and the hashes differ, report "can't verify drift (compose X on host vs Y in whalenurse)".
   - This is a real risk: v5.5.0 changed image-digest reconciliation and v5.6.0 pinned the hash byte layout. M0 had only one compose version, so skew is exercised by the CI matrix (§6).

**On any failure:** skip the recreate, notify with the specific cause, and set SUSPENDED.
- If whalenurse itself moved a shared tag (via rung 3 on another service), the message says which service did it and when.

**Strictness:** `WHALENURSE_DRIFT_GUARD=strict|warn` (default `strict`). `warn` recreates anyway and says so.

**Minimum versions per rung** (decided 17 Oct 2026, after the first CI run of the M0b `image-drift` job):
- **Rung 1 (restart, Docker API only) works from Engine v25**, the minimum for whalenurse as a whole.
- **Rungs 2 and 3 (recreate, pull) and the drift guard require Engine ≥ 28.1 and Compose ≥ v5.2.0**, the oldest compose in the CI matrix (§6). Engine 28.1 is the first with `image inspect --platform` (§3.6 check 2); the dry-run oracle's output is pinned by the compose matrix (on the runner's compose 2.38.2 it printed `null` instead of JSON lines).
- **whalenurse checks both at startup** (the Engine version from the API, the compose version of the binary chosen in §3.4). If either is below the minimum it logs **one warning** that names the versions, **disables rungs 2 and 3** for every container, and the ladder ends after rung 1 with notify + SUSPENDED, as if `whalenurse.actions=restart` were set. The same applies per container when its labels select rungs that are disabled. Status and the dashboard show the disabled rungs and the reason.
- **No store-guessing fallback.** Plain `<ref>` `.Id` equals the label on the classic store but not on the containerd store, and guessing the store is how the guard would flag or miss drift silently. Below the minimum, the guard does not run at all, and neither does the recreate it protects.

### 3.7 Pull rung (opt-in, `whalenurse.heal.pull=true`)

Runs only after restart and recreate have failed.
1. Run `<compose> pull <svc>`.
2. If the digest is unchanged, skip to notify + SUSPENDED.
3. If it changed, run `up ... --pull never`, then the dependents step.

Run drift checks 1 and 3 (§3.6) **before** step 1, not after the pull. After a pull that changed the image, the dry-run oracle reports `Recreate` because the image changed (E5), so checking afterwards would block every pull rung. After the pull, only check that the digest changed.

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
- **Base image: `gcr.io/distroless/static:nonroot`** (E7). The statically linked compose plugin runs in it (and in `alpine`). The image runs as uid 65532, which cannot reach the socket by default (`permission denied while trying to connect to the docker API`), so add `group_add` for the socket's group (`stat -c %g /var/run/docker.sock` on the host). There is no shell, so the image's own healthcheck must call the binary.
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
| `WHALENURSE_CONTROL_SOCKET` | `/run/whalenurse.sock` (unix socket for `/healthz` and, from M2a, the CLI; added in M1) |
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

**Version minimums (fixed, not configurable):** Engine v25 for rung 1 (restart). Rungs 2 and 3 and the drift guard need Engine ≥ 28.1 and Compose ≥ v5.2.0. whalenurse checks them at startup, logs one warning if either is lower, disables rungs 2 and 3, and ends the ladder with notify + SUSPENDED after rung 1 (§3.6). `WHALENURSE_DRIFT_GUARD=warn` does not bypass this: below the minimum there is no recreate to guard.

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
- **Minimum Docker Engine v25** for rung 1 (restart). Engine v29 rejects API versions below 1.44. Rungs 2 and 3 and the drift guard require Engine ≥ 28.1 and Compose ≥ v5.2.0 (§3.6).
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
- shutdown kills (M0b fixtures `m0b-daemon-restart-shutdown` and `m0b-reboot-shutdown`):
  - markers created in the 30 s before an events-stream loss are discarded, and an older real manual stop survives;
  - a changed boot id drops all markers and opens a restore window;
  - the heartbeat: whalenurse killed by the shutdown itself (no stream loss) and restarted with the same boot id discards the markers set in the 30 s before `last_seen`, an older real manual stop survives, a gap over `POLL_INTERVAL` opens the restore window, and a SUSPENDED container stays SUSPENDED through it;
  - the crash-loop rule: three quick restarts followed by a quiet are not a loop, restarts continuing to within `CRASHLOOP_AFTER` are, and the verdict does not depend on the poll interval or `GRACE`;
  - a `start` inside a restore window, or after a quarantined kill, does not clear SUSPENDED, and a SUSPENDED container killed by a shutdown and restored stays SUSPENDED;
  - negative test for the rejected burst rule: the same `kill[15]` burst with the events stream still up (`docker compose stop`) keeps its markers and the containers stay stopped;
  - a `restart: "no"` parent stopped by a shutdown and its dependents stay down with one notification, and `heal-exited=always` starts the parent and then the dependents;
- `on-failure` + exit 0 is left alone;
- oneoff containers and swarm tasks are skipped;
- OOM 137 vs stop 137;
- failed first start (`created` + 126/127/128 + `State.Error`), failed restart (`exited` + previous exit code + `State.Error`) and failed boot-time restore (`exited` + 127 + `State.Error`, `RestartCount` 0), all classified by `State.Error` alone;
- the port-conflict matcher accepts `Bind for 0.0.0.0:80`, `Bind for :::80` and `Bind for [::]:80`;
- an API error or a closed stream while the daemon restores is "not ready" and opens no incident, and the sweep runs after the reconnect;
- the startup version check: Engine below 28.1 or compose below v5.2.0 logs one warning, disables rungs 2 and 3, and an unhealthy container ends at notify + SUSPENDED after rung 1; with both at the minimum nothing is disabled;
- stale-reference classification: `No such container` → recreate; `cannot join network namespace of a non running container` → heal the parent first, then restart the dependent;
- a container still `restarting` after GRACE is re-inspected by the poll;
- dependents are suppressed while the parent's incident is open.

**Other tests:**
- **Rules:** scrubbed `docker inspect` fixtures, including captures from M0.
- **Store:** state survives a restart; retention pruning.
- **API:** golden JSON per endpoint (this is the future hub contract); dashboard pages render against a fixture store, with a test that env values never appear in output.

**Integration tests** (CI, real Docker, projects `whalenurse-test-*` only). The tag says which milestone owns the test:
- **M2a:** toggled healthcheck → restart, then "needs recreate (M2b)", then SUSPENDED (rung 2 does not exist yet);
- **M2a:** state survives a whalenurse restart (SUSPENDED and the flap history are still there, and the ladder does not start over);
- **M2a:** pause, resume and unsuspend over the control socket against a real daemon;
- **M2a:** dry-run performs no actions;
- **M2b:** toggled healthcheck → restart, then recreate, then SUSPENDED;
- **M2b:** heal never pulls (a service with `pull_policy: always`);
- **M2b:** the config-drift guard blocks a recreate after a compose edit;
- **M2b:** the image-drift guard blocks a recreate after a re-tag, and does not block an unchanged container, on both image stores (the comparator of §3.6);
- **M2b:** a ref-holding dependent skips the hash check and is recreated after its parent is;
- **M3:** notifications reach a local webhook receiver;
- **M4:** the pull rung, against a local `registry:2`.

**CI matrix:** latest Docker Engine on both the classic and the containerd image store, crossed with three compose versions. The `image-drift` job (`test/m0/m0b/image-drift.sh`) runs on both stores today.

- **From M2a** the integration tests run on Engine 29.x with compose v5.6.0 on both stores, installed the way the `image-drift` job installs them (the runner's Engine 28.0.4 has no `image inspect --platform`, and its compose 2.38.2 prints `null` for the dry-run oracle). M2a does not drive compose: it uses compose only to set up test stacks, so the other two versions add nothing there.
- **From M2b** the full compose matrix applies, because whalenurse then runs compose itself and the oracle's output and the config hash are what the matrix pins:
- **v5.6.0**, the version M0 ran on;
- **the latest release**, floating. It is v5.6.0 today, so the two jobs coincide until a newer release;
- **v5.2.0**, the only release with compose bug #13878. v5.3.0 (2 Jul 2026) contains the fix, and v5.2.0 (23 Jun) is the only release before the fix (29 Jun). It exercises recreate-on-every-`up` for sidecars, config-hash and dry-run output across versions, and the version-skew check (§3.6).

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
- Poll and startup sweep; rules from §3.3, with every heal action mapped to restart/start in M1. One exception: **never apply rung 1 to a dependent whose parent ID is gone** (§3.5: the restart fails and turns a running dependent into an exited one, E1). Log it as "needs recreate (M2)" and take no action. This covers a stale `NetworkMode` and the `No such container` stale-reference error.
- Skips: own container, oneoff, swarm, not opted in. Also opt-in labels, the action-in-flight window, the state key, rate limit, dry-run, the domain event stream (in memory), `/healthz` and JSON logs.
- Dockerfile: static binary, minimal image, read-only rootfs. Document socket access (group or root).
- CI: build, vet, unit tests, integration test (unhealthy container gets restarted; dry-run does nothing).
- **Done when:** the unit and integration tests pass, and `whalenurse run --dry-run` against a test stack logs the expected decisions.

### M0b — Scenarios M0 could not run (no app code; after M1, must finish before M2a)

M0 ran on a VM where the daemon could not be restarted and the journal was not readable (findings §6). M0b closes those gaps; its results and the proposed §3 changes are in [m0b-findings.md](m0b-findings.md). The scripts are in `test/m0/m0b/` (prepare, maintainer step, collect). It extends `test/m0` and `scripts/m0`; the safety rules of §0 apply unchanged, and it needs the maintainer's confirmation of the host.

- **[maintainer]** Daemon restart and host reboot (E9's last scenario) on the throwaway VM. Record:
  - the shutdown `kill` events and how the manual-stop markers (§3.3) clear on Docker's boot restart;
  - boot-time restore: `failed to start container` is logged and not retried, and `State.Error` is set;
  - the shared 5s `container:` wait (§3.5), and whether dependents come up before their parent.
- **[maintainer]** Journal evidence: run `scripts/m0/capture.sh` on a host where `journalctl -u docker` is readable, and confirm `failed to start container` and `restartmanger wait error` appear for E8 and for boot-time restore.
- **Classic image store:** switch the VM's `daemon.json` to the classic store (**[maintainer]**: needs a daemon restart), and run E5's image-drift part, ideally all of `run.sh`. Confirm what `com.docker.compose.image` holds there (§3.6). CI covers this too, so write the check so that CI can run it.
- **Engine 29.9.1** (containerd 2.4.2): upgrade the VM (**[maintainer]**: restarts the daemon) and re-run the suite. Findings §1 found no change in 29.9.1 that touches any experiment, so expect no differences.
- **Done (17 Oct 2026):** the verdicts were approved and are in this plan as v3.3. The CI `image-drift` job (it installs Engine 29.x and compose 5.6.0, because the runner's Engine 28.0.4 has no `image inspect --platform`) and the M0b self-test are in `ci.yml`.
- **Done when:** each item has an answer with scrubbed evidence in [m0b-findings.md](m0b-findings.md) (a separate file, not a section of `m0-findings.md`) and fixtures in `testdata/`, and §3.3, §3.5 and §3.6 are each marked "confirmed" or "needs change" for what M0b tested. Report anything that contradicts a plan rule before M2 starts.

### M2a — State, the full ladder, the CLI and shutdown handling (no compose)

Rung 2 does not exist yet. Where the ladder would recreate, it records "needs recreate (M2b)" as M1 does (a `RungUnavailable` event), and goes on to SUSPENDED. It still never API-restarts a dependent whose parent ID is gone.

- CI: the integration tests run on Docker Engine 29.x with compose v5.6.0 on both image stores, installed the way the `image-drift` job installs them (§6, *CI matrix*).
- The full ladder: cooldown backoff, recovery period, flap limit, SUSPENDED, PAUSED. Crash-loop detection by the §3.3 rule 3 definition (≥3 `RestartCount` increases, the first ≥ `CRASHLOOP_AFTER` ago, cleared after `RECOVERY_PERIOD` without one), independent of the inspect rate, with a test that varies the poll interval and `GRACE`. SUSPENDED clears as in §3.2.
- SQLite store; control socket and CLI (pause/resume/unsuspend/status).
- `State.Error` classification and the `heal-exited` modes; manual-stop markers.
- Shutdown handling (§3.3, *Manual-stop markers*): quarantine on stream loss, persisted boot id, restore window, the `last_seen` heartbeat for whalenurse's own end (v3.5), the sweep after every reconnect, "daemon not ready" for API errors during a restore. M1's tracker still reads a shutdown as manual stops (pinned in `rules_test.go`, `multiKeyEventVerdicts`); M2a changes that expectation on purpose. The throwaway-VM check of this (whalenurse in a container, `systemctl restart docker`) is `test/m0/m0b/whalenurse-restart/`.
- **Done when:** the M2a integration tests in §6 pass, state survives a whalenurse restart, and an unhealthy container goes restart → "needs recreate (M2b)" → SUSPENDED.

### M2b — Compose recreate, the drift guard and dependents

- `internal/compose`: binary lookup (§3.4), command builder, the dry-run oracle with its `config --hash` fallback (§3.6).
- Drift guard (§3.6), including the ref-holding skip.
- Dependents (§3.5): discovery, ordering, staleness sweep, the remedy mapping (recreate via non-forced `up` when the parent ID is gone, API restart otherwise), and the "needs recreate (M2b)" cases M1 only logs. A parent left down by policy leaves its dependents down (one notification).
- The startup version check and the per-rung minimums of §3.6 (one warning, rungs 2 and 3 disabled, ladder ends at notify + SUSPENDED after rung 1).
- Rule 1's third shape and the address-independent port matcher (M1's matcher already ignores the address); the image-drift comparator of §3.6. (The third shape is already pinned by the M0b fixtures and `rules_test.go`; this bullet stays whole because its second half is part of the drift guard.)
- CI: the full compose matrix of §6 (v5.2.0, v5.6.0, latest) on both stores.
- **Done when:** the M2b integration tests in §6 pass.

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
1. What makes the maintainer's stuck containers sit at `Exited (N)`? **Open.** Hypothesis A is reproduced (E8) and matches the moby source, but only `scripts/m0/scan.sh` and `capture.sh` on the real host can confirm it. The exit code `docker ps` shows will be the previous run's, not 128.
2. ~~`:ro` stack mounts?~~ **Answered:** they work, with the stacks root at the identical path. See [findings §5](m0-findings.md#5-answers-to-the-plans-open-questions-9) and [E6](m0-findings.md#e6-recreate-from-a-helper-container-with-the-stacks-directory-ro).
3. ~~Does the host compose plugin run in the image?~~ **Answered:** yes, in `alpine` and `distroless/static`; a non-root user needs the socket's group. See [findings §5](m0-findings.md#5-answers-to-the-plans-open-questions-9) and [E7](m0-findings.md#e7-does-the-host-compose-plugin-run-in-the-candidate-base-images).
4. ~~Should the dry-run plan replace the config-hash check?~~ **Answered:** it is the primary check for non-reference services and parents, with `config --hash` as the fallback (§3.6). See [findings §5](m0-findings.md#5-answers-to-the-plans-open-questions-9) and [E5](m0-findings.md#e5---dry-run-as-the-recreate-oracle).
5. ~~Does compose recreate dependents on a non-forced `up`?~~ **Answered:** yes after a parent recreate, no after a parent restart (E2). See [findings §5](m0-findings.md#5-answers-to-the-plans-open-questions-9).
6. Final defaults for grace, cooldown, crash-loop and rate limits (after M0).
7. ~~Confirm SQLite at the start of M2a.~~ **Answered (17 Oct 2026):** `modernc.org/sqlite` v1.60.1 (29 Sep 2026, `go 1.26.0`; pure Go, so the `CGO_ENABLED=0` build holds; about 19 MB binary). See [m2a-notes.md](m2a-notes.md).
8. ~~Image drift on Engines older than 28.1, and compose versions older than the matrix?~~ **Answered (17 Oct 2026):** rung 1 keeps Engine v25 as the minimum. Rungs 2 and 3 and the drift guard require Engine ≥ 28.1 and Compose ≥ v5.2.0. whalenurse checks both at startup; below that it logs one warning, disables rungs 2 and 3, and the ladder ends with notify + SUSPENDED after rung 1. No store-guessing fallback. See §3.6, *Minimum versions per rung*.

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

- Docker Engine v25 or later (restart only); Engine 28.1 or later and Compose v5.2.0 or later for recreate, pull and the drift guard
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
