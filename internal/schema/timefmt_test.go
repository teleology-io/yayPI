package schema

import (
	"strings"
	"testing"

	"github.com/teleology-io/yayPI/internal/config"
)

func timeCfg(sc config.RootSerializationConfig, fields ...config.FieldDef) *config.RootConfig {
	base := []config.FieldDef{
		{Name: "id", Type: "uuid", PrimaryKey: true},
		{Name: "seen_at", Type: "timestamptz"},
		{Name: "born_on", Type: "date"},
	}
	return &config.RootConfig{
		Serialization: sc,
		Entities: []*config.EntityConfig{{Entity: config.EntityDef{
			Name: "Thing", Table: "things", Fields: append(base, fields...),
		}}},
	}
}

func fieldSpec(t *testing.T, reg *Registry, name string) string {
	t.Helper()
	e, _ := reg.GetEntity("Thing")
	for _, f := range e.Fields {
		if f.Name == name {
			if f.Time == nil {
				t.Fatalf("%s has no time format", name)
			}
			return f.Time.Spec
		}
	}
	t.Fatalf("no field %s", name)
	return ""
}

func TestTimeFormatDefaultsAndOverrides(t *testing.T) {
	reg, err := Build(timeCfg(config.RootSerializationConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	if fieldSpec(t, reg, "seen_at") != "rfc3339" || fieldSpec(t, reg, "born_on") != "date" {
		t.Fatal("defaults not applied")
	}

	reg, err = Build(timeCfg(
		config.RootSerializationConfig{TimestampFormat: "YYYY-MM-DD HH:mm", DateFormat: "DD/MM/YYYY", Timezone: "Europe/Berlin"},
		config.FieldDef{Name: "logged_at", Type: "timestamptz", Serialization: config.SerializationDef{Format: "unix_ms"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	if fieldSpec(t, reg, "seen_at") != "YYYY-MM-DD HH:mm" || fieldSpec(t, reg, "born_on") != "DD/MM/YYYY" || fieldSpec(t, reg, "logged_at") != "unix_ms" {
		t.Fatal("project format or field override not applied")
	}
}

func TestTimeFormatErrors(t *testing.T) {
	cases := map[string]*config.RootConfig{
		"serialization.timestamp_format": timeCfg(config.RootSerializationConfig{TimestampFormat: "unixms"}),
		"serialization.timezone":         timeCfg(config.RootSerializationConfig{Timezone: "Mars/Olympus"}),
		"timestamptz and date fields only": timeCfg(config.RootSerializationConfig{},
			config.FieldDef{Name: "title", Type: "string", Serialization: config.SerializationDef{Format: "date"}}),
		"field at: serialization.format": timeCfg(config.RootSerializationConfig{},
			config.FieldDef{Name: "at", Type: "timestamptz", Serialization: config.SerializationDef{Format: "YYYY-[MM"}}),
	}
	for want, cfg := range cases {
		_, err := Build(cfg)
		if err == nil {
			t.Errorf("%s: no error", want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't mention %q", err, want)
		}
	}
}
