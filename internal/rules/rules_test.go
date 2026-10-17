package rules_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/docker"
	"github.com/jackspiering/whalenurse/internal/rules"
)

var t0 = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

// scenario is one expectation for one inspect fixture. A fixture may have
// several: the base case (no events, heal-exited=crashed) and variations.
type scenario struct {
	name    string
	fixture string
	mutate  func(*docker.Container)
	cfg     func(*config.Container)
	input   func(*rules.Input, map[string]docker.Container)
	want    rules.Decision // Class, Action, Cause, Skip are compared
}

func d(class rules.Class, action rules.Action) rules.Decision {
	return rules.Decision{Class: class, Action: action}
}

func withCause(dec rules.Decision, c rules.Cause) rules.Decision { dec.Cause = c; return dec }

// parentRunning/parentStopped build a resolved parent for a fixture.
func parent(id string, running bool, started time.Time, busy bool) rules.Parent {
	return rules.Parent{Kind: rules.RefNetwork, Ref: id, Found: true, ID: id, Running: running, StartedAt: started, Busy: busy}
}

const parentID = "676d1a43e20e50eb30aba4a0d84e8938284cfb083dfa4b2396305d4207e6bec8"

var scenarios = []scenario{
	// ---- E1: dependents of a recreated parent -------------------------------
	{name: "healthy parent", fixture: "e1-parent-after-recreate", want: d(rules.ClassOK, rules.ActionNone)},
	{
		// The parent ID the dependent points at (48d0...) does not exist in the world:
		// the dependent is running but disconnected. Never restart it (E1).
		name: "dependent running, parent ID gone", fixture: "e1-dep-stale-after-parent-recreate",
		want: d(rules.ClassStaleParentGone, rules.ActionNeedsRecreate),
	},
	{
		name: "unhealthy dependent, parent ID gone: still no restart", fixture: "e1-dep-stale-after-parent-recreate",
		mutate: func(c *docker.Container) { c.Health = "unhealthy" },
		want:   d(rules.ClassStaleParentGone, rules.ActionNeedsRecreate),
	},
	{
		name: "dependent running, parent restarted later (same ID)", fixture: "e1-dep-stale-after-parent-recreate",
		mutate: func(c *docker.Container) {
			c.NetworkMode = docker.Ref("271b934b370b5008fb8c09499e4e2e70a5a669ec3d02e0d2907fcd24a590be8f")
		},
		want: d(rules.ClassStaleParentRestarted, rules.ActionNeedsDependentRestart),
	},
	{
		name: "dependent running, started after its parent", fixture: "e1-dep-stale-after-parent-recreate",
		mutate: func(c *docker.Container) {
			c.NetworkMode = docker.Ref("271b934b370b5008fb8c09499e4e2e70a5a669ec3d02e0d2907fcd24a590be8f")
			c.StartedAt = c.StartedAt.Add(time.Minute)
		},
		want: d(rules.ClassOK, rules.ActionNone),
	},
	{
		name: "dependent failed on the stale restart (No such container)", fixture: "e1-dep-stale-restart-failed",
		want: withCause(d(rules.ClassStartFailed, rules.ActionNeedsRecreate), rules.CauseParentGone),
	},

	// ---- E8: rule 1, State.Error ---------------------------------------------
	{
		// Exited, exit code 1 (the previous run's), RestartCount 1: the exit code is ignored.
		name: "failed restart-policy restart", fixture: "e8-stuck-failed-restart",
		want: withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseMissingMount),
	},
	{
		name: "failed restart wins over heal-exited=never", fixture: "e8-stuck-failed-restart",
		cfg:  func(c *config.Container) { c.HealExited = config.HealNever },
		want: withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseMissingMount),
	},
	{
		name: "failed restart wins over a kill marker", fixture: "e8-stuck-failed-restart",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Evidence = rules.Evidence{Known: true, ManualStop: true}
		},
		want: withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseMissingMount),
	},
	{
		name: "failed restart, exit code irrelevant", fixture: "e8-stuck-failed-restart",
		mutate: func(c *docker.Container) { c.ExitCode = 0; c.RestartPolicy = "no" },
		want:   withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseMissingMount),
	},
	{name: "first start: command not found (127)", fixture: "e8c-nocmd", want: withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseExecNotFound)},
	{name: "first start: not executable (126)", fixture: "e8c-noexec", want: withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseExecPermission)},
	{name: "first start: ENOTDIR (127)", fixture: "e8c-enotdir", want: withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseNotADirectory)},
	{name: "first start: port already allocated (128)", fixture: "e8d-port-allocated", want: withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CausePortAllocated)},
	{
		name: "first start failed, exit code zeroed", fixture: "e8c-nocmd",
		mutate: func(c *docker.Container) { c.ExitCode = 0 },
		want:   withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseExecNotFound),
	},

	// ---- E8e: stale references by cause ---------------------------------------
	{
		name: "parent removed (No such container)", fixture: "e8e-dep-parent-removed",
		want: withCause(d(rules.ClassStartFailed, rules.ActionNeedsRecreate), rules.CauseParentGone),
	},
	{
		name: "parent stopped, still stopped: act only on the parent", fixture: "e8e-dep-parent-stopped",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Parents = []rules.Parent{parent(parentID, false, time.Time{}, false)}
		},
		want: withCause(d(rules.ClassWaitingForParent, rules.ActionWaitForParent), rules.CauseParentStopped),
	},
	{
		name: "parent stopped, now running: restart the dependent", fixture: "e8e-dep-parent-stopped",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Parents = []rules.Parent{parent(parentID, true, t0, false)}
		},
		want: withCause(d(rules.ClassStartFailed, rules.ActionStart), rules.CauseParentStopped),
	},
	{
		name: "parent stopped, parent has an open incident: wait", fixture: "e8e-dep-parent-stopped",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Parents = []rules.Parent{parent(parentID, true, t0, true)}
		},
		want: withCause(d(rules.ClassWaitingForParent, rules.ActionWaitForParent), rules.CauseParentStopped),
	},
	{
		name: "parent stopped error, but the parent has since disappeared", fixture: "e8e-dep-parent-stopped",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Parents = []rules.Parent{{Kind: rules.RefNetwork, Ref: parentID}}
		},
		want: withCause(d(rules.ClassStaleParentGone, rules.ActionNeedsRecreate), rules.CauseParentStopped),
	},
	{
		name: "reference we could not resolve: do nothing", fixture: "e8e-dep-parent-stopped",
		input: func(in *rules.Input, _ map[string]docker.Container) { in.Parents = nil },
		want:  withCause(d(rules.ClassParentUnresolved, rules.ActionNone), rules.CauseParentStopped),
	},

	// ---- restarting / crash loops ---------------------------------------------
	{
		name: "restarting, first seen a minute ago", fixture: "e8-control-restarting",
		input: func(in *rules.Input, _ map[string]docker.Container) { in.RestartingSince = in.Now.Add(-time.Minute) },
		want:  d(rules.ClassRestarting, rules.ActionNone),
	},
	{
		name: "restarting, age unknown", fixture: "e8-control-restarting",
		want: d(rules.ClassRestarting, rules.ActionNone),
	},
	{
		name: "restarting for 10 minutes: crash loop", fixture: "e8-control-restarting",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.RestartingSince = in.Now.Add(-10 * time.Minute)
		},
		want: d(rules.ClassCrashLoop, rules.ActionRestart),
	},
	{
		name: "restarting for 10 minutes (always policy)", fixture: "e9-loop-restarting",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.RestartingSince = in.Now.Add(-10 * time.Minute)
		},
		want: d(rules.ClassCrashLoop, rules.ActionRestart),
	},
	{
		name: "restarting just under the limit", fixture: "e9-loop-restarting",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.RestartingSince = in.Now.Add(-5*time.Minute + time.Second)
		},
		want: d(rules.ClassRestarting, rules.ActionNone),
	},

	// ---- unhealthy -------------------------------------------------------------
	{name: "unhealthy", fixture: "e9-hc-unhealthy", want: d(rules.ClassUnhealthy, rules.ActionRestart)},
	{
		name: "healthcheck starting", fixture: "e9-hc-unhealthy",
		mutate: func(c *docker.Container) { c.Health = "starting" },
		want:   d(rules.ClassStarting, rules.ActionNone),
	},
	{
		name: "healthy", fixture: "e9-hc-unhealthy",
		mutate: func(c *docker.Container) { c.Health = "healthy" },
		want:   d(rules.ClassOK, rules.ActionNone),
	},

	// ---- rule 4: exited, no State.Error -----------------------------------------
	{name: "crashed (exit 1, no events seen)", fixture: "e9-crash-exited", want: d(rules.ClassExitedCrashed, rules.ActionStart)},
	{
		name: "crashed, die without kill seen", fixture: "e9-crash-exited",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Evidence = rules.Evidence{Known: true, Crashed: true}
		},
		want: d(rules.ClassExitedCrashed, rules.ActionStart),
	},
	{
		name: "exit 1 but a kill was seen: stopped by hand", fixture: "e9-crash-exited",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Evidence = rules.Evidence{Known: true, ManualStop: true}
		},
		want: d(rules.ClassExitedManual, rules.ActionNone),
	},
	{
		name: "crashed, heal-exited=never: notify only", fixture: "e9-crash-exited",
		cfg:  func(c *config.Container) { c.HealExited = config.HealNever },
		want: d(rules.ClassExitedNever, rules.ActionNotify),
	},
	{
		name: "crashed, heal-exited=always", fixture: "e9-crash-exited",
		cfg:  func(c *config.Container) { c.HealExited = config.HealAlways },
		want: d(rules.ClassExitedAlways, rules.ActionStart),
	},
	{
		name: "exit 0 under policy no is intended", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.ExitCode = 0 },
		want:   d(rules.ClassExitedIntended, rules.ActionNone),
	},
	{
		name: "exit 0 under on-failure is intended, even with heal-exited=never", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.ExitCode = 0; c.RestartPolicy = "on-failure" },
		cfg:    func(c *config.Container) { c.HealExited = config.HealNever },
		want:   d(rules.ClassExitedIntended, rules.ActionNone),
	},
	{
		name: "exit 0 under policy no, heal-exited=always: healed (always means any exited container)", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.ExitCode = 0 },
		cfg:    func(c *config.Container) { c.HealExited = config.HealAlways },
		want:   d(rules.ClassExitedAlways, rules.ActionStart),
	},
	{
		name: "exit 0 under unless-stopped, no events: left alone", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.ExitCode = 0; c.RestartPolicy = "unless-stopped" },
		want:   d(rules.ClassExitedClean, rules.ActionNone),
	},
	{
		name: "exit 0 under unless-stopped, heal-exited=always", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.ExitCode = 0; c.RestartPolicy = "unless-stopped" },
		cfg:    func(c *config.Container) { c.HealExited = config.HealAlways },
		want:   d(rules.ClassExitedAlways, rules.ActionStart),
	},
	{
		name: "on-failure:3 exhausted", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.RestartPolicy = "on-failure"; c.MaxRetries = 3; c.RestartCount = 3 },
		cfg:    func(c *config.Container) { c.HealExited = config.HealNever },
		want:   d(rules.ClassRetriesExhausted, rules.ActionStart),
	},
	{
		name: "on-failure:3 not yet exhausted", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.RestartPolicy = "on-failure"; c.MaxRetries = 3; c.RestartCount = 2 },
		cfg:    func(c *config.Container) { c.HealExited = config.HealNever },
		want:   d(rules.ClassExitedNever, rules.ActionNotify),
	},
	{name: "OOM kill (137 + OOMKilled)", fixture: "e9-oom-killed", want: d(rules.ClassExitedOOM, rules.ActionStart)},
	{
		name: "OOM kill, heal-exited=never", fixture: "e9-oom-killed",
		cfg:  func(c *config.Container) { c.HealExited = config.HealNever },
		want: d(rules.ClassExitedNever, rules.ActionNotify),
	},
	{name: "docker stop (143): notify only", fixture: "e9-sleeper-stopped", want: d(rules.ClassExitedSignalled, rules.ActionNotify)},
	{name: "docker kill (137, no OOM): notify only", fixture: "e9-sleeper-killed", want: d(rules.ClassExitedSignalled, rules.ActionNotify)},
	{name: "stop that hit its timeout (137): notify only", fixture: "e9-stubborn-stop-timeout", want: d(rules.ClassExitedSignalled, rules.ActionNotify)},
	{
		name: "docker stop with a kill marker: left alone", fixture: "e9-sleeper-stopped",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Evidence = rules.Evidence{Known: true, ManualStop: true}
		},
		want: d(rules.ClassExitedManual, rules.ActionNone),
	},
	{
		name: "docker stop (143), heal-exited=always", fixture: "e9-sleeper-stopped",
		cfg:  func(c *config.Container) { c.HealExited = config.HealAlways },
		want: d(rules.ClassExitedAlways, rules.ActionStart),
	},
	{
		name: "137 with OOM evidence from the oom event", fixture: "e9-sleeper-killed",
		input: func(in *rules.Input, _ map[string]docker.Container) {
			in.Evidence = rules.Evidence{Known: true, OOM: true, ManualStop: false, Crashed: true}
		},
		mutate: func(c *docker.Container) { c.OOMKilled = true },
		want:   d(rules.ClassExitedOOM, rules.ActionStart),
	},

	// ---- other statuses ---------------------------------------------------------
	{
		name: "created, never started", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.Status = "created"; c.ExitCode = 0 },
		want:   d(rules.ClassNeverStarted, rules.ActionStart),
	},
	{
		name: "dead", fixture: "e9-crash-exited",
		mutate: func(c *docker.Container) { c.Status = "dead" },
		want:   d(rules.ClassDead, rules.ActionNotify),
	},

	// ---- rule 0: skips ------------------------------------------------------------
	{
		name: "skip: own container", fixture: "e9-hc-unhealthy",
		input: func(in *rules.Input, _ map[string]docker.Container) { in.Self = true },
		want:  rules.Decision{Class: rules.ClassSkip, Action: rules.ActionNone, Skip: rules.SkipSelf},
	},
	{
		name: "skip: compose run container", fixture: "e9-hc-unhealthy",
		mutate: func(c *docker.Container) { c.Labels = withLabel(c.Labels, docker.LabelOneOff, "True") },
		want:   rules.Decision{Class: rules.ClassSkip, Action: rules.ActionNone, Skip: rules.SkipOneOff},
	},
	{
		name: "skip: swarm task", fixture: "e9-hc-unhealthy",
		mutate: func(c *docker.Container) { c.Labels = withLabel(c.Labels, docker.LabelSwarmTaskID, "abc123") },
		want:   rules.Decision{Class: rules.ClassSkip, Action: rules.ActionNone, Skip: rules.SkipSwarm},
	},
	{
		name: "skip: not opted in", fixture: "e9-hc-unhealthy",
		cfg:  func(c *config.Container) { c.OptedIn = false },
		want: rules.Decision{Class: rules.ClassSkip, Action: rules.ActionNone, Skip: rules.SkipNotOptedIn},
	},
	{
		name: "skip: docker pause", fixture: "e9-hc-unhealthy",
		mutate: func(c *docker.Container) { c.Paused = true; c.Status = "paused" },
		want:   rules.Decision{Class: rules.ClassSkip, Action: rules.ActionNone, Skip: rules.SkipDockerPause},
	},
}

