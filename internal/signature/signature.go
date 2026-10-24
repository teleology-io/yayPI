// Package signature signs outbound webhook bodies and verifies inbound ones. One scheme
// definition (config.WebhookSignatureDef) drives both directions, so a yayPi sender and a
// yayPi receiver agree by default (yaypi_v1) and well-known providers are named presets.
package signature

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
)

// Scheme names.
const (
	YaypiV1    = "yaypi_v1"
	HMACSHA256 = "hmac_sha256"
	HMACSHA1   = "hmac_sha1"
	GitHub     = "github"
	Stripe     = "stripe"
	Slack      = "slack"
	Plugin     = "plugin"
	None       = "none"
)

// DefaultTolerance bounds the age of a signed timestamp when none is configured.
const DefaultTolerance = 5 * time.Minute

// Verification failures. Callers answer all of them with 401 and log the reason.
var (
	ErrMissing   = errors.New("signature header missing")
	ErrMalformed = errors.New("signature malformed")
	ErrMismatch  = errors.New("signature mismatch")
	ErrStale     = errors.New("signature timestamp outside tolerance")
)

// Scheme is a resolved signing scheme.
type Scheme struct {
	Name            string
	Header          string
	Prefix          string
	Encoding        string // hex | base64
	TimestampHeader string
	Tolerance       time.Duration
	SignedPayload   string
	Handler         string // scheme: plugin
	newHash         func() hash.Hash
}

// Effective returns the scheme name in force for a webhook: the configured one, else
// yaypi_v1 when a secret is set, else none.
func Effective(def *config.WebhookSignatureDef, secret string) string {
	if def != nil && def.Scheme != "" {
		return def.Scheme
	}
	if secret != "" {
		return YaypiV1
	}
	return None
}

// Resolve builds the scheme for a webhook; nil means unsigned (scheme none).
func Resolve(def *config.WebhookSignatureDef, secret string) (*Scheme, error) {
	name := Effective(def, secret)
	var s Scheme
	switch name {
	case None:
		return nil, nil
	case YaypiV1:
		s = Scheme{Header: "X-Yaypi-Signature", Prefix: "v1=", TimestampHeader: "X-Yaypi-Timestamp", newHash: sha256.New}
	case GitHub:
		s = Scheme{Header: "X-Hub-Signature-256", Prefix: "sha256=", newHash: sha256.New}
	case Slack:
		s = Scheme{Header: "X-Slack-Signature", Prefix: "v0=", TimestampHeader: "X-Slack-Request-Timestamp",
			SignedPayload: "v0:{timestamp}:{body}", newHash: sha256.New}
	case Stripe:
		s = Scheme{Header: "Stripe-Signature", newHash: sha256.New}
	case HMACSHA256:
		s = Scheme{newHash: sha256.New}
	case HMACSHA1:
		s = Scheme{newHash: sha1.New}
	case Plugin:
		s = Scheme{}
	default:
		return nil, fmt.Errorf("signature.scheme %q must be one of yaypi_v1, hmac_sha256, hmac_sha1, github, stripe, slack, plugin, none", name)
	}
	s.Name = name
	if def != nil {
		if def.Header != "" {
			s.Header = def.Header
		}
		if def.Prefix != "" {
			s.Prefix = def.Prefix
		}
		if def.Encoding != "" {
			s.Encoding = def.Encoding
		}
		if def.TimestampHeader != "" {
			s.TimestampHeader = def.TimestampHeader
		}
		if def.SignedPayload != "" {
			s.SignedPayload = def.SignedPayload
		}
		s.Handler = def.Handler
		tol, err := config.ParseDuration(def.Tolerance)
		if err != nil {
			return nil, fmt.Errorf("signature.tolerance: %w", err)
		}
		s.Tolerance = tol
	}
	if s.Encoding == "" {
		s.Encoding = "hex"
	}
	if s.Encoding != "hex" && s.Encoding != "base64" {
		return nil, fmt.Errorf("signature.encoding %q must be hex or base64", s.Encoding)
	}
	if s.Tolerance == 0 {
		s.Tolerance = DefaultTolerance
	}
	if name == Stripe {
		s.TimestampHeader = "" // carried inside Stripe-Signature as t=
	}
	if s.SignedPayload == "" {
		s.SignedPayload = "{body}"
		if s.TimestampHeader != "" || name == Stripe {
			s.SignedPayload = "{timestamp}.{body}"
		}
	}
	switch {
	case name == Plugin:
		if s.Handler == "" {
			return nil, fmt.Errorf("signature.scheme plugin needs signature.handler (<plugin>.<verifier>)")
		}
	case s.Header == "":
		return nil, fmt.Errorf("signature.scheme %s needs signature.header", name)
	}
	return &s, nil
}

