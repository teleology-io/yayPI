package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

func newTestRegistry(t *testing.T, ep *schema.Endpoint) *schema.Registry {
	t.Helper()
	reg := schema.NewRegistry()
	reg.RegisterEntity(&schema.Entity{Name: "widgets", Table: "widgets"})
	reg.RegisterEndpoint(ep)
	return reg
}

// TestCustomHandler_NoAuth verifies a plugin-registered `method`/`handler` endpoint (the
// stub this test locks in — see registerCustomHandler) is reachable and receives the
// request when no auth is required.
func TestCustomHandler_NoAuth(t *testing.T) {
	dispatcher := plugin.NewDispatcher()
	dispatcher.RegisterRoutePlugin(fakeRoutePlugin{
		name: "test",
		handlers: map[string]sdk.RouteHandlerFunc{
			"Ping": func(rc sdk.RouteContext) {
				rc.Response.WriteHeader(http.StatusTeapot)
			},
		},
	})

	reg := newTestRegistry(t, &schema.Endpoint{
		Path:    "/widgets/ping",
		Entity:  "widgets",
		Method:  "GET",
		Handler: "test.Ping",
	})

	h := Build(reg, nil, Config{Dispatcher: dispatcher, BaseURL: "/api/v1"})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/widgets/ping", nil)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusTeapot {
		t.Fatalf("expected %d, got %d", http.StatusTeapot, rr.Code)
	}
}

// TestCustomHandler_RequiresAuth verifies auth.require on a custom-handler endpoint is
// enforced by the same middleware chain CRUD endpoints get — the handler should never run
// for an unauthenticated request.
func TestCustomHandler_RequiresAuth(t *testing.T) {
	dispatcher := plugin.NewDispatcher()
	called := false
	dispatcher.RegisterRoutePlugin(fakeRoutePlugin{
		name: "test",
		handlers: map[string]sdk.RouteHandlerFunc{
			"Ping": func(rc sdk.RouteContext) {
				called = true
				rc.Response.WriteHeader(http.StatusOK)
			},
		},
	})

	reg := newTestRegistry(t, &schema.Endpoint{
		Path:    "/widgets/ping",
		Entity:  "widgets",
		Method:  "GET",
		Handler: "test.Ping",
		Auth:    &schema.Auth{Require: true},
	})

	h := Build(reg, nil, Config{
		Dispatcher: dispatcher,
		AuthSecret: []byte("test-secret"),
		AuthAlg:    "HS256",
		BaseURL:    "/api/v1",
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/widgets/ping", nil)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rr.Code)
	}
	if called {
		t.Fatal("handler ran despite missing auth")
	}
}

// TestCustomHandler_UnregisteredSkipped verifies an endpoint referencing a handler name
// nothing registered doesn't panic and simply isn't routed (404).
func TestCustomHandler_UnregisteredSkipped(t *testing.T) {
	reg := newTestRegistry(t, &schema.Endpoint{
		Path:    "/widgets/ping",
		Entity:  "widgets",
		Method:  "GET",
		Handler: "nope.Ping",
	})

	h := Build(reg, nil, Config{Dispatcher: plugin.NewDispatcher(), BaseURL: "/api/v1"})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/widgets/ping", nil)
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected %d, got %d", http.StatusNotFound, rr.Code)
	}
}

type fakeRoutePlugin struct {
	name     string
	handlers map[string]sdk.RouteHandlerFunc
}

func (f fakeRoutePlugin) Info() sdk.PluginInfo           { return sdk.PluginInfo{Name: f.name} }
func (f fakeRoutePlugin) Init(sdk.InitContext) error     { return nil }
func (f fakeRoutePlugin) Shutdown(context.Context) error { return nil }
func (f fakeRoutePlugin) Handlers() map[string]sdk.RouteHandlerFunc {
	return f.handlers
}
