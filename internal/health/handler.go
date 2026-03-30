// Package health provides liveness and readiness HTTP handlers.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
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

// readiness pings all databases. Returns 503 with details if any fail.
func (h *Handler) readiness(w http.ResponseWriter, r *http.Request) {
	if h.checker == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	type dbResult struct {
		OK  bool   `json:"ok"`
		Err string `json:"error,omitempty"`
	}

	checks := h.checker.HealthCheck(ctx)
	results := make(map[string]dbResult, len(checks))
	allOK := true

	for name, err := range checks {
		if err != nil {
			results[name] = dbResult{OK: false, Err: err.Error()}
			allOK = false
		} else {
			results[name] = dbResult{OK: true}
		}
	}

	if allOK {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "databases": results})
	} else {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"status": "unavailable", "databases": results})
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
