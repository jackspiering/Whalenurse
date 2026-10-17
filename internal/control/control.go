// Package control serves whalenurse over a local unix socket: /healthz for the
// image's HEALTHCHECK (distroless has no shell or curl) and the commands of the
// CLI (pause, resume, unsuspend, status). The commands are writes, so they are
// served on the socket only: the TCP listener (WHALENURSE_HTTP_ADDR) gets the
// read-only api.Handler and has no write endpoints at all (plan §2, *Control in v1*).
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Backend is what the commands act on (the daemon).
type Backend interface {
	// Pause stops actions for the scope for d (zero: until resumed). The strings are the human reply.
	Pause(scope string, d time.Duration) (string, error)
	Resume(scope string) (string, error)
	Unsuspend(scope string) (string, error)
	Status() any
}

// ErrNothingToDo makes a command answer 404 instead of 400: the scope is valid but matched nothing.
var ErrNothingToDo = errors.New("nothing to do")

// maxBody bounds a command's request body; they are a few bytes.
const maxBody = 4 << 10

// Request is the body of pause, resume and unsuspend.
type Request struct {
	Scope string `json:"scope"`
	For   string `json:"for,omitempty"` // pause only: a duration ("2h", "1d"), "0" for no expiry; empty for the default
}

// Reply is the body of every command's answer.
type Reply struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Mux serves health and the commands. Only Serve on a unix socket should use it.
func Mux(health http.Handler, b Backend, parseFor func(string) (time.Duration, error)) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/healthz", health)
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, b.Status()) })
	cmd := func(path string, run func(Request) (string, error)) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			var req Request
			if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, Reply{Error: "bad request: " + err.Error()})
				return
			}
			msg, err := run(req)
			switch {
			case errors.Is(err, ErrNothingToDo):
				writeJSON(w, http.StatusNotFound, Reply{Error: err.Error()})
			case err != nil:
				writeJSON(w, http.StatusBadRequest, Reply{Error: err.Error()})
			default:
				writeJSON(w, http.StatusOK, Reply{Message: msg})
			}
		})
	}
	cmd("/v1/pause", func(r Request) (string, error) {
		d, err := parseFor(r.For)
		if err != nil {
			return "", err
		}
		return b.Pause(r.Scope, d)
	})
	cmd("/v1/resume", func(r Request) (string, error) { return b.Resume(r.Scope) })
	cmd("/v1/unsuspend", func(r Request) (string, error) { return b.Unsuspend(r.Scope) })
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

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

func client(path string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}},
	}
}

// Do performs a request against the socket and returns the status and the whole body.
func Do(ctx context.Context, path, method, urlPath string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://whalenurse"+urlPath, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client(path).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, err
}

// Get performs a GET against the socket and returns the status and the body.
func Get(ctx context.Context, path, urlPath string) (int, string, error) {
	code, b, err := Do(ctx, path, http.MethodGet, urlPath, nil)
	return code, string(b), err
}

// Command sends one command and returns the daemon's message. A refusal comes back as an error carrying the daemon's reason.
func Command(ctx context.Context, path, name string, req Request) (string, error) {
	code, b, err := Do(ctx, path, http.MethodPost, "/v1/"+name, req)
	if err != nil {
		return "", fmt.Errorf("cannot reach the whalenurse daemon on %s (is it running, and are you in the same container?): %w", path, err)
	}
	var rep Reply
	if err := json.Unmarshal(b, &rep); err != nil {
		return "", fmt.Errorf("unexpected reply (%d): %.200s", code, b)
	}
	if code != http.StatusOK {
		if rep.Error == "" {
			rep.Error = fmt.Sprintf("HTTP %d", code)
		}
		return "", errors.New(rep.Error)
	}
	return rep.Message, nil
}

// Health asks the running daemon for /healthz; a non-nil error means unhealthy.
func Health(ctx context.Context, path string) error {
	code, body, err := Get(ctx, path, "/healthz")
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("unhealthy (%d): %s", code, strings.TrimSpace(body))
	}
	return nil
}
