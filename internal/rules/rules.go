// Package rules classifies a container from a fresh inspect, following the
// precedence in plan §3.3. It is pure: no Docker calls, no clock (the caller
// passes Now), no state. Events are inputs only through Evidence.
package rules

import (
	"fmt"
	"strings"
	"time"

	"github.com/jackspiering/whalenurse/internal/config"
	"github.com/jackspiering/whalenurse/internal/docker"
)

// Action is what the rules want done. Restart and Start are the only actions
// M1 performs; NeedsRecreate and NeedsDependentRestart are recorded and logged
// and take effect in M2.
type Action string

const (
	ActionNone                  Action = "none"
	ActionRestart               Action = "restart"                 // API restart: running, unhealthy or crash-looping
	ActionStart                 Action = "start"                   // API start: created or exited
	ActionNeedsRecreate         Action = "needs-recreate"          // M2: recreate via compose
	ActionNeedsDependentRestart Action = "needs-dependent-restart" // M2: the dependents step
	ActionWaitForParent         Action = "wait-for-parent"         // act only on the parent
	ActionNotify                Action = "notify"                  // log + domain event, no action
)

// Class names the condition found. Skip reasons use ClassSkip plus Skip.
type Class string

const (
	ClassSkip                 Class = "skip"
	ClassOK                   Class = "ok"
	ClassStarting             Class = "starting" // healthcheck still starting
	ClassUnhealthy            Class = "unhealthy"
	ClassRestarting           Class = "restarting" // restarting, not yet long enough to be a crash loop
	ClassCrashLoop            Class = "crash-loop" // restarting longer than CRASHLOOP_AFTER
	ClassRetriesExhausted     Class = "retries-exhausted"
	ClassNeverStarted         Class = "never-started"
	ClassStartFailed          Class = "start-failed" // State.Error is set
	ClassExitedCrashed        Class = "exited-crashed"
	ClassExitedOOM            Class = "exited-oom"
	ClassExitedAlways         Class = "exited-always" // heal-exited=always
	ClassExitedIntended       Class = "exited-intended"
	ClassExitedManual         Class = "exited-manual"    // a kill was seen: someone stopped it
	ClassExitedClean          Class = "exited-clean"     // exit 0 under an always-style policy
	ClassExitedSignalled      Class = "exited-signalled" // 137 or 143 without OOM
	ClassExitedNever          Class = "exited-never"     // heal-exited=never
	ClassDead                 Class = "dead"
	ClassStaleParentGone      Class = "stale-parent-gone"
	ClassStaleParentRestarted Class = "stale-parent-restarted"
	ClassWaitingForParent     Class = "waiting-for-parent"
	ClassParentUnresolved     Class = "parent-unresolved"
)

// Skip reasons (rule 0).
const (
	SkipSelf        = "self"
	SkipOneOff      = "oneoff"
	SkipSwarm       = "swarm"
	SkipNotOptedIn  = "not-opted-in"
	SkipDockerPause = "docker-paused"
	SkipRemoving    = "removing"
)

// Reference kinds for Parent.
const (
	RefNetwork = "network"
	RefIPC     = "ipc"
	RefPID     = "pid"
)

// Parent is the resolved target of one container:<ref> mode of the container
// being classified. The caller resolves it; the rules never look anything up.
type Parent struct {
	Kind      string
	Ref       string
	Found     bool
	ID        string
	Running   bool
	StartedAt time.Time
	Busy      bool // the parent has an open incident or an action in flight
}

// Input is everything Classify may look at.
type Input struct {
	Now            time.Time
	C              docker.Container
	Cfg            config.Container
	CrashloopAfter time.Duration
	Self           bool
	Parents        []Parent // one per container: reference in network_mode, ipc or pid
	Evidence       Evidence
	LoopSince      time.Time // when the daemon first saw it restarting or its RestartCount rising; zero if not looping
}

// Decision is the result of Classify.
type Decision struct {
	Class  Class
	Action Action
	Cause  Cause  // set when Class is ClassStartFailed
	Skip   string // set when Class is ClassSkip
	Reason string
}

func (d Decision) String() string {
	return fmt.Sprintf("%s/%s", d.Class, d.Action)
}

// Exit codes with a meaning for the rules.
const (
	exitSigkill = 137
	exitSigterm = 143
)

