package schema

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/pkg/types"
)

func parseDefault(t *testing.T, src string) *config.DefaultDef {
	t.Helper()
	var f struct {
		Default *config.DefaultDef `yaml:"default"`
	}
	if err := yaml.Unmarshal([]byte(src), &f); err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return f.Default
}

func TestDefaultLiteralsBecomeTheSameSQLAsBefore(t *testing.T) {
	// The SQL must match what the old SQL-expression spelling produced, so
	// existing databases don't see a default change.
	cases := []struct {
		yaml string
		ft   types.FieldType
		sql  string
		json any
	}{
		{`default: ""`, types.FieldTypeText, "''", ""},
		{`default: draft`, types.FieldTypeString, "'draft'", "draft"},
		{`default: it's`, types.FieldTypeText, "'it''s'", "it's"},
		{`default: active`, types.FieldTypeEnum, "'active'", "active"},
		{`default: 0`, types.FieldTypeInteger, "0", int64(0)},
		{`default: "0"`, types.FieldTypeInteger, "0", int64(0)},
		{`default: 1.5`, types.FieldTypeDecimal, "1.5", 1.5},
		{`default: false`, types.FieldTypeBoolean, "false", false},
		{`default: uuid`, types.FieldTypeUUID, "gen_random_uuid()", nil},
		{`default: now`, types.FieldTypeTimestamptz, "now()", nil},
		{`default: 2026-01-01`, types.FieldTypeDate, "'2026-01-01'", "2026-01-01"},
		{`default: {sql: "lower(hex(randomblob(8)))"}`, types.FieldTypeText, "lower(hex(randomblob(8)))", nil},
		{`default: {sql: "'[]'"}`, types.FieldTypeJSONB, "'[]'", nil},
		{`default: null`, types.FieldTypeText, "", nil},
		{`other: 1`, types.FieldTypeText, "", nil},
	}
	for _, c := range cases {
		d := parseDefault(t, c.yaml)
		got, err := defaultSQL(d, c.ft)
		if err != nil || got != c.sql {
			t.Errorf("%s (%s): got %q, %v; want %q", c.yaml, c.ft, got, err, c.sql)
		}
		if v := defaultValue(d, c.ft); v != c.json {
			t.Errorf("%s: JSON value %#v, want %#v", c.yaml, v, c.json)
		}
	}
}

func TestDefaultRejectsWrongTypesAndOldSpellings(t *testing.T) {
	cases := []struct {
		yaml string
		ft   types.FieldType
		hint string
	}{
		{`default: "'draft'"`, types.FieldTypeString, `write "draft"`},
		{`default: "''"`, types.FieldTypeText, `write ""`},
		{`default: gen_random_uuid()`, types.FieldTypeUUID, "`uuid`"},
		{`default: now()`, types.FieldTypeTimestamptz, `{sql: "now()"}`},
		{`default: abc`, types.FieldTypeInteger, "integer"},
		{`default: "yes"`, types.FieldTypeBoolean, "true or false"},
		{`default: 1`, types.FieldTypeBoolean, "true or false"},
		{`default: "[]"`, types.FieldTypeJSONB, "{sql:"},
		{`default: {sql: ""}`, types.FieldTypeText, "{sql:"},
	}
	for _, c := range cases {
		var f struct {
			Default *config.DefaultDef `yaml:"default"`
		}
		err := yaml.Unmarshal([]byte(c.yaml), &f)
		if err == nil {
			_, err = defaultSQL(f.Default, c.ft)
		}
		if err == nil || !strings.Contains(err.Error(), c.hint) {
			t.Errorf("%s (%s): error %v, want it to mention %q", c.yaml, c.ft, err, c.hint)
		}
	}
}
