// Package engine holds the per-container state machine: incidents, the ladder,
// the rate limit, the action-in-flight window and the manual-stop markers.
// It is pure. It makes no Docker calls and takes its time from an injected
// clock; the daemon feeds it decisions and performs the actions it returns.
//
// M1 implements rung 1 (restart) only. The state kept here is in memory and a
// minimal stand-in for what M2 persists: an incident runs the restart rung
// once, waits the cooldown, and if the container is still bad it gives up
// (the GaveUp event; M2's SUSPENDED) until the container recovers, is
// recreated, or is started by hand after a manual stop.
// The engine is not safe for concurrent use; the daemon drives it from one goroutine.
package engine

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jackspiering/whalenurse/internal/clock"
	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/docker"
	"github.com/jackspiering/whalenurse/internal/rules"
)

// Params configures an Engine.
type Params struct {
	Host         string
	BaseCooldown time.Duration
	RateLimit    int           // actions per hour; 0 disables the limit
	InflightTail time.Duration // how long after an action its events are still attributed to it
	DryRun       bool          // only labels events; the no-op actor is wired by the caller
}

// Target identifies the container an evaluation is about.
type Target struct {
	Key  string
	ID   string
	Name string
}

// Plan is what the daemon should do after Evaluate.
type Plan struct {
	Act       rules.Action // ActionNone, ActionRestart or ActionStart
	Rung      string
	RecheckAt time.Time // inspect again then (cooldown); zero for none
}

// Result is the outcome of an action.
type Result struct {
	Err    error
	DryRun bool
}

type window struct{ start, end time.Time } // end zero: still open

type incident struct {
	opened        time.Time
	class         rules.Class
	rung          int // index into the configured rungs of the next rung to run
	cooldownUntil time.Time
	exhausted     bool
	rateNoted     bool
}

type keyState struct {
	incident *incident
	windows  []window
	tracker  rules.Tracker
	advisory string // dedupe key of the last advisory event
	name     string
}

// Engine is the state machine for all keys on one host.
type Engine struct {
	p     Params
	clk   clock.Clock
	bus   *Bus
	keys  map[string]*keyState
	spent []time.Time // timestamps of actions in the last hour
}

// New creates an Engine that publishes to bus.
func New(p Params, clk clock.Clock, bus *Bus) *Engine {
	if p.InflightTail == 0 {
		p.InflightTail = 2 * time.Second
	}
	return &Engine{p: p, clk: clk, bus: bus, keys: map[string]*keyState{}}
}

func (e *Engine) state(key string) *keyState {
	st, ok := e.keys[key]
	if !ok {
		st = &keyState{}
		e.keys[key] = st
	}
	return st
}

func (e *Engine) emit(t Target, typ EventType, msg string, attrs map[string]string) {
	e.bus.Publish(Event{Time: e.clk.Now(), Host: e.p.Host, Key: t.Key, Container: t.Name, Type: typ, Message: msg, Attrs: attrs})
}

// Busy reports whether the key has an open incident or an action in flight.
// The rules use it to hold back the dependents of a busy parent.
func (e *Engine) Busy(key string) bool {
	st, ok := e.keys[key]
	if !ok {
		return false
	}
	if st.incident != nil && !st.incident.exhausted {
		return true
	}
	for _, w := range st.windows {
		if w.end.IsZero() {
			return true
		}
	}
	return false
}

// IncidentOpen reports whether the key has an incident, and whether the ladder
// gave up on it.
func (e *Engine) IncidentOpen(key string) (open, gaveUp bool) {
	if st, ok := e.keys[key]; ok && st.incident != nil {
		return true, st.incident.exhausted
	}
	return false, false
}

// Evidence returns what the events so far say about how the key stopped.
func (e *Engine) Evidence(key string) rules.Evidence {
	if st, ok := e.keys[key]; ok {
		return st.tracker.Evidence()
	}
	return rules.Evidence{}
}

// Forget drops all state for a key whose container no longer exists.
func (e *Engine) Forget(t Target) {
	if st, ok := e.keys[t.Key]; ok {
		if st.incident != nil {
			e.emit(t, IncidentClosed, "container is gone", map[string]string{"reason": "gone"})
		}
		delete(e.keys, t.Key)
	}
}

