package config

import (
	"strings"
	"testing"
)

func cfgWithAuth(secret string) *RootConfig {
	return &RootConfig{
		Auth: AuthConfig{Secret: secret, Algorithm: "HS256"},
		Endpoints: []*EndpointFileConfig{{Endpoints: []EndpointDef{{
			Path: "/x", Entity: BuiltinUserEntityName, CRUD: []string{"list"},
			List: &ListConfig{Auth: &AuthRequirement{Roles: []string{"admin"}}},
		}}}},
	}
}

func hasErr(errs []ValidationError, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e.Message, sub) {
			return true
		}
	}
	return false
}

func TestValidateSecretRequiredWhenAuthUsed(t *testing.T) {
	if !hasErr(Validate(cfgWithAuth("")), "auth.secret is required") {
		t.Fatal("expected missing-secret error")
	}
	if !hasErr(Validate(cfgWithAuth("short")), "at least 32 bytes") {
		t.Fatal("expected short-secret error")
	}
	if hasErr(Validate(cfgWithAuth(strings.Repeat("k", 32))), "auth.secret") {
		t.Fatal("32-byte secret should pass")
	}
}

func TestValidateSecretNotRequiredWithoutAuth(t *testing.T) {
	if hasErr(Validate(&RootConfig{}), "auth.secret") {
		t.Fatal("no auth configured — secret should not be required")
	}
}

func TestValidateEmptyDSN(t *testing.T) {
	cfg := &RootConfig{Databases: []DBConfig{{Name: "primary", DSN: ""}}}
	if !hasErr(Validate(cfg), "dsn is empty") {
		t.Fatal("expected empty-dsn error")
	}
}
