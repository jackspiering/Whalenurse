package engine

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackspiering/whalenurse/internal/clock"
	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/docker"
	"github.com/jackspiering/whalenurse/internal/rules"
)

// memStore is a Persister that keeps what the engine writes, so a test can start a second engine from it.
type memStore struct {
	keys    map[string]KeyRecord
	closed  []ClosedIncident
	pauses  map[string]Pause
	actions []time.Time
}

func newMemStore() *memStore {
	return &memStore{keys: map[string]KeyRecord{}, pauses: map[string]Pause{}}
}
func (m *memStore) SaveKey(r KeyRecord)            { m.keys[r.Key] = r }
func (m *memStore) DeleteKey(k string)             { delete(m.keys, k) }
func (m *memStore) CloseIncident(c ClosedIncident) { m.closed = append(m.closed, c) }
func (m *memStore) SavePause(p Pause)              { m.pauses[p.Scope] = p }
func (m *memStore) DeletePause(s string)           { delete(m.pauses, s) }
func (m *memStore) SaveAction(at time.Time)        { m.actions = append(m.actions, at) }
func (m *memStore) snapshot() Snapshot {
	var s Snapshot
	for _, k := range m.keys {
		s.Keys = append(s.Keys, k)
	}
	for _, p := range m.pauses {
		s.Pauses = append(s.Pauses, p)
	}
	s.Actions = append(s.Actions, m.actions...)
	return s
}

func newEngine(t *testing.T, rate int) (*Engine, *clock.Fake, *Recorder) {
	return newEngineWith(t, nil, func(p *Params) { p.RateLimit = rate })
}

func newEngineWith(t *testing.T, clk *clock.Fake, tweak func(*Params)) (*Engine, *clock.Fake, *Recorder) {
	t.Helper()
	if clk == nil {
		clk = clock.NewFake(time.Time{})
	}
	bus := &Bus{}
	rec := &Recorder{}
	bus.Subscribe(rec.Record)
	p := Params{Host: "h", BaseCooldown: 30 * time.Second, RecoveryPeriod: 10 * time.Minute, CrashloopAfter: 5 * time.Minute, FlapLimit: 3, FlapWindow: 24 * time.Hour}
	if tweak != nil {
		tweak(&p)
	}
	return New(p, clk, bus), clk, rec
}

var (
	tgt = Target{Key: "h/p/s/1", ID: "abc", Name: "p-s-1"}
	cfg = config.Container{OptedIn: true, Actions: []string{"restart", "recreate"}, HealExited: config.HealCrashed}
	bad = rules.Decision{Class: rules.ClassUnhealthy, Action: rules.ActionRestart, Reason: "unhealthy"}
	ok  = rules.Decision{Class: rules.ClassOK, Action: rules.ActionNone, Reason: "running"}

	hcObs = Obs{HasHealthcheck: true}
	noHC  = Obs{}
)

// run performs one planned action the way the daemon does and returns the plan.
func run(e *Engine, d rules.Decision, c config.Container, o Obs) Plan {
	p := e.Evaluate(tgt, d, c, o)
	if p.Act == "" || p.Act == rules.ActionNone {
		return p
	}
	e.Begin(tgt, p, d)
	p.RecheckAt = e.End(tgt, p, Result{}, c)
	return p
}

func acted(p Plan) bool { return p.Act != "" && p.Act != rules.ActionNone }

