package engine

import "time"

// The types below are what the engine persists (plan §3.2, *Persisted from M2a*).
// The engine stays pure: it calls a Persister it was given and never opens a
// file. internal/store implements the interface on SQLite.

// IncidentRecord is the persisted form of an open incident.
type IncidentRecord struct {
	Opened        time.Time
	Class         string
	Rung          int // index of the next rung to run
	CooldownUntil time.Time
	Spent         bool   // every rung ran, or the key was flapping when the incident opened
	SpentReason   string // "ladder" or "flapping"
	Suspended     bool   // currently SUSPENDED
	SuspendedAt   time.Time
	Recovering    bool // recovered, waiting out RECOVERY_PERIOD
	RecoverySince time.Time
	RecoveryCount int // RestartCount when the recovery period began
}

// LoopRecord is the crash-loop tracker's state (rule 3).
type LoopRecord struct {
	Have      bool // LastCount is a real baseline
	LastCount int
	LastObs   time.Time
	N         int       // RestartCount increases counted since the loop began
	First     time.Time // when the first of them happened
	Last      time.Time // the latest
}

// KeyRecord is everything persisted for one state key.
type KeyRecord struct {
	Key      string
	Name     string
	MarkerAt time.Time // the manual-stop marker; zero for none
	Loop     LoopRecord
	Flaps    []time.Time // when incidents opened, within FlapWindow
	Incident *IncidentRecord
}

// ClosedIncident is the history row written when an incident ends.
type ClosedIncident struct {
	Key     string
	Opened  time.Time
	Closed  time.Time
	Class   string
	Outcome string // recovered, gone, recreated, stopped, unsuspended
}

// Pause is a PAUSED scope: a project or a project/service.
type Pause struct {
	Scope   string
	Until   time.Time // zero: indefinite
	Created time.Time
}

// Snapshot is what the engine loads at start.
type Snapshot struct {
	Keys    []KeyRecord
	Pauses  []Pause
	Actions []time.Time // timestamps of actions in the last hour (the rate limit)
}

// Persister receives every change to persistent state. Implementations must be
// quick and must not call back into the engine. A failure is the Persister's
// to log: the engine keeps working from memory.
type Persister interface {
	SaveKey(KeyRecord)
	DeleteKey(key string)
	CloseIncident(ClosedIncident)
	SavePause(Pause)
	DeletePause(scope string)
	SaveAction(at time.Time)
}