// ObserveDockerEvent records a Docker event for a key. Events that fall inside
// an action's in-flight window are attributed to the action and not
// interpreted: they set no manual-stop marker, clear nothing and open nothing.
// It reports whether the event was attributed.
func (e *Engine) ObserveDockerEvent(t Target, ev docker.Event) (attributed bool) {
	st := e.state(t.Key)
	at := ev.Time
	if at.IsZero() {
		at = e.clk.Now()
	}
	e.prune(st)
	for _, w := range st.windows {
		if !at.Before(w.start) && (w.end.IsZero() || !at.After(w.end)) {
			e.emit(t, EventAttributed, "docker event during a whalenurse action", map[string]string{"action": ev.Action})
			return true
		}
	}

	wasManual := st.tracker.Evidence().ManualStop
	st.tracker.Apply(ev.Action, ev.Detail)
	isManual := st.tracker.Evidence().ManualStop
	switch {
	case !wasManual && isManual:
		e.emit(t, ManualStopMarked, "kill seen outside any action: container stopped by hand", map[string]string{"action": ev.Action})
	case wasManual && !isManual:
		e.emit(t, ManualStopCleared, "start seen: manual-stop marker cleared", nil)
	}

	// Leaving the gave-up state: a recreate by someone else, or a start by hand
	// after a manual stop. A plain start (the restart policy at work) does not
	// count, or every policy restart would re-arm the ladder.
	switch ev.Action {
	case "create":
		e.reset(t, st, "container was recreated outside whalenurse")
	case "start":
		if wasManual {
			e.reset(t, st, "started by hand after a manual stop")
		}
	}
	return false
}

func (e *Engine) reset(t Target, st *keyState, why string) {
	if st.incident != nil && st.incident.exhausted {
		e.emit(t, Unsuspended, why, map[string]string{"reason": why})
		st.incident = nil
	} else if st.incident != nil && why == "container was recreated outside whalenurse" {
		e.emit(t, IncidentClosed, why, map[string]string{"reason": "recreated"})
		st.incident = nil
	}
	st.advisory = ""
}

func (e *Engine) prune(st *keyState) {
	cut := e.clk.Now().Add(-time.Minute)
	kept := st.windows[:0]
	for _, w := range st.windows {
		if w.end.IsZero() || w.end.After(cut) {
			kept = append(kept, w)
		}
	}
	st.windows = kept
}

// Evaluate feeds the engine one classification and returns what to do. The
// caller must have inspected the container no earlier than GRACE after the
// trigger.
func (e *Engine) Evaluate(t Target, d rules.Decision, cfg config.Container) Plan {
	now := e.clk.Now()
	st := e.state(t.Key)
	st.name = t.Name
	e.prune(st)

	switch d.Action {
	case rules.ActionRestart, rules.ActionStart:
		return e.heal(t, st, d, cfg, now)
	case rules.ActionNeedsRecreate:
		e.advise(t, st, d, NeedsRecreate)
	case rules.ActionNeedsDependentRestart:
		e.advise(t, st, d, NeedsDependentRestart)
	case rules.ActionWaitForParent:
		e.advise(t, st, d, WaitingForParent)
	case rules.ActionNotify:
		e.advise(t, st, d, NotifyOnly)
	default: // ActionNone
		st.advisory = ""
		return e.settle(t, st, d, cfg, now)
	}
	return Plan{}
}

// settle handles a decision that needs no action while an incident may be open.
func (e *Engine) settle(t Target, st *keyState, d rules.Decision, cfg config.Container, now time.Time) Plan {
	inc := st.incident
	if inc == nil || d.Class == rules.ClassSkip {
		return Plan{}
	}
	switch d.Class {
	case rules.ClassOK:
		e.emit(t, Recovered, "container is healthy again", map[string]string{"opened": inc.opened.UTC().Format(time.RFC3339)})
		st.incident = nil
	case rules.ClassStarting, rules.ClassRestarting:
		// Not recovered yet and not failing: look again after the cooldown.
		return Plan{RecheckAt: now.Add(config.CooldownFor(e.p.BaseCooldown, cfg.Cooldown, 0))}
	default:
		// Stopped on purpose, or in a state that needs no action: the incident is over.
		e.emit(t, IncidentClosed, d.Reason, map[string]string{"reason": string(d.Class)})
		st.incident = nil
	}
	return Plan{}
}

// advise emits an advisory event once per (class, action) until the state changes.
func (e *Engine) advise(t Target, st *keyState, d rules.Decision, typ EventType) {
	sig := string(d.Class) + "/" + string(d.Action)
	if st.advisory == sig {
		return
	}
	st.advisory = sig
	attrs := map[string]string{"class": string(d.Class)}
	if d.Cause != rules.CauseNone {
		attrs["cause"] = string(d.Cause)
	}
	e.emit(t, typ, d.Reason, attrs)
}