func TestLadderStepsAndCooldown(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	p := e.Evaluate(tgt, bad, cfg, hcObs)
	if p.Act != rules.ActionRestart || p.Rung != "restart" {
		t.Fatalf("%+v", p)
	}
	e.Begin(tgt, p, bad)
	recheck := e.End(tgt, p, Result{}, cfg)
	if want := clk.Now().Add(30 * time.Second); !recheck.Equal(want) {
		t.Fatalf("recheck %v want %v", recheck, want)
	}
	// Inside the cooldown: no second action, just a recheck.
	clk.Advance(10 * time.Second)
	if p := e.Evaluate(tgt, bad, cfg, hcObs); acted(p) || p.RecheckAt.IsZero() {
		t.Fatalf("%+v", p)
	}
	// After it, rung 2 does not exist yet: it is recorded as needing M2b and the container is SUSPENDED.
	clk.Advance(21 * time.Second)
	if p := e.Evaluate(tgt, bad, cfg, hcObs); acted(p) {
		t.Fatalf("second pass must not restart again: %+v", p)
	}
	if rec.Count(Suspended) != 1 || rec.Count(RungUnavailable) != 1 {
		t.Fatalf("%v", rec.Types())
	}
	for _, ev := range rec.Events() {
		if ev.Type == RungUnavailable && !strings.Contains(ev.Message, "needs recreate (M2b)") {
			t.Fatalf("rung 2 must be recorded as needing M2b: %q", ev.Message)
		}
		if ev.Type == Suspended && ev.Attrs["reason"] != ReasonLadder {
			t.Fatalf("%+v", ev)
		}
	}
	if open, susp := e.IncidentOpen(tgt.Key); !open || !susp {
		t.Fatal("incident should be open and suspended")
	}
	if e.Busy(tgt.Key) {
		t.Fatal("a suspended incident does not hold back dependents; there is nothing left to wait for")
	}
	// Still bad: SUSPENDED means nothing more is tried and nothing more is said.
	clk.Advance(time.Hour)
	if p := e.Evaluate(tgt, bad, cfg, hcObs); acted(p) || rec.Count(Suspended) != 1 {
		t.Fatalf("%+v %v", p, rec.Types())
	}
}

func TestCooldownGrowsPerRung(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	c := cfg
	c.Actions = []string{"restart", "restart", "restart"} // not reachable by labels; shows the doubling
	p := run(e, bad, c, hcObs)
	if want := clk.Now().Add(30 * time.Second); !p.RecheckAt.Equal(want) {
		t.Fatalf("after rung 1: %v", p.RecheckAt)
	}
	clk.Advance(31 * time.Second)
	p = run(e, bad, c, hcObs)
	if want := clk.Now().Add(60 * time.Second); !p.RecheckAt.Equal(want) {
		t.Fatalf("after rung 2: %v", p.RecheckAt.Sub(clk.Now()))
	}
}

func TestRecoveryClosesTheIncidentOnlyAfterTheRecoveryPeriod(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	run(e, bad, cfg, hcObs)
	clk.Advance(31 * time.Second)

	// Healthy with a healthcheck: recovered, but the incident stays open for RECOVERY_PERIOD.
	p := e.Evaluate(tgt, ok, cfg, hcObs)
	if p.RecheckAt.IsZero() || rec.Count(Recovered) != 0 {
		t.Fatalf("%+v %v", p, rec.Types())
	}
	if open, _ := e.IncidentOpen(tgt.Key); !open {
		t.Fatal("incident closed early")
	}
	if e.Busy(tgt.Key) {
		t.Fatal("a recovering (healthy) incident must not hold back dependents")
	}
	clk.Advance(9 * time.Minute)
	e.Evaluate(tgt, ok, cfg, hcObs)
	if rec.Count(Recovered) != 0 {
		t.Fatal("recovered before RECOVERY_PERIOD")
	}
	clk.Advance(61 * time.Second)
	e.Evaluate(tgt, ok, cfg, hcObs)
	if rec.Count(Recovered) != 1 {
		t.Fatalf("%v", rec.Types())
	}
	if open, _ := e.IncidentOpen(tgt.Key); open {
		t.Fatal("incident should be closed")
	}
}

