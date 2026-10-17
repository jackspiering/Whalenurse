package engine

import (
	"time"

	"github.com/jackspiering/whalenurse/internal/rules"
)

// crashLoopIncreases is how many RestartCount increases make a crash loop (rule 3).
const crashLoopIncreases = 3

// Obs is what one inspect says besides the rules' verdict.
type Obs struct {
	RestartCount   int
	FinishedAt     time.Time // when the previous run ended: the best timestamp for a new increase
	HasHealthcheck bool
}

// Observe feeds one inspect into the crash-loop tracker of the key and returns
// the verdict for rule 3: at least 3 RestartCount increases, the first at least
// CRASHLOOP_AFTER ago and the latest less than CRASHLOOP_AFTER ago; the count is
// cleared once RECOVERY_PERIOD has passed without a new increase.
//
// The rule counts increases between inspects (the sum of the deltas), so the
// poll interval and GRACE decide only how late it notices, not whether. An
// increase is stamped with the previous run's FinishedAt when that falls
// between the two inspects, otherwise with the time of the inspect that saw it.
// Both only ever delay the verdict, never bring it forward.
func (e *Engine) Observe(t Target, o Obs) rules.Loop {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.state(t.Key)
	now := e.clk.Now()
	l := &st.loop
	before := *l

	switch {
	case !l.Have || o.RestartCount < l.LastCount:
		// First sight, or the count went down: the container was recreated.
		*l = LoopRecord{Have: true, LastCount: o.RestartCount}
	case o.RestartCount > l.LastCount:
		stamp := now
		if !o.FinishedAt.IsZero() && !o.FinishedAt.Before(l.LastObs) && !o.FinishedAt.After(now) {
			stamp = o.FinishedAt
		}
		if l.N == 0 {
			l.First = stamp
		}
		l.N += o.RestartCount - l.LastCount
		l.Last = stamp
		l.LastCount = o.RestartCount
	}
	if l.N > 0 && now.Sub(l.Last) >= e.p.RecoveryPeriod {
		*l = LoopRecord{Have: true, LastCount: o.RestartCount}
	}
	l.LastObs = now

	// Persist when the counting changed; LastObs alone is not worth a write.
	if before.Have != l.Have || before.LastCount != l.LastCount || before.N != l.N {
		st.dirty = true
	}
	e.flush(t.Key, st)

	// At least 3 increases, the first at least CRASHLOOP_AFTER ago, and the latest within CRASHLOOP_AFTER: a few
	// quick restarts followed by a long quiet are not a loop (plan §3.3 rule 3, v3.5).
	if l.N >= crashLoopIncreases && now.Sub(l.First) >= e.p.CrashloopAfter && now.Sub(l.Last) < e.p.CrashloopAfter {
		return rules.Loop{Active: true, Since: l.First, Increases: l.N}
	}
	return rules.Loop{}
}
