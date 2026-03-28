package db

import (
	"regexp"
	"strings"
)

// SecretString wraps a string value and redacts credentials when printed or logged.
type SecretString struct {
	value string
}

// NewSecretString creates a SecretString from a raw value.
func NewSecretString(v string) SecretString {
	return SecretString{value: v}
}

// Value returns the raw underlying string (use only for actual connections).
func (s SecretString) Value() string {
	return s.value
}

// String returns a redacted version safe for logging.
func (s SecretString) String() string {
	return RedactDSN(s.value)
}

// MarshalText implements encoding.TextMarshaler — always redacts.
func (s SecretString) MarshalText() ([]byte, error) {
	return []byte("[redacted]"), nil
}

// credentialsRe matches the user:password@ portion of a DSN URL.
var credentialsRe = regexp.MustCompile(`://([^:@/]+):([^@/]+)@`)

// RedactDSN replaces the password in a DSN URL with ***.
func RedactDSN(dsn string) string {
	if !strings.Contains(dsn, "://") {
		// key=value style DSN — redact password= field
		return redactKVDSN(dsn)
	}
	return credentialsRe.ReplaceAllString(dsn, "://$1:***@")
}

// kvPasswordRe matches password=<value> in a key=value DSN.
var kvPasswordRe = regexp.MustCompile(`(?i)(password\s*=\s*)([^\s]+)`)

func redactKVDSN(dsn string) string {
	return kvPasswordRe.ReplaceAllString(dsn, "${1}***")
}
