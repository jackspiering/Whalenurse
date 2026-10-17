package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestDefaultsMatchThePlan(t *testing.T) {
	g, warns, err := FromEnv([]string{"PATH=/bin", "WHALENURSE_HOST_ID=box"})
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	if g.HostID != "box" || g.Mode != ModeLabel || g.DryRun || g.PollInterval != 60*time.Second || g.Grace != 30*time.Second ||
		g.Cooldown != 30*time.Second || g.CrashloopAfter != 5*time.Minute || g.RateLimit != 10 || g.StartupSettle != 2*time.Minute ||
		g.LogLevel != slog.LevelInfo || g.HTTPAddr != "" {
		t.Fatalf("%+v", g)
	}
}

func TestEnvOverridesAndErrors(t *testing.T) {
	g, warns, err := FromEnv([]string{
		"WHALENURSE_MODE=ALL", "WHALENURSE_DRY_RUN=true", "WHALENURSE_GRACE=5s", "WHALENURSE_RATE_LIMIT=3",
		"WHALENURSE_LOG_LEVEL=debug", "WHALENURSE_HTTP_ADDR=:8484", "WHALENURSE_STARTUP_SETTLE=0s",
		"WHALENURSE_FLAP_LIMIT=5", "WHALENURSE_NOTIFY_URLS=ntfy://x", "WHALENURSE_TYPO=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if g.Mode != ModeAll || !g.DryRun || g.Grace != 5*time.Second || g.RateLimit != 3 || g.LogLevel != slog.LevelDebug ||
		g.HTTPAddr != ":8484" || g.StartupSettle != 0 {
		t.Fatalf("%+v", g)
	}
	joined := strings.Join(warns, "|")
	for _, want := range []string{"WHALENURSE_FLAP_LIMIT is set but not used until M2", "WHALENURSE_NOTIFY_URLS is set but not used until M3", "WHALENURSE_TYPO is not a known variable"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning %q in %v", want, warns)
		}
	}
	// The notify URL may hold a token: it must never be echoed in a warning.
	if strings.Contains(joined, "ntfy://") {
		t.Errorf("a warning leaked a variable value: %v", warns)
	}

	for _, bad := range []string{"WHALENURSE_MODE=nope", "WHALENURSE_GRACE=soon", "WHALENURSE_POLL_INTERVAL=0s", "WHALENURSE_RATE_LIMIT=0", "WHALENURSE_DRY_RUN=maybe", "WHALENURSE_LOG_LEVEL=loud"} {
		if _, _, err := FromEnv([]string{bad}); err == nil {
			t.Errorf("%s should be rejected", bad)
		}
	}
}

func TestParseLabels(t *testing.T) {
	cases := []struct {
		name   string
		mode   Mode
		labels map[string]string
		opted  bool
		acts   string
		heal   HealExited
		cd     time.Duration
		warns  int
	}{
		{"label mode, no label", ModeLabel, nil, false, "restart,recreate", HealCrashed, 0, 0},
		{"enabled", ModeLabel, map[string]string{LabelEnable: "true"}, true, "restart,recreate", HealCrashed, 0, 0},
		{"all mode", ModeAll, nil, true, "restart,recreate", HealCrashed, 0, 0},
		{"all mode opt-out", ModeAll, map[string]string{LabelEnable: "false"}, false, "restart,recreate", HealCrashed, 0, 0},
		{"bad enable keeps the mode default", ModeLabel, map[string]string{LabelEnable: "yes please"}, false, "restart,recreate", HealCrashed, 0, 1},
		{"actions", ModeLabel, map[string]string{LabelEnable: "1", LabelActions: " Restart "}, true, "restart", HealCrashed, 0, 0},
		{"bad action falls back", ModeLabel, map[string]string{LabelEnable: "1", LabelActions: "restart,reboot"}, true, "restart,recreate", HealCrashed, 0, 1},
		{"pull only via heal.pull", ModeLabel, map[string]string{LabelEnable: "1", LabelActions: "restart,pull"}, true, "restart,recreate", HealCrashed, 0, 1},
		{"heal.pull appends the rung", ModeLabel, map[string]string{LabelEnable: "1", LabelHealPull: "true"}, true, "restart,recreate,pull", HealCrashed, 0, 0},
		{"heal-exited", ModeLabel, map[string]string{LabelEnable: "1", LabelHealExited: "always"}, true, "restart,recreate", HealAlways, 0, 0},
		{"bad heal-exited", ModeLabel, map[string]string{LabelEnable: "1", LabelHealExited: "sometimes"}, true, "restart,recreate", HealCrashed, 0, 1},
		{"cooldown", ModeLabel, map[string]string{LabelEnable: "1", LabelCooldown: "2m"}, true, "restart,recreate", HealCrashed, 2 * time.Minute, 0},
		{"bad cooldown", ModeLabel, map[string]string{LabelEnable: "1", LabelCooldown: "-1s"}, true, "restart,recreate", HealCrashed, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, warns := ParseLabels(tc.labels, tc.mode)
			if c.OptedIn != tc.opted || strings.Join(c.Actions, ",") != tc.acts || c.HealExited != tc.heal || c.Cooldown != tc.cd || len(warns) != tc.warns {
				t.Fatalf("%+v warns=%v", c, warns)
			}
		})
	}
}

func TestCooldownFor(t *testing.T) {
	base := 30 * time.Second
	for rung, want := range map[int]time.Duration{0: 30 * time.Second, 1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 4: 5 * time.Minute, 9: 5 * time.Minute} {
		if got := CooldownFor(base, 0, rung); got != want {
			t.Errorf("rung %d: %s, want %s", rung, got, want)
		}
	}
	if got := CooldownFor(base, 10*time.Minute, 0); got != CooldownMax {
		t.Errorf("override is capped: %s", got)
	}
	if got := CooldownFor(base, 3*time.Minute, 0); got != 3*time.Minute {
		t.Errorf("override: %s", got)
	}
}
