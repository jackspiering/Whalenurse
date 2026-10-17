package control

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackspiering/whalenurse/internal/api"
)

type fakeChecker struct {
	ok  bool
	msg string
}

func (f *fakeChecker) Healthy() (bool, string) { return f.ok, f.msg }

type fakeBackend struct {
	calls []string
	big   int
}

func (b *fakeBackend) Pause(scope string, d time.Duration) (string, error) {
	b.calls = append(b.calls, fmt.Sprintf("pause %s %s", scope, d))
	if scope == "bad/scope/x" {
		return "", fmt.Errorf("scope %q: want <project> or <project>/<service>", scope)
	}
	return "paused " + scope, nil
}
func (b *fakeBackend) Resume(scope string) (string, error) {
	b.calls = append(b.calls, "resume "+scope)
	return "", fmt.Errorf("%w: %s is not paused", ErrNothingToDo, scope)
}
func (b *fakeBackend) Unsuspend(scope string) (string, error) {
	b.calls = append(b.calls, "unsuspend "+scope)
	return "unsuspended " + scope, nil
}
func (b *fakeBackend) Status() any {
	return map[string]string{"filler": strings.Repeat("x", b.big)}
}

func parseFor(v string) (time.Duration, error) {
	if v == "" {
		return 2 * time.Hour, nil
	}
	return time.ParseDuration(v)
}

func serve(t *testing.T, h http.Handler) (sock string, stop func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wn") // short path: unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	sock = filepath.Join(dir, "w.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, sock, h) }()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return sock, func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(sock); err == nil {
			t.Error("socket not removed on shutdown")
		}
		os.RemoveAll(dir)
	}
}

func TestHealthOverUnixSocket(t *testing.T) {
	chk := &fakeChecker{ok: true, msg: "ok"}
	sock, stop := serve(t, api.Handler(chk))
	defer stop()
	ctx := context.Background()

	if err := Health(ctx, sock); err != nil {
		t.Fatalf("healthy daemon reported: %v", err)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}
	chk.ok, chk.msg = false, "docker events stream down for 5m0s"
	if err := Health(ctx, sock); err == nil {
		t.Fatal("unhealthy daemon reported healthy")
	}
	if code, _, _ := Get(ctx, sock, "/api/v1/status"); code != 404 {
		t.Fatalf("unknown path: %d", code)
	}
}

func TestCommandsOverTheSocket(t *testing.T) {
	be := &fakeBackend{}
	sock, stop := serve(t, Mux(api.Handler(&fakeChecker{ok: true, msg: "ok"}), be, parseFor))
	defer stop()
	ctx := context.Background()

	if err := Health(ctx, sock); err != nil {
		t.Fatal(err)
	}
	msg, err := Command(ctx, sock, "pause", Request{Scope: "p/web", For: "90m"})
	if err != nil || msg != "paused p/web" {
		t.Fatalf("%q %v", msg, err)
	}
	if _, err := Command(ctx, sock, "pause", Request{Scope: "p"}); err != nil {
		t.Fatal(err)
	}
	if msg, err := Command(ctx, sock, "unsuspend", Request{Scope: "p"}); err != nil || msg != "unsuspended p" {
		t.Fatalf("%q %v", msg, err)
	}
	// A scope that matches nothing is a 404 with the reason, a malformed one a 400; both come back as errors.
	if _, err := Command(ctx, sock, "resume", Request{Scope: "p"}); err == nil || !strings.Contains(err.Error(), "is not paused") {
		t.Fatalf("%v", err)
	}
	if _, err := Command(ctx, sock, "pause", Request{Scope: "bad/scope/x"}); err == nil || !strings.Contains(err.Error(), "want <project>") {
		t.Fatalf("%v", err)
	}
	if _, err := Command(ctx, sock, "pause", Request{Scope: "p", For: "soon"}); err == nil {
		t.Fatal("a bad --for must be refused")
	}
	want := []string{"pause p/web 1h30m0s", "pause p 2h0m0s", "unsuspend p", "resume p", "pause bad/scope/x 2h0m0s"}
	if strings.Join(be.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("backend calls = %v, want %v", be.calls, want)
	}

	// Commands are POST only.
	if code, _, _ := Do(ctx, sock, http.MethodGet, "/v1/pause", nil); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/pause: %d", code)
	}
	if code, _, _ := Do(ctx, sock, http.MethodPost, "/v1/pause", nil); code != http.StatusBadRequest {
		t.Fatalf("POST without a body: %d", code)
	}
}

// The status reply is far larger than the 512 bytes the M1 client read.
func TestStatusIsNotTruncated(t *testing.T) {
	be := &fakeBackend{big: 200 << 10}
	sock, stop := serve(t, Mux(api.Handler(&fakeChecker{ok: true}), be, parseFor))
	defer stop()
	code, body, err := Do(context.Background(), sock, http.MethodGet, "/v1/status", nil)
	if err != nil || code != 200 || len(body) < 200<<10 {
		t.Fatalf("code=%d len=%d err=%v", code, len(body), err)
	}
}

func TestCommandWithoutADaemonSaysSo(t *testing.T) {
	_, err := Command(context.Background(), filepath.Join(t.TempDir(), "none.sock"), "pause", Request{Scope: "p"})
	if err == nil || !strings.Contains(err.Error(), "cannot reach the whalenurse daemon") {
		t.Fatalf("%v", err)
	}
}
