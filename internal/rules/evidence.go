package rules

import (
	"strings"
	"time"
)

// QuarantineWindow is how far back a stream loss reaches: the daemon's shutdown
// limit is 15 s, plus the stop timeouts (plan §3.3, *Manual-stop markers*, mechanism a).
const QuarantineWindow = 30 * time.Second

// Evidence is what the event stream has told us about how a container stopped.
// It exists only for the `crashed` heal-exited mode and for manual-stop
// markers; the decision itself is still made by inspecting the container.
type Evidence struct {
	Known      bool // at least one lifecycle event was seen since the last start
	ManualStop bool // a kill preceded the die (docker stop, docker kill): the manual-stop marker
	Crashed    bool // a die with no preceding kill
	OOM        bool // an oom event was seen
}

// Verdict names what an event sequence amounts to, for tests and logs.
type Verdict string

const (
	VerdictNone         Verdict = "none"
	VerdictRunning      Verdict = "running"         // ends with a start; markers cleared
	VerdictManualStop   Verdict = "manual-stop"     // kill ... die
	VerdictShutdownKill Verdict = "shutdown-killed" // the kills were quarantined by a stream loss: no marker (M2a)
	VerdictCrash        Verdict = "crash"           // die without kill
	VerdictOOM          Verdict = "oom"             // oom ... die
	VerdictUnhealthy    Verdict = "unhealthy"       // health_status: unhealthy (trigger only)
	VerdictRecreated    Verdict = "recreated"       // destroy seen: a new container took over
	VerdictCrashedLoop  Verdict = "crash-loop"      // several die/start cycles with no kill
)

// StreamLost is the pseudo-event Summarize understands for "the events stream
// ended here for a reason other than whalenurse stopping" (EOF or an error).
const StreamLost = "stream_lost"

// Tracker folds container events into Evidence for one state key. Feed it the
// events of that key in order, minus any attributed to a whalenurse action.
type Tracker struct {
	ev          Evidence
	markedAt    time.Time // when the manual-stop marker was set; zero if none or unknown
	quarantined bool      // a marker was discarded by a stream loss and no start has followed
	recreated   bool
	unhealthy   bool
	cycles      int
}

// Apply consumes one event with no timestamp. action and detail are the split
// form of the Docker action ("health_status: unhealthy" is "health_status", "unhealthy").
func (t *Tracker) Apply(action, detail string) { t.ApplyAt(action, detail, time.Time{}) }

// ApplyAt is Apply with the time of the event, which a stream loss needs to
// decide whether a marker is recent enough to be a shutdown kill.
func (t *Tracker) ApplyAt(action, detail string, at time.Time) {
	switch action {
	case "kill":
		t.ev.Known = true
		if !t.ev.ManualStop {
			t.markedAt = at
		}
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
		// A start (manual or by the restart policy) ends the previous stop.
		t.ev = Evidence{}
		t.markedAt = time.Time{}
		t.quarantined = false
		t.unhealthy = false
	case "destroy":
		t.recreated = true
	case "health_status":
		t.unhealthy = strings.EqualFold(detail, "unhealthy")
	case StreamLost:
		t.Quarantine(at.Add(-QuarantineWindow))
	}
}

// Evidence returns the current evidence.
func (t *Tracker) Evidence() Evidence { return t.ev }

// Cleared reports whether no marker is set (a manual stop has not been seen).
func (t *Tracker) Cleared() bool { return !t.ev.ManualStop }

// MarkedAt returns when the marker was set; zero if there is none.
func (t *Tracker) MarkedAt() time.Time {
	if !t.ev.ManualStop {
		return time.Time{}
	}
	return t.markedAt
}

// SetMarker restores a persisted manual-stop marker.
func (t *Tracker) SetMarker(at time.Time) {
	t.ev = Evidence{Known: true, ManualStop: true}
	t.markedAt = at
}

// Quarantine discards the evidence of a stop that happened at or after since
// (plan §3.3, mechanism a). The stream ended without a clean goodbye, so a kill
// this recent may be the daemon shutting down, not a person. The container is
// then judged by rule 4 without evidence, which for a signal exit is
// notify-only: the safe outcome. A marker older than since survives.
// It reports whether anything was discarded.
func (t *Tracker) Quarantine(since time.Time) bool {
	if !t.ev.ManualStop || t.markedAt.Before(since) {
		return false
	}
	t.ev = Evidence{}
	t.markedAt = time.Time{}
	t.quarantined = true
	return true
}

// Drop discards the marker unconditionally (the boot id changed, mechanism b).
func (t *Tracker) Drop() bool {
	if !t.ev.ManualStop {
		return false
	}
	t.ev = Evidence{}
	t.markedAt = time.Time{}
	t.quarantined = true
	return true
}

// EventLine is one event for Summarize. At is optional; StreamLost needs it.
type EventLine struct {
	Action, Detail string
	At             time.Time
}

// Summarize reduces an ordered event sequence to a verdict. Exec and
// unrelated events are ignored. A StreamLost line stands for the events stream
// ending there.
func Summarize(seq []EventLine) Verdict {
	var t Tracker
	last := VerdictNone
	dies := 0
	for _, e := range seq {
		if strings.HasPrefix(e.Action, "exec_") {
			continue
		}
		t.ApplyAt(e.Action, e.Detail, e.At)
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
	case t.quarantined:
		return VerdictShutdownKill
	case ev.Crashed && dies >= 3:
		return VerdictCrashedLoop
	case ev.Crashed:
		return VerdictCrash
	}
	return VerdictNone
}
