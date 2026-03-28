package openapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Handler serves pre-built OpenAPI specs over HTTP.
type Handler struct {
	specs map[string][]byte // spec name → pre-marshaled JSON
}

// NewHandler pre-marshals all specs to JSON at construction time.
func NewHandler(specs map[string]*Spec) (*Handler, error) {
	h := &Handler{specs: make(map[string][]byte, len(specs))}
	for name, spec := range specs {
		b, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshaling spec %q: %w", name, err)
		}
		h.specs[name] = b
	}
	return h, nil
}

// Mount registers the spec endpoints on the router at /openapi/{name}.json.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/openapi/{name}.json", h.serve)
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	b, ok := h.specs[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
