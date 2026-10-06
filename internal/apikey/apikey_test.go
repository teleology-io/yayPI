package apikey

import (
	"context"
	"testing"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/testutil"
	"github.com/teleology-io/yayPI/pkg/types"
)

func TestStaticLookup(t *testing.T) {
	look := StaticLookup([]config.StaticAPIKey{{Key: "k1", Role: "admin", Name: "ci"}})
	if s := look(context.Background(), "k1"); s == nil || s.Role != "admin" || s.ID != "ci" {
		t.Fatalf("got %+v", s)
	}
	if look(context.Background(), "nope") != nil {
		t.Fatal("unknown key accepted")
	}
}

func TestDBLookupHashedWithSubject(t *testing.T) {
	reg := schema.NewRegistry()
	reg.RegisterEntity(&schema.Entity{Name: "ApiKey", Table: "api_keys", SoftDelete: true, Fields: []schema.Field{
		{Name: "id", ColumnName: "id", Type: types.FieldTypeString, PrimaryKey: true},
		{Name: "token", ColumnName: "token", Type: types.FieldTypeString, Unique: true},
		{Name: "role", ColumnName: "role", Type: types.FieldTypeString},
		{Name: "user_id", ColumnName: "user_id", Type: types.FieldTypeString},
		{Name: "deleted_at", ColumnName: "deleted_at", Type: types.FieldTypeTimestamptz, Nullable: true},
	}})
	dbm := testutil.SQLiteDB(t, reg)
	key, hash, _ := Generate()
	if _, err := dbm.Default().SQL.Exec(`INSERT INTO api_keys (id, token, role, user_id) VALUES ('k', ?, 'svc', 'owner-1')`, hash); err != nil {
		t.Fatal(err)
	}
	look, err := DBLookup(&config.APIKeyConfig{Entity: "ApiKey"}, reg, dbm)
	if err != nil {
		t.Fatal(err)
	}
	s := look(context.Background(), key)
	if s == nil || s.Role != "svc" || s.ID != "owner-1" {
		t.Fatalf("got %+v", s)
	}
	if look(context.Background(), hash) != nil {
		t.Fatal("the stored digest itself must not work as a key")
	}
}
