// Package daemon wires the pieces together: it turns Docker events and polls
// into inspections, asks internal/rules what is wrong, asks internal/engine
// what to do, and performs rung 1 through the docker.Actor. Run drives it from
// one goroutine; the engine it owns is safe to call from the control socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackspiering/whalenurse/internal/clock"
	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/docker"
	"github.com/jackspiering/whalenurse/internal/engine"
	"github.com/jackspiering/whalenurse/internal/rules"
)

// actionTimeout bounds one restart or start call.
const actionTimeout = 2 * time.Minute

// MetaBootID is the store key the kernel boot id is kept under.
const MetaBootID = "boot_id"

// BootIDPath is where Linux publishes the boot id (the same value on the host and in a container).
const BootIDPath = "/proc/sys/kernel/random/boot_id"

// Meta is the key-value part of the store: the persisted boot id.
type Meta interface {
	Meta(key string) (string, bool)
	SetMeta(key, value string)
}

// Options are the daemon's dependencies.
type Options struct {
	Config config.Global
	Reader docker.Reader
	Actor  docker.Actor // a docker.DryRun in dry-run mode
	Clock  clock.Clock
	Bus    *engine.Bus
	Log    *slog.Logger
	SelfID string

	// Persist is where the engine keeps its state; nil keeps it in memory (dry-run,
	// most tests). Snapshot is what was loaded from the store, applied before the
	// first event. Meta, when set, keeps the boot id.
	Persist  engine.Persister
	Snapshot engine.Snapshot
	Meta     Meta
	// BootID reads the kernel's boot id; nil reads BootIDPath.
	BootID func() string

	// ReconnectBackoff is the first wait before resubscribing to events (doubles up to 30s).
	ReconnectBackoff time.Duration
}

type pending struct {
	due time.Time
}

// Daemon is the healer.
type Daemon struct {
	o   Options
	cfg config.Global
	rd  docker.Reader
	eng *engine.Engine
	clk clock.Clock
	log *slog.Logger

	started     time.Time
	settled     bool
	settledFlag atomic.Bool // settled, readable from the control socket
	nextPoll    time.Time
	sweepAt     time.Time // dependents sweep requested by a parent start/create
	reconnectAt time.Time // full sweep after a reconnect that followed a lost stream
	streamLost  bool      // the stream ended and has not been re-established yet
	streamSeen  bool      // a stream has been established at least once
	apiDown     bool
	pend        map[string]pending
	warned      map[string]bool
	idx         index

	eventsUp   atomic.Bool
	eventsDown atomic.Int64 // unix nanos when the stream went down; 0 while up
	lastTick   atomic.Int64
}

// New builds a Daemon.
func New(o Options) *Daemon {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.ReconnectBackoff == 0 {
		o.ReconnectBackoff = time.Second
	}
	g := o.Config
	d := &Daemon{
		o: o, cfg: g, rd: o.Reader, clk: o.Clock, log: o.Log,
		eng: engine.New(engine.Params{
			Host: g.HostID, BaseCooldown: g.Cooldown, RateLimit: g.RateLimit, DryRun: g.DryRun,
			RecoveryPeriod: g.RecoveryPeriod, CrashloopAfter: g.CrashloopAfter, FlapLimit: g.FlapLimit, FlapWindow: g.FlapWindow,
			Persist: o.Persist,
		}, o.Clock, o.Bus),
		pend:   map[string]pending{},
		warned: map[string]bool{},
	}
	d.eng.Restore(o.Snapshot)
	d.started = o.Clock.Now()
	d.eventsDown.Store(d.started.UnixNano())
	d.lastTick.Store(d.started.UnixNano())
	d.checkBoot()
	return d
}

// checkBoot compares the kernel's boot id with the one stored with the markers
// (plan §3.3, mechanism b). A reboot stops whalenurse in the same second as the
// containers, so a marker can exist with no matching event in this run: if the
// boot id changed, every marker is dropped and the first STARTUP_SETTLE is a
// restore window.
func (d *Daemon) checkBoot() {
	if d.o.Meta == nil {
		return
	}
	read := d.o.BootID
	if read == nil {
		read = readBootID
	}
	id := strings.TrimSpace(read())
	if id == "" {
		return
	}
	prev, had := d.o.Meta.Meta(MetaBootID)
	d.o.Meta.SetMeta(MetaBootID, id)
	if had && prev != id {
		d.log.Info("new boot id: dropping manual-stop markers", "previous", prev, "current", id)
		d.eng.DropAllMarkers("boot id changed: the host rebooted")
		d.eng.OpenRestoreWindow(d.cfg.StartupSettle, "new boot id")
	}
}