func TestFailingAgainWhileRecoveringContinuesTheSameIncident(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	run(e, bad, cfg, hcObs) // rung 1
	clk.Advance(31 * time.Second)
	e.Evaluate(tgt, ok, cfg, hcObs) // recovering
	clk.Advance(2 * time.Minute)
	p := e.Evaluate(tgt, bad, cfg, hcObs) // fails again, 2 minutes into the recovery
	// One pass per incident: no second restart, the next rung (recreate) is not there yet, so SUSPENDED.
	if acted(p) || rec.Count(IncidentOpened) != 1 || rec.Count(Suspended) != 1 {
		t.Fatalf("%+v %v", p, rec.Types())
	}
}

func TestWithoutAHealthcheckRecoveryNeedsAQuietRecoveryPeriod(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	run(e, bad, cfg, noHC)
	clk.Advance(31 * time.Second)
	e.Evaluate(tgt, ok, cfg, noHC) // running; the clock starts
	clk.Advance(6 * time.Minute)
	// RestartCount changed: the quiet period starts over.
	e.Evaluate(tgt, ok, cfg, Obs{RestartCount: 1})
	clk.Advance(6 * time.Minute)
	e.Evaluate(tgt, ok, cfg, Obs{RestartCount: 1})
	if rec.Count(Recovered) != 0 {
		t.Fatalf("recovered 6 minutes after the last restart: %v", rec.Types())
	}
	clk.Advance(5 * time.Minute)
	e.Evaluate(tgt, ok, cfg, Obs{RestartCount: 1})
	if rec.Count(Recovered) != 1 {
		t.Fatalf("%v", rec.Types())
	}
}

func suspendIt(t *testing.T, e *Engine, clk *clock.Fake) {
	t.Helper()
	run(e, bad, cfg, hcObs)
	clk.Advance(31 * time.Second)
	e.Evaluate(tgt, bad, cfg, hcObs)
	if !e.Suspended(tgt.Key) {
		t.Fatal("setup: not suspended")
	}
}

func TestSuspendedClearsWhenItReturnsToHealthy(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	suspendIt(t, e, clk)
	e.Evaluate(tgt, ok, cfg, hcObs) // healthy again (it has a healthcheck)
	if e.Suspended(tgt.Key) || rec.Count(Unsuspended) != 1 {
		t.Fatalf("%v", rec.Types())
	}
	if open, _ := e.IncidentOpen(tgt.Key); !open {
		t.Fatal("the incident closes only after RECOVERY_PERIOD, not when SUSPENDED clears")
	}
}

func TestSuspendedWithoutHealthcheckClearsOnlyAfterTheRecoveryPeriod(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	run(e, bad, cfg, noHC)
	clk.Advance(31 * time.Second)
	e.Evaluate(tgt, bad, cfg, noHC)
	e.Evaluate(tgt, ok, cfg, noHC)
	if !e.Suspended(tgt.Key) {
		t.Fatal("running for a second proves nothing without a healthcheck")
	}
	clk.Advance(10*time.Minute + time.Second)
	e.Evaluate(tgt, ok, cfg, noHC)
	if e.Suspended(tgt.Key) || rec.Count(Unsuspended) != 1 || rec.Count(Recovered) != 1 {
		t.Fatalf("%v", rec.Types())
	}
}

func docEv(action string, at time.Time) docker.Event {
	return docker.Event{Action: action, Time: at}
}

func TestSuspendedClearsOnExternalCreateOrAStartAfterAManualStop(t *testing.T) {
	// A start by the restart policy must not clear it (it would re-arm the ladder on every cycle of a loop).
	e, clk, rec := newEngine(t, 0)
	suspendIt(t, e, clk)
	e.ObserveDockerEvent(tgt, docEv("die", clk.Now()))
	e.ObserveDockerEvent(tgt, docEv("start", clk.Now()))
	if !e.Suspended(tgt.Key) {
		t.Fatal("a policy restart unsuspended it")
	}
	// kill, then start: someone stopped it and someone started it.
	e.ObserveDockerEvent(tgt, docEv("kill", clk.Now()))
	e.ObserveDockerEvent(tgt, docEv("die", clk.Now()))
	e.ObserveDockerEvent(tgt, docEv("start", clk.Now()))
	if e.Suspended(tgt.Key) || rec.Count(Unsuspended) != 1 {
		t.Fatalf("%v", rec.Types())
	}

	e, clk, rec = newEngine(t, 0)
	suspendIt(t, e, clk)
	e.ObserveDockerEvent(tgt, docEv("create", clk.Now()))
	if e.Suspended(tgt.Key) || rec.Count(Unsuspended) != 1 {
		t.Fatalf("%v", rec.Types())
	}
}

