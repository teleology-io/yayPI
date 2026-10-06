// Package health provides liveness and readiness HTTP handlers.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

// Checker is implemented by anything that can ping its backends.
// db.Manager satisfies this interface via its HealthCheck method.
type Checker interface {
	HealthCheck(ctx context.Context) map[string]error
}

// Handler serves /health (liveness) and /ready (readiness) endpoints.
type Handler struct {
	checker       Checker // nil if no databases configured
	livenessPath  string
	readinessPath string
	draining      atomic.Bool
}

// New creates a Handler. checker may be nil (liveness-only mode).
func New(checker Checker, livenessPath, readinessPath string) *Handler {
	if livenessPath == "" {
		livenessPath = "/health"
	}
	if readinessPath == "" {
		readinessPath = "/ready"
	}
	return &Handler{
		checker:       checker,
		livenessPath:  livenessPath,
		readinessPath: readinessPath,
	}
}

// SetDraining makes readiness fail so load balancers stop routing new traffic here
// while in-flight requests finish (called at the start of graceful shutdown).
func (h *Handler) SetDraining() { h.draining.Store(true) }

// Mount registers /health and /ready on r.
func (h *Handler) Mount(r interface {
	Get(pattern string, handlerFn http.HandlerFunc)
}) {
	r.Get(h.livenessPath, h.liveness)
	r.Get(h.readinessPath, h.readiness)
}

// liveness always returns 200 — the process is alive if it can respond.
func (h *Handler) liveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readiness pings all databases. Failures are reported per database as ok:false only;
// driver error text (which can contain hostnames or DSN fragments) goes to the log, not
// the response.
func (h *Handler) readiness(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
		return
	}
	if h.checker == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	type dbResult struct {
		OK bool `json:"ok"`
	}
	checks := h.checker.HealthCheck(ctx)
	results := make(map[string]dbResult, len(checks))
	allOK := true
	for name, err := range checks {
		if err != nil {
			log.Warn().Str("database", name).Err(err).Msg("readiness check failed")
			allOK = false
		}
		results[name] = dbResult{OK: err == nil}
	}

	status, code := "ok", http.StatusOK
	if !allOK {
		status, code = "unavailable", http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{"status": status, "databases": results})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