func readBootID() string {
	b, err := os.ReadFile(BootIDPath)
	if err != nil {
		return ""
	}
	return string(b)
}

// Engine exposes the state machine (status, tests).
func (d *Daemon) Engine() *engine.Engine { return d.eng }

// Healthy reports whether the daemon is working: the events stream is up (or
// has been down for less than twice the poll interval) and the loop is alive.
func (d *Daemon) Healthy() (bool, string) {
	now := d.clk.Now()
	limit := 2 * d.cfg.PollInterval
	if !d.eventsUp.Load() {
		down := time.Unix(0, d.eventsDown.Load())
		if now.Sub(down) > limit {
			return false, fmt.Sprintf("docker events stream down for %s", now.Sub(down).Round(time.Second))
		}
	}
	if beat := time.Unix(0, d.lastTick.Load()); now.Sub(beat) > 3*d.cfg.PollInterval+actionTimeout {
		return false, fmt.Sprintf("main loop silent for %s", now.Sub(beat).Round(time.Second))
	}
	return true, "ok"
}

// streamSignal tells the loop that the events stream ended or came back.
type streamSignal struct {
	lost bool
	at   time.Time
}

// Run blocks until ctx ends: it subscribes to events (reconnecting) and ticks.
func (d *Daemon) Run(ctx context.Context) error {
	evCh := make(chan docker.Event, 256)
	sigCh := make(chan streamSignal, 16)
	go d.watchEvents(ctx, evCh, sigCh)

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	d.log.Info("whalenurse started",
		"host", d.cfg.HostID, "mode", d.cfg.Mode, "dry_run", d.cfg.DryRun,
		"poll", d.cfg.PollInterval.String(), "grace", d.cfg.Grace.String(), "startup_settle", d.cfg.StartupSettle.String())
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-evCh:
			d.HandleEvent(ctx, ev)
		case sig := <-sigCh:
			// Events are delivered before the signal that follows them: drain what is queued.
			d.drain(ctx, evCh)
			if sig.lost {
				d.StreamLost(sig.at)
			} else {
				d.StreamConnected()
			}
		case <-tick.C:
			d.Tick(ctx)
		}
	}
}

func (d *Daemon) drain(ctx context.Context, evCh chan docker.Event) {
	for {
		select {
		case ev := <-evCh:
			d.HandleEvent(ctx, ev)
		default:
			return
		}
	}
}

// streamStable is how long a subscription must stay open, without delivering an
// event, before it counts as established: Events() returns before the daemon
// has accepted the request, so during an outage every failed retry would
// otherwise look like a reconnect.
const streamStable = 2 * time.Second

