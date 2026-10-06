// Package mailer sends HTML email over SMTP. Configure it with the `smtp:` block in
// yaypi.yaml; any field left empty falls back to its env var:
//
//	host        SMTP_HOST           — mail server hostname
//	port        SMTP_PORT           — mail server port (default: 587)
//	username    SMTP_USER           — SMTP username
//	password    SMTP_PASS           — SMTP password
//	from_name   SMTP_SENDER_NAME    — display name in From header
//	from_email  SMTP_SENDER_EMAIL   — address in From header
package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
)

// Mailer sends HTML email over SMTP. Entity-triggered emails are rendered by the outbox
// package and delivered through Send with retries.
type Mailer struct {
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

// New returns a Mailer from the smtp: config block (nil-safe), filling empty fields from
// SMTP_* env vars. It returns nil when no host is configured either way.
func New(cfg *config.SMTPConfig) *Mailer {
	var c config.SMTPConfig
	if cfg != nil {
		c = *cfg
	}
	pick := func(v, env string) string {
		if v != "" {
			return v
		}
		return os.Getenv(env)
	}
	host := pick(c.Host, "SMTP_HOST")
	if host == "" {
		return nil
	}
	port := ""
	if c.Port > 0 {
		port = strconv.Itoa(c.Port)
	}
	port = pick(port, "SMTP_PORT")
	if port == "" {
		port = "587"
	}
	return &Mailer{smtp: smtpConfig{
		host:        host,
		port:        port,
		user:        pick(c.Username, "SMTP_USER"),
		pass:        pick(c.Password, "SMTP_PASS"),
		senderName:  pick(c.FromName, "SMTP_SENDER_NAME"),
		senderEmail: pick(c.FromEmail, "SMTP_SENDER_EMAIL"),
	}}
}

// FromEnv returns a Mailer configured only from SMTP_* env vars (nil if SMTP_HOST is unset).
func FromEnv() *Mailer { return New(nil) }

// Send delivers one HTML email.
func (m *Mailer) Send(_ context.Context, to, subject, htmlBody string) error {
	if !isValidEmail(to) {
		return fmt.Errorf("invalid recipient %q", to)
	}
	return m.send(to, subject, htmlBody)
}

// ── SMTP send ─────────────────────────────────────────────────────────────────

func (m *Mailer) send(to, subject, htmlBody string) error {
	addr := net.JoinHostPort(m.smtp.host, m.smtp.port)
	// Header values may come from record data; strip CR/LF so nothing can inject
	// extra headers (Bcc:, etc.) or end the header block early.
	to, subject = headerSafe(to), mime.QEncoding.Encode("utf-8", headerSafe(subject))
	from := mime.QEncoding.Encode("utf-8", headerSafe(m.smtp.senderName)) + " <" + headerSafe(m.smtp.senderEmail) + ">"

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

// headerSafe removes characters that could break out of a single header line.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, s)
}

// isValidEmail is a minimal email validator.
func isValidEmail(s string) bool {
	if strings.ContainsAny(s, "\r\n<>,; \t") {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 {
		return false
	}
	return strings.Contains(s[at+1:], ".")
}