func withLabel(m map[string]string, k, v string) map[string]string {
	out := map[string]string{}
	for a, b := range m {
		out[a] = b
	}
	out[k] = v
	return out
}

// TestInspectFixtures runs every scenario, and fails if a fixture in
// testdata/inspect has no scenario or a scenario names a missing fixture, so
// "every fixture" stays true when new captures are added.
func TestInspectFixtures(t *testing.T) {
	fixtures := listFixtures(t, "inspect", ".json")
	world := map[string]docker.Container{}
	for _, f := range fixtures {
		world[f] = loadInspect(t, f)
	}

	covered := map[string]bool{}
	for _, sc := range scenarios {
		covered[sc.fixture] = true
		if _, ok := world[sc.fixture]; !ok {
			t.Errorf("scenario %q names fixture %q, which does not exist in testdata/inspect", sc.name, sc.fixture)
		}
	}
	for _, f := range fixtures {
		if !covered[f] {
			t.Errorf("fixture testdata/inspect/%s.json has no expected classification in scenarios", f)
		}
	}

	for _, sc := range scenarios {
		t.Run(sc.fixture+"/"+sc.name, func(t *testing.T) {
			c, ok := world[sc.fixture]
			if !ok {
				t.Skip("missing fixture")
			}
			c.Labels = withLabel(c.Labels, "x", "") // private copy of the map
			if sc.mutate != nil {
				sc.mutate(&c)
			}
			cfg := config.Container{OptedIn: true, Actions: []string{"restart", "recreate"}, HealExited: config.HealCrashed}
			if sc.cfg != nil {
				sc.cfg(&cfg)
			}
			in := rules.Input{Now: t0, C: c, Cfg: cfg, CrashloopAfter: 5 * time.Minute}
			in.Parents = resolveParents(c, world)
			if sc.input != nil {
				sc.input(&in, world)
			}
			got := rules.Classify(in)
			if got.Class != sc.want.Class || got.Action != sc.want.Action || got.Cause != sc.want.Cause || got.Skip != sc.want.Skip {
				t.Fatalf("got class=%s action=%s cause=%q skip=%q (%s)\nwant class=%s action=%s cause=%q skip=%q",
					got.Class, got.Action, got.Cause, got.Skip, got.Reason,
					sc.want.Class, sc.want.Action, sc.want.Cause, sc.want.Skip)
			}
			if got.Reason == "" {
				t.Error("empty Reason")
			}
		})
	}
}

