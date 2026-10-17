package daemon

import (
	"fmt"
	"strings"
	"time"

	"github.com/jackspiering/whalenurse/internal/control"
	"github.com/jackspiering/whalenurse/internal/engine"
)

// Status is the reply of `whalenurse status`.
type Status struct {
	Host         string        `json:"host"`
	Mode         string        `json:"mode"`
	DryRun       bool          `json:"dry_run"`
	Persistent   bool          `json:"persistent"` // false in dry-run: nothing is stored
	EventsUp     bool          `json:"events_up"`
	Settled      bool          `json:"settled"`
	Healthy      bool          `json:"healthy"`
	HealthDetail string        `json:"health_detail,omitempty"`
	Engine       engine.Status `json:"engine"`
}

// Status snapshots the daemon for the CLI.
func (d *Daemon) Status() any {
	ok, detail := d.Healthy()
	return Status{
		Host: d.cfg.HostID, Mode: string(d.cfg.Mode), DryRun: d.cfg.DryRun, Persistent: d.o.Persist != nil,
		EventsUp: d.eventsUp.Load(), Settled: d.settledFlag.Load(), Healthy: ok, HealthDetail: detail,
		Engine: d.eng.Status(),
	}
}

var _ control.Backend = (*Daemon)(nil)

// Pause implements control.Backend.
func (d *Daemon) Pause(scope string, dur time.Duration) (string, error) {
	p, err := d.eng.Pause(scope, dur)
	if err != nil {
		return "", err
	}
	if p.Until.IsZero() {
		return fmt.Sprintf("paused %s until you run resume", p.Scope), nil
	}
	return fmt.Sprintf("paused %s until %s", p.Scope, p.Until.Local().Format(time.RFC1123)), nil
}

// Resume implements control.Backend.
func (d *Daemon) Resume(scope string) (string, error) {
	was, err := d.eng.Resume(scope)
	if err != nil {
		return "", err
	}
	if !was {
		return "", fmt.Errorf("%w: %s is not paused", control.ErrNothingToDo, strings.TrimSpace(scope))
	}
	return fmt.Sprintf("resumed %s; its containers are looked at again within a second", strings.TrimSpace(scope)), nil
}

// Unsuspend implements control.Backend.
func (d *Daemon) Unsuspend(scope string) (string, error) {
	keys, err := d.eng.Unsuspend(scope)
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", fmt.Errorf("%w: no suspended container under %s", control.ErrNothingToDo, strings.TrimSpace(scope))
	}
	return "unsuspended " + strings.Join(keys, ", "), nil
}

// ParseFor reads the --for value: empty is the default pause, "0" is indefinite.
func ParseFor(v string, parse func(string) (time.Duration, error)) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return engine.DefaultPause, nil
	}
	d, err := parse(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("--for %q: want a duration such as 2h or 1d, or 0 for no expiry", v)
	}
	return d, nil
}
