// Package control serves whalenurse over a local unix socket. In M1 it carries
// /healthz so the image's HEALTHCHECK can call the binary (distroless has no
// shell or curl); M2 adds the pause/resume/unsuspend/status commands here.
package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Serve listens on the unix socket path until ctx ends. The socket is created
// with mode 0600; the parent directory must exist and be writable.
func Serve(ctx context.Context, path string, h http.Handler) error {
	_ = os.Remove(path) // a stale socket from a previous run
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = os.Remove(path)
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Get performs a GET against the socket and returns the status and body.
func Get(ctx context.Context, path, urlPath string) (int, string, error) {
	c := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://whalenurse"+urlPath, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, string(buf[:n]), nil
}

// Health asks the running daemon for /healthz; a non-nil error means unhealthy.
func Health(ctx context.Context, path string) error {
	code, body, err := Get(ctx, path, "/healthz")
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("unhealthy (%d): %s", code, body)
	}
	return nil
}