// resolveParents resolves the container: references of c against the fixtures
// themselves, the way the daemon resolves them against the container list.
func resolveParents(c docker.Container, world map[string]docker.Container) []rules.Parent {
	var out []rules.Parent
	for kind, mode := range map[string]string{rules.RefNetwork: c.NetworkMode, rules.RefIPC: c.IpcMode, rules.RefPID: c.PidMode} {
		ref := docker.ContainerRef(mode)
		if ref == "" {
			continue
		}
		p := rules.Parent{Kind: kind, Ref: ref}
		for _, w := range world {
			if docker.MatchesRef(w.ID, w.Name, ref) {
				p.Found, p.ID, p.Running, p.StartedAt = true, w.ID, w.Running, w.StartedAt
			}
		}
		out = append(out, p)
	}
	return out
}

// TestStartErrorCauses pins the matcher against every State.Error in the
// fixtures: the strings live in one place, backed by captures.
func TestStartErrorCauses(t *testing.T) {
	want := map[string]rules.Cause{
		"e1-dep-stale-restart-failed": rules.CauseParentGone,
		"e8-stuck-failed-restart":     rules.CauseMissingMount,
		"e8c-nocmd":                   rules.CauseExecNotFound,
		"e8c-noexec":                  rules.CauseExecPermission,
		"e8c-enotdir":                 rules.CauseNotADirectory,
		"e8d-port-allocated":          rules.CausePortAllocated,
		"e8e-dep-parent-removed":      rules.CauseParentGone,
		"e8e-dep-parent-stopped":      rules.CauseParentStopped,
	}
	for _, f := range listFixtures(t, "inspect", ".json") {
		c := loadInspect(t, f)
		w, has := want[f]
		got := rules.ClassifyStartError(c.Error)
		switch {
		case c.Error == "" && has:
			t.Errorf("%s: expected a State.Error", f)
		case c.Error != "" && !has:
			t.Errorf("%s: has State.Error %q but no expected cause", f, c.Error)
		case has && got != w:
			t.Errorf("%s: cause %q, want %q", f, got, w)
		}
	}
}

