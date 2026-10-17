package rules

import "strings"

// Evidence is what the event stream has told us about how a container stopped.
// It exists only for the `crashed` heal-exited mode and for manual-stop
// markers; the decision itself is still made by inspecting the container.
type Evidence struct {
	Known      bool // at least one lifecycle event was seen since the last start
	ManualStop bool // a kill preceded the die (docker stop, docker kill, daemon shutdown)
	Crashed    bool // a die with no preceding kill
	OOM        bool // an oom event was seen
}

// Verdict names what an event sequence amounts to, for tests and logs.
type Verdict string

const (
	VerdictNone        Verdict = "none"
	VerdictRunning     Verdict = "running"     // ends with a start; markers cleared
	VerdictManualStop  Verdict = "manual-stop" // kill ... die
	VerdictCrash       Verdict = "crash"       // die without kill
	VerdictOOM         Verdict = "oom"         // oom ... die
	VerdictUnhealthy   Verdict = "unhealthy"   // health_status: unhealthy (trigger only)
	VerdictRecreated   Verdict = "recreated"   // destroy seen: a new container took over
	VerdictCrashedLoop Verdict = "crash-loop"  // several die/start cycles with no kill
)

// Tracker folds container events into Evidence for one state key. Feed it the
// events of that key in order, minus any attributed to a whalenurse action.
type Tracker struct {
	ev        Evidence
	recreated bool
	unhealthy bool
	cycles    int
}

// Apply consumes one event. action and detail are the split form of the
// Docker action ("health_status: unhealthy" is "health_status", "unhealthy").
func (t *Tracker) Apply(action, detail string) {
	switch action {
	case "kill":
		t.ev.Known = true
		t.ev.ManualStop = true
	case "oom":
		t.ev.Known = true
		t.ev.OOM = true
	case "die":
		t.ev.Known = true
		if !t.ev.ManualStop {
			t.ev.Crashed = true
			t.cycles++
		}
	case "start":
		// A start (manual or by the restart policy) ends the previous stop:
		// the daemon's boot restart clears the shutdown kill markers this way.
		t.ev = Evidence{}
		t.unhealthy = false
	case "destroy":
		t.recreated = true
	case "health_status":
		t.unhealthy = strings.EqualFold(detail, "unhealthy")
	}
}

// Evidence returns the current evidence.
func (t *Tracker) Evidence() Evidence { return t.ev }

// Cleared reports whether no marker is set (a manual stop has not been seen).
func (t *Tracker) Cleared() bool { return !t.ev.ManualStop }

// EventLine is one event for Summarize.
type EventLine struct{ Action, Detail string }

// Summarize reduces an ordered event sequence to a verdict. Exec and
// unrelated events are ignored.
func Summarize(seq []EventLine) Verdict {
	var t Tracker
	last := VerdictNone
	dies := 0
	for _, e := range seq {
		if strings.HasPrefix(e.Action, "exec_") {
			continue
		}
		t.Apply(e.Action, e.Detail)
		switch e.Action {
		case "die":
			dies++
			last = VerdictNone
		case "start":
			last = VerdictRunning
		case "health_status":
			if t.unhealthy {
				last = VerdictUnhealthy
			}
		}
	}
	ev := t.Evidence()
	switch {
	case t.recreated && last == VerdictRunning:
		return VerdictRecreated
	case last == VerdictRunning || last == VerdictUnhealthy:
		return last
	case ev.OOM:
		return VerdictOOM
	case ev.ManualStop:
		return VerdictManualStop
	case ev.Crashed && dies >= 3:
		return VerdictCrashedLoop
	case ev.Crashed:
		return VerdictCrash
	}
	return VerdictNone
}