// Classify applies the §3.3 precedence to one inspected container.
func Classify(in Input) Decision {
	c := in.C
	// Rule 0: skip entirely.
	switch {
	case in.Self:
		return skip(SkipSelf, "whalenurse's own container")
	case strings.EqualFold(c.Labels[docker.LabelOneOff], "true"):
		return skip(SkipOneOff, "compose run container")
	case c.Labels[docker.LabelSwarmTaskID] != "":
		return skip(SkipSwarm, "swarm task")
	case !in.Cfg.OptedIn:
		return skip(SkipNotOptedIn, "not opted in")
	case c.Status == "removing":
		return skip(SkipRemoving, "being removed")
	case c.Paused || c.Status == "paused":
		return skip(SkipDockerPause, "paused with docker pause")
	}
	return guardReferences(in, classifyState(in))
}

func skip(reason, text string) Decision {
	return Decision{Class: ClassSkip, Action: ActionNone, Skip: reason, Reason: text}
}

func classifyState(in Input) Decision {
	c := in.C

	// Rule 1: a non-empty State.Error is a failed start. It overrides every
	// rule below and the exit code is never read: a failed first start is
	// `created` with 126/127/128, a failed restart-policy restart is `exited`
	// with the previous run's code.
	if c.Error != "" && (c.Status == "created" || c.Status == "exited") {
		cause := ClassifyStartError(c.Error)
		d := Decision{Class: ClassStartFailed, Action: ActionStart, Cause: cause, Reason: "failed start (" + string(cause) + "): " + trim(c.Error)}
		switch cause {
		case CauseParentGone, CauseNetworkGone:
			// Restarting cannot work: the thing it referenced no longer exists.
			d.Action = ActionNeedsRecreate
		}
		// CauseParentStopped stays a Start; guardReferences turns it into
		// "wait for the parent" until the parent runs, then it is a plain start.
		return d
	}

	switch c.Status {
	case "dead":
		return Decision{Class: ClassDead, Action: ActionNotify, Reason: "container is dead; it cannot be started"}
	case "created":
		return Decision{Class: ClassNeverStarted, Action: ActionStart, Reason: "created but never started"}
	case "restarting":
		return classifyRestarting(in)
	case "running":
		return classifyRunning(in)
	case "exited":
		return classifyExited(in)
	}
	return Decision{Class: ClassOK, Action: ActionNone, Reason: "status " + c.Status}
}

func classifyRunning(in Input) Decision {
	if in.C.Health == "unhealthy" {
		return Decision{Class: ClassUnhealthy, Action: ActionRestart, Reason: "healthcheck reports unhealthy"}
	}
	// A loop that crashes every ~15s is mostly `running`: the engine resets its
	// restart backoff after a run of 10s, so it is rarely `restarting` for long.
	if !in.LoopSince.IsZero() && in.Now.Sub(in.LoopSince) >= in.CrashloopAfter {
		return crashLoop(in)
	}
	switch in.C.Health {
	case "starting":
		return Decision{Class: ClassStarting, Action: ActionNone, Reason: "healthcheck still starting"}
	}
	return Decision{Class: ClassOK, Action: ActionNone, Reason: "running"}
}

// Rule 3: restarting, or restarting over and over, for longer than CRASHLOOP_AFTER.
// The age is measured by the daemon from when it first saw the loop, not from StartedAt.
func classifyRestarting(in Input) Decision {
	if in.LoopSince.IsZero() || in.Now.Sub(in.LoopSince) < in.CrashloopAfter {
		return Decision{Class: ClassRestarting, Action: ActionNone, Reason: "restarting; not yet a crash loop"}
	}
	return crashLoop(in)
}

func crashLoop(in Input) Decision {
	return Decision{Class: ClassCrashLoop, Action: ActionRestart,
		Reason: fmt.Sprintf("crash loop for %s (restart count %d)", in.Now.Sub(in.LoopSince).Round(time.Second), in.C.RestartCount)}
}