func TestFlapLimitSuspendsTheNextIncident(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	// Three incidents, each one healed and closed after RECOVERY_PERIOD: within the limit.
	for i := 0; i < 3; i++ {
		p := run(e, bad, cfg, hcObs)
		if !acted(p) {
			t.Fatalf("incident %d was not healed: %v", i+1, rec.Types())
		}
		clk.Advance(31 * time.Second)
		e.Evaluate(tgt, ok, cfg, hcObs)
		clk.Advance(11 * time.Minute)
		e.Evaluate(tgt, ok, cfg, hcObs)
		if open, _ := e.IncidentOpen(tgt.Key); open {
			t.Fatalf("incident %d did not close: %v", i+1, rec.Types())
		}
	}
	if rec.Count(Recovered) != 3 || rec.Count(Suspended) != 0 {
		t.Fatalf("%v", rec.Types())
	}
	// More than FLAP_LIMIT (3) within FLAP_WINDOW: the 4th is SUSPENDED at once, nothing is tried,
	// although every earlier incident recovered.
	if p := e.Evaluate(tgt, bad, cfg, hcObs); acted(p) {
		t.Fatalf("the 4th incident was healed: %+v", p)
	}
	if !e.Suspended(tgt.Key) {
		t.Fatalf("%v", rec.Types())
	}
	var reason string
	for _, ev := range rec.Events() {
		if ev.Type == Suspended {
			reason = ev.Attrs["reason"]
		}
	}
	if reason != ReasonFlapping {
		t.Fatalf("suspended for %q", reason)
	}
}

func TestFlapWindowExpiryAndPerContainerLimit(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	for i := 0; i < 3; i++ {
		run(e, bad, cfg, hcObs)
		clk.Advance(31 * time.Second)
		e.Evaluate(tgt, ok, cfg, hcObs)
		clk.Advance(11 * time.Minute)
		e.Evaluate(tgt, ok, cfg, hcObs)
	}
	clk.Advance(25 * time.Hour) // the window has passed
	if p := e.Evaluate(tgt, bad, cfg, hcObs); !acted(p) {
		t.Fatal("the flap count should have expired with the window")
	}

	// whalenurse.flap-limit=1: the second incident in the window is the flapping one.
	e, clk, _ = newEngine(t, 0)
	one := cfg
	one.FlapLimit = 1
	run(e, bad, one, hcObs)
	clk.Advance(31 * time.Second)
	e.Evaluate(tgt, ok, one, hcObs)
	clk.Advance(11 * time.Minute)
	e.Evaluate(tgt, ok, one, hcObs)
	if p := e.Evaluate(tgt, bad, one, hcObs); acted(p) || !e.Suspended(tgt.Key) {
		t.Fatal("per-container flap limit ignored")
	}
}

func TestUnsuspendForgetsFlapHistoryOfAFlappingKey(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	for i := 0; i < 3; i++ {
		run(e, bad, cfg, hcObs)
		clk.Advance(31 * time.Second)
		e.Evaluate(tgt, ok, cfg, hcObs)
		clk.Advance(11 * time.Minute)
		e.Evaluate(tgt, ok, cfg, hcObs)
	}
	e.Evaluate(tgt, bad, cfg, hcObs) // flapping
	keys, err := e.Unsuspend("p")
	if err != nil || len(keys) != 1 || e.Suspended(tgt.Key) || rec.Count(Unsuspended) != 1 {
		t.Fatalf("%v %v %v", keys, err, rec.Types())
	}
	if p := e.Evaluate(tgt, bad, cfg, hcObs); !acted(p) {
		t.Fatal("without clearing the history the next incident would be suspended again at once")
	}
	if got := e.TakeReeval(); len(got) != 1 {
		t.Fatalf("unsuspend must queue the key for re-evaluation: %v", got)
	}
}

