package token

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var secret = []byte(strings.Repeat("s", 32))

func TestSignParseRoundTrip(t *testing.T) {
	k, err := New(Config{Secret: secret, Issuer: "app", RequireIssuer: true, Audience: "api", AccessTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(jwt.MapClaims{"sub": "u1", "role": "admin"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := k.Parse(tok)
	if err != nil || c["sub"] != "u1" || c["role"] != "admin" {
		t.Fatalf("claims %v err %v", c, err)
	}
}

func TestParseRejectsWrongIssuerAndLegacyRefresh(t *testing.T) {
	a, _ := New(Config{Secret: secret, Issuer: "a"})
	b, _ := New(Config{Secret: secret, Issuer: "b", RequireIssuer: true})
	tok, _ := a.Sign(jwt.MapClaims{"sub": "u1"})
	if _, err := b.Parse(tok); err == nil {
		t.Fatal("issuer mismatch should fail")
	}
	legacy := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u1", "type": "refresh", "exp": time.Now().Add(time.Hour).Unix(),
	})
	s, _ := legacy.SignedString(secret)
	noIss, _ := New(Config{Secret: secret})
	if _, err := noIss.Parse(s); err == nil {
		t.Fatal("legacy refresh token must not be accepted as an access token")
	}
}

func TestParseRejectsAlgorithmConfusion(t *testing.T) {
	k, _ := New(Config{Secret: secret})
	other := jwt.NewWithClaims(jwt.SigningMethodHS512, jwt.MapClaims{"sub": "u1", "exp": time.Now().Add(time.Hour).Unix()})
	s, _ := other.SignedString(secret)
	if _, err := k.Parse(s); err == nil {
		t.Fatal("HS512 token accepted by HS256 verifier")
	}
}

func TestES256WithJWKS(t *testing.T) {
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalECPrivateKey(priv)
	path := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := New(Config{Algorithm: "ES256", PrivateKeyFile: path, KeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(jwt.MapClaims{"sub": "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Parse(tok); err != nil {
		t.Fatal(err)
	}
	set := k.JWKS()
	if len(set["keys"]) != 1 || set["keys"][0].Crv != "P-256" || set["keys"][0].Kid != "k1" {
		t.Fatalf("jwks %+v", set)
	}
	hs, _ := New(Config{Secret: secret})
	if hs.JWKS() != nil {
		t.Fatal("HMAC must not publish JWKS")
	}
}
