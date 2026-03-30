// Package mailer provides SMTP email sending triggered by entity lifecycle events.
// Configure via "kind: email" YAML files; SMTP credentials come from env vars.
//
// Required env vars:
//
//	SMTP_HOST           — mail server hostname
//	SMTP_PORT           — mail server port (default: 587)
//	SMTP_USER           — SMTP username
//	SMTP_PASS           — SMTP password
//	SMTP_SENDER_NAME    — display name in From header
//	SMTP_SENDER_EMAIL   — address in From header
package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"html/template"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/pkg/sdk"
)

// Mailer implements sdk.EntityHookPlugin and fires emails on entity events.
type Mailer struct {
	defs map[string][]config.EmailDef // entity → []EmailDef
	smtp smtpConfig
}

type smtpConfig struct {
	host        string
	port        string
	user        string
	pass        string
	senderName  string
	senderEmail string
}

// New creates a Mailer from a slice of EmailDef. Returns nil if no SMTP host is set.
func New(defs []config.EmailDef) *Mailer {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		return nil // SMTP not configured — silently no-op
	}
	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}

	m := &Mailer{
		defs: make(map[string][]config.EmailDef),
		smtp: smtpConfig{
			host:        host,
			port:        port,
			user:        os.Getenv("SMTP_USER"),
			pass:        os.Getenv("SMTP_PASS"),
			senderName:  os.Getenv("SMTP_SENDER_NAME"),
			senderEmail: os.Getenv("SMTP_SENDER_EMAIL"),
		},
	}
	for _, d := range defs {
		m.defs[d.Entity] = append(m.defs[d.Entity], d)
	}
	return m
}

// ── sdk.Plugin interface ──────────────────────────────────────────────────────

func (m *Mailer) Info() sdk.PluginInfo {
	return sdk.PluginInfo{Name: "mailer", Version: "1.0.0", Description: "SMTP email notifications"}
}
func (m *Mailer) Init(_ sdk.InitContext) error  { return nil }
func (m *Mailer) Shutdown(_ context.Context) error { return nil }

// ── sdk.EntityHookPlugin interface ───────────────────────────────────────────

func (m *Mailer) BeforeCreate(_ sdk.HookContext, _ string, data map[string]any) (map[string]any, error) {
	return data, nil
}
func (m *Mailer) BeforeUpdate(_ sdk.HookContext, _ string, _ string, data map[string]any) (map[string]any, error) {
	return data, nil
}
func (m *Mailer) BeforeDelete(_ sdk.HookContext, _ string, _ string) error { return nil }

func (m *Mailer) AfterCreate(ctx sdk.HookContext, entity string, record map[string]any) error {
	return m.dispatch(ctx.Ctx, entity, "after_create", record)
}
func (m *Mailer) AfterUpdate(ctx sdk.HookContext, entity string, record map[string]any) error {
	return m.dispatch(ctx.Ctx, entity, "after_update", record)
}
func (m *Mailer) AfterDelete(ctx sdk.HookContext, entity string, id string) error {
	return m.dispatch(ctx.Ctx, entity, "after_delete", map[string]any{"id": id})
}

// ── Dispatch ──────────────────────────────────────────────────────────────────

func (m *Mailer) dispatch(_ context.Context, entity, trigger string, record map[string]any) error {
	defs := m.defs[entity]
	for _, d := range defs {
		if d.Trigger != trigger {
			continue
		}
		if d.Condition != "" && !evalSimpleCondition(d.Condition, record) {
			continue
		}

		to, err := renderText(d.To, record)
		if err != nil || to == "" {
			log.Warn().Str("email", d.Name).Msg("mailer: could not resolve 'to' address")
			continue
		}
		if !isValidEmail(to) {
			log.Warn().Str("email", d.Name).Str("to", to).Msg("mailer: invalid 'to' address")
			continue
		}

		subject, _ := renderText(d.Subject, record)
		body, err := renderHTML(d.Body, record)
		if err != nil {
			log.Warn().Err(err).Str("email", d.Name).Msg("mailer: template render failed")
			continue
		}

		go func(to, subject, body string) {
			if err := m.send(to, subject, body); err != nil {
				log.Warn().Err(err).Str("to", to).Msg("mailer: send failed")
			}
		}(to, subject, body)
	}
	return nil
}

// ── SMTP send ─────────────────────────────────────────────────────────────────

func (m *Mailer) send(to, subject, htmlBody string) error {
	addr := net.JoinHostPort(m.smtp.host, m.smtp.port)
	from := m.smtp.senderName + " <" + m.smtp.senderEmail + ">"

	var msg bytes.Buffer
	msg.WriteString("From: " + from + "\r\n")
	msg.WriteString("To: " + to + "\r\n")
	msg.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	msg.WriteString("Subject: " + subject + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	msg.WriteString("\r\n")

	qpw := quotedprintable.NewWriter(&msg)
	_, _ = qpw.Write([]byte(htmlBody))
	_ = qpw.Close()

	var auth smtp.Auth
	if m.smtp.user != "" {
		auth = smtp.PlainAuth("", m.smtp.user, m.smtp.pass, m.smtp.host)
	}

	// Try STARTTLS first (port 587), fall back to plain (port 25).
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{
		ServerName: m.smtp.host,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		// Non-TLS fallback (port 25 or dev SMTP like mailpit)
		return smtp.SendMail(addr, auth, m.smtp.senderEmail, []string{to}, msg.Bytes())
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, m.smtp.host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer client.Close()

	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(m.smtp.senderEmail); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	_, err = w.Write(msg.Bytes())
	_ = w.Close()
	return err
}

// ── Template rendering ────────────────────────────────────────────────────────

// renderText renders a string that may contain {{record.field}} placeholders.
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

// renderHTML renders an HTML template with {{record.field}} placeholders.
func renderHTML(tmpl string, record map[string]any) (string, error) {
	t, err := template.New("").Option("missingkey=zero").Parse(toGoTemplate(tmpl))
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{"record": record}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// toGoTemplate converts {{record.email}} → {{.record.email}} for Go's text/template.
var recordFieldRe = regexp.MustCompile(`\{\{(record\.[^}]+)\}\}`)

func toGoTemplate(s string) string {
	return recordFieldRe.ReplaceAllString(s, `{{.$1}}`)
}

// ── Condition evaluation ──────────────────────────────────────────────────────

// evalSimpleCondition evaluates a basic condition like `record.reset_token != ""`.
// Supports != "" and != null checks only for v1. Returns true (fire) on parse error.
func evalSimpleCondition(cond string, record map[string]any) bool {
	// Support: record.<field> != ""  and  record.<field> != null
	re := regexp.MustCompile(`record\.(\w+)\s*!=\s*["']?(\S*)["']?`)
	m := re.FindStringSubmatch(cond)
	if m == nil {
		return true // unknown condition — fire anyway
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

// isValidEmail is a minimal email validator.
func isValidEmail(s string) bool {
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 {
		return false
	}
	return strings.Contains(s[at+1:], ".")
}