func TestUnsuspendIgnoresKeysOutsideTheScope(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	suspendIt(t, e, clk)
	for _, scope := range []string{"other", "p/other", "pp"} {
		if keys, _ := e.Unsuspend(scope); len(keys) != 0 {
			t.Fatalf("scope %q reached %v", scope, keys)
		}
	}
	if keys, _ := e.Unsuspend("p/s"); len(keys) != 1 {
		t.Fatal("p/s should match")
	}
	if _, err := e.Unsuspend("a/b/c"); err == nil {
		t.Fatal("a three-part scope should be rejected")
	}
}

func TestPausedKeysGetNoActionsAndResumeReevaluates(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	if _, err := e.Pause("p/s", time.Hour); err != nil {
		t.Fatal(err)
	}
	if paused, until := e.Paused(tgt.Key); !paused || !until.Equal(clk.Now().Add(time.Hour)) {
		t.Fatalf("%v %v", paused, until)
	}
	if p := e.Evaluate(tgt, bad, cfg, hcObs); acted(p) || rec.Count(IncidentOpened) != 0 {
		t.Fatalf("paused, yet: %+v %v", p, rec.Types())
	}
	other := Target{Key: "h/p/t/1"}
	if p := e.Evaluate(other, bad, cfg, hcObs); !acted(p) {
		t.Fatal("a pause of p/s must not reach p/t")
	}
	// Events are still recorded while paused.
	e.ObserveDockerEvent(tgt, docEv("kill", clk.Now()))
	if !e.Evidence(tgt.Key).ManualStop {
		t.Fatal("events must be recorded while paused")
	}
	if ok, _ := e.Resume("p/s"); !ok {
		t.Fatal("resume found no pause")
	}
	if ok, _ := e.Resume("p/s"); ok {
		t.Fatal("resume of a scope that is not paused should say so")
	}
	if got := e.TakeReeval(); len(got) == 0 || got[0] != tgt.Key {
		t.Fatalf("resume must queue the keys under it: %v", got)
	}
	if p := e.Evaluate(tgt, bad, cfg, hcObs); !acted(p) {
		t.Fatal("not re-evaluated after resume")
	}
	if rec.Count(Paused) != 1 || rec.Count(Resumed) != 1 {
		t.Fatalf("%v", rec.Types())
	}
}

func TestPauseExpires(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	e.Pause("p", 2*time.Hour)
	clk.Advance(2*time.Hour - time.Second)
	e.ExpirePauses()
	if paused, _ := e.Paused(tgt.Key); !paused || rec.Count(Resumed) != 0 {
		t.Fatal("expired early")
	}
	clk.Advance(2 * time.Second)
	if paused, _ := e.Paused(tgt.Key); paused {
		t.Fatal("still paused after expiry (even before ExpirePauses runs)")
	}
	e.Evaluate(tgt, ok, cfg, hcObs) // creates the key state
	e.ExpirePauses()
	if rec.Count(Resumed) != 1 {
		t.Fatalf("%v", rec.Types())
	}
	if got := e.TakeReeval(); len(got) != 1 {
		t.Fatalf("expiry must queue the key for re-evaluation: %v", got)
	}
	// Indefinite pauses (--for 0) never expire.
	e.Pause("p", 0)
	clk.Advance(1000 * time.Hour)
	e.ExpirePauses()
	if paused, until := e.Paused(tgt.Key); !paused || !until.IsZero() {
		t.Fatal("an indefinite pause expired")
	}
}

