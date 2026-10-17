package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackspiering/whalenurse/internal/api"
)

type fakeChecker struct {
	ok  bool
	msg string
}

func (f *fakeChecker) Healthy() (bool, string) { return f.ok, f.msg }

func TestHealthOverUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "wn") // short path: unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "w.sock")
	chk := &fakeChecker{ok: true, msg: "ok"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, sock, api.Handler(chk)) }()

	var herr error
	for i := 0; i < 100; i++ {
		if herr = Health(ctx, sock); herr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if herr != nil {
		t.Fatalf("healthy daemon reported: %v", herr)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", fi, err)
	}

	chk.ok, chk.msg = false, "docker events stream down for 5m0s"
	if err := Health(ctx, sock); err == nil {
		t.Fatal("unhealthy daemon reported healthy")
	}
	if code, _, _ := Get(ctx, sock, "/api/v1/status"); code != 404 {
		t.Fatalf("M1 serves /healthz only, got %d", code)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("socket not removed on shutdown")
	}
}
