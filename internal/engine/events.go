package engine

import (
	"sync"
	"time"
)

// EventType names a domain event. Store, notify and the API subscribe to one
// stream of these (plan §5), so history and notifications cannot disagree.
type EventType string

const (
	IncidentOpened        EventType = "IncidentOpened"
	IncidentClosed        EventType = "IncidentClosed" // closed without recovery: gone, stopped by hand, recreated
	ActionStarted         EventType = "ActionStarted"
	ActionResult          EventType = "ActionResult"
	RungUnavailable       EventType = "RungUnavailable" // a configured rung does not exist yet: recreate is M2b
	Suspended             EventType = "Suspended"       // the ladder is spent, or the key is flapping; nothing more is tried
	Recovered             EventType = "Recovered"       // recovered for RECOVERY_PERIOD: the incident closes
	Unsuspended           EventType = "Unsuspended"
	Paused                EventType = "Paused"
	Resumed               EventType = "Resumed" // by the resume command, or because the pause expired
	RateLimited           EventType = "RateLimited"
	NeedsRecreate         EventType = "NeedsRecreate"         // dependent whose parent ID is gone; M2b recreates it
	NeedsDependentRestart EventType = "NeedsDependentRestart" // stale dependent after a parent restart; M2b
	WaitingForParent      EventType = "WaitingForParent"
	NotifyOnly            EventType = "NotifyOnly"
	ActionRefused         EventType = "ActionRefused" // the pre-action check found a vanished parent
	EventAttributed       EventType = "EventAttributed"
	ManualStopMarked      EventType = "ManualStopMarked"
	ManualStopCleared     EventType = "ManualStopCleared"
	MarkersQuarantined    EventType = "MarkersQuarantined" // a stream loss or a new boot id discarded manual-stop markers
	RestoreWindowOpened   EventType = "RestoreWindowOpened"
	RestoreEvent          EventType = "RestoreEvent" // a kill or start seen inside the restore window; interpreted as a restore
)

// Event is one domain event. IDs are monotonic per host and times are UTC, so
// the stream can later serve as the hub's `since` cursor.
type Event struct {
	ID        uint64            `json:"id"`
	Time      time.Time         `json:"time"`
	Host      string            `json:"host"`
	Key       string            `json:"key"`
	Container string            `json:"container,omitempty"`
	Type      EventType         `json:"type"`
	Message   string            `json:"message"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// Bus fans events out, in order, to subscribers. Subscribers run
// synchronously inside Publish and must not block.
type Bus struct {
	mu   sync.Mutex
	next uint64
	subs []func(Event)
}

// Subscribe registers a subscriber for all later events.
func (b *Bus) Subscribe(fn func(Event)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = append(b.subs, fn)
}

// Publish stamps the ID and delivers the event.
func (b *Bus) Publish(e Event) Event {
	b.mu.Lock()
	b.next++
	e.ID = b.next
	e.Time = e.Time.UTC()
	subs := append([]func(Event){}, b.subs...)
	b.mu.Unlock()
	for _, fn := range subs {
		fn(e)
	}
	return e
}

// Recorder is a subscriber that keeps every event, for tests and /status.
type Recorder struct {
	mu     sync.Mutex
	events []Event
}

// Record is the subscriber function.
func (r *Recorder) Record(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// Events returns a copy of everything recorded.
func (r *Recorder) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// Types returns the recorded event types in order.
func (r *Recorder) Types() []EventType {
	var out []EventType
	for _, e := range r.Events() {
		out = append(out, e.Type)
	}
	return out
}

// Count returns how many events of the type were recorded.
func (r *Recorder) Count(t EventType) int {
	n := 0
	for _, e := range r.Events() {
		if e.Type == t {
			n++
		}
	}
	return n
}