func (e *Engine) heal(t Target, st *keyState, d rules.Decision, cfg config.Container, now time.Time) Plan {
	st.advisory = ""
	if st.incident == nil {
		st.incident = &incident{opened: now, class: d.Class}
		e.emit(t, IncidentOpened, d.Reason, map[string]string{"class": string(d.Class), "action": string(d.Action)})
	}
	inc := st.incident
	if inc.exhausted {
		return Plan{}
	}
	for _, w := range st.windows {
		if w.end.IsZero() {
			return Plan{} // an action is running; its result will schedule the next look
		}
	}
	if now.Before(inc.cooldownUntil) {
		return Plan{RecheckAt: inc.cooldownUntil}
	}

	for inc.rung < len(cfg.Actions) {
		r := cfg.Actions[inc.rung]
		if r == config.RungRestart {
			if !e.rateOK(now) {
				if !inc.rateNoted {
					inc.rateNoted = true
					e.emit(t, RateLimited, fmt.Sprintf("rate limit of %d actions/hour reached; will retry", e.p.RateLimit), nil)
				}
				return Plan{RecheckAt: e.nextSlot(now)}
			}
			inc.rateNoted = false
			return Plan{Act: d.Action, Rung: r}
		}
		e.emit(t, RungUnavailable, fmt.Sprintf("rung %q needs M2 or later; skipped", r), map[string]string{"rung": r})
		inc.rung++
	}
	inc.exhausted = true
	msg := "restart did not help"
	if inc.rung == 0 {
		msg = "no usable rung configured"
	}
	e.emit(t, GaveUp, msg+": "+d.Reason+" (recreate arrives in M2)", map[string]string{"class": string(d.Class), "held": "until recovery"})
	return Plan{}
}

// Begin opens the in-flight window for an action and returns when it started.
func (e *Engine) Begin(t Target, plan Plan, d rules.Decision) {
	now := e.clk.Now()
	st := e.state(t.Key)
	st.windows = append(st.windows, window{start: now})
	e.spent = append(e.spent, now)
	e.emit(t, ActionStarted, string(plan.Act)+": "+d.Reason, e.actionAttrs(plan, false))
}

// End closes the window, records the result and returns when to look again.
func (e *Engine) End(t Target, plan Plan, res Result, cfg config.Container) (recheck time.Time) {
	now := e.clk.Now()
	st := e.state(t.Key)
	for i := range st.windows {
		if st.windows[i].end.IsZero() {
			st.windows[i].end = now.Add(e.p.InflightTail)
		}
	}
	msg := "ok"
	if res.Err != nil {
		msg = "failed: " + res.Err.Error()
	}
	attrs := e.actionAttrs(plan, res.Err == nil)
	if res.DryRun {
		attrs["dry_run"] = "true"
		msg = "dry-run: would " + string(plan.Act)
	}
	e.emit(t, ActionResult, string(plan.Act)+" "+msg, attrs)
	inc := st.incident
	if inc == nil {
		return time.Time{}
	}
	cd := config.CooldownFor(e.p.BaseCooldown, cfg.Cooldown, inc.rung)
	inc.rung++
	inc.cooldownUntil = now.Add(cd)
	return inc.cooldownUntil
}

// Refuse cancels a planned action whose pre-action check failed. The rung is
// not consumed and no rate-limit slot is spent (Begin was never called).
func (e *Engine) Refuse(t Target, d rules.Decision, why string) {
	e.emit(t, ActionRefused, why, map[string]string{"class": string(d.Class)})
}

func (e *Engine) actionAttrs(plan Plan, ok bool) map[string]string {
	a := map[string]string{"action": string(plan.Act), "rung": plan.Rung, "ok": strconv.FormatBool(ok)}
	if e.p.DryRun {
		a["dry_run"] = "true"
	}
	return a
}

func (e *Engine) rateOK(now time.Time) bool {
	if e.p.RateLimit <= 0 {
		return true
	}
	cut := now.Add(-time.Hour)
	kept := e.spent[:0]
	for _, t := range e.spent {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	e.spent = kept
	return len(e.spent) < e.p.RateLimit
}

func (e *Engine) nextSlot(now time.Time) time.Time {
	if len(e.spent) == 0 {
		return now.Add(time.Minute)
	}
	return e.spent[0].Add(time.Hour + time.Second)
}