func TestScopeParsing(t *testing.T) {
	for in, want := range map[string]string{"p": "p", " p/s ": "p/s", "p/s/": "p/s"} {
		if got, err := ParseScope(in); err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "/", "a//b", "a/b/c"} {
		if _, err := ParseScope(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if !scopeMatches("p", "h/p/s/1") || !scopeMatches("p/s", "h/p/s/1") || scopeMatches("p", "h/pp/s/1") || scopeMatches("s", "h/p/s/1") {
		t.Fatal("scope matching")
	}
	if !scopeMatches("plain", "h/plain") { // a container outside compose is addressed by name
		t.Fatal("non-compose key")
	}
}

func TestRateLimitIsKeptAcrossARestart(t *testing.T) {
	ms := newMemStore()
	e, clk, _ := newEngineWith(t, nil, func(p *Params) { p.RateLimit = 2; p.Persist = ms })
	for _, k := range []string{"h/p/a/1", "h/p/b/1"} {
		tt := Target{Key: k}
		pl := e.Evaluate(tt, bad, cfg, hcObs)
		e.Begin(tt, pl, bad)
		e.End(tt, pl, Result{}, cfg)
	}
	clk.Advance(time.Minute)
	e2, _, rec2 := newEngineWith(t, clk, func(p *Params) { p.RateLimit = 2 })
	e2.Restore(ms.snapshot())
	if p := e2.Evaluate(Target{Key: "h/p/c/1"}, bad, cfg, hcObs); acted(p) || rec2.Count(RateLimited) != 1 {
		t.Fatalf("the budget was forgotten: %+v %v", p, rec2.Types())
	}
}

func TestStateSurvivesARestartOfTheEngine(t *testing.T) {
	ms := newMemStore()
	e, clk, _ := newEngineWith(t, nil, func(p *Params) { p.Persist = ms })
	suspendIt(t, e, clk)
	e.Pause("q", time.Hour)
	e.ObserveDockerEvent(Target{Key: "h/q/z/1"}, docEv("kill", clk.Now()))

	e2, _, rec2 := newEngineWith(t, clk, func(p *Params) { p.Persist = ms })
	e2.Restore(ms.snapshot())
	if !e2.Suspended(tgt.Key) {
		t.Fatal("SUSPENDED was lost")
	}
	if paused, _ := e2.Paused("h/q/z/1"); !paused {
		t.Fatal("the pause was lost")
	}
	if !e2.Evidence("h/q/z/1").ManualStop {
		t.Fatal("the manual-stop marker was lost")
	}
	// Still bad after the restart: nothing is tried again, the ladder does not start over.
	if p := e2.Evaluate(tgt, bad, cfg, hcObs); acted(p) || rec2.Count(IncidentOpened) != 0 {
		t.Fatalf("%+v %v", p, rec2.Types())
	}
	st := e2.Status()
	if len(st.Keys) == 0 || st.Keys[0].State != "SUSPENDED" || len(st.Pauses) != 1 {
		t.Fatalf("%+v", st)
	}
}

func TestClosedIncidentsReachTheHistory(t *testing.T) {
	ms := newMemStore()
	e, clk, _ := newEngineWith(t, nil, func(p *Params) { p.Persist = ms })
	run(e, bad, cfg, hcObs)
	clk.Advance(31 * time.Second)
	e.Evaluate(tgt, ok, cfg, hcObs)
	clk.Advance(11 * time.Minute)
	e.Evaluate(tgt, ok, cfg, hcObs)
	if len(ms.closed) != 1 || ms.closed[0].Outcome != "recovered" || ms.closed[0].Key != tgt.Key {
		t.Fatalf("%+v", ms.closed)
	}
}

func TestEventIDsAreMonotonicAndUTC(t *testing.T) {
	e, _, rec := newEngine(t, 0)
	p := e.Evaluate(tgt, bad, cfg, hcObs)
	e.Begin(tgt, p, bad)
	e.End(tgt, p, Result{}, cfg)
	var last uint64
	for _, ev := range rec.Events() {
		if ev.ID <= last || ev.Time.Location() != time.UTC || ev.Host != "h" || ev.Key != tgt.Key {
			t.Fatalf("%+v", ev)
		}
		last = ev.ID
	}
}

func TestAdvisoriesAreDeduped(t *testing.T) {
	e, _, rec := newEngine(t, 0)
	d := rules.Decision{Class: rules.ClassStaleParentGone, Action: rules.ActionNeedsRecreate, Reason: "gone"}
	for i := 0; i < 5; i++ {
		e.Evaluate(tgt, d, cfg, hcObs)
	}
	if rec.Count(NeedsRecreate) != 1 {
		t.Fatalf("%v", rec.Types())
	}
	e.Evaluate(tgt, ok, cfg, hcObs) // state changed...
	e.Evaluate(tgt, d, cfg, hcObs)  // ...and came back: say it again
	if rec.Count(NeedsRecreate) != 2 {
		t.Fatalf("%v", rec.Types())
	}
}

// eventsFixture loads a scrubbed events fixture. Its times are offsets from 0,
// so the caller rebases them onto the clock.
func eventsFixture(t *testing.T, name string) []docker.Event {
	f, err := os.Open("../../testdata/events/" + name + ".ndjson")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []docker.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		ev, err := docker.ParseEvent(sc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
	return out
}

func TestRealEventSequencesAndTheInFlightWindow(t *testing.T) {
	// e9-restart is `docker restart`: kill stop die start restart. Outside a
	// window the kill sets a marker and the start clears it. Inside our own
	// action's window it is attributed and sets nothing.
	run := func(inWindow bool) *Recorder {
		e, clk, rec := newEngine(t, 0)
		if inWindow {
			p := Plan{Act: rules.ActionRestart, Rung: "restart"}
			e.Evaluate(tgt, bad, cfg, hcObs)
			e.Begin(tgt, p, bad)
		}
		base := clk.Now()
		for _, ev := range eventsFixture(t, "e9-restart") {
			off := time.Duration(0) // a zero time is offset 0
			if !ev.Time.IsZero() {
				off = time.Duration(ev.Time.UnixNano())
			}
			ev.Time = base.Add(off)
			e.ObserveDockerEvent(tgt, ev)
		}
		return rec
	}
	out := run(false)
	if out.Count(ManualStopMarked) != 1 || out.Count(ManualStopCleared) != 1 || out.Count(EventAttributed) != 0 {
		t.Fatalf("outside a window: %v", out.Types())
	}
	in := run(true)
	if in.Count(ManualStopMarked) != 0 || in.Count(EventAttributed) < 4 {
		t.Fatalf("inside a window: %v", in.Types())
	}

	// `docker stop` by hand leaves the marker set, so an exit 1 is not healed.
	e, _, _ := newEngine(t, 0)
	for _, ev := range eventsFixture(t, "e9-stop") {
		e.ObserveDockerEvent(tgt, ev)
	}
	if !e.Evidence(tgt.Key).ManualStop {
		t.Fatal("docker stop should leave a manual-stop marker")
	}
	// A crash leaves none.
	e, _, _ = newEngine(t, 0)
	for _, ev := range eventsFixture(t, "e9-crash") {
		e.ObserveDockerEvent(tgt, ev)
	}
	if ev := e.Evidence(tgt.Key); ev.ManualStop || !ev.Crashed {
		t.Fatalf("%+v", ev)
	}
}

func TestRefuseDoesNotConsumeTheRung(t *testing.T) {
	e, _, rec := newEngine(t, 0)
	p := e.Evaluate(tgt, bad, cfg, hcObs)
	e.Refuse(tgt, bad, "parent vanished")
	if rec.Count(ActionRefused) != 1 {
		t.Fatal(rec.Types())
	}
	if p2 := e.Evaluate(tgt, bad, cfg, hcObs); p2.Act != p.Act {
		t.Fatalf("the rung must still be available after a refusal: %+v", p2)
	}
}
