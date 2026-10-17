package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Label keys.
const (
	LabelEnable     = "whalenurse.enable"
	LabelActions    = "whalenurse.actions"
	LabelHealExited = "whalenurse.heal-exited"
	LabelHealPull   = "whalenurse.heal.pull"
	LabelCooldown   = "whalenurse.cooldown"
	LabelFlapLimit  = "whalenurse.flap-limit"
)

// Rung names, as used in whalenurse.actions.
const (
	RungRestart  = "restart"
	RungRecreate = "recreate"
	RungPull     = "pull"
)

// HealExited is the whalenurse.heal-exited mode.
type HealExited string

const (
	HealCrashed HealExited = "crashed"
	HealAlways  HealExited = "always"
	HealNever   HealExited = "never"
)

// Container is the effective per-container configuration.
type Container struct {
	OptedIn    bool
	Actions    []string // ladder rungs, in order
	HealExited HealExited
	Cooldown   time.Duration // 0: use the global base
	FlapLimit  int           // 0: use the global limit
}

// HasRung reports whether the ladder includes the rung.
func (c Container) HasRung(r string) bool {
	for _, a := range c.Actions {
		if a == r {
			return true
		}
	}
	return false
}

// ParseLabels derives the per-container configuration. Invalid values fall
// back to the default and produce a warning; they never opt a container out
// silently or in by accident.
func ParseLabels(labels map[string]string, mode Mode) (Container, []string) {
	c := Container{
		OptedIn:    mode == ModeAll,
		Actions:    []string{RungRestart, RungRecreate},
		HealExited: HealCrashed,
	}
	var warns []string
	warn := func(format string, a ...any) { warns = append(warns, fmt.Sprintf(format, a...)) }

	if v, ok := labels[LabelEnable]; ok {
		b, err := strconv.ParseBool(strings.TrimSpace(v))
		if err != nil {
			warn("%s=%q is not a boolean; container is %s", LabelEnable, v, map[bool]string{true: "kept in scope", false: "not opted in"}[c.OptedIn])
		} else {
			c.OptedIn = b
		}
	}
	if v, ok := labels[LabelActions]; ok {
		var acts []string
		valid := true
		for _, a := range strings.Split(v, ",") {
			a = strings.ToLower(strings.TrimSpace(a))
			switch a {
			case RungRestart, RungRecreate:
				acts = append(acts, a)
			case RungPull:
				// Rung 3 is appended only by whalenurse.heal.pull=true (plan §3.2).
				valid = false
				warn("%s: pull is enabled with %s=true, not here; using the default", LabelActions, LabelHealPull)
			case "":
			default:
				valid = false
				warn("%s: unknown action %q (want restart, recreate); using the default", LabelActions, a)
			}
		}
		if valid && len(acts) > 0 {
			c.Actions = acts
		}
	}
	if v, ok := labels[LabelHealPull]; ok {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err != nil {
			warn("%s=%q is not a boolean; ignored", LabelHealPull, v)
		} else if b && !c.HasRung(RungPull) {
			c.Actions = append(c.Actions, RungPull)
		}
	}
	if v, ok := labels[LabelHealExited]; ok {
		switch h := HealExited(strings.ToLower(strings.TrimSpace(v))); h {
		case HealCrashed, HealAlways, HealNever:
			c.HealExited = h
		default:
			warn("%s=%q: want crashed, always or never; using crashed", LabelHealExited, v)
		}
	}
	if v, ok := labels[LabelCooldown]; ok {
		if d, err := ParseDuration(strings.TrimSpace(v)); err != nil || d <= 0 {
			warn("%s=%q is not a positive duration; ignored", LabelCooldown, v)
		} else {
			c.Cooldown = d
		}
	}
	if v, ok := labels[LabelFlapLimit]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err != nil || n < 1 {
			warn("%s=%q is not a positive integer; ignored", LabelFlapLimit, v)
		} else {
			c.FlapLimit = n
		}
	}
	return c, warns
}
