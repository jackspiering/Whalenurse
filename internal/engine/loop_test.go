package engine

import (
	"fmt"
	"testing"
	"time"
)

func TestCrashLoopNeedsThreeIncreasesAndCrashloopAfter(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	e.Observe(tgt, Obs{RestartCount: 0}) // baseline
	clk.Advance(time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 1}); l.Active {
		t.Fatal("one restart is not a loop")
	}
	clk.Advance(time.Minute)
	e.Observe(tgt, Obs{RestartCount: 2})
	clk.Advance(time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 3}); l.Active {
		t.Fatalf("3 increases but the first only 2 minutes ago: %+v", l)
	}
	clk.Advance(2*time.Minute + 59*time.Second)
	if l := e.Observe(tgt, Obs{RestartCount: 3}); l.Active {
		t.Fatalf("just under CRASHLOOP_AFTER: %+v", l)
	}
	clk.Advance(2 * time.Second)
	if l := e.Observe(tgt, Obs{RestartCount: 3}); !l.Active || l.Increases != 3 {
		t.Fatalf("%+v", l)
	}
}

func TestCrashLoopCountsDeltasBetweenInspects(t *testing.T) {
	// One inspect that sees the count jump by 5 is 5 increases, not 1: two snapshots would say "changed once".
	e, clk, _ := newEngine(t, 0)
	e.Observe(tgt, Obs{})
	clk.Advance(time.Minute)
	e.Observe(tgt, Obs{RestartCount: 5})
	clk.Advance(5 * time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 5}); !l.Active || l.Increases != 5 {
		t.Fatalf("%+v", l)
	}
}

func TestCrashLoopClearsAfterRecoveryPeriodWithoutAnIncrease(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	e.Observe(tgt, Obs{})
	for i := 1; i <= 4; i++ {
		clk.Advance(2 * time.Minute)
		e.Observe(tgt, Obs{RestartCount: i})
	}
	clk.Advance(5 * time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 4}); !l.Active {
		t.Fatalf("a loop still going 5 minutes after the last restart: %+v", l)
	}
	clk.Advance(5*time.Minute + time.Second) // RECOVERY_PERIOD (10m) since the last increase
	if l := e.Observe(tgt, Obs{RestartCount: 4}); l.Active {
		t.Fatalf("not cleared after RECOVERY_PERIOD: %+v", l)
	}
	// And the count starts from zero again.
	clk.Advance(time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 5}); l.Active {
		t.Fatalf("%+v", l)
	}
}

func TestCrashLoopResetsWhenTheContainerIsRecreated(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	e.Observe(tgt, Obs{})
	for i := 1; i <= 3; i++ {
		clk.Advance(time.Minute)
		e.Observe(tgt, Obs{RestartCount: i})
	}
	clk.Advance(5 * time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 0}); l.Active { // a new container: the count restarted
		t.Fatalf("%+v", l)
	}
}

// Pinned on purpose (an open question for the maintainer, see docs/m2a-notes.md): taken literally, rule 3 calls
// three quick restarts followed by a long quiet a loop once CRASHLOOP_AFTER has passed since the first of them,
// and keeps calling it one until RECOVERY_PERIOD has passed since the last.
func TestCrashLoopLiteralEdgeThreeQuickRestartsThenQuiet(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	e.Observe(tgt, Obs{})
	clk.Advance(10 * time.Second)
	e.Observe(tgt, Obs{RestartCount: 3}) // three restarts at once, then stable
	clk.Advance(5*time.Minute + 10*time.Second)
	if l := e.Observe(tgt, Obs{RestartCount: 3}); !l.Active {
		t.Fatalf("the literal rule says loop: %+v", l)
	}
	clk.Advance(5 * time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 3}); l.Active {
		t.Fatalf("cleared 10 minutes after the last increase: %+v", l)
	}
}

// The verdict must not depend on how often the container is inspected: for a container that restarts every
// 15 s, any inspect interval says "loop" no earlier than CRASHLOOP_AFTER after it began and no later than a
// couple of intervals after that.
func TestCrashLoopVerdictDoesNotDependOnTheInspectRate(t *testing.T) {
	const period = 15 * time.Second
	for _, step := range []time.Duration{5 * time.Second, 15 * time.Second, 40 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			e, clk, _ := newEngine(t, 0)
			t0 := clk.Now()
			e.Observe(tgt, Obs{}) // baseline before the loop starts
			var detected time.Duration = -1
			for elapsed := time.Duration(0); elapsed <= 30*time.Minute && detected < 0; {
				clk.Advance(step)
				elapsed += step
				n := int(elapsed / period) // restarts so far
				finished := t0.Add(time.Duration(n) * period)
				if l := e.Observe(tgt, Obs{RestartCount: n, FinishedAt: finished}); l.Active {
					detected = elapsed
				}
			}
			if detected < 0 {
				t.Fatal("never detected")
			}
			if detected < 5*time.Minute {
				t.Fatalf("detected after %s: before CRASHLOOP_AFTER", detected)
			}
			if limit := 5*time.Minute + 2*step + period; detected > limit { // the first increase is only noticed up to one interval late, and the verdict one more
				t.Fatalf("detected after %s, want within %s", detected, limit)
			}
		})
	}
}

func TestTwoRestartsAreNeverALoopHoweverOftenWeLook(t *testing.T) {
	for _, step := range []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute} {
		e, clk, _ := newEngine(t, 0)
		e.Observe(tgt, Obs{})
		for i := 0; i < 20; i++ {
			clk.Advance(step)
			n := 0
			if i >= 1 {
				n = 2
			}
			if l := e.Observe(tgt, Obs{RestartCount: n}); l.Active {
				t.Fatalf("step %s: 2 restarts reported as a loop: %+v", step, l)
			}
		}
	}
}

func TestLoopStateSurvivesARestart(t *testing.T) {
	ms := newMemStore()
	e, clk, _ := newEngineWith(t, nil, func(p *Params) { p.Persist = ms })
	e.Observe(tgt, Obs{})
	for i := 1; i <= 3; i++ {
		clk.Advance(time.Minute)
		e.Observe(tgt, Obs{RestartCount: i})
	}
	e2, _, _ := newEngineWith(t, clk, nil)
	e2.Restore(ms.snapshot())
	clk.Advance(4 * time.Minute)
	if l := e2.Observe(tgt, Obs{RestartCount: 4}); !l.Active || l.Increases != 4 {
		t.Fatalf("the loop count was lost across a restart: %+v", l)
	}
}
