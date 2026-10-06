package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/teleology-io/yayPI/internal/plugin"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/token"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

const testSecret = "test-secret-test-secret-test-secret"

func testKeys(t *testing.T) *token.Keys {
	t.Helper()
	k, err := token.New(token.Config{Secret: []byte(testSecret), AccessTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func signTestToken(t *testing.T, role string) string {
	t.Helper()
	s, err := testKeys(t).Sign(jwt.MapClaims{"sub": "u1", "role": role})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func okPluginDispatcher() *plugin.Dispatcher {
	d := plugin.NewDispatcher()
	d.RegisterRoutePlugin(fakeRoutePlugin{
		name: "test",
		handlers: map[string]sdk.RouteHandlerFunc{
			"Ping": func(rc sdk.RouteContext) { rc.Response.WriteHeader(http.StatusOK) },
		},
	})
	return d
}

// TestRolesEnforcedWithoutPolicyEngine locks in that auth.roles is honoured even when no
// Casbin policy engine is configured (previously the whole check was skipped).
func TestRolesEnforcedWithoutPolicyEngine(t *testing.T) {
	reg := newTestRegistry(t, &schema.Endpoint{
		Path: "/widgets/ping", Entity: "widgets", Method: "GET", Handler: "test.Ping",
		Auth: &schema.Auth{Require: true, Roles: []string{"admin"}},
	})
	h := Build(reg, nil, Config{
		Dispatcher: okPluginDispatcher(), Tokens: testKeys(t), BaseURL: "/api",
	})

	for role, want := range map[string]int{"member": http.StatusForbidden, "admin": http.StatusOK} {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/widgets/ping", nil)
		req.Header.Set("Authorization", "Bearer "+signTestToken(t, role))
		h.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Errorf("role %s: expected %d, got %d", role, want, rr.Code)
		}
	}
}

// TestRolesImplyAuth verifies roles without require: true still rejects anonymous callers.
func TestRolesImplyAuth(t *testing.T) {
	reg := newTestRegistry(t, &schema.Endpoint{
		Path: "/widgets/ping", Entity: "widgets", Method: "GET", Handler: "test.Ping",
		Auth: &schema.Auth{Roles: []string{"admin"}},
	})
	h := Build(reg, nil, Config{
		Dispatcher: okPluginDispatcher(), Tokens: testKeys(t), BaseURL: "/api",
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/widgets/ping", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// TestRequireAuthWithoutJWTConfigFailsClosed verifies require: true with no JWT secret
// configured denies instead of leaving the route open.
func TestRequireAuthWithoutJWTConfigFailsClosed(t *testing.T) {
	reg := newTestRegistry(t, &schema.Endpoint{
		Path: "/widgets/ping", Entity: "widgets", Method: "GET", Handler: "test.Ping",
		Auth: &schema.Auth{Require: true},
	})
	h := Build(reg, nil, Config{Dispatcher: okPluginDispatcher(), BaseURL: "/api"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/widgets/ping", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// TestEndpointMaxBodyOverride verifies an endpoint's max_body_size can raise the server
// default (e.g. for an upload route) while other routes keep the default.
func TestEndpointMaxBodyOverride(t *testing.T) {
	d := plugin.NewDispatcher()
	d.RegisterRoutePlugin(fakeRoutePlugin{name: "test", handlers: map[string]sdk.RouteHandlerFunc{
		"Upload": func(rc sdk.RouteContext) {
			if _, err := io.ReadAll(rc.Request.Body); err != nil {
				rc.Response.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			rc.Response.WriteHeader(http.StatusOK)
		},
	}})
	reg := schema.NewRegistry()
	reg.RegisterEntity(&schema.Entity{Name: "widgets", Table: "widgets"})
	reg.RegisterEndpoint(&schema.Endpoint{Path: "/small", Entity: "widgets", Method: "POST", Handler: "test.Upload"})
	reg.RegisterEndpoint(&schema.Endpoint{Path: "/big", Entity: "widgets", Method: "POST", Handler: "test.Upload", MaxBodyBytes: 1 << 20})
	h := Build(reg, nil, Config{Dispatcher: d, BaseURL: "/api", MaxBodyBytes: 100})

	body := strings.Repeat("x", 1000)
	for path, want := range map[string]int{"/api/small": http.StatusRequestEntityTooLarge, "/api/big": http.StatusOK} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		if rr.Code != want {
			t.Errorf("%s: got %d, want %d", path, rr.Code, want)
		}
	}
}
