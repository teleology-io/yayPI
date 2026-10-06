package audit

import (
	"testing"

	"github.com/teleology-io/yayPI/internal/schema"
)

func TestDiffRedactsAndSkipsUnchanged(t *testing.T) {
	e := &schema.Entity{Fields: []schema.Field{
		{Name: "title", ColumnName: "title"},
		{Name: "secret", ColumnName: "secret", OmitLog: true},
		{Name: "same", ColumnName: "same"},
		{Name: "updated_at", ColumnName: "updated_at"},
	}}
	d := Diff(e,
		map[string]any{"title": "a", "secret": "x", "same": 1, "updated_at": "t1"},
		map[string]any{"title": "b", "secret": "y", "same": 1, "updated_at": "t2"})
	if len(d) != 2 || d["title"] != [2]any{"a", "b"} || d["secret"] != [2]any{redacted, redacted} {
		t.Fatalf("diff %v", d)
	}
}
