// Package webhook fires outbound HTTP requests triggered by entity lifecycle events.
// Configure via "kind: webhooks" YAML files.
package webhook

import (
	"bytes"
	"context"
	"html/template"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Dispatcher implements sdk.EntityHookPlugin and fires webhooks on entity events.
type Dispatcher struct {
	defs   map[string][]config.WebhookDef // entity → []WebhookDef
	client *http.Client
}

// New creates a Dispatcher from a slice of WebhookDef.
func New(defs []config.WebhookDef) *Dispatcher {
	d := &Dispatcher{
		defs:   make(map[string][]config.WebhookDef),
		client: &http.Client{Timeout: 10 * time.Second},
	}
	for _, wd := range defs {
		d.defs[wd.Entity] = append(d.defs[wd.Entity], wd)
	}
	return d
}

// ── sdk.Plugin interface ──────────────────────────────────────────────────────

func (d *Dispatcher) Info() sdk.PluginInfo {
	return sdk.PluginInfo{Name: "webhook", Version: "1.0.0", Description: "Outbound webhook notifications"}
}
func (d *Dispatcher) Init(_ sdk.InitContext) error     { return nil }
func (d *Dispatcher) Shutdown(_ context.Context) error { return nil }

// ── sdk.EntityHookPlugin interface ───────────────────────────────────────────

func (d *Dispatcher) BeforeCreate(_ sdk.HookContext, _ string, data map[string]any) (map[string]any, error) {
	return data, nil
}
func (d *Dispatcher) BeforeUpdate(_ sdk.HookContext, _ string, _ string, data map[string]any) (map[string]any, error) {
	return data, nil
}
func (d *Dispatcher) BeforeDelete(_ sdk.HookContext, _ string, _ string) error { return nil }

func (d *Dispatcher) AfterCreate(ctx sdk.HookContext, entity string, record map[string]any) error {
	return d.dispatch(ctx.Ctx, entity, "after_create", record)
}
func (d *Dispatcher) AfterUpdate(ctx sdk.HookContext, entity string, record map[string]any) error {
	return d.dispatch(ctx.Ctx, entity, "after_update", record)
}
func (d *Dispatcher) AfterDelete(ctx sdk.HookContext, entity string, id string) error {
	return d.dispatch(ctx.Ctx, entity, "after_delete", map[string]any{"id": id})
}

// ── Dispatch ──────────────────────────────────────────────────────────────────

func (d *Dispatcher) dispatch(_ context.Context, entity, trigger string, record map[string]any) error {
	for _, wd := range d.defs[entity] {
		if wd.Trigger != trigger {
			continue
		}
		if wd.Condition != "" && !evalSimpleCondition(wd.Condition, record) {
			continue
		}

		wd := wd // capture
		go func() {
			if err := d.fire(wd, record); err != nil {
				log.Warn().Err(err).Str("webhook", wd.Name).Msg("webhook: send failed")
			}
		}()
	}
	return nil
}

func (d *Dispatcher) fire(wd config.WebhookDef, record map[string]any) error {
	rawURL, err := renderText(wd.URL, record)
	if err != nil || rawURL == "" {
		return nil
	}
	if !isSafeURL(rawURL) {
		log.Warn().Str("url", rawURL).Str("webhook", wd.Name).Msg("webhook: blocked SSRF target")
		return nil
	}

	payload := "{}"
	if wd.Payload != "" {
		payload, err = renderText(wd.Payload, record)
		if err != nil {
			return err
		}
	}

	method := strings.ToUpper(wd.Method)
	if method == "" {
		method = http.MethodPost
	}

	timeout := 5 * time.Second
	if wd.Timeout != "" {
		if t, err := time.ParseDuration(wd.Timeout); err == nil {
			timeout = t
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewBufferString(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yayPI-webhook/1")
	for k, v := range wd.Headers {
		rendered, _ := renderText(v, record)
		req.Header.Set(k, rendered)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Warn().Str("webhook", wd.Name).Int("status", resp.StatusCode).Msg("webhook: non-2xx response")
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// isSafeURL blocks loopback, link-local, and RFC-1918 addresses to prevent SSRF.
func isSafeURL(rawURL string) bool {
	// Quick hostname extract (no full URL parse to keep it simple)
	host := rawURL
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	if idx := strings.IndexAny(host, "/?#"); idx >= 0 {
		host = host[:idx]
	}
	// Remove port
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// DNS name — trust it (full resolution SSRF would require a resolver)
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	// Block RFC-1918
	for _, block := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		_, cidr, _ := net.ParseCIDR(block)
		if cidr != nil && cidr.Contains(ip) {
			return false
		}
	}
	return true
}

var recordFieldRe = regexp.MustCompile(`\{\{(record\.[^}]+)\}\}`)

func toGoTemplate(s string) string {
	return recordFieldRe.ReplaceAllString(s, `{{.$1}}`)
}

func renderText(tmpl string, record map[string]any) (string, error) {
	t, err := template.New("").Option("missingkey=zero").Parse(toGoTemplate(tmpl))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{"record": record}); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}

func evalSimpleCondition(cond string, record map[string]any) bool {
	re := regexp.MustCompile(`record\.(\w+)\s*!=\s*["']?(\S*)["']?`)
	m := re.FindStringSubmatch(cond)
	if m == nil {
		return true
	}
	field, check := m[1], m[2]
	val, ok := record[field]
	if !ok {
		return false
	}
	switch check {
	case `""`, "''", "":
		s, _ := val.(string)
		return s != ""
	case "null", "nil":
		return val != nil
	}
	return true
}
