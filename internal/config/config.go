// Package config parses the WHALENURSE_* environment and the whalenurse.*
// container labels. Only the M1 subset is acted on; variables that belong to
// later milestones are accepted with a warning so a full config file does not
// break an M1 deployment.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mode selects which containers are in scope.
type Mode string

const (
	ModeLabel Mode = "label" // only containers with whalenurse.enable=true
	ModeAll   Mode = "all"   // everything except whalenurse.enable=false
)

// Global is the daemon-wide configuration.
type Global struct {
	HostID         string
	Mode           Mode
	DryRun         bool
	PollInterval   time.Duration
	Grace          time.Duration
	Cooldown       time.Duration // base; doubles per rung, capped at CooldownMax
	CrashloopAfter time.Duration
	RateLimit      int // actions per hour
	StartupSettle  time.Duration
	LogLevel       slog.Level
	HTTPAddr       string // empty: no TCP listener
	ControlSocket  string // unix socket for /healthz (and the CLI from M2)
}

// CooldownMax caps the cooldown backoff.
const CooldownMax = 5 * time.Minute

// Defaults returns the plan's §4 defaults for the M1 subset.
func Defaults() Global {
	host, _ := os.Hostname()
	return Global{
		HostID:         host,
		Mode:           ModeLabel,
		PollInterval:   60 * time.Second,
		Grace:          30 * time.Second,
		Cooldown:       30 * time.Second,
		CrashloopAfter: 5 * time.Minute,
		RateLimit:      10,
		StartupSettle:  2 * time.Minute,
		LogLevel:       slog.LevelInfo,
		ControlSocket:  "/run/whalenurse.sock",
	}
}

// later lists variables defined in the plan that M1 does not act on.
var later = map[string]string{
	"WHALENURSE_NOTIFY_URLS":       "M3",
	"WHALENURSE_RECOVERY_PERIOD":   "M2",
	"WHALENURSE_FLAP_LIMIT":        "M2",
	"WHALENURSE_FLAP_WINDOW":       "M2",
	"WHALENURSE_DRIFT_GUARD":       "M2",
	"WHALENURSE_COMPOSE_BIN":       "M2",
	"WHALENURSE_DATA_DIR":          "M2",
	"WHALENURSE_HTTP_TOKEN":        "M5",
	"WHALENURSE_HISTORY_RETENTION": "M2",
	"WHALENURSE_HEARTBEAT_URL":     "M3",
}

// FromEnv parses environ ("KEY=value" strings, as os.Environ returns). It
// returns warnings for ignored or unknown WHALENURSE_* variables, and an error
// for a value that is present but invalid.
func FromEnv(environ []string) (Global, []string, error) {
	g := Defaults()
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "WHALENURSE_") {
			env[k] = v
		}
	}
	var warns []string
	var errs []string
	take := func(k string) (string, bool) {
		v, ok := env[k]
		delete(env, k)
		v = strings.TrimSpace(v)
		return v, ok && v != ""
	}
	dur := func(k string, dst *time.Duration, allowZero bool) {
		if v, ok := take(k); ok {
			d, err := time.ParseDuration(v)
			switch {
			case err != nil:
				errs = append(errs, fmt.Sprintf("%s=%q: %v", k, v, err))
			case d < 0 || (d == 0 && !allowZero):
				errs = append(errs, fmt.Sprintf("%s=%q: must be positive", k, v))
			default:
				*dst = d
			}
		}
	}

	if v, ok := take("WHALENURSE_HOST_ID"); ok {
		g.HostID = v
	}
	if g.HostID == "" {
		g.HostID = "unknown"
	}
	if v, ok := take("WHALENURSE_MODE"); ok {
		switch Mode(strings.ToLower(v)) {
		case ModeLabel, ModeAll:
			g.Mode = Mode(strings.ToLower(v))
		default:
			errs = append(errs, fmt.Sprintf("WHALENURSE_MODE=%q: want label or all", v))
		}
	}
	if v, ok := take("WHALENURSE_DRY_RUN"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Sprintf("WHALENURSE_DRY_RUN=%q: %v", v, err))
		}
		g.DryRun = b
	}
	dur("WHALENURSE_POLL_INTERVAL", &g.PollInterval, false)
	dur("WHALENURSE_GRACE", &g.Grace, true)
	dur("WHALENURSE_COOLDOWN", &g.Cooldown, true)
	dur("WHALENURSE_CRASHLOOP_AFTER", &g.CrashloopAfter, false)
	dur("WHALENURSE_STARTUP_SETTLE", &g.StartupSettle, true)
	if v, ok := take("WHALENURSE_RATE_LIMIT"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Sprintf("WHALENURSE_RATE_LIMIT=%q: want a positive integer", v))
		} else {
			g.RateLimit = n
		}
	}
	if v, ok := take("WHALENURSE_LOG_LEVEL"); ok {
		if err := g.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Sprintf("WHALENURSE_LOG_LEVEL=%q: %v", v, err))
		}
	}
	if v, ok := take("WHALENURSE_HTTP_ADDR"); ok {
		g.HTTPAddr = v
	}
	if v, ok := take("WHALENURSE_CONTROL_SOCKET"); ok {
		g.ControlSocket = v
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if ms, ok := later[k]; ok {
			warns = append(warns, fmt.Sprintf("%s is set but not used until %s", k, ms))
		} else {
			warns = append(warns, fmt.Sprintf("%s is not a known variable", k))
		}
	}
	if len(errs) > 0 {
		return g, warns, fmt.Errorf("invalid configuration: %s", strings.Join(errs, "; "))
	}
	return g, warns, nil
}

// CooldownFor returns the cooldown after rung n (0-based): base, doubling per
// rung, capped at CooldownMax. A positive override replaces the base.
func CooldownFor(base, override time.Duration, rung int) time.Duration {
	if override > 0 {
		base = override
	}
	d := base
	for i := 0; i < rung && d < CooldownMax; i++ {
		d *= 2
	}
	return min(d, CooldownMax)
}
