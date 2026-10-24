package openapi

import (
	"testing"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/timefmt"
	"github.com/teleology-io/yayPI/pkg/types"
)

func TestBuildIncludesReplaceAndErrors(t *testing.T) {
	reg := schema.NewRegistry()
	reg.Specs = []schema.SpecMeta{{Name: "api", Title: "API", Version: "1"}}
	reg.RegisterEntity(&schema.Entity{Name: "Post", Table: "posts", Fields: []schema.Field{
		{Name: "id", ColumnName: "id", Type: types.FieldTypeUUID, PrimaryKey: true},
		{Name: "author_id", ColumnName: "author_id", Type: types.FieldTypeUUID, DefaultFrom: "subject.id"},
	}})
	reg.RegisterEntity(&schema.Entity{Name: "YaypiOutbox", Table: "yaypi_outbox", Internal: true})
	reg.RegisterEndpoint(&schema.Endpoint{
		Path: "/posts", Entity: "Post", CRUD: []string{"list", "create", "replace"},
		Auth: &schema.Auth{Roles: []string{"admin"}},
	})
	spec := Build(reg, "p", true)["api"]
	if spec.Paths["/posts/{id}"] == nil || spec.Paths["/posts/{id}"].Put == nil {
		t.Fatal("replace (PUT) not documented")
	}
	create := spec.Paths["/posts"].Post
	if _, ok := create.Responses["409"]; !ok || create.Security == nil {
		t.Fatalf("create missing 409 or security: %+v", create)
	}
	if _, leaked := spec.Components.Schemas["YaypiOutbox"]; leaked {
		t.Fatal("internal entity exposed in spec")
	}
	if _, has := create.RequestBody.Content["application/json"].Schema.Properties["author_id"]; has {
		t.Fatal("default_from field documented as client-writable")
	}
}

func TestTimeFormatsInSchemas(t *testing.T) {
	unix, _ := timefmt.New("unix", nil)
	pattern, _ := timefmt.New("YYYY-MM-DD HH:mm", nil)
	reg := schema.NewRegistry()
	reg.Specs = []schema.SpecMeta{{Name: "api", Title: "API", Version: "1"}}
	reg.RegisterEntity(&schema.Entity{Name: "Ev", Table: "evs", Fields: []schema.Field{
		{Name: "id", ColumnName: "id", Type: types.FieldTypeUUID, PrimaryKey: true},
		{Name: "a", ColumnName: "a", Type: types.FieldTypeTimestamptz},
		{Name: "b", ColumnName: "b", Type: types.FieldTypeTimestamptz, Time: unix},
		{Name: "c", ColumnName: "c", Type: types.FieldTypeTimestamptz, Time: pattern},
	}})
	reg.RegisterEndpoint(&schema.Endpoint{Path: "/evs", Entity: "Ev", CRUD: []string{"list"}})
	props := Build(reg, "p", false)["api"].Components.Schemas["Ev"].Properties
	if props["a"].Format != "date-time" {
		t.Errorf("default: %+v", props["a"])
	}
	if props["b"].Type != "integer" {
		t.Errorf("unix: %+v", props["b"])
	}
	if props["c"].Type != "string" || props["c"].Format != "" || props["c"].Description != "Format: YYYY-MM-DD HH:mm" {
		t.Errorf("pattern: %+v", props["c"])
	}
}

func TestAuthRoutesFollowMountSwitches(t *testing.T) {
	user, err := schema.NewBuiltinUser([]config.FieldDef{{Name: "display_name", Type: "string"}})
	if err != nil {
		t.Fatal(err)
	}
	reg := schema.NewRegistry()
	reg.Specs = []schema.SpecMeta{{Name: "api", Title: "API", Version: "1"}}
	reg.RegisterEntity(user)
	if spec := Build(reg, "p", true)["api"]; spec.Paths["/auth/login"] != nil {
		t.Fatal("auth routes documented without kind: auth")
	}

	reg.AuthEndpoint = &config.AuthEndpointDef{
		BasePath: "/account",
		Register: &config.RegisterDef{Enabled: true},
		Login:    &config.LoginDef{Enabled: true},
		Me:       &config.MeDef{Enabled: false},
		Refresh:  &config.RefreshDef{Enabled: true, Store: "body"},
	}
	spec := Build(reg, "p", true)["api"]
	for _, p := range []string{"/account/register", "/account/login", "/account/refresh", "/account/logout", "/account/logout-all"} {
		if spec.Paths[p] == nil || spec.Paths[p].Post == nil {
			t.Errorf("%s not documented", p)
		}
	}
	for _, p := range []string{"/account/me", "/account/password/forgot", "/auth/login"} {
		if spec.Paths[p] != nil {
			t.Errorf("%s documented but not mounted", p)
		}
	}
	body := spec.Paths["/account/register"].Post.RequestBody.Content["application/json"].Schema
	if body.Properties["display_name"] == nil || body.Properties["role"] != nil || body.Properties["password"] == nil {
		t.Errorf("register body: %+v", body.Properties)
	}
	if spec.Components.Schemas["AuthSession"].Properties["refresh_token"] == nil {
		t.Error("refresh.store: body should put refresh_token in the session")
	}
	if spec.Paths["/account/logout-all"].Post.Security == nil || spec.Paths["/account/login"].Post.Security != nil {
		t.Error("security: logout-all needs a bearer, login doesn't")
	}
}