func TestStartErrorStrings(t *testing.T) {
	cases := map[string]rules.Cause{
		"": rules.CauseNone,
		"joining network namespace of container: No such container: abc":                  rules.CauseParentGone,
		"cannot join network namespace of a non running container: container x is exited": rules.CauseParentStopped,
		"network abc not found": rules.CauseNetworkGone,
		"invalid mount config for type \"bind\": bind source path does not exist: /srv/x": rules.CauseMissingMount,
		"driver failed ... Bind for 0.0.0.0:80 failed: port is already allocated":         rules.CausePortAllocated,
		"something nobody has seen before":                                                rules.CauseOther,
	}
	for in, want := range cases {
		if got := rules.ClassifyStartError(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	for _, c := range []rules.Cause{rules.CauseParentGone, rules.CauseParentStopped, rules.CauseNetworkGone} {
		if !c.IsStaleReference() {
			t.Errorf("%s should be a stale reference", c)
		}
	}
	if rules.CauseMissingMount.IsStaleReference() {
		t.Error("missing-mount is not a stale reference")
	}
	_ = strings.TrimSpace
}

// expected verdicts for the events fixtures: what each sequence amounts to.
var eventVerdicts = map[string]rules.Verdict{
	"e1-recreate-parent":      rules.VerdictRecreated,
	"e8-stuck-failed-restart": rules.VerdictCrash, // and nothing after it: the failed restart emits no event
	"e9-crash":                rules.VerdictCrash,
	"e9-kill":                 rules.VerdictManualStop,
	"e9-loop-restart-policy":  rules.VerdictCrashedLoop,
	"e9-oom":                  rules.VerdictOOM,
	"e9-recreate":             rules.VerdictRecreated,
	"e9-restart":              rules.VerdictRunning, // kill ... die then start: the marker is cleared
	"e9-stop":                 rules.VerdictManualStop,
	"e9-stop-timeout":         rules.VerdictManualStop,
	"e9-unhealthy":            rules.VerdictUnhealthy,
}

// TestEventFixtures classifies every events fixture, grouped by state key
// (not container ID: a recreate spans two IDs).
func TestEventFixtures(t *testing.T) {
	names := listFixtures(t, "events", ".ndjson")
	for _, n := range names {
		if _, ok := eventVerdicts[n]; !ok {
			t.Errorf("testdata/events/%s.ndjson has no expected verdict", n)
		}
	}
	for n := range eventVerdicts {
		found := false
		for _, f := range names {
			found = found || f == n
		}
		if !found {
			t.Errorf("verdict for %q, but testdata/events/%s.ndjson does not exist", n, n)
		}
	}
	for _, n := range names {
		want, ok := eventVerdicts[n]
		if !ok {
			continue
		}
		t.Run(n, func(t *testing.T) {
			byKey := map[string][]rules.EventLine{}
			for _, ev := range loadEvents(t, n) {
				k := docker.StateKey("h", ev.Name, ev.Labels)
				byKey[k] = append(byKey[k], rules.EventLine{Action: ev.Action, Detail: ev.Detail})
			}
			if len(byKey) != 1 {
				t.Fatalf("expected one state key, got %d", len(byKey))
			}
			for k, seq := range byKey {
				if got := rules.Summarize(seq); got != want {
					t.Errorf("%s: verdict %q, want %q", k, got, want)
				}
			}
		})
	}
}

// TestFailedRestartEmitsNoEvent documents E8: the events of a failed
// restart-policy restart end at the die of the earlier run, so only the
// poll's inspect (State.Error) can see it.
func TestFailedRestartEmitsNoEvent(t *testing.T) {
	evs := loadEvents(t, "e8-stuck-failed-restart")
	var actions []string
	for _, e := range evs {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); got != "create,start,die" {
		t.Fatalf("events = %s", got)
	}
	c := loadInspect(t, "e8-stuck-failed-restart")
	if c.Error == "" || c.Status != "exited" || c.RestartCount != 1 {
		t.Fatalf("fixture no longer shows a failed restart: %+v", c)
	}
}

func TestTrackerManualStopClearsOnStart(t *testing.T) {
	var tr rules.Tracker
	for _, a := range []string{"kill", "die"} {
		tr.Apply(a, "")
	}
	if !tr.Evidence().ManualStop {
		t.Fatal("kill then die should mark a manual stop")
	}
	tr.Apply("start", "") // Docker's own boot restart after a daemon shutdown clears it
	if tr.Evidence().ManualStop || tr.Evidence().Known {
		t.Fatalf("start should clear the marker: %+v", tr.Evidence())
	}
}
