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
	// An inspect that sees the count jump by 3 is 3 increases, not 1: two snapshots would say "changed once".
	e, clk, _ := newEngine(t, 0)
	e.Observe(tgt, Obs{})
	clk.Advance(time.Minute)
	e.Observe(tgt, Obs{RestartCount: 3})
	clk.Advance(2 * time.Minute)
	e.Observe(tgt, Obs{RestartCount: 5})
	clk.Advance(3 * time.Minute) // the first increase is 6 minutes old, the latest 3
	if l := e.Observe(tgt, Obs{RestartCount: 5}); !l.Active || l.Increases != 5 {
		t.Fatalf("%+v", l)
	}
}

func TestCrashLoopNeedsALatestIncreaseWithinCrashloopAfterAndClearsAfterRecoveryPeriod(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	e.Observe(tgt, Obs{})
	for i := 1; i <= 4; i++ {
		clk.Advance(2 * time.Minute)
		e.Observe(tgt, Obs{RestartCount: i})
	}
	clk.Advance(4 * time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 4}); !l.Active {
		t.Fatalf("a loop that restarted 4 minutes ago is still a loop: %+v", l)
	}
	clk.Advance(61 * time.Second) // 5m1s since the last restart: quiet for CRASHLOOP_AFTER, so not a loop any more
	if l := e.Observe(tgt, Obs{RestartCount: 4}); l.Active {
		t.Fatalf("no restart for CRASHLOOP_AFTER, yet still a loop: %+v", l)
	}
	// ...but the count is kept until RECOVERY_PERIOD: one more restart 6 minutes after the last resumes the loop.
	clk.Advance(time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 5}); !l.Active || l.Increases != 5 {
		t.Fatalf("%+v", l)
	}
	// After RECOVERY_PERIOD without an increase it starts from zero: one restart is not a loop.
	clk.Advance(10*time.Minute + time.Second)
	e.Observe(tgt, Obs{RestartCount: 5})
	clk.Advance(time.Minute)
	if l := e.Observe(tgt, Obs{RestartCount: 6}); l.Active {
		t.Fatalf("not cleared after RECOVERY_PERIOD: %+v", l)
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

// Plan v3.5 refines rule 3: the latest increase must be within CRASHLOOP_AFTER too. Three quick restarts
// followed by a long quiet were a loop for a while under the literal v3.4 text (and a stable container could
// be restarted and SUSPENDED); now they never are. Restarts that continue to within CRASHLOOP_AFTER still are.
func TestCrashLoopThreeQuickRestartsThenQuietIsNotALoop(t *testing.T) {
	e, clk, _ := newEngine(t, 0)
	e.Observe(tgt, Obs{})
	clk.Advance(10 * time.Second)
	e.Observe(tgt, Obs{RestartCount: 3}) // three restarts seen at once, then stable
	start := clk.Now()
	for _, wait := range []time.Duration{time.Minute, 4 * time.Minute, time.Minute, 2 * time.Minute} {
		clk.Advance(wait)
		if l := e.Observe(tgt, Obs{RestartCount: 3}); l.Active {
			t.Fatalf("a quiet container was called a loop %s after its restarts: %+v", clk.Now().Sub(start), l)
		}
	}

	// Spread over four minutes, then quiet: a loop from the 5th minute until 5 minutes after the last restart.
	e, clk, _ = newEngine(t, 0)
	e.Observe(tgt, Obs{})
	for i := 1; i <= 3; i++ {
		e.Observe(tgt, Obs{RestartCount: i})
		clk.Advance(2 * time.Minute)
	}
	// restarts at +0, +2m, +4m; now +6m
	if l := e.Observe(tgt, Obs{RestartCount: 3}); !l.Active {
		t.Fatalf("%+v", l)
	}
	clk.Advance(3*time.Minute + time.Second) // +9m1s: the last restart was 5m1s ago
	if l := e.Observe(tgt, Obs{RestartCount: 3}); l.Active {
		t.Fatalf("%+v", l)
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
