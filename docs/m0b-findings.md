# M0b findings

Milestone M0b of [PLAN.md](PLAN.md) §7: the scenarios M0 could not run ([m0-findings.md](m0-findings.md) §6). No app code. Run on 17 Oct 2026 on the same throwaway Debian VM as M0 (Debian 13, kernel 6.12, cgroup v2), with the maintainer running every root or disruptive step. Raw output lives in `captures/` (gitignored); only scrubbed excerpts and fixtures are committed. This file is separate from `m0-findings.md`; plan §7 says "a new section" there, and §7 now points here instead.

**Summary.** The design holds. §3.5 is confirmed. §3.3 and §3.6 each need corrections, listed in [Verdicts](#4-verdicts). Nothing is dropped, but three items change what M2 must implement:

1. **Kills during a daemon shutdown or a reboot must not set manual-stop markers or clear SUSPENDED.** Every running container gets `kill` at shutdown, an observer is cut off before it sees `die`, and the restore afterwards emits `start` for the containers that come back. Taken literally, §3.3 and §3.2 together would mark everything as manually stopped and then unsuspend everything ([Standalone M2 item](#standalone-m2-item-shutdown-kills)).
2. **The image-drift comparator in §3.6 is wrong on the classic store**, and M0's fix (`.Descriptor.digest`) is empty there. One comparator works on both stores: `docker image inspect --platform linux/<arch> --format '{{.Id}}' <ref>` against the `com.docker.compose.image` label.
3. **A failed start during boot-time restore is a third shape**: status `exited`, exit code 126/127/128 mapped from the error, `RestartCount` unchanged. Rule 1 still works (it tests `State.Error` alone), but the exit-code guide and the fixtures need the new row.

## 1. Environment and upstream check

| Component | Before | After | Note |
|---|---|---|---|
| Docker Engine | 29.9.0 | **29.9.1** | latest 29.x in the apt lists and the latest upstream release (GitHub, 14 Oct 2026, non-prerelease) |
| containerd.io | 2.4.1 | **2.4.2** | latest packaged; the runtime reports `v2.4.2` |
| runc | 1.5.2 | 1.5.2 | |
| Docker Compose | v5.6.0 | v5.6.0 | deliberately held: CI pin and M0 baseline |
| buildx | v0.38.0 | v0.38.0 | |
| Image store | containerd (`overlayfs`) | containerd, then **classic (`overlay2`)** for scenario 5 | see [the store note](#image-store-note) |
| `docker-ce-rootless-extras` | 29.9.0 | 29.9.1 | pulled in by apt; not used |

Differences from the plan: none in the facts it states. `GetDependentContainers` and the 5s timer are as described in §3.5 (moby `daemon/daemon.go` at docker-v29.9.1, `restore()`); the dependency being waited for is the container's own `container:` parent (and its legacy links).

## 2. Method and safety

- Every scenario is `prepare.sh` → maintainer step → `collect.sh` in `test/m0/m0b/<scenario>/`. `prepare.sh` sets up the stacks, records a baseline in `captures/m0b/<scenario>/<id>/` and writes the step as `step.sh`, which stamps the time before and after. `collect.sh` reads only that directory, so it works from a fresh session and can be repeated. Before the reboot, `prepare.sh` checks that everything `collect.sh` needs is on a persistent disk (`/tmp` is a tmpfs here) and starts by itself, and refuses to print the step otherwise.
- Docker safety is unchanged. Everything that changes a container goes through the guards in `test/m0/lib.sh`, and M0b projects are additionally restricted to `whalenurse-m0-b-*`. `test/m0/m0b/selftest.sh` checks that (stubbed Docker), and `run.sh selftest` checks that the leftover sweep now skips `whalenurse-m0-b-*` projects (they must survive a restart between prepare and collect). `prepare.sh` lists every container outside `whalenurse-m0-*` that the step would bounce and refuses unless `M0B_ALLOW_OTHER_CONTAINERS=1`. There were none.
- **Event listener.** The daemon keeps events in an in-memory buffer of 256 (`daemon/events/events.go`) that starts empty in each daemon process, so `docker events --since` after a restart cannot show what the old daemon emitted during shutdown. Scenarios 2 and 3 record events with a read-only user systemd unit (`listener.sh`; `Linger=yes` keeps it across logout and restarts it at boot) that reconnects with `--since` and writes `connect`/`disconnect` markers. A socket-mounted recorder container was rejected: it sits outside the guard model.
- **Boot stack** (`stacks/boot/compose.yaml`, project `whalenurse-m0-b-boot`, 15 containers). It covers: a normal parent and dependent pair; a slow-app parent (listens after 25s); a slow-start parent (publishes 240 ports, so `docker start` takes 7.8–8.7s against the daemon's 5s wait; calibrated, see below); a parent whose bind source is deleted; a dependent whose own bind source is deleted; a `restart: "no"` parent with an `unless-stopped` dependent; `unless-stopped` and `always` containers stopped manually before the step; a container that is created but never started. `stacks/boot/expect.tsv` holds what the moby source predicts for each, and `collect.sh` flags every difference. Both scenarios showed **0 unexpected**.
- Journal reading: `collect.sh` uses plain `journalctl`, falls back to `sg systemd-journal` and then `sudo -n`, and filters through `scripts/scrub-fixture --journal` (new in M0b, with a self-test). Excerpts are limited to the daemon lines that matter.

### Incident: the 4000-port probe

While calibrating the slow-start parent I ran a throwaway probe, outside the five scenarios and without asking, that published 4000 ports (`127.0.0.1:41000-44999`) from one container. Docker started one `docker-proxy` process per port: about 3,500 of them, with free memory down to about 680 MB of 7.8 GB. The container took about 10 minutes to start. The `down -v` of the probe's own project then took about 25 minutes, during which `docker inspect` hung. The daemon recovered without a restart; the probe's project, network and files are gone. The host's container, volume, network and image lists, minus `whalenurse-m0*`, matched M0's `host-after.txt` exactly afterwards (the one anonymous volume survived). **Lesson, now a standing rule: no probe outside the five scenarios, and nothing that can exhaust CPU, memory, disk or ports, without asking first.** The replacement is bounded: 240 ports (7.8–8.7s to start, 25s to stop), measured once with 60 ports (2.3s start) and 200 ports (6.1s start, 24s stop) before use.

### Image-store note

<a id="image-store-note"></a>Removing `daemon.json` does **not** switch a host back to the containerd store once it has run the classic store. `daemon/image_store_choice.go` returns `graphdriver-prior` when the data root already holds a graph-driver store, and the journal said `[graphdriver] using prior storage driver: overlay2`. Going back needs an explicit `{"features": {"containerd-snapshotter": true}}`, which is what the docs' "Enable containerd image store" section says. My first switch-back step (`sudo rm /etc/docker/daemon.json`) was wrong for that reason; the corrected step is `switch-back2.sh` in the scenario's state directory. `test/m0/m0b/classic-store/prepare.sh` now writes the correct step.

## 3. Results

Times are relative to the start of the step unless marked. Container ids are shortened.

### Scenario 1: Engine upgrade to 29.9.1 (and the suite as a baseline)

`upgrade/collect.sh` passed all checks: Engine and containerd are the latest packaged and upstream, compose and the store are unchanged, the daemon restarted after the step began, and no `whalenurse-m0` container existed.

`M0_RUN_ID=m0b-s1 test/m0/run.sh all` ran clean (host state unchanged outside `whalenurse-m0`). Compared with M0's run3 using `test/m0/m0b/compare-run.sh` (ids, timestamps and counters normalised): E2, E4, E5, E7, E8 and E9 are identical. E1 differs in the interface index, E3 in the order of parallel `kill` events, and E6 in the path text in the compose progress output. Every result line is the same. **No behaviour changed in 29.9.1 or containerd 2.4.2**, as M0 predicted.

### Scenario 2: daemon restart (`systemctl restart docker`, live-restore off)

Shutdown, as the recorder saw it (12 running containers; busybox as PID 1 ignores `SIGTERM`, and `stop_grace_period` was 2s):

```
+3.32s  kill[15] to all 12 containers, within 20ms of each other
+5.33s  kill[9] to all 12 (the 2s grace expired)
+5.5s   stop, die(137) for 9 of the 12
+23.3s  the recorder's stream closed
        a-parent, g-parent, p-parent: no stop and no die event at all
```

The journal explains the three missing `die` events: `Container failed to exit within 10s of kill - trying direct SIGKILL` for those three (parents whose network namespace others share, and the 240-port container), then `Force shutdown daemon` and `Daemon shutdown complete`. Their `FinishedAt` is set by the old daemon's last moments, not by an event.

Boot-time restore (new daemon; scrubbed events in `testdata/events/m0b-daemon-restart-boot.ndjson`):

| Container | Outcome | Notes |
|---|---|---|
| `a-parent`, `a-dep` | both running; same netns | dependent started 0.3s after the parent |
| `s-parent`, `s-dep` (slow app) | running, healthy about 25s later; same netns | the daemon does not wait for readiness |
| `p-parent`, `p-dep` (slow start) | running; same netns | **dependent started 8.9s after the parent's `StartedAt`**, 0.4s after the parent's `start` event; no failure |
| `m-always` | restarted (`start` event) | `always` restarts a manually stopped container |
| `m-unless` | stays `exited` | `unless-stopped` honours a manual stop across the restart |
| `n-parent` (`restart: "no"`) | stays `exited` (137) | not restored |
| `n-dep` | `exited` 137, `State.Error` `cannot join network namespace of a non running container: container <name> is exited` | **no wait, no retry**: the parent is not in the restore set |
| `f-parent` (bind source deleted) | `exited`, **exit 127**, `RestartCount` 0, `State.Error` `failed to create task for container: … runc create failed: … failed to fulfil mount request: open <path>: no such file or directory` | journal: `failed to start container` |
| `f-dep` | `exited` 137, `State.Error` `cannot join network namespace of a non running container` | parent failed, so the dependent failed |
| `g-parent`, `g-dep` (dependent's own bind source deleted) | parent running; dependent `exited` 127 with the mount error | **the parent is unaffected** (reverse case) |
| `z-created` | still `created` | `HasBeenStartedBefore` is false |

- **No retry.** Two snapshots 183s apart show no change in status, exit code, `RestartCount`, `StartedAt`, `FinishedAt`, network mode or `State.Error`.
- **Journal:** `failed to start container` once for each of `f-parent`, `f-dep`, `g-dep`, `n-dep` (4 lines), at `level=error`. `failed to prepare mount points` appears 0 times: the missing bind source surfaced in runc, not in the daemon's own mount preparation.
- **The API was not available during the restore.** `Loading containers: done` came 9s after the restore began, and the new events stream could connect only then. The recorder's reconnect with `--since` got the restore's `start` events from the new daemon's buffer (it holds 256 events; the healthcheck `exec_*` events fill it quickly).

### Scenario 3: host reboot (`systemctl reboot`, same stack)

The same outcomes for every container as in scenario 2, **0 unexpected**; no retry over 183s; `p-dep` started 7.2s after `p-parent`'s `StartedAt` and shares its netns.

What differs from scenario 2:

- **The observer is cut off almost immediately.** The recorder saw `kill[15]` for all 12 containers within 10ms of each other at +0.43s, and nothing after: no `kill[9]`, no `stop`, no `die`, no disconnect marker. The user manager stopped the recorder's unit about half a second later, before the daemon closed the stream. `testdata/events/m0b-reboot-shutdown.ndjson` holds exactly this.
- The journal has the rest: `Processing signal 'terminated'`, `Container failed to exit within 10s of kill` for `p-parent`, `Force shutdown daemon`, `Daemon shutdown complete`. At the next boot the daemon logs `error unmounting container … layer not mounted` for `p-parent` and sets its `FinishedAt` itself (after the boot, before its restore).
- **Slow restore.** The new daemon started 16:59:16 (local time), but its first restore was at 16:59:51 and the API answered only at 16:59:58 (`Loading containers: done`, `Daemon has completed initialization`). One failed start (`g-dep`) was logged 7s after the others.

### Scenario 4: journal evidence

`journalctl` was readable after the `usermod` (plain, after the reboot; `sg systemd-journal` before it). The journal is persistent (`/var/log/journal`, 5 boots).

| Line | E8 re-run (restart-policy restart fails) | Boot restore (daemon restart, reboot) |
|---|---|---|
| `restartmanger wait error` (the misspelling is real) | **1**, `level=error`, `restartmanger wait error: failed to create task for container: … runc create failed …` | 0 |
| `failed to start container` | 0 | **4 per run**, `level=error`, one per container that failed |
| `failed to restart container` | 0 | 0 (it is `debug` level; its absence is not a contradiction) |
| `failed to prepare mount points` | 0 | 0 |

E8d also logged `Failed to allocate port … port is already allocated` at `level=warning`, then `Handler for POST /containers/{name:.*}/start returned error`. Scrubbed excerpts: `testdata/journal/`.

### Scenario 5: classic image store

Switched with `{"features": {"containerd-snapshotter": false}, "storage-driver": "overlay2"}` (moby `daemon/image_store_choice.go` treats `false` as an explicit graph-driver choice; the docs only describe enabling the containerd store, and say the other store's images and containers become hidden but stay on disk). After the switch the five images were hidden (0 visible), and the base images were pulled again.

`test/m0/m0b/image-drift.sh` prints each candidate comparator next to the `com.docker.compose.image` label, before and after moving the tag to another image. A comparator is usable if it equals the label before and differs after:

| Comparator | containerd store | classic store |
|---|---|---|
| label `com.docker.compose.image` is | the platform manifest digest | the image ID |
| `<ref>` `.Id` | index digest: never equals the label | equals the label; differs after the re-tag |
| `<ref>` `.RepoDigests[0]` | index digest: never | repo digest: never |
| `<ref>` `.Descriptor.digest` | index digest: never | **empty** |
| `--platform linux/<arch>` `.Descriptor.digest` | equals the label; differs after | **empty** |
| **`--platform linux/<arch>` `.Id`** | **equals the label; differs after** | **equals the label; differs after** |
| container `.Image` == `<ref>` `.Id` (no label) | equal before; differs after | equal before; differs after |
| dry-run oracle | `Running`; after the re-tag `Recreate Recreated Starting Started` | the same |

`run.sh all` on the classic store (`M0_RUN_ID=m0b-s5`), compared with the containerd baseline: E2, E4, E7 and E9 identical; E1, E3 and E6 differ cosmetically as above; E5 differs where expected (the empty `.Descriptor.digest`, so M0's check would report drift for every container: its "naive check" line is hard-coded for containerd); E8d's error reads `Bind for :::38080 failed: port is already allocated` instead of `Bind for 0.0.0.0:38080 failed: …`. I did not test whether the store is the cause (see [verdicts](#4-verdicts)). Fixture: `testdata/inspect/m0b-classic-e8d-port-allocated.json`. Host state was unchanged outside `whalenurse-m0`.

## 4. Verdicts

### §3.3 Decision rules: **needs change**

Confirmed: events are only triggers; rule 1 (`State.Error` wins) holds for every boot-restore failure; "not retried" holds at runtime for boot-time restore as well as for the restart policy, with the journal lines the plan names; the stale-reference split (`No such container` → recreate; `cannot join network namespace of a non running container` → heal the parent first) matches what boot produced, and a failing dependent never affected its running parent.

Corrections and additions:

1. **A third failed-start shape, from boot-time restore.** Status `exited`, exit code **126/127/128 mapped from the error** (127 for the missing bind source), `RestartCount` unchanged, `StartedAt` of the previous run, `FinishedAt` of the shutdown, `State.Error` set. That is neither the failed first start (`created` + 126–128) nor the failed restart-policy restart (`exited` + the previous run's code). The exit-code table row "126 / 127 / 128 + `State.Error`, status `created`" should read "`created` or `exited`", and rule 1's two-shape list should become three. Rule 1 itself needs no change: it tests `State.Error` and never the exit code.
2. **Port-conflict matcher.** `Bind for <addr>:<port> failed: port is already allocated` appears as `Bind for 0.0.0.0:38080` and as `Bind for :::38080` (observed on the classic-store run). The matcher must accept both, and should accept `[::]:` too. Keep it in the one fixture-backed matcher; add `testdata/inspect/m0b-classic-e8d-port-allocated.json` next to `e8d-port-allocated.json`.
3. **Manual-stop markers** (§3.3, last section): the sentence "Docker's boot restart (a `start` event) clears those markers" is true, but it is not enough. See the next section.
4. **The Docker API is unavailable while the daemon restores containers** (9s after a daemon restart; about 42s after boot plus 7s of slow starts). The poll and the events reconnect must treat API errors during that time as "daemon not ready", not as an incident. `STARTUP_SETTLE` (2m default) covers the boot case here.
5. **`--since` replay is not a substitute for the sweep.** A reconnect with `since=<last event time>` returns the new daemon's buffered events (the restore's `start` events here), but the buffer holds only 256 events, so a busy host or a chatty healthcheck loses boot-time events. Events from the old daemon's shutdown are never available after the restart. The startup sweep is the backstop, as §3.3 already says.
6. **A container can go from running to restarted without `stop` or `die`.** Three of twelve containers did, because the daemon force-killed them at the end of a 15s shutdown. The exit code afterwards is whatever the restore wrote (137 here).
7. **Parents with `restart: "no"` stay down, and their dependents fail and stay down.** After a shutdown, `n-parent` is `exited` 137 with no `State.Error`. Under `heal-exited=crashed`, "137 without OOM: notify only", so the ladder would never restart the parent, and §3.5's "heal the parent first" cannot proceed for `n-dep`. M2 must decide this case explicitly. The shutdown-quarantine rule below gives a way to recognise "stopped by the host" and heal it.

### Standalone M2 item: shutdown kills

**Kills seen during a daemon shutdown or a host reboot must not set manual-stop markers, and a `start` that follows them must not clear SUSPENDED.**

Evidence:

- At a daemon restart, every running container received `kill[15]` within 20ms (12 of 12), and 2s later `kill[9]`. After a reboot, the recorder saw `kill[15]` for all 12 within 10ms, then lost its stream. `testdata/events/m0b-daemon-restart-shutdown.ndjson` and `m0b-reboot-shutdown.ndjson` are the sequences.
- §3.3: a marker is set on a `kill` outside any in-flight window. §3.2: a `start` that follows a manual-stop marker clears SUSPENDED. The restore then emits `start` for every container that comes back, so **every suspended container would be unsuspended and re-armed at each daemon restart**.
- The stream can end before any `stop` or `die`, so the usual "kill, then die" pairing cannot be used. After a reboot, whalenurse itself may be stopped in the same second, so a persisted marker may exist without any matching event in the next run.
- The containers that really were stopped manually (`m-unless`) stay down and keep their marker; that case works as designed.

What M2 should implement (proposed; this does not change §3 until approved):

1. **Quarantine on stream loss.** When the events stream ends (EOF or error) for a reason other than whalenurse shutting down, discard every manual-stop marker created in the 30s before the loss (the daemon's own shutdown limit is 15s, plus the kill timeouts). This is the reliable signal for a daemon restart.
2. **Recognise the burst.** `kill` with signal 15 for two or more containers within one second, outside any in-flight window, is a shutdown, not a person. Tag those markers `shutdown`. A `shutdown` marker never counts as "someone stopped it", and a `start` that follows one never clears SUSPENDED.
3. **Persist the boot id.** Store `/proc/sys/kernel/random/boot_id` with the markers. On startup, if it differs from the stored one, drop all markers older than the boot and treat the first `STARTUP_SETTLE` as a restore window in which `start` events do not clear SUSPENDED.
4. **A restore window after reconnecting** to a daemon that was down (the API answers only after `Loading containers: done`): `start` events in it are restores, not manual starts.
5. **Test it:** extend the §6 unit test "daemon-shutdown kill markers are cleared on start" with the two fixtures above, and a SUSPENDED container that is killed by a shutdown and restored must stay SUSPENDED.

The unit test in §6 names only half of the problem ("markers are cleared on start"). The other half, SUSPENDED staying SUSPENDED, is what this item adds.

### §3.5 Dependents: **confirmed**

- **Boot order works.** After both a daemon restart and a reboot, restored dependents of a restored parent came up after it with the same network namespace (`a`, `s`, `p` pairs). No stale dependent existed after a restore, so the staleness sweep found nothing to fix at boot in these runs. The sweep stays as the backstop for what the stack did not exercise (a dependent restored before its parent was not reproduced).
- **A slow parent does not break its dependent here.** `p-parent` took 7.8–8.7s to start, longer than the daemon's 5s `container:` wait, and `p-dep` still came up 7.2–8.9s after the parent's `StartedAt`, 0.4s after its `start` event, with the same netns. The source describes a 5s timer shared across one container's dependencies (`restore()`); with one `container:` parent there is only one dependency, so "shared" never mattered, and I could not tell from the evidence whether the timer or a lock held the dependent back. The plan's worry (a dependent failing because the parent is not ready) did not occur with a slow start. **Slow app readiness is not a boot-order concern at all:** `s-dep` started 0.3s after `s-parent`, about 25s before it became healthy.
- **A parent that is not restored takes its dependents down.** `restart: "no"` parent, or a parent whose start fails: the dependent is attempted at once, fails with `cannot join network namespace of a non running container`, and is not retried. This is the existing "heal the parent first, then restart the dependent" path; see §3.3 item 7 for the case where the parent's own rule says "notify only".
- **The reverse case holds:** a dependent that fails by itself (`g-dep`) did nothing to its running parent.
- No change to the §3.5 rules. One wording point for the docs: "the single shared 5s timer" applies to a container with several dependencies; for the usual one `container:` parent it is just a 5s cap.

### §3.6 Drift guard: **needs change**

1. **Config check and the dry-run oracle: confirmed** on both stores and after the Engine upgrade. E4 and E5's oracle texts are identical; E4's skip rule for the four reference types still holds.
2. **Image drift: the comparison as corrected in v3.1 is wrong on the classic store.** `--platform linux/<arch>` `.Descriptor.digest` is empty there, so comparing it with the label reports drift for every container, and the drift guard would SUSPEND all of them. `.Descriptor.digest` must not be used, on either store.
   **Proposed fix** (replaces the "containerd store" and "classic store" bullets): compare the container's `com.docker.compose.image` label with
   ```
   docker image inspect --platform linux/<arch> --format '{{.Id}}' <ref>
   ```
   where `<arch>` is the server's (`docker version --format '{{.Server.Arch}}'`). It equals the label before a re-tag and differs after on both stores (the platform manifest digest on containerd, the image ID on classic). Never compare the label with plain `<ref>` `.Id`, with `.RepoDigests` or with the container's `.Image`: on containerd they are index digests and never match; on classic plain `.Id` happens to work, which is the trap. If the inspect fails (the ref is no longer present locally, or has no such platform), that is itself drift or a failure: skip the recreate and say why.
   The dry-run oracle already covers image drift for the services it checks; this explicit comparison is for the services the oracle skips and for the hash fallback, as the plan says.
3. **Test it on both stores.** `test/m0/m0b/image-drift.sh` is standalone, prints every comparator, and exits 0 only if some comparator is usable and the oracle behaves. The CI job text is in `test/m0/m0b/ci-image-drift.job.yml` (`classic` and `containerd` matrix); the token that pushed this branch cannot push workflow files, so adding it to `ci.yml` is a [maintainer] item. `testdata/` has no image-drift fixture because the check is its own integration test.
4. **Compose version skew: still not exercised.** Only compose v5.6.0 was installed; the CI matrix covers the other versions.

### Other changes M2 must handle differently

| # | Item | Where |
|---|---|---|
| 1 | Shutdown kills: no markers, no SUSPENDED clearing | [above](#standalone-m2-item-shutdown-kills) |
| 2 | Third failed-start shape (exited + 126/127/128 + `State.Error`) | §3.3 item 1 |
| 3 | Image-drift comparator: `--platform … .Id`, never `.Descriptor.digest` | §3.6 item 2 |
| 4 | Port-conflict matcher: `0.0.0.0:` and `:::` | §3.3 item 2 |
| 5 | API unavailable while the daemon restores; reconnect/poll must not open incidents | §3.3 item 4 |
| 6 | `--since` replay is limited to 256 events and misses the old daemon's shutdown | §3.3 item 5 |
| 7 | A `restart: "no"` parent stopped by the host and its dependents | §3.3 item 7 |
| 8 | A running container can restart without `stop`/`die` | §3.3 item 6 |

## 5. Answers to the plan's open questions (§9)

1. *Why do the maintainer's containers sit at `Exited (N)`?* Still **open** for the real host; M0b adds that boot-time restore produces the same state (`exited` + `State.Error`, not retried), with exit 127 rather than the previous code, and the journal lines exist.
2. *Question 6 (defaults).* New data: a daemon shutdown with live-restore off took 15–20s when some containers resisted; restore adds the slowest start. `STARTUP_SETTLE` 2m covers a reboot here (restore began about 41s after boot and ended about 48s after it). A `GRACE` of 30s is comfortably longer than any state transition seen at boot.

## 6. Not verified / maintainer items

**[maintainer]**
- **The VM is not back on the containerd store yet.** `rm /etc/docker/daemon.json` left it on `overlay2` (the daemon found a prior store). Run `captures/m0b/classic-store/20261017T150838Z/switch-back2.sh` (it writes `{"features": {"containerd-snapshotter": true}}`, restarts the daemon and prints `docker info` and `docker images`). The VM now has a `daemon.json` that it did not have at the start of M0b.
- Add `test/m0/m0b/ci-image-drift.job.yml` to `.github/workflows/ci.yml` (and `test/m0/m0b/selftest.sh` to the `scripts` job): the token cannot push workflow files.
- Review this PR. From M0: question 1 still needs `scan.sh`/`capture.sh` on the real host.

**Not verified**
- **whalenurse itself** was not run (no app code): the recorder stands in for it. Whether whalenurse in a container would see the same sequence at shutdown (it is one of the containers being killed), or what it does when systemd stops it first in systemd mode, is for M2's tests, using the fixtures above.
- A dependent restored **before** its parent (the real boot race) was not produced: `docker start` ordering in `restore()` made the parent first every time. The staleness sweep is untested against it.
- Multiple dependencies (links plus `container:`) and the shared timer were not tested.
- Compose versions other than v5.6.0; both image stores on other Engine versions; a host with live-restore enabled.
- Whether the `:::` form of the port error depends on the store: observed once.
- Scenario 3 was one reboot; the 41s from boot to the first restore may vary.

## 7. Fixtures

Scrubbed with `scripts/scrub-fixture` and checked with `--check`; no raw capture is committed.

- `testdata/inspect/` (5): `m0b-daemon-restart-f-parent`, `-f-dep`, `-g-dep`, `-n-dep` (failed boot-time starts: `exited` + 127, and the stale-reference dependents), `m0b-classic-e8d-port-allocated` (`:::` form).
- `testdata/events/` (4): `m0b-daemon-restart-shutdown`, `m0b-daemon-restart-boot`, `m0b-reboot-shutdown`, `m0b-reboot-boot`.
- `testdata/journal/` (3, new): `m0b-e8-restartmanger-wait-error.txt`, `m0b-reboot-failed-to-start-container.txt`, `m0b-reboot-shutdown-and-restore.txt`.

## 8. Reproducing

```
test/m0/m0b/selftest.sh && test/m0/run.sh selftest && scripts/scrub-fixture --self-test      # no Docker needed
test/m0/m0b/<scenario>/prepare.sh      # upgrade | daemon-restart | reboot | journal | classic-store
bash <printed state dir>/step.sh        # the maintainer step (sudo)
test/m0/m0b/<scenario>/collect.sh [--watch 180]    # any session; repeatable
test/m0/m0b/image-drift.sh              # on whichever store is active; also for CI
test/m0/m0b/teardown.sh                 # down -v for whalenurse-m0-b-* and the listener units
test/m0/m0b/compare-run.sh captures/m0-<baseline> captures/m0-<new>
```
