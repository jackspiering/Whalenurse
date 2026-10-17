# M0 findings

Milestone M0 of [PLAN.md](PLAN.md): diagnose and verify, no app code. Run on 17 Oct 2026 on a throwaway Debian VM (snapshotted before the session) with the maintainer's permission to run `test/m0/run.sh` there. Raw output lives in `captures/` (gitignored); only scrubbed excerpts and fixtures are committed.

**Summary.** The design holds. §3.5 is confirmed. §3.3 and §3.6 each need small corrections, listed in [Verdicts](#verdicts). Nothing contradicts a rule badly enough to stop work, but three items change what M1/M2 must implement:

1. A failed *restart-policy* restart emits **no event at all** and **keeps the previous exit code** (1, not 126–128); only `State.Error` marks it (§3.3).
2. A `network_mode` dependent whose parent was *recreated* must be **recreated, never API-restarted**: the restart fails and turns a running-but-disconnected container into an exited one (§3.5).
3. `compose --dry-run --progress json up …` is a stable recreate oracle, and the image-drift comparison in §3.6 as written is wrong on the containerd store (§3.6).

## 1. Environment and upstream check

Recorded before any experiment (`captures/m0-run*/PREP/versions.txt`). "Latest" was read from GitHub releases on 17 Oct 2026.

| Component | Installed | Latest upstream | Note |
|---|---|---|---|
| Docker Engine | 29.9.0 (API 1.56, min 1.40) | 29.9.1 (14 Oct) | 29.9.1 bumps containerd to 2.4.2 (security fixes in image pull/import) and has no change that touches any experiment here. Not upgraded: the package upgrade restarts the daemon, which was ruled out. |
| containerd | v2.4.1 | v2.4.2 | as above |
| runc | 1.5.2 | 1.5.2 | |
| Docker Compose | v5.6.0 | v5.6.0 | **CI pin for the "M0 version"**; plus latest release |
| buildx | v0.38.0 | v0.38.0 | not used |
| moby client | — | `client/v0.6.2` (8 Oct) | matches the plan; nothing imported in M0 |
| Go | — | 1.27.2 | M1 uses this |
| Image store | **containerd** (`driver-type: io.containerd.snapshotter.v1`) | | The classic store was **not** tested: switching it means editing `daemon.json` and restarting the daemon. See [maintainer items](#6-not-verified--maintainer-items). |
| Host | Debian 13, kernel 6.12, cgroup v2 | | |

Differences from the plan:

- **Compose #13878 is closed, not open.** It was fixed on 29 Jun 2026 by compose PR #13880 ("hash resolved service refs to match executor"). v5.6.0 does not recreate unchanged `network_mode: service:` dependents (E2a). The plan's §3.5 warning still applies to older compose versions, so the CI matrix should keep one pre-fix version (anything v5.2.0 up to a release before 29 Jun) if recreate-on-every-`up` should be tested.
- The plan's compose release notes are right: v5.5.0 "overhauls image digest reconciliation" (PR #14011/#14041) and v5.6.0 "pins the service config-hash byte layout" (PR #14215).
- The compose plugin is a **static** Go binary (`file`: statically linked; `ldd`: not a dynamic executable), at `/usr/libexec/docker/cli-plugins/docker-compose`, as in the plan.

## 2. Method and safety

- `test/m0/run.sh` only acts through wrappers in `test/m0/lib.sh`, each refusing unless the compose project matches `^whalenurse-m0-[a-z0-9][a-z0-9_-]*$`: `dc` (always `-p`, refuses a caller-supplied `-p`/`--project-name`, and `down`), `m0_down` (the only `down -v`), `dk_ctr` (stop/start/restart/kill/rm/exec, looks up the container's project label first), `m0_tag`/`m0_rmi` (`whalenurse-m0/*` image tags only) and `m0_pull` (base images only).
- `run.sh selftest` checks the guards against a stubbed `docker` (no Docker needed): 14 bad project names ×3 entry points, `-p`/`down` smuggling, containers from other projects, unlabeled and unknown containers, tags outside `whalenurse-m0/`. It also fails when the regex is loosened (checked by mutating it). During the run, a call-order bug of mine (`dk_ctr rm -f <id>`) was refused with `REFUSED` and stopped the run, which is the intended fail-closed behavior.
- `scripts/m0/scan.sh` and `capture.sh` call Docker only through `ro_docker`, which allowlists `ps`, `inspect`, `events`, `version`, `info` and `compose version`. `scripts/m0/selftest.sh` proves this against a fake `docker` on `PATH` that logs argv. Both scripts were also run once against the live daemon (a stopped container with `restart: no` was correctly not listed; the capture directory is mode 0700).
- Base images pulled once into the VM: `busybox:1.37`, `alpine:3.22`, `gcr.io/distroless/static:{latest,nonroot}`. They are the only pulls; nothing else was pulled, pruned or removed. `whalenurse-m0/busybox:1` is a local tag of `busybox:1.37` and is still present.
- **Host state before = after.** The container, volume, network and image lists, minus anything named `whalenurse-m0`, are identical before and after a full `run.sh all` (`host-before.txt` vs `host-after.txt`). The one pre-existing anonymous volume `3f907146…` survived. No `whalenurse-m0-*` project remains.
- No Docker daemon restart, no sudo (it needs a password here), no edit to the host's Docker configuration.

Result files for the run used below: `captures/m0-run3/<E?>/summary.txt` (evidence), `…/poll.tsv` (E8), `…/events-*.ndjson` (raw events).

## 3. Results

Container IDs are shortened. Host paths are shown as `<host path>`.

### E1: are `network_mode: service:` dependents broken after the parent is recreated?

**Yes, silently.** Compose resolves `service:parent` to `container:<full parent id>` at create time (`HostConfig.NetworkMode`). After `up -d --force-recreate --no-deps parent`:

```
baseline  netmode(dep)=container:48d0a0133f45   netns parent=net:[4026532428] dep=net:[4026532428]
recreate  parent 48d0a0133f45 -> 271b934b370b
          dep status=running exit=0  netmode(dep)=container:48d0a0133f45 (the OLD id)
          netns new parent=net:[4026532491]  dep=net:[4026532428]   <- different
          dep interfaces: lo           new parent: lo,eth0
          wget 127.0.0.1:8080 from dep: FAILS
          StartedAt parent=…02:51:10Z  dep=…02:51:06Z               <- parent newer
events    create kill[15] kill[9] stop die(137) destroy rename start health_status:healthy
```

The dependent keeps **running** with only a loopback interface. No state-based rule notices it, so the staleness sweep (§3.5) is the only detector.

**Rung 1 on the stale dependent makes it worse.** `docker restart dep` after the parent's recreate fails and leaves the dependent **exited**:

```
Error response from daemon: Cannot restart container f6af7a5fe578…: joining network namespace of container: No such container: 48d0a0133f45…
dep status=exited exit=137  State.Error="joining network namespace of container: No such container: 48d0a0133f45…"
```

**Parent restart with the same ID (part B):** the dependent also keeps the old namespace (`parent net:[…491]`, `dep net:[…428]`, wget fails). `docker restart dep` repairs it (both `net:[…491]`, wget works). So §3.5's "parent restart → restart dependents via the API" is right.

### E2: does a non-forced `up -d --no-deps --pull never --no-build <dep>` recreate dependents?

| Case | Dependent recreated? |
|---|---|
| (a) nothing changed (the #13878 baseline), `up --no-deps dep` and plain `up -d` | **No** (ids unchanged; compose v5.6.0) |
| (b) parent recreated, dependent running with a stale namespace | **Yes**; new NetworkMode `container:<new parent>`, namespaces match, wget works |
| (c) parent only *restarted* (same ID) | **No**; the dependent stays stale, so rung 1 (API restart) is required |
| (d) parent recreated, dependent already failed by a rung-1 attempt (exited, `State.Error`) | **Yes**; recreated and running, namespace matches |

**Answer to open question 5: yes.** The non-forced path from §3.5 works for a recreated parent, including for an already-exited dependent. `--force-recreate` is not needed; keep it only as a fallback if a post-check still finds a stale `NetworkMode`.

### E3: does `--no-deps` suppress `depends_on: {restart: true}`?

**Yes**, with positive controls. A parent with a plain dependent (`depends_on … restart: true`) and a `network_mode` dependent (also `restart: true`):

| Step | Parent recreated | plain restarted | netdep |
|---|---|---|---|
| (i) `up --force-recreate --no-deps parent` | yes | **no** | untouched, stale |
| (ii) config change, `up --no-deps parent` | yes | **no** | untouched, stale |
| (iii) *control:* config change, `up -d` (all in scope) | yes | **yes** | **recreated**, netns matches |
| (iv) *control:* config change, `up -d parent plain` | yes | **yes** | untouched, stale |

So whalenurse's explicit dependents step is needed after `--no-deps`, and there is no double restart. Note from (iii): when dependents are in scope, compose restarts or recreates them *before* the parent is recreated.

### E4: `config --hash` vs the `com.docker.compose.config-hash` label

The invocation was built from the container's own labels (`project.working_dir`, `config_files`, `environment_file`, `service`) and run from `/`:

```
service  label            config --hash    match
parent   427fb0d10b7c52fc 427fb0d10b7c52fc MATCH
plain    0e21c523cf95a159 0e21c523cf95a159 MATCH
netdep   5d94ca07c6ddcf72 09f221854d407b2a MISMATCH    (network_mode: service:)
ipcdep   9e29ba3a5e9bb654 4642cf5cae6ee4c2 MISMATCH    (ipc: service:)
piddep   f305178ca83e4c87 76f13dd867cfd6a3 MISMATCH    (pid: service:)
vfdep    6604b84f6f896e71 7d531420e6936082 MISMATCH    (volumes_from)
```

- The same table after the parent is recreated, and again after the dependents are recreated through compose (fresh labels), is unchanged for `parent`/`plain` (MATCH) and **still MISMATCH for all four reference holders**, so the skip rule in §3.6 is confirmed for all four reference types (`network_mode`, `ipc`, `pid`, `volumes_from`).
- *Positive control:* editing `plain`'s environment changes its `--hash` (`0e21c523…` → `1e0c1bc5…`) while the label stays, so a real config drift is a mismatch.
- Syntax: `--hash='*'` prints one `<service> <hash>` line per service, sorted by name; `--hash=parent,plain` prints two lines; `--hash='parent, plain'` fails with `no such service:  plain`, so spaces are not trimmed.

### E5: `--dry-run` as the recreate oracle

`docker compose … --dry-run --progress json up -d --no-deps --pull never --no-build <svc>` prints one JSON object per line: `{"id":"Container <name>","status":"Working|Done","text":"<action>"}`.

| Case | `text` values for the service's container |
|---|---|
| A. unchanged | `Running` only |
| B. config changed (`P=2`) | `Recreate`, `Recreated`, `Starting`, `Started` |
| C. dependent after its parent was recreated | `Recreate` … (same as B) |
| C2. parent itself, after its own recreate | `Running` only |
| D. image tag moved (`whalenurse-m0/e5img:latest` busybox → alpine) | `Recreate` … (same as B) |
| F. container stopped, config unchanged | `Starting`, `Started` (no `Recreate`) |

- **No side effects:** container IDs were identical before and after every dry run (all cases).
- The `plain` progress (`--progress plain`) carries the same words as text lines, with a trailing space; JSON is the one to parse. The temporary `<id>_<name>` in the `Starting` line is compose's rename scheme.
- **Answer to open question 4: adopt it**, as the check for non-reference services (and for parents), with the exact rule "a `Recreate` text for the service's container ⇒ drift". It covers config *and* image drift with compose's own decision. It does not say *why*, and for a dependent after a parent recreate `Recreate` is expected rather than drift, so keep the §3.6 skip for reference holders.
- The json/plain output format is a compose implementation detail. Pin it with the CI compose matrix and fall back to the hash check if the output cannot be parsed.

*Image drift (§3.6 check 2), containerd store.* `com.docker.compose.image` is the **platform manifest digest**, not the image ID:

```
container label com.docker.compose.image = sha256:66a6306db78bf2dbf34…   (linux/amd64 manifest)
container .Image                          = sha256:bdf57e528e45e443382…   (index digest = image Id)
ref .Id                                   = sha256:bdf57e528e45e443382…   before the retag
ref --platform linux/amd64 .Descriptor.digest = sha256:66a6306db78bf2dbf34…   before the retag  == label
after retag to alpine:  .Id = sha256:5291449c3df73caf6ed…   --platform .Descriptor.digest = sha256:3e9b4b680bfc9fb5269…
label == ref .Id                           : never equal, before or after the retag
label == ref --platform .Descriptor.digest : equal before, DRIFT after the retag
```

So the plan's "compare the label with what the reference resolves to" is only correct with `docker image inspect --platform <linux/arch>` and `.Descriptor.digest` on the containerd store. Comparing with `.Id`, or with the container's own `.Image`, would report drift on every container. The classic store was not tested.

### E6: recreate from a helper container with the stacks directory `:ro`

**Works** with the host's compose plugin mounted `:ro` at `/usr/local/bin/docker-compose`, the socket, and the stacks root mounted `:ro` at the **identical path**:

```
roprobe   touch <stack dir>/probe -> "Read-only file system"   write-rc=1   (the mount is really read-only)
healer    docker-compose -p … --project-directory <stack dir> -f <stack dir>/compose.yaml --env-file <stack dir>/vars.env
              up -d --force-recreate --no-deps --pull never --no-build svc      rc=0
          svc recreated: yes, running; relative bind ./data works; env_file and the interpolated variable applied;
          working_dir label == the host path; `config --hash` from the host == the new label (MATCH)
```

*Negative control* (same directory mounted at `/stacks`, `--project-directory /stacks/e6`): rc=1, nothing was changed:
`Error response from daemon: invalid mount config for type "bind": bind source path does not exist: /stacks/e6/data`. The existing container stayed up. This is the reason for the identical-path rule.

### E7: does the host compose plugin run in the candidate base images?

**Yes.** The plugin is statically linked, and `docker-compose version` and `docker-compose -p … ps -a` (a real API call) both work in:

| Image | User | Result |
|---|---|---|
| `alpine:3.22` | root | works |
| `gcr.io/distroless/static:latest` (no shell) | root | works |
| `gcr.io/distroless/static:nonroot` | 65532 + `group_add: [<docker gid>]` | works |
| `gcr.io/distroless/static:nonroot` | 65532, no group | `permission denied while trying to connect to the docker API at unix:///var/run/docker.sock` (rc=1) |

**Answer to open question 3:** yes, `distroless/static` is viable; a non-root user needs the socket's group.

### E8: hypothesis A (failed restart, no retry)

**Confirmed.** `restart: unless-stopped`, a bind source (`create_host_path: false`) removed 1s after the first start, process exits 1 after 4s. Polled every 5s for 150s:

```
t    status   exit  restarts  State.Error
0    running  0     0
5    exited   1     1         failed to create task for container: failed to create shim task: OCI runtime create failed …
…    exited   1     1         (identical at 35s, 110s and 140s)
docker ps -a: Exited (1) 2 minutes ago        events: create start die(1)   <- nothing after the die
full State.Error: …runc create failed: unable to start container process: error during container init:
                  failed to fulfil mount request: open <host path>/src: no such file or directory
control (same policy, no bind mount): restarts 1,2,…,10; status running → restarting (backoff) → running
```

What differs from the plan's expectation (§3.3 rule 1; E8's "Expect `Exited (128?)`"):

- **The exit code stays at the previous run's code (1), not 126/127/128.** This matches moby `daemon/monitor.go` at docker-v29.9.1: when the restart's `containerStart` fails it calls `State.SetStopped(&ctrExitStatus)` with the *previous* exit status, logs `failed to restart container` at debug and `restartmanger wait error` at error level, and never retries.
- **No event is emitted for the failed restart** (no `die`, no `start`). The only trigger is the `die(1)` of the earlier run, then GRACE, then inspect. A later failed retry (the container can be `restarting` with a growing backoff when GRACE expires, as the control shows) is found only by the poll.
- The exit-code mapping does hold for a failed **first** start, E8c below.

**E8c, failed first start** (`restart: unless-stopped`; `compose up` returns rc=1; checked again after 15s, `RestartCount` stays 0, no retry):

| Service | Status | Exit | `State.Error` ends with |
|---|---|---|---|
| `command: /nonexistent` | `created` | **127** | `failed to create task for container: … runc create failed: unable to start container process: error during container init: exec: "/nonexistent": stat /nonexistent: no such file or directory` |
| `entrypoint: /etc/passwd` | `created` | **126** | `… error during container init: exec: "/etc/passwd": permission denied` |
| `command: /etc/passwd/x` | `created` | **127** | `… error during container init: exec: "/etc/passwd/x": stat /etc/passwd/x: not a directory: Are you trying to mount a directory onto a file (or vice-versa)? …` (the suffix is Docker's generic hint and is misleading here) |

**E8d, port already allocated:** `created`, exit **128**, `State.Error`: `failed to set up container networking: driver failed programming external connectivity on endpoint <name> (<id>): Bind for 0.0.0.0:38080 failed: port is already allocated`.

**E8e, stale network references** (dependent started via the API):

| Parent | Result | Dependent after |
|---|---|---|
| stopped, not removed | `cannot join network namespace of a non running container: container <name> is exited` | exited, 137, same text in `State.Error` |
| removed or recreated | `joining network namespace of container: No such container: <old id>` | exited, 137, same text in `State.Error` |

Journal evidence for E8 could not be collected (no journal access, see section 6). The source and the `State.Error` fields are enough to answer the question.

### E9: event sequences

All on Engine 29.9.0 with the containerd store. Sequences from `docker events` (container events, `exec_*` noise removed); scrubbed copies are in `testdata/events/`:

| Scenario | Sequence |
|---|---|
| `docker stop` (process traps TERM, exit 143) | `kill[15] stop die(143)` |
| `docker stop -t 2`, TERM ignored | `kill[15] kill[9] stop die(137)` |
| `docker kill` | `kill[9] die(137)` |
| `docker start` of a stopped container | `start` |
| `docker restart` | `kill[15] stop die(143) start restart` |
| compose `up --force-recreate --no-deps` | `create kill[15] stop die(143) destroy rename start` |
| crash (`exit 1`, `restart: no`) | `create start die(1)`, no `kill` |
| OOM (`mem_limit: 16m`) | `create start oom die(137)`, `OOMKilled=true`, no `kill` |
| restart policy (`always`, exits 1 each second) | `create start die(1) start die(1) start die(1) …`, no `kill`, **no `restart` event** |
| healthcheck going bad | one `health_status: unhealthy` while `FailingStreak` reached 6 (edge-triggered: confirmed) |

Not run: the daemon-restart / host-reboot scenario (maintainer item). From moby `daemon/daemon.go` at docker-v29.9.1 (source-checked, **not** run): boot-time restore logs `failed to start container` and does not retry; before starting a container with `network_mode: container:X`, the daemon waits for X's restart notification using **one** `time.NewTimer(5 * time.Second)` shared across all the container's dependencies, and only when X is itself being restarted.

## 4. Verdicts

### §3.3 Decision rules: **needs change**

The design stands: events are only triggers, rule 1 wins, and `heal-exited` reasoning needs no change. Corrections to the text and rules:

1. **Event order.** The plan says `docker stop` emits `kill → die → stop`. On Engine 29.9.0 it is `kill → stop → die` (E9). The conclusion "order is unreliable, so inspect after GRACE" is unchanged; fix the example, and phrase it as "the order differs between operations and Engine versions".
2. **Rule 1 and exit codes.** Replace "A failed start sets `State.Error` and the exit code 126/127/128" with the observed split:
   - *failed first start* (`docker start`, `compose up`): status `created`, exit 126/127/128, `State.Error` set (E8c, E8d);
   - *failed restart by the restart policy*: status `exited`, **exit code of the previous run**, `State.Error` set, `RestartCount` incremented (E8).
   Rule 1 must test `State.Error != ""` on both `created` and `exited`, and must not use the exit code. Update the exit-code reading guide: "126/127/128 + `State.Error`" is first-start only, and the dashboard should show `State.Error` itself.
3. **Detection of a failed restart.** There is no event for it. Say so in rule 1, and make the poll responsible for it. Each poll should also re-inspect containers that are still `restarting` after GRACE, because a retry that fails later is silent.
4. **Stale-reference strings.** Add `cannot join network namespace of a non running container` (parent stopped, not removed). It needs different handling from `No such container` (parent recreated): recreating the dependent cannot help until the parent runs, so the ladder should heal the parent first (§3.5 already suppresses dependents while the parent has an incident) and then restart the dependent. Keep all the strings in the one fixture-backed matcher (`testdata/inspect/e1-dep-stale-restart-failed.json`, `e8e-*`, `e8c-*`, `e8d-*`, `e8-stuck-failed-restart.json`).
5. **"Other" cause strings seen** (for the same matcher): `failed to fulfil mount request: open <path>: no such file or directory` (bind source gone at restart), `invalid mount config for type "bind": bind source path does not exist: <path>` (at create, `create_host_path: false`), `Bind for <addr> failed: port is already allocated`, `exec: "<cmd>": stat <cmd>: no such file or directory`, `exec: "<cmd>": permission denied`, `stat <cmd>: not a directory`. The scrubbed fixtures keep `0.0.0.0` and `127.0.0.1` verbatim (the scrubber leaves them alone so strings like `Bind for 0.0.0.0:38080 failed` still match); every other IP becomes `192.0.2.99`.
6. **Hypothesis A is confirmed** on Engine 29.9.0 by reproduction and by the moby source (the "not retried" claims for both the restart path and the boot-restore path). Whether the maintainer's *real* stuck containers are this case is still open (question 1): it needs `scan.sh`/`capture.sh` on the real host.
7. **Scrubber (§3.3 last bullets).** `scripts/scrub-fixture` implements the three listed rules and adds, because a raw capture can hold secrets: path-valued compose labels (`working_dir`, `config_files`, `environment_file`) are rewritten to `/srv/stacks/<project>[/<file>]`; `Config.Cmd`, `Entrypoint`, `Args` and healthcheck test arguments and health-log output become `<scrubbed>`; there is an events mode (whitelisted attributes, other attributes dropped, times rebased to 0) and a `--check` leak check. Container IDs are kept so `container:<id>` references still match. Please extend the §3.3 wording to say so. Other label *keys* are kept (only the values are replaced), as written in §3.3.

### §3.5 Dependents: **confirmed**

E1, E2 and E3 confirm every rule, with these clarifications to record (no design change):

- **Mapping of the staleness conditions to the remedy.** Condition 1 (`NetworkMode` targets an ID that is not the parent's current ID, or no longer exists) means the parent was recreated: **recreate** the dependent. Condition 2 (same ID, parent's `StartedAt` newer) means the parent was restarted: **restart** the dependent via the API. Never API-restart a dependent of type 1: the restart fails and turns a running-but-disconnected container into an exited one (E1 A2).
- **Preferred recreate path:** the non-forced `up -d --no-deps --pull never --no-build <dep…>` works, including for an already-exited dependent (E2b, E2d). It does not repair a dependent after a mere parent restart (E2c).
- **`--no-deps` does suppress `depends_on.restart`** (E3), so the explicit dependents step is required.
- **A stale dependent stays `running`** with only `lo`, so only the staleness sweep (or the parent's `start`/`create` event) finds it.
- **#13878 is closed** (fixed in compose PR #13880; absent in v5.6.0, E2a). External `create` events from the maintainer's own `up` are still possible on older compose versions.
- Boot race: the single shared 5s timer is **source-checked, not run** (needs a daemon restart).

### §3.6 Drift guard: **needs change**

1. **Config check: confirmed**, including the skip rule for all four reference types (E4: `network_mode`, `ipc`, `pid` and `volumes_from` holders never match, even when freshly created). Syntax notes in E4 hold (sorted `<service> <hash>` lines, commas are not trimmed).
2. **Replace or supplement the check with the dry-run oracle (open question 4).** Proposal: for non-reference services and parents, run `<compose> --dry-run --progress json up -d --no-deps --pull never --no-build <svc>` and treat a `Recreate` text for the service's container as drift. It covers config and image drift together and is side-effect free (E5). Keep `config --hash` as the fallback when the output is not parseable, and for the reference holders keep the skip.
3. **Image drift: the comparison as written is wrong on the containerd store.** The label `com.docker.compose.image` is the platform manifest digest, whereas `docker image inspect <ref>` `.Id` and the container's `.Image` are the index digest. A naive comparison reports drift for every container and would SUSPEND everything. On this store, compare the label with `docker image inspect --platform linux/<arch> <ref>` `.Descriptor.digest` (E5), or avoid the comparison by using the dry-run oracle. The classic store is untested (CI / maintainer item), so the integration test must run on both stores before the rule is final.
4. **Compose version skew:** the `com.docker.compose.version` label is present (`5.6.0` in the fixtures). Skew behavior was not exercised: only one compose version is installed here, so the version matrix belongs in CI.

## 5. Answers to the plan's open questions (§9)

1. *Why do the maintainer's containers sit at `Exited (N)`?* Hypothesis A is reproducible (E8) and matches the source. Whether it is the cause on the real host is **open** until `scan.sh`/`capture.sh` run there. Note the exit code seen in `docker ps` will be the previous run's code, not 128.
2. *`:ro` stack mounts?* Work, with the stacks root at the identical path (E6).
3. *Does the host compose plugin run in the image?* Yes, in `alpine` and `distroless/static` (E7); a non-root user needs the socket group.
4. *Dry-run instead of the hash check?* Yes for non-reference services and parents (E5), with the fallback and caveats above.
5. *Does compose recreate dependents on a non-forced `up`?* Yes after a parent recreate; no after a parent restart (E2).
6. *Defaults.* Data for the later tuning: a restart-policy retry can lag a long time (the control's `RestartCount` was 10 after 110s with `restarting` periods), so with `GRACE=30s` a container can still be `restarting` when it is inspected; a failed retry later is silent. The 60s poll is the right backstop, but see §3.3 correction 3.

## 6. Not verified / maintainer items

**[maintainer]**
- **Daemon restart / host reboot** (E9's last scenario, boot-time restore, the shared 5s `container:` wait, shutdown `kill` events): not run by instruction. Only source-checked.
- **Journal evidence for E8**: `journalctl -u docker` was not readable here (no passwordless sudo; the user is not in `adm`/`systemd-journal`); `capture.sh` writes a note (including journalctl's own error) instead. The journal path was exercised only against a fake `journalctl`, plus a check that the `--since` values it generates (`-30m`, `-2h`, `-90s`, `-1d`) are accepted by the real `journalctl`; reading real daemon logs is unverified. On the real host, run `scripts/m0/capture.sh <container>` with journal access to confirm `failed to start container`/`restartmanger wait error`.
- **Real host:** run `scripts/m0/scan.sh` and `capture.sh` when a stuck container appears (question 1); scrub with `scripts/scrub-fixture` before committing anything.
- **Classic image store**: needs `daemon.json` and a daemon restart. Run E5's image-drift part (and ideally all of `run.sh`) there, or cover it in the CI matrix.
- **Compose version matrix:** only v5.6.0 was available; choose the older version for the matrix (anything before the 29 Jun 2026 fix of #13878).
- Engine 29.9.1 / containerd 2.4.2 upgrade on the VM, if the CI image should be tested against the exact latest.
- From H: the rename, About text and topics are done (checked with `gh repo view`). Local clones, including this worktree, may still have `origin` on the old `…/Whalenurse` URL; GitHub redirects it, and `git remote set-url origin https://github.com/jackspiering/whalenurse.git` fixes it.

## 7. Fixtures

Scrubbed with `scripts/scrub-fixture` and checked with `--check` (no `Config.Env`, hostnames, IPs, MACs, home or repository paths); no raw capture is committed.

- `testdata/inspect/` (18): `e1-parent-after-recreate` + `e1-dep-stale-after-parent-recreate` (stale `NetworkMode`, `StartedAt` ordering), `e1-dep-stale-restart-failed`, `e8-stuck-failed-restart` (hypothesis A), `e8-control-restarting`, `e8c-nocmd/noexec/enotdir`, `e8d-port-allocated`, `e8e-dep-parent-stopped`, `e8e-dep-parent-removed`, `e9-crash-exited`, `e9-oom-killed`, `e9-sleeper-stopped`, `e9-sleeper-killed`, `e9-stubborn-stop-timeout`, `e9-loop-restarting`, `e9-hc-unhealthy`.
- `testdata/events/` (11): `e8-stuck-failed-restart` (the failed restart: `create start die(1)` and nothing after), `e9-stop`, `e9-kill`, `e9-restart`, `e9-recreate`, `e9-stop-timeout`, `e9-crash`, `e9-oom`, `e9-loop-restart-policy`, `e9-unhealthy`, `e1-recreate-parent`.

## 8. Reproducing

```
scripts/m0/selftest.sh && scripts/scrub-fixture --self-test && test/m0/run.sh selftest   # no Docker needed
M0_RUN_ID=run1 test/m0/run.sh all                   # about 8 minutes; E8 observes for M0_E8_WAIT=150s
M0_RUN_ID=run1 test/m0/promote-fixtures.sh captures/m0-run1 <names…>   # scrub raw fixtures into testdata/
test/m0/run.sh cleanup                              # down -v on any leftover whalenurse-m0-* project
```
