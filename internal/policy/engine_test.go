package policy

import (
	"os"
	"path/filepath"
	"testing"
)

const testModelConf = `
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
`

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	modelPath := filepath.Join(dir, "model.conf")
	if err := os.WriteFile(modelPath, []byte(testModelConf), 0o644); err != nil {
		t.Fatalf("writing model.conf: %v", err)
	}
	policyPath := filepath.Join(dir, "policy.csv")
	if err := os.WriteFile(policyPath, nil, 0o644); err != nil {
		t.Fatalf("writing policy.csv: %v", err)
	}
	e, err := NewEngine(modelPath, policyPath)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestLoadFromRolesConfig_ExactResourceAndAction(t *testing.T) {
	e := newTestEngine(t)
	roles := []RoleConfig{
		{Name: "member", Permissions: []PermissionConfig{
			{Resource: "children", Actions: []string{"list", "get"}},
		}},
	}
	if err := e.LoadFromRolesConfig(roles, []string{"children", "curriculum_items"}); err != nil {
		t.Fatalf("LoadFromRolesConfig: %v", err)
	}

	if ok, _ := e.Enforce("member", "children", "list"); !ok {
		t.Error("expected member to be allowed children:list")
	}
	if ok, _ := e.Enforce("member", "children", "delete"); ok {
		t.Error("expected member to be denied children:delete (not granted)")
	}
	if ok, _ := e.Enforce("member", "curriculum_items", "list"); ok {
		t.Error("expected member to be denied curriculum_items:list (not granted)")
	}
}

func TestLoadFromRolesConfig_WildcardResourceExpandsToAllEntities(t *testing.T) {
	e := newTestEngine(t)
	roles := []RoleConfig{
		{Name: "admin", Permissions: []PermissionConfig{
			{Resource: "*", Actions: []string{"list", "get"}},
		}},
	}
	allResources := []string{"children", "curriculum_items", "lesson_history"}
	if err := e.LoadFromRolesConfig(roles, allResources); err != nil {
		t.Fatalf("LoadFromRolesConfig: %v", err)
	}

	for _, resource := range allResources {
		if ok, _ := e.Enforce("admin", resource, "list"); !ok {
			t.Errorf("expected admin to be allowed %s:list via wildcard resource", resource)
		}
	}
	if ok, _ := e.Enforce("admin", "children", "delete"); ok {
		t.Error("expected admin to be denied children:delete (wildcard resource, but action not granted)")
	}
}

func TestLoadFromRolesConfig_WildcardActionExpandsToAllActions(t *testing.T) {
	e := newTestEngine(t)
	roles := []RoleConfig{
		{Name: "admin", Permissions: []PermissionConfig{
			{Resource: "children", Actions: []string{"*"}},
		}},
	}
	if err := e.LoadFromRolesConfig(roles, []string{"children"}); err != nil {
		t.Fatalf("LoadFromRolesConfig: %v", err)
	}

	for _, action := range AllActions {
		if ok, _ := e.Enforce("admin", "children", action); !ok {
			t.Errorf("expected admin to be allowed children:%s via wildcard action", action)
		}
	}
	if ok, _ := e.Enforce("admin", "curriculum_items", "list"); ok {
		t.Error("expected admin to be denied curriculum_items:list (wildcard action, but resource not granted)")
	}
}

func TestLoadFromRolesConfig_FullWildcardGrantsEverything(t *testing.T) {
	e := newTestEngine(t)
	roles := []RoleConfig{
		{Name: "admin", Permissions: []PermissionConfig{
			{Resource: "*", Actions: []string{"*"}},
		}},
	}
	allResources := []string{"children", "curriculum_items"}
	if err := e.LoadFromRolesConfig(roles, allResources); err != nil {
		t.Fatalf("LoadFromRolesConfig: %v", err)
	}

	for _, resource := range allResources {
		for _, action := range AllActions {
			if ok, _ := e.Enforce("admin", resource, action); !ok {
				t.Errorf("expected admin to be allowed %s:%s via full wildcard", resource, action)
			}
		}
	}
}
