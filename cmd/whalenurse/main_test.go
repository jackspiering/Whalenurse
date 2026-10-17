package main

import (
	"bytes"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackspiering/whalenurse/internal/daemon"
	"github.com/jackspiering/whalenurse/internal/engine"
)

type fakeDaemon struct{ calls []string }

func (f *fakeDaemon) Healthy() (bool, string) { return true, "ok" }
func (f *fakeDaemon) Pause(s string, d time.Duration) (string, error) {
	f.calls = append(f.calls, "pause "+s)
	return "paused", nil
}
func (f *fakeDaemon) Resume(s string) (string, error) {
	f.calls = append(f.calls, "resume "+s)
	return "resumed", nil
}
func (f *fakeDaemon) Unsuspend(s string) (string, error) {
	f.calls = append(f.calls, "unsuspend "+s)
	return "ok", nil
}
func (f *fakeDaemon) Status() any { f.calls = append(f.calls, "status"); return daemon.Status{} }

// There are no network-exposed write endpoints in v1: whatever the TCP listener is, it cannot pause,
// resume or unsuspend anything. Only the unix socket handler can.
func TestTCPHandlerHasNoWriteEndpoints(t *testing.T) {
	d := &fakeDaemon{}
	tcp, sock := handlers(d)

	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/pause"}, {"POST", "/v1/resume"}, {"POST", "/v1/unsuspend"}, {"GET", "/v1/status"},
		{"PUT", "/v1/pause"}, {"DELETE", "/v1/pause"},
	} {
		rec := httptest.NewRecorder()
		tcp.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"scope":"p"}`)))
		if rec.Code < 400 {
			t.Errorf("TCP handler answered %s %s with %d", tc.method, tc.path, rec.Code)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("the TCP handler reached the daemon: %v", d.calls)
	}
	rec := httptest.NewRecorder()
	tcp.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	sock.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/pause", strings.NewReader(`{"scope":"p"}`)))
	if rec.Code != http.StatusOK || len(d.calls) != 1 {
		t.Fatalf("the socket handler should pause: %d %v", rec.Code, d.calls)
	}
}

func TestPrintStatus(t *testing.T) {
	var b bytes.Buffer
	printStatus(&b, daemon.Status{
		Host: "h", Mode: "label", Healthy: true, EventsUp: true, Settled: true,
		Engine: engine.Status{
			RateLimit: 10, ActionsLastH: 2,
			Pauses: []engine.Pause{{Scope: "p/web"}},
			Keys:   []engine.KeyStatus{{Key: "h/p/web/1", State: "SUSPENDED", Class: "unhealthy", SuspendReason: "ladder", Rung: 2, Flaps: 1}},
		},
	})
	out := b.String()
	for _, want := range []string{"host h", "2 actions in the last hour (limit 10)", "PAUSED", "p/web  until resume", "h/p/web/1", "SUSPENDED", "suspended: ladder", "rung 2 done"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output lacks %q:\n%s", want, out)
		}
	}
}

func TestSplitArgsAcceptsFlagsAfterThePositional(t *testing.T) {
	for _, args := range [][]string{{"p/web", "--for", "2h"}, {"--for", "2h", "p/web"}, {"--for=2h", "p/web"}} {
		fs := flag.NewFlagSet("pause", flag.ContinueOnError)
		dur := pauseFor(fs)
		pos, err := splitArgs(fs, args)
		if err != nil || len(pos) != 1 || pos[0] != "p/web" || *dur != "2h" {
			t.Errorf("%v: pos=%v dur=%q err=%v", args, pos, *dur, err)
		}
	}
}
