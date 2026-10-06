package mailer

import (
	"testing"

	"github.com/teleology-io/yayPI/internal/config"
)

func TestNewPrefersConfigAndFallsBackToEnv(t *testing.T) {
	t.Setenv("SMTP_HOST", "env-host")
	t.Setenv("SMTP_USER", "env-user")
	t.Setenv("SMTP_PORT", "")

	m := New(&config.SMTPConfig{Host: "cfg-host", FromEmail: "a@b.co"})
	if m.smtp.host != "cfg-host" || m.smtp.user != "env-user" || m.smtp.port != "587" || m.smtp.senderEmail != "a@b.co" {
		t.Fatalf("got %+v", m.smtp)
	}
	if New(nil).smtp.host != "env-host" {
		t.Fatal("env-only config not picked up")
	}
	t.Setenv("SMTP_HOST", "")
	if New(&config.SMTPConfig{}) != nil {
		t.Fatal("no host anywhere should disable email")
	}
}
