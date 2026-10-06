package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/dialect"
	"github.com/teleology-io/yayPI/internal/handler"
)

type compiledWebhook struct {
	def  config.WebhookDef
	cond Condition
}

type compiledEmail struct {
	def  config.EmailDef
	cond Condition
}

// Observer turns entity writes into outbox messages, inside the write transaction.
type Observer struct {
	webhooks map[string][]compiledWebhook // "Entity/trigger" → defs
	emails   map[string][]compiledEmail

	// The outbox table lives in the default database. Writes to entities stored in
	// another database enqueue there directly (not atomically with the change).
	DefaultDBName  string
	DefaultDB      *sql.DB
	DefaultDialect dialect.Dialect
}

func triggerFor(action string) string { return "after_" + action }

// NewObserver compiles webhook and email definitions. Invalid conditions or templates
// are reported here, at boot, rather than misfiring at runtime.
func NewObserver(webhooks []config.WebhookDef, emails []config.EmailDef) (*Observer, error) {
	o := &Observer{webhooks: map[string][]compiledWebhook{}, emails: map[string][]compiledEmail{}}
	for _, w := range webhooks {
		if err := checkTrigger(w.Trigger); err != nil {
			return nil, fmt.Errorf("webhook %q: %w", w.Name, err)
		}
		cond, err := CompileCondition(w.Condition)
		if err != nil {
			return nil, fmt.Errorf("webhook %q: %w", w.Name, err)
		}
		if w.Payload != "" {
			probe := Render(w.Payload, map[string]any{}, func(any) string { return "0" })
			if !json.Valid([]byte(probe)) {
				return nil, fmt.Errorf("webhook %q: payload template is not valid JSON", w.Name)
			}
		}
		o.webhooks[w.Entity+"/"+w.Trigger] = append(o.webhooks[w.Entity+"/"+w.Trigger], compiledWebhook{w, cond})
	}
	for _, e := range emails {
		if err := checkTrigger(e.Trigger); err != nil {
			return nil, fmt.Errorf("email %q: %w", e.Name, err)
		}
		cond, err := CompileCondition(e.Condition)
		if err != nil {
			return nil, fmt.Errorf("email %q: %w", e.Name, err)
		}
		if _, err := RenderHTML(e.Body, map[string]any{}); err != nil {
			return nil, fmt.Errorf("email %q: body template: %w", e.Name, err)
		}
		o.emails[e.Entity+"/"+e.Trigger] = append(o.emails[e.Entity+"/"+e.Trigger], compiledEmail{e, cond})
	}
	return o, nil
}

func checkTrigger(t string) error {
	switch t {
	case "after_create", "after_update", "after_delete":
		return nil
	}
	return fmt.Errorf("trigger %q must be after_create, after_update or after_delete", t)
}

// OnWrite implements handler.TxObserver.
func (o *Observer) OnWrite(ctx context.Context, tx *sql.Tx, d dialect.Dialect, ev handler.WriteEvent) error {
	key := ev.Entity.Name + "/" + triggerFor(ev.Action)
	record := ev.After
	if record == nil {
		record = ev.Before
	}
	if record == nil {
		record = map[string]any{"id": ev.ID}
	}
	var target execer = tx
	if db := ev.Entity.Database; db != "" && db != o.DefaultDBName && o.DefaultDB != nil {
		target, d = o.DefaultDB, o.DefaultDialect
	}
	for _, w := range o.webhooks[key] {
		if !w.cond(record) {
			continue
		}
		if err := Enqueue(ctx, target, d, KindWebhook, w.def.Name, renderWebhook(w.def, ev, record)); err != nil {
			return fmt.Errorf("enqueue webhook %q: %w", w.def.Name, err)
		}
	}
	for _, e := range o.emails[key] {
		if !e.cond(record) {
			continue
		}
		to := strings.TrimSpace(Render(e.def.To, record, HeaderEscaper))
		if to == "" {
			continue
		}
		html, err := RenderHTML(e.def.Body, record)
		if err != nil {
			return fmt.Errorf("render email %q: %w", e.def.Name, err)
		}
		msg := EmailPayload{To: to, Subject: Render(e.def.Subject, record, HeaderEscaper), HTML: html}
		if err := Enqueue(ctx, target, d, KindEmail, e.def.Name, msg); err != nil {
			return fmt.Errorf("enqueue email %q: %w", e.def.Name, err)
		}
	}
	return nil
}

// renderWebhook builds the request. Without a payload template the body is a standard
// envelope: {"event", "entity", "id", "data"}.
func renderWebhook(def config.WebhookDef, ev handler.WriteEvent, record map[string]any) WebhookPayload {
	method := strings.ToUpper(def.Method)
	if method == "" {
		method = "POST"
	}
	body := ""
	if def.Payload != "" {
		body = Render(def.Payload, record, JSONEscaper)
	} else {
		b, _ := json.Marshal(map[string]any{
			"event": ev.Entity.Name + "." + ev.Action + "d", "entity": ev.Entity.Name, "id": ev.ID, "data": record,
		})
		body = string(b)
	}
	headers := map[string]string{}
	for k, v := range def.Headers {
		headers[k] = Render(v, record, HeaderEscaper)
	}
	return WebhookPayload{URL: Render(def.URL, record, URLEscaper), Method: method, Headers: headers, Body: body}
}
