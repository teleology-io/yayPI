package signature

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
)

func headersOf(m map[string]string) http.Header {
	h := http.Header{}
	for k, v := range m {
		h.Set(k, v)
	}
	return h
}

func TestRoundTripEveryScheme(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	body := []byte(`{"id":"evt_1","type":"contact.created"}`)
	for _, def := range []*config.WebhookSignatureDef{
		nil, // secret only → yaypi_v1
		{Scheme: GitHub},
		{Scheme: Slack},
		{Scheme: Stripe},
		{Scheme: HMACSHA256, Header: "X-Sig"},
		{Scheme: HMACSHA1, Header: "X-Sig", Encoding: "base64", Prefix: "sha1="},
		{Scheme: HMACSHA256, Header: "X-Sig", TimestampHeader: "X-Ts", SignedPayload: "{timestamp}:{body}"},
	} {
		s, err := Resolve(def, "s3cret")
		if err != nil {
			t.Fatalf("%+v: %v", def, err)
		}
		h := headersOf(s.Sign("s3cret", body, now))
		if err := s.Verify("s3cret", h, body, now.Add(time.Minute)); err != nil {
			t.Errorf("%s: valid signature rejected: %v", s.Name, err)
		}
		if err := s.Verify("s3cret", h, []byte(`{"id":"evt_2"}`), now); !errors.Is(err, ErrMismatch) {
			t.Errorf("%s: tampered body: got %v, want ErrMismatch", s.Name, err)
		}
		if err := s.Verify("other", h, body, now); !errors.Is(err, ErrMismatch) {
			t.Errorf("%s: wrong secret: got %v, want ErrMismatch", s.Name, err)
		}
		if err := s.Verify("s3cret", http.Header{}, body, now); !errors.Is(err, ErrMissing) {
			t.Errorf("%s: no header: got %v, want ErrMissing", s.Name, err)
		}
		if s.TimestampHeader != "" || s.Name == Stripe {
			if err := s.Verify("s3cret", h, body, now.Add(10*time.Minute)); !errors.Is(err, ErrStale) {
				t.Errorf("%s: old timestamp: got %v, want ErrStale", s.Name, err)
			}
		}
	}
}

// yaypi_v1 must stay byte-compatible with what earlier releases sent.
func TestYaypiV1Format(t *testing.T) {
	s, _ := Resolve(nil, "k")
	got := s.Sign("k", []byte("body"), time.Unix(42, 0))
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write([]byte("42.body"))
	if want := "v1=" + hex.EncodeToString(mac.Sum(nil)); got["X-Yaypi-Signature"] != want || got["X-Yaypi-Timestamp"] != "42" {
		t.Fatalf("got %v, want signature %s", got, want)
	}
}

func TestResolveRules(t *testing.T) {
	if s, _ := Resolve(nil, ""); s != nil {
		t.Fatal("no secret and no signature block should be unsigned")
	}
	if s, _ := Resolve(&config.WebhookSignatureDef{Scheme: None}, ""); s != nil {
		t.Fatal("scheme none should be unsigned")
	}
	for _, def := range []*config.WebhookSignatureDef{
		{Scheme: HMACSHA256},                    // no header
		{Scheme: "md5"},                         // unknown
		{Scheme: Plugin},                        // no handler
		{Scheme: GitHub, Encoding: "base32"},    // bad encoding
		{Scheme: Slack, Tolerance: "five mins"}, // bad duration
	} {
		if _, err := Resolve(def, "k"); err == nil {
			t.Errorf("%+v: expected an error", def)
		}
	}
}