// Rules 3 (on-failure exhausted) and 4 (exited without State.Error).
func classifyExited(in Input) Decision {
	c, ev := in.C, in.Evidence
	signalled := c.ExitCode == exitSigkill || c.ExitCode == exitSigterm

	if c.RestartPolicy == "on-failure" && c.MaxRetries > 0 && c.RestartCount >= c.MaxRetries &&
		c.ExitCode != 0 && !signalled && !ev.ManualStop {
		return Decision{Class: ClassRetriesExhausted, Action: ActionStart,
			Reason: fmt.Sprintf("on-failure:%d exhausted (exit %d)", c.MaxRetries, c.ExitCode)}
	}

	// heal-exited=always heals any exited container.
	if in.Cfg.HealExited == config.HealAlways {
		return Decision{Class: ClassExitedAlways, Action: ActionStart, Reason: "heal-exited=always"}
	}

	// Intended exit: policy no or on-failure with exit code 0. Left alone in
	// `crashed` and `never` modes.
	if c.ExitCode == 0 && (c.RestartPolicy == "no" || c.RestartPolicy == "" || c.RestartPolicy == "on-failure") {
		return Decision{Class: ClassExitedIntended, Action: ActionNone, Reason: "exited 0 under restart policy " + policyName(c)}
	}

	switch in.Cfg.HealExited {
	case config.HealNever:
		return Decision{Class: ClassExitedNever, Action: ActionNotify, Reason: fmt.Sprintf("exited (%d); heal-exited=never", c.ExitCode)}
	}

	// heal-exited=crashed.
	switch {
	case c.OOMKilled:
		return Decision{Class: ClassExitedOOM, Action: ActionStart, Reason: "killed by the OOM killer"}
	case ev.ManualStop:
		return Decision{Class: ClassExitedManual, Action: ActionNone, Reason: "a kill was seen before the exit: stopped by hand"}
	case ev.Crashed:
		return Decision{Class: ClassExitedCrashed, Action: ActionStart, Reason: fmt.Sprintf("died with exit %d, no preceding kill", c.ExitCode)}
	}
	// No events seen (whalenurse was down): fall back to the exit code.
	switch {
	case c.ExitCode == 0:
		return Decision{Class: ClassExitedClean, Action: ActionNone, Reason: "exited 0 under restart policy " + policyName(c)}
	case signalled:
		return Decision{Class: ClassExitedSignalled, Action: ActionNotify,
			Reason: fmt.Sprintf("exit %d without OOM: stopped by a signal, not healing", c.ExitCode)}
	}
	return Decision{Class: ClassExitedCrashed, Action: ActionStart, Reason: fmt.Sprintf("exited with code %d", c.ExitCode)}
}

func policyName(c docker.Container) string {
	if c.RestartPolicy == "" {
		return "no"
	}
	return c.RestartPolicy
}

// guardReferences enforces plan §3.5 for containers that share a namespace
// with another container (network_mode/ipc/pid: container:<x>):
//   - parent ID gone: never restart or start it. That fails and turns a
//     running-but-disconnected dependent into an exited one (E1). It needs a
//     recreate (M2).
//   - parent not running, or busy with its own incident: act only on the parent.
//   - a running dependent whose parent started later is stale: M2's dependents
//     step restarts it, M1 only records it.
func guardReferences(in Input, d Decision) Decision {
	if in.C.Status == "removing" {
		return d
	}
	refs := 0
	for _, m := range []string{in.C.NetworkMode, in.C.IpcMode, in.C.PidMode} {
		if docker.ContainerRef(m) != "" {
			refs++
		}
	}
	if refs == 0 {
		return d
	}
	if len(in.Parents) < refs {
		// A container: reference we could not resolve: do nothing rather than guess.
		if d.Action == ActionRestart || d.Action == ActionStart {
			return Decision{Class: ClassParentUnresolved, Action: ActionNone, Cause: d.Cause, Reason: "could not resolve the container: reference; not acting"}
		}
		return d
	}

	for _, p := range in.Parents {
		if !p.Found {
			// Parent ID gone. Regardless of what else is wrong, an API restart/start
			// is forbidden, and a running dependent is silently stale.
			if d.Action == ActionRestart || d.Action == ActionStart ||
				(d.Action == ActionNone && in.C.Running && (d.Class == ClassOK || d.Class == ClassStarting)) {
				return Decision{Class: ClassStaleParentGone, Action: ActionNeedsRecreate, Cause: d.Cause,
					Reason: fmt.Sprintf("%s: container:%s no longer exists (parent was recreated or removed); needs recreate (M2), not restarting", p.Kind, p.Ref)}
			}
			return d
		}
	}
	switch d.Action {
	case ActionRestart, ActionStart:
		for _, p := range in.Parents {
			if !p.Running || p.Busy {
				why := "is not running"
				if p.Busy {
					why = "has an incident open or an action in flight"
				}
				return Decision{Class: ClassWaitingForParent, Action: ActionWaitForParent, Cause: d.Cause,
					Reason: fmt.Sprintf("parent %s %s; acting on the parent only", docker.ShortID(p.ID), why)}
			}
		}
	case ActionNone:
		if in.C.Running && (d.Class == ClassOK) {
			for _, p := range in.Parents {
				if p.Running && !p.StartedAt.IsZero() && p.StartedAt.After(in.C.StartedAt) {
					return Decision{Class: ClassStaleParentRestarted, Action: ActionNeedsDependentRestart,
						Reason: fmt.Sprintf("parent %s started after this container; its namespace is stale; needs restart (M2 dependents step)", docker.ShortID(p.ID))}
				}
			}
		}
	}
	return d
}

func trim(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