// IsPlugin reports whether verification is delegated to a plugin verifier.
func (s *Scheme) IsPlugin() bool { return s != nil && s.Name == Plugin }

// Sign returns the headers that sign body at time ts.
func (s *Scheme) Sign(secret string, body []byte, ts time.Time) map[string]string {
	unix := strconv.FormatInt(ts.Unix(), 10)
	digest := s.digest(secret, unix, body)
	if s.Name == Stripe {
		return map[string]string{s.Header: "t=" + unix + ",v1=" + digest}
	}
	out := map[string]string{s.Header: s.Prefix + digest}
	if s.TimestampHeader != "" {
		out[s.TimestampHeader] = unix
	}
	return out
}

// Verify checks an inbound request's signature over body at time now.
func (s *Scheme) Verify(secret string, h http.Header, body []byte, now time.Time) error {
	raw := strings.TrimSpace(h.Get(s.Header))
	if raw == "" {
		return ErrMissing
	}
	var unix string
	var candidates []string
	if s.Name == Stripe {
		for _, part := range strings.Split(raw, ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
			if !ok {
				continue
			}
			switch k {
			case "t":
				unix = v
			case "v1":
				candidates = append(candidates, v)
			}
		}
		if unix == "" || len(candidates) == 0 {
			return ErrMalformed
		}
	} else {
		if s.Prefix != "" {
			if !strings.HasPrefix(raw, s.Prefix) {
				return ErrMalformed
			}
			raw = strings.TrimPrefix(raw, s.Prefix)
		}
		candidates = []string{raw}
		if s.TimestampHeader != "" {
			unix = strings.TrimSpace(h.Get(s.TimestampHeader))
			if unix == "" {
				return ErrMissing
			}
		}
	}
	if unix != "" {
		sec, err := strconv.ParseInt(unix, 10, 64)
		if err != nil {
			return ErrMalformed
		}
		age := now.Sub(time.Unix(sec, 0))
		if age > s.Tolerance || age < -s.Tolerance {
			return ErrStale
		}
	}
	want, err := s.decode(s.digest(secret, unix, body))
	if err != nil {
		return err
	}
	for _, c := range candidates {
		got, err := s.decode(c)
		if err == nil && hmac.Equal(got, want) {
			return nil
		}
	}
	return ErrMismatch
}

func (s *Scheme) digest(secret, unix string, body []byte) string {
	mac := hmac.New(s.newHash, []byte(secret))
	before, after, hasBody := strings.Cut(s.SignedPayload, "{body}")
	mac.Write([]byte(strings.ReplaceAll(before, "{timestamp}", unix)))
	if hasBody {
		mac.Write(body)
		mac.Write([]byte(strings.ReplaceAll(after, "{timestamp}", unix)))
	}
	sum := mac.Sum(nil)
	if s.Encoding == "base64" {
		return base64.StdEncoding.EncodeToString(sum)
	}
	return hex.EncodeToString(sum)
}

func (s *Scheme) decode(v string) ([]byte, error) {
	var b []byte
	var err error
	if s.Encoding == "base64" {
		b, err = base64.StdEncoding.DecodeString(v)
	} else {
		b, err = hex.DecodeString(strings.ToLower(v))
	}
	if err != nil {
		return nil, ErrMalformed
	}
	return b, nil
}