// watchEvents keeps one subscription alive. It resubscribes with since=<last
// event time> after an error and tells the loop when the stream was lost and
// when it is back; a stream that ends because ctx ended is not a loss, and a
// retry that fails before the stream is established is not a reconnect.
func (d *Daemon) watchEvents(ctx context.Context, out chan<- docker.Event, sig chan<- streamSignal) {
	var since time.Time
	backoff := d.o.ReconnectBackoff
	send := func(s streamSignal) bool {
		select {
		case sig <- s:
			return true
		case <-ctx.Done():
			return false
		}
	}
	for ctx.Err() == nil {
		sctx, cancel := context.WithCancel(ctx)
		s := d.rd.Events(sctx, since)
		d.setEvents(true)
		if backoff > d.o.ReconnectBackoff {
			d.log.Info("docker events stream reconnected")
		}
		established := false
		establish := func() bool {
			if established {
				return true
			}
			established = true
			return send(streamSignal{})
		}
		stable := time.NewTimer(streamStable)
		got := false
	read:
		for {
			select {
			case <-stable.C:
				if !establish() {
					cancel()
					return
				}
			case ev, ok := <-s.Events:
				if !ok {
					break read
				}
				if !establish() {
					cancel()
					return
				}
				got = true
				if ev.Time.After(since) {
					since = ev.Time
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					cancel()
					return
				}
			case <-ctx.Done():
				cancel()
				return
			}
		}
		stable.Stop()
		cancel()
		d.setEvents(false)
		var err error
		select {
		case err = <-s.Err:
		default:
		}
		if ctx.Err() != nil {
			return
		}
		if established {
			if !send(streamSignal{lost: true, at: d.clk.Now()}) {
				return
			}
		}
		d.log.Warn("docker events stream ended; will resubscribe", "error", fmt.Sprint(err), "retry_in", backoff.String())
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if got {
			backoff = d.o.ReconnectBackoff
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (d *Daemon) setEvents(up bool) {
	if up {
		d.eventsDown.Store(0)
	} else if d.eventsDown.Load() == 0 {
		d.eventsDown.Store(d.clk.Now().UnixNano())
	}
	d.eventsUp.Store(up)
}

// StreamLost is called when the events stream ended for a reason other than
// whalenurse stopping (plan §3.3, mechanism a). Every manual-stop marker set in
// the 30 s before it may be the daemon's shutdown kill rather than a person, so
// those markers are discarded: that container is then judged by rule 4 without
// evidence, which for a signal exit is notify-only.
func (d *Daemon) StreamLost(at time.Time) {
	d.streamLost = true
	if cleared := d.eng.QuarantineMarkers(at); len(cleared) > 0 {
		d.log.Info("events stream lost: manual-stop markers set in the last 30s discarded", "count", len(cleared))
	}
}

// StreamConnected is called each time a subscription is established. After a
// lost stream it opens the restore window (mechanism c) and schedules the full
// sweep that replay with since= can never replace (the new daemon's buffer holds
// 256 events and never the old daemon's shutdown).
func (d *Daemon) StreamConnected() {
	first := !d.streamSeen
	d.streamSeen = true
	if first || !d.streamLost {
		return
	}
	d.streamLost = false
	now := d.clk.Now()
	d.eng.OpenRestoreWindow(d.cfg.StartupSettle, "reconnected after a lost events stream")
	d.reconnectAt = now.Add(d.cfg.StartupSettle)
	d.log.Info("events stream reconnected after a loss; sweeping everything after the settle period", "sweep_at", d.reconnectAt.UTC().Format(time.RFC3339))
}

// Tick does all the work that is due: the startup sweep, the poll, the sweep
// after a reconnect, pause expiry and every scheduled inspection.
func (d *Daemon) Tick(ctx context.Context) {
	now := d.clk.Now()
	d.lastTick.Store(now.UnixNano())

	if !d.settled {
		if now.Sub(d.started) < d.cfg.StartupSettle {
			return
		}
		d.settled = true
		d.settledFlag.Store(true)
		d.nextPoll = now.Add(d.cfg.PollInterval)
		d.log.Info("startup settle over; sweeping all containers")
		d.sweep(ctx)
	} else if !now.Before(d.nextPoll) {
		d.nextPoll = now.Add(d.cfg.PollInterval)
		d.poll(ctx)
	}
	if !d.reconnectAt.IsZero() && !now.Before(d.reconnectAt) {
		d.reconnectAt = time.Time{}
		d.log.Info("sweeping all containers after the events stream was re-established")
		d.sweep(ctx)
	}
	if !d.sweepAt.IsZero() && !now.Before(d.sweepAt) {
		d.sweepAt = time.Time{}
		d.sweepDependents(ctx)
	}
	d.eng.ExpirePauses()
	for _, k := range d.eng.TakeReeval() {
		d.schedule(k, now)
	}
	d.processDue(ctx)
}

func (d *Daemon) schedule(key string, due time.Time) {
	if p, ok := d.pend[key]; ok && !p.due.After(due) {
		return
	}
	d.pend[key] = pending{due: due}
}

// HandleEvent reacts to one Docker event. Events are only triggers: the
// decision is made later, by inspecting the container after GRACE.
func (d *Daemon) HandleEvent(ctx context.Context, ev docker.Event) {
	switch ev.Action {
	case "create", "start", "die", "kill", "oom", "destroy", "health_status":
	default:
		return // exec_*, stop, restart, rename, attach, ...
	}
	if d.o.SelfID != "" && ev.ID == d.o.SelfID {
		return
	}
	cfg, _ := config.ParseLabels(ev.Labels, d.cfg.Mode)
	if !cfg.OptedIn || ev.Labels[docker.LabelOneOff] == "True" || ev.Labels[docker.LabelSwarmTaskID] != "" {
		return
	}
	t := engine.Target{Key: docker.StateKey(d.cfg.HostID, ev.Name, ev.Labels), ID: ev.ID, Name: ev.Name}
	if d.eng.ObserveDockerEvent(t, ev) {
		return
	}
	if !d.settled {
		return // the startup sweep looks at everything
	}
	now := d.clk.Now()
	switch ev.Action {
	case "die", "oom", "create", "start":
		d.schedule(t.Key, now.Add(d.cfg.Grace))
		if ev.Action == "start" || ev.Action == "create" {
			if d.sweepAt.IsZero() {
				d.sweepAt = now.Add(d.cfg.Grace) // its dependents may now be stale
			}
		}
	case "health_status":
		if ev.Detail == "unhealthy" {
			d.schedule(t.Key, now.Add(d.cfg.Grace))
		}
	}
}

// apiError notes a failed Docker call. While the daemon restores containers
// the API is unavailable: that means "not ready", never an incident, so the
// caller just returns and the next poll or the sweep after the reconnect tries again.
func (d *Daemon) apiError(what string, err error) {
	if !d.apiDown {
		d.apiDown = true
		d.log.Warn("docker API not ready; no incident is opened, will retry", "while", what, "error", err.Error())
	}
}

func (d *Daemon) apiOK() {
	if d.apiDown {
		d.apiDown = false
		d.log.Info("docker API is answering again")
	}
}

// sweep inspects every container at once, without a grace period: it runs once
// after STARTUP_SETTLE, and again after every reconnect that follows a lost
// events stream, and each time it has already waited.
func (d *Daemon) sweep(ctx context.Context) {
	if err := d.refresh(ctx); err != nil {
		// Not ready yet: try again on the next tick instead of waiting a whole poll.
		d.reconnectAt = d.clk.Now().Add(d.cfg.Grace)
		return
	}
	for _, s := range d.idx.all {
		d.schedule(d.keyOf(s), d.clk.Now())
	}
}

func (d *Daemon) sweepDependents(ctx context.Context) {
	if err := d.refresh(ctx); err != nil {
		return
	}
	for _, s := range d.idx.all {
		if hasRef(s) {
			d.schedule(d.keyOf(s), d.clk.Now())
		}
	}
}

// poll is the level-triggered backstop for edge-triggered events, and the only
// detector of a failed restart-policy restart (which emits no event). It also
// reads RestartCount of every opted-in container, because a crash loop that
// whalenurse saw no `die` events for is otherwise invisible (the container list
// does not carry the count).
func (d *Daemon) poll(ctx context.Context) {
	if err := d.refresh(ctx); err != nil {
		return
	}
	now := d.clk.Now()
	for _, s := range d.idx.all {
		cfg, _ := config.ParseLabels(s.Labels, d.cfg.Mode)
		if !cfg.OptedIn {
			continue
		}
		d.schedule(d.keyOf(s), now.Add(d.cfg.Grace))
	}
}

func (d *Daemon) processDue(ctx context.Context) {
	now := d.clk.Now()
	var due []string
	for k, p := range d.pend {
		if !p.due.After(now) {
			due = append(due, k)
		}
	}
	if len(due) == 0 {
		return
	}
	sort.Strings(due)
	if err := d.refresh(ctx); err != nil {
		return
	}
	// Parents before dependents: a dependent's ladder is held back while its
	// parent has an incident open, so the parent must get to open it first.
	sort.SliceStable(due, func(i, j int) bool {
		return !hasRef(d.idx.byKey[due[i]]) && hasRef(d.idx.byKey[due[j]])
	})
	for _, k := range due {
		delete(d.pend, k)
		d.process(ctx, k)
	}
}

// process inspects one key and runs it through rules and engine.
func (d *Daemon) process(ctx context.Context, key string) {
	sum, ok := d.idx.byKey[key]
	t := engine.Target{Key: key}
	if !ok {
		d.eng.Forget(t)
		return
	}
	c, err := d.rd.Inspect(ctx, sum.ID)
	if errors.Is(err, docker.ErrNotFound) {
		d.eng.Forget(t)
		return
	}
	if err != nil {
		d.apiError("inspect "+key, err)
		d.schedule(key, d.clk.Now().Add(d.cfg.PollInterval))
		return
	}
	d.apiOK()
	t.ID, t.Name = c.ID, c.Name
	now := d.clk.Now()

	cfg, warns := config.ParseLabels(c.Labels, d.cfg.Mode)
	for _, w := range warns {
		if id := key + "|" + w; !d.warned[id] {
			d.warned[id] = true
			d.log.Warn("label problem", "key", key, "detail", w)
		}
	}

	in := rules.Input{
		Now: now, C: c, Cfg: cfg,
		Self:     d.o.SelfID != "" && c.ID == d.o.SelfID,
		Parents:  d.parentsOf(ctx, c),
		Evidence: d.eng.Evidence(key),
	}
	dec := rules.Classify(in)
	if dec.Class == rules.ClassSkip {
		d.log.Debug("classified", "key", key, "container", c.Name, "class", dec.Class, "skip", dec.Skip)
		return
	}
	// Rule 3's crash-loop verdict needs memory between inspects, so the engine owns it.
	obs := engine.Obs{RestartCount: c.RestartCount, FinishedAt: c.FinishedAt, HasHealthcheck: c.Health != ""}
	if in.Loop = d.eng.Observe(t, obs); in.Loop.Active {
		dec = rules.Classify(in)
	}
	d.log.Debug("classified", "key", key, "container", c.Name, "class", dec.Class, "action", dec.Action, "reason", dec.Reason)

	plan := d.eng.Evaluate(t, dec, cfg, obs)
	if !plan.RecheckAt.IsZero() {
		d.schedule(key, plan.RecheckAt)
	}
	if plan.Act == rules.ActionNone || plan.Act == "" {
		return
	}
	d.execute(ctx, t, c, dec, plan, cfg)
}

// execute performs one rung-1 action. Just before touching the container it
// re-checks every container: reference, because the parent can be recreated
// between the decision and the action, and an API restart of a dependent
// whose parent ID is gone turns a running dependent into an exited one (E1).
func (d *Daemon) execute(ctx context.Context, t engine.Target, c docker.Container, dec rules.Decision, plan engine.Plan, cfg config.Container) {
	for _, mode := range []string{c.NetworkMode, c.IpcMode, c.PidMode} {
		ref := docker.ContainerRef(mode)
		if ref == "" {
			continue
		}
		p, err := d.rd.Inspect(ctx, ref)
		switch {
		case errors.Is(err, docker.ErrNotFound):
			d.refuse(t, dec, fmt.Sprintf("container:%s no longer exists; needs recreate (M2b), not restarting", docker.ShortID(ref)))
			return
		case err != nil:
			d.refuse(t, dec, fmt.Sprintf("cannot verify container:%s (%v); not acting", docker.ShortID(ref), err))
			return
		case !p.Running:
			d.refuse(t, dec, fmt.Sprintf("parent %s is not running; act on the parent only", docker.ShortID(p.ID)))
			return
		}
	}

	d.eng.Begin(t, plan, dec)
	actx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	var err error
	switch plan.Act {
	case rules.ActionRestart:
		err = d.o.Actor.Restart(actx, c.ID)
	case rules.ActionStart:
		err = d.o.Actor.Start(actx, c.ID)
	}
	recheck := d.eng.End(t, plan, engine.Result{Err: err, DryRun: d.cfg.DryRun}, cfg)
	if !recheck.IsZero() {
		d.schedule(t.Key, recheck)
	}
}

func (d *Daemon) refuse(t engine.Target, dec rules.Decision, why string) {
	d.eng.Refuse(t, dec, why)
	d.schedule(t.Key, d.clk.Now().Add(d.cfg.Grace))
}

// ---- container index -------------------------------------------------------

type index struct {
	all   []docker.Summary
	byKey map[string]docker.Summary
	byID  map[string]docker.Summary
}

func (d *Daemon) keyOf(s docker.Summary) string {
	return docker.StateKey(d.cfg.HostID, s.Name, s.Labels)
}

func hasRef(s docker.Summary) bool { return docker.ContainerRef(s.NetworkMode) != "" }

// refresh reloads the container list. When two containers share a key (compose
// renames the old one while it recreates), the newest wins.
func (d *Daemon) refresh(ctx context.Context) error {
	list, err := d.rd.List(ctx)
	if err != nil {
		d.apiError("container list", err)
		return err
	}
	d.apiOK()
	idx := index{all: list, byKey: map[string]docker.Summary{}, byID: map[string]docker.Summary{}}
	for _, s := range list {
		idx.byID[s.ID] = s
		k := d.keyOf(s)
		if old, ok := idx.byKey[k]; !ok || s.Created.After(old.Created) {
			idx.byKey[k] = s
		}
	}
	d.idx = idx
	return nil
}

// parentsOf resolves each container: reference of c against the last list.
func (d *Daemon) parentsOf(ctx context.Context, c docker.Container) []rules.Parent {
	var out []rules.Parent
	for _, r := range []struct{ kind, mode string }{
		{rules.RefNetwork, c.NetworkMode}, {rules.RefIPC, c.IpcMode}, {rules.RefPID, c.PidMode},
	} {
		ref := docker.ContainerRef(r.mode)
		if ref == "" {
			continue
		}
		p := rules.Parent{Kind: r.kind, Ref: ref}
		for _, s := range d.idx.all {
			if !docker.MatchesRef(s.ID, s.Name, ref) {
				continue
			}
			p.Found, p.ID = true, s.ID
			p.Running = s.State == "running"
			p.Busy = d.eng.Busy(d.keyOf(s))
			if pc, err := d.rd.Inspect(ctx, s.ID); err == nil {
				p.StartedAt = pc.StartedAt
				p.Running = pc.Running
			}
			break
		}
		out = append(out, p)
	}
	return out
}
