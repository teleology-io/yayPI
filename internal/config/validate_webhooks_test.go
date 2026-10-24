package config

import (
	"strings"
	"testing"
)

func webhookCfg(w WebhookDef) *RootConfig {
	return &RootConfig{
		Entities: []*EntityConfig{{Entity: EntityDef{
			Name: "Contact", NaturalKey: []string{"external_id"},
			Fields: []FieldDef{{Name: "id", Type: "uuid", PrimaryKey: true}, {Name: "external_id", Type: "string", Unique: true}, {Name: "email", Type: "string"}},
		}}},
		WebhookFiles: []*WebhookFileConfig{{Webhooks: []WebhookDef{w}}},
	}
}

func inbound(mut func(*WebhookDef)) WebhookDef {
	w := WebhookDef{
		Name: "in", Mode: WebhookInbound, Path: "/hooks/in", Secret: "s",
		Routes: map[string]WebhookRouteDef{"*": {Entity: "Contact", Op: "upsert", Map: map[string]any{"external_id": "{{body.id}}"}}},
	}
	if mut != nil {
		mut(&w)
	}
	return w
}

func webhookErrors(w WebhookDef) string {
	var msgs []string
	for _, e := range Validate(webhookCfg(w)) {
		msgs = append(msgs, e.Message)
	}
	return strings.Join(msgs, "\n")
}

func TestInboundSignatureRules(t *testing.T) {
	ok := []WebhookDef{
		inbound(nil), // secret alone → yaypi_v1
		inbound(func(w *WebhookDef) { w.Signature = &WebhookSignatureDef{Scheme: "github"} }),
		inbound(func(w *WebhookDef) { // unsigned but guarded and deduplicated
			w.Secret, w.DedupeKey = "", "body.id"
			w.Token = &WebhookTokenDef{Header: "X-Token", Value: "t"}
		}),
		inbound(func(w *WebhookDef) {
			w.Secret, w.DedupeKey, w.AllowedIPs = "", "body.id", []string{"10.0.0.0/8"}
			w.Signature = &WebhookSignatureDef{Scheme: "none"}
		}),
	}
	for i, w := range ok {
		if errs := webhookErrors(w); errs != "" {
			t.Errorf("case %d: unexpected errors:\n%s", i, errs)
		}
	}

	bad := map[string]WebhookDef{
		"unsigned receiver":             inbound(func(w *WebhookDef) { w.Secret = "" }),
		"secret would never be checked": inbound(func(w *WebhookDef) { w.Signature = &WebhookSignatureDef{Scheme: "none"} }),
		"needs secret": inbound(func(w *WebhookDef) {
			w.Secret = ""
			w.Signature = &WebhookSignatureDef{Scheme: "stripe"}
		}),
		"dedupe_key is required": inbound(func(w *WebhookDef) {
			w.Secret = ""
			w.Token = &WebhookTokenDef{Header: "X-Token", Value: "t"}
		}),
		"needs signature.header": inbound(func(w *WebhookDef) { w.Signature = &WebhookSignatureDef{Scheme: "hmac_sha256"} }),
		"exactly one of header":  inbound(func(w *WebhookDef) { w.Token = &WebhookTokenDef{Value: "t"} }),
		"not an IP or CIDR":      inbound(func(w *WebhookDef) { w.AllowedIPs = []string{"nope"} }),
		"url is only valid":      inbound(func(w *WebhookDef) { w.URL = "https://x" }),
		"must start with /":      inbound(func(w *WebhookDef) { w.Path = "hooks" }),
		"never matches":          inbound(func(w *WebhookDef) { w.Routes["x.created"] = w.Routes["*"] }),
		"must reference body":    inbound(func(w *WebhookDef) { w.Routes["*"].Map["email"] = "{{record.email}}" }),
		"has no field":           inbound(func(w *WebhookDef) { w.Routes["*"].Map["nope"] = "{{body.x}}" }),
		"primary key or exactly": inbound(func(w *WebhookDef) {
			w.Routes["*"] = WebhookRouteDef{Entity: "Contact", Op: "delete", Key: map[string]any{"email": "{{body.e}}"}}
		}),
		`op "merge" must be`:       inbound(func(w *WebhookDef) { w.Routes["*"] = WebhookRouteDef{Entity: "Contact", Op: "merge"} }),
		"path is only valid":       {Name: "out", Entity: "Contact", Trigger: "after_create", URL: "https://x", Path: "/x"},
		"scheme plugin is inbound": {Name: "out", Entity: "Contact", Trigger: "after_create", URL: "https://x", Signature: &WebhookSignatureDef{Scheme: "plugin", Handler: "a.b"}},
	}
	for want, w := range bad {
		if errs := webhookErrors(w); !strings.Contains(errs, want) {
			t.Errorf("%q: got errors:\n%s", want, errs)
		}
	}
}

func TestOutboundUnchanged(t *testing.T) {
	// The pre-existing outbound shape (no mode) still validates, signed or not.
	for _, secret := range []string{"", "s"} {
		w := WebhookDef{Name: "out", Entity: "Contact", Trigger: "after_create", URL: "https://x", Secret: secret}
		if errs := webhookErrors(w); errs != "" {
			t.Errorf("secret=%q: %s", secret, errs)
		}
	}
}

func TestUpsertAndJobValidation(t *testing.T) {
	cfg := webhookCfg(inbound(nil))
	cfg.Entities = append(cfg.Entities, &EntityConfig{Entity: EntityDef{Name: "Note", Fields: []FieldDef{{Name: "id", Type: "uuid", PrimaryKey: true}}}})
	cfg.Endpoints = []*EndpointFileConfig{{Endpoints: []EndpointDef{
		{Path: "/contacts", Entity: "Contact", CRUD: []string{"upsert"}, Create: &CreateConfig{OnConflict: "ignore"}},
		{Path: "/notes", Entity: "Note", CRUD: []string{"upsert"}},
		{Path: "/notes2", Entity: "Note", CRUD: []string{"create"}, Create: &CreateConfig{OnConflict: "merge"}},
	}}}
	cfg.Jobs = []*JobConfig{{Jobs: []JobDef{
		{Name: "a", Schedule: "@hourly", Handler: "plugin", Plugin: "crm.Sync", OnFailure: "alerts.Failed"},
		{Name: "b", Schedule: "@hourly", Handler: "plugin"},
		{Name: "c", Schedule: "@hourly", Handler: "shell"},
	}}}
	var msgs []string
	for _, e := range Validate(cfg) {
		msgs = append(msgs, e.Message)
	}
	all := strings.Join(msgs, "\n")
	for _, want := range []string{
		`"/notes": crud upsert needs entity "Note"`,
		`on_conflict "merge" must be`,
		`job "b": handler: plugin needs plugin`,
		`job "c": handler "shell"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, `"/contacts"`) || strings.Contains(all, `job "a"`) {
		t.Errorf("valid config flagged:\n%s", all)
	}
}
