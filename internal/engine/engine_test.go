package engine

import (
	"bufio"
	"os"
	"testing"
	"time"

	"github.com/jackspiering/whalenurse/internal/clock"
	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/docker"
	"github.com/jackspiering/whalenurse/internal/rules"
)

func newEngine(t *testing.T, rate int) (*Engine, *clock.Fake, *Recorder) {
	clk := clock.NewFake(time.Time{})
	bus := &Bus{}
	rec := &Recorder{}
	bus.Subscribe(rec.Record)
	return New(Params{Host: "h", BaseCooldown: 30 * time.Second, RateLimit: rate}, clk, bus), clk, rec
}

var (
	tgt = Target{Key: "h/p/s/1", ID: "abc", Name: "p-s-1"}
	cfg = config.Container{OptedIn: true, Actions: []string{"restart", "recreate"}, HealExited: config.HealCrashed}
	bad = rules.Decision{Class: rules.ClassUnhealthy, Action: rules.ActionRestart, Reason: "unhealthy"}
	ok  = rules.Decision{Class: rules.ClassOK, Action: rules.ActionNone, Reason: "running"}
)

func TestLadderStepsAndCooldown(t *testing.T) {
	e, clk, rec := newEngine(t, 0)
	p := e.Evaluate(tgt, bad, cfg)
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
	if p := e.Evaluate(tgt, bad, cfg); p.Act != "" && p.Act != rules.ActionNone || p.RecheckAt.IsZero() {
		t.Fatalf("%+v", p)
	}
	clk.Advance(21 * time.Second)
	if p := e.Evaluate(tgt, bad, cfg); p.Act != "" && p.Act != rules.ActionNone {
		t.Fatalf("second pass must not restart again: %+v", p)
	}
	if rec.Count(GaveUp) != 1 || rec.Count(RungUnavailable) != 1 {
		t.Fatalf("%v", rec.Types())
	}
	if open, gave := e.IncidentOpen(tgt.Key); !open || !gave {
		t.Fatal("incident should be open and gave up")
	}
	if e.Busy(tgt.Key) {
		t.Fatal("a gave-up incident does not hold back dependents; there is nothing left to wait for")
	}
	// Recovery.
	e.Evaluate(tgt, ok, cfg)
	if rec.Count(Recovered) != 1 {
		t.Fatalf("%v", rec.Types())
	}
	if open, _ := e.IncidentOpen(tgt.Key); open {
		t.Fatal("incident should be closed")
	}
}

func TestEventIDsAreMonotonicAndUTC(t *testing.T) {
	e, _, rec := newEngine(t, 0)
	p := e.Evaluate(tgt, bad, cfg)
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
		e.Evaluate(tgt, d, cfg)
	}
	if rec.Count(NeedsRecreate) != 1 {
		t.Fatalf("%v", rec.Types())
	}
	e.Evaluate(tgt, ok, cfg) // state changed...
	e.Evaluate(tgt, d, cfg)  // ...and came back: say it again
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
			e.Evaluate(tgt, bad, cfg)
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
	p := e.Evaluate(tgt, bad, cfg)
	e.Refuse(tgt, bad, "parent vanished")
	if rec.Count(ActionRefused) != 1 {
		t.Fatal(rec.Types())
	}
	if p2 := e.Evaluate(tgt, bad, cfg); p2.Act != p.Act {
		t.Fatalf("the rung must still be available after a refusal: %+v", p2)
	}
}
