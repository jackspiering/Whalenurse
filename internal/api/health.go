// Package api serves the HTTP surface. In M1 that is /healthz only; the
// versioned /api/v1 JSON, SSE and metrics arrive in M5.
package api

import (
	"fmt"
	"net/http"
)

// Checker reports whether whalenurse is working.
type Checker interface {
	Healthy() (ok bool, detail string)
}

// Handler serves /healthz: 200 "ok", or 503 with the reason (for example the
// events stream being down for too long).
func Handler(c Checker) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		ok, detail := c.Healthy()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		fmt.Fprintln(w, detail)
	})
	return mux
}
