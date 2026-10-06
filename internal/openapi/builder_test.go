package openapi

import (
	"testing"

	"github.com/teleology-io/yayPI/internal/schema"
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
