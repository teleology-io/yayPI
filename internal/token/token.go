// Package token signs and verifies yayPi access tokens (JWTs). It is shared by the auth
// handler (issuing) and the auth middleware (verifying) so both always agree on the
// algorithm, keys, issuer, audience and lifetime.
package token

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Config describes how access tokens are signed and checked.
type Config struct {
	Algorithm      string        // HS256/384/512, RS256/384/512, ES256/384/512 (default HS256)
	Secret         []byte        // HMAC key for HS*
	PrivateKeyFile string        // PEM private key for RS*/ES* signing
	PublicKeyFile  string        // PEM public key for RS*/ES* verification (derived from private key if empty)
	KeyID          string        // optional "kid" header / JWKS key id
	Issuer         string        // "iss" set on issued tokens
	RequireIssuer  bool          // also require "iss" == Issuer on verify
	Audience       string        // "aud" set on issue and required on verify when non-empty
	AccessTTL      time.Duration // access token lifetime (default 15m)
	Leeway         time.Duration // clock skew allowance on verify (default 30s)
}

// Keys is the ready-to-use signer/verifier built from a Config.
type Keys struct {
	method    jwt.SigningMethod
	signKey   any
	verifyKey any
	publicKey crypto.PublicKey // nil for HMAC
	kid       string
	issuer    string
	audience  string
	ttl       time.Duration
	parser    *jwt.Parser
}

// ErrInvalid is returned for any token that fails verification.
var ErrInvalid = errors.New("invalid or expired token")

// New validates cfg and loads keys.
func New(cfg Config) (*Keys, error) {
	alg := cfg.Algorithm
	if alg == "" {
		alg = "HS256"
	}
	method := jwt.GetSigningMethod(alg)
	if method == nil || strings.EqualFold(alg, "none") {
		return nil, fmt.Errorf("unsupported JWT algorithm %q", alg)
	}
	k := &Keys{method: method, kid: cfg.KeyID, issuer: cfg.Issuer, audience: cfg.Audience, ttl: cfg.AccessTTL}
	if k.ttl <= 0 {
		k.ttl = 15 * time.Minute
	}

	switch {
	case strings.HasPrefix(alg, "HS"):
		if len(cfg.Secret) == 0 {
			return nil, fmt.Errorf("%s requires auth.secret", alg)
		}
		k.signKey, k.verifyKey = cfg.Secret, cfg.Secret
	case strings.HasPrefix(alg, "RS"), strings.HasPrefix(alg, "PS"):
		priv, pub, err := loadKeyPair(cfg, jwt.ParseRSAPrivateKeyFromPEM, jwt.ParseRSAPublicKeyFromPEM)
		if err != nil {
			return nil, err
		}
		k.signKey, k.verifyKey, k.publicKey = priv, pub, pub
	case strings.HasPrefix(alg, "ES"):
		priv, pub, err := loadKeyPair(cfg, jwt.ParseECPrivateKeyFromPEM, jwt.ParseECPublicKeyFromPEM)
		if err != nil {
			return nil, err
		}
		k.signKey, k.verifyKey, k.publicKey = priv, pub, pub
	default:
		return nil, fmt.Errorf("unsupported JWT algorithm %q", alg)
	}

	leeway := cfg.Leeway
	if leeway == 0 {
		leeway = 30 * time.Second
	}
	opts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{alg}),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(leeway),
	}
	if k.issuer != "" && cfg.RequireIssuer {
		opts = append(opts, jwt.WithIssuer(k.issuer))
	}
	if k.audience != "" {
		opts = append(opts, jwt.WithAudience(k.audience))
	}
	k.parser = jwt.NewParser(opts...)
	return k, nil
}

// loadKeyPair reads a PEM private key (if configured) and public key (configured or
// derived from the private key). Verify-only deployments may set just the public key.
func loadKeyPair[Priv interface{ Public() crypto.PublicKey }, Pub any](
	cfg Config,
	parsePriv func([]byte) (Priv, error),
	parsePub func([]byte) (Pub, error),
) (any, any, error) {
	var priv any
	var pub any
	if cfg.PrivateKeyFile != "" {
		b, err := os.ReadFile(cfg.PrivateKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("reading auth.private_key_file: %w", err)
		}
		p, err := parsePriv(b)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing auth.private_key_file: %w", err)
		}
		priv, pub = p, p.Public()
	}
	if cfg.PublicKeyFile != "" {
		b, err := os.ReadFile(cfg.PublicKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("reading auth.public_key_file: %w", err)
		}
		p, err := parsePub(b)
		if err != nil {
			return nil, nil, fmt.Errorf("parsing auth.public_key_file: %w", err)
		}
		pub = p
	}
	if pub == nil {
		return nil, nil, fmt.Errorf("%s requires auth.private_key_file and/or auth.public_key_file", cfg.Algorithm)
	}
	return priv, pub, nil
}

// TTL returns the access token lifetime.
func (k *Keys) TTL() time.Duration { return k.ttl }

// Algorithm returns the configured signing algorithm.
func (k *Keys) Algorithm() string { return k.method.Alg() }

// PublicKey returns the verification public key for asymmetric algorithms (nil for HS*).
func (k *Keys) PublicKey() crypto.PublicKey { return k.publicKey }

// KeyID returns the configured "kid".
func (k *Keys) KeyID() string { return k.kid }

// Sign issues an access token for the given claims. exp, iat, nbf, iss and aud are set
// here; callers supply identity claims (sub, role, email, tv).
func (k *Keys) Sign(claims jwt.MapClaims) (string, error) {
	if k.signKey == nil {
		return "", errors.New("token signing is not configured (no private key)")
	}
	now := time.Now()
	out := jwt.MapClaims{}
	for key, v := range claims {
		out[key] = v
	}
	out["typ"] = "access"
	out["iat"] = now.Unix()
	out["nbf"] = now.Unix()
	out["exp"] = now.Add(k.ttl).Unix()
	if k.issuer != "" {
		out["iss"] = k.issuer
	}
	if k.audience != "" {
		out["aud"] = k.audience
	}
	t := jwt.NewWithClaims(k.method, out)
	if k.kid != "" {
		t.Header["kid"] = k.kid
	}
	return t.SignedString(k.signKey)
}

// Parse verifies a token string and returns its claims.
func (k *Keys) Parse(tokenStr string) (jwt.MapClaims, error) {
	claims := jwt.MapClaims{}
	t, err := k.parser.ParseWithClaims(tokenStr, claims, func(*jwt.Token) (any, error) {
		return k.verifyKey, nil
	})
	if err != nil || !t.Valid {
		return nil, ErrInvalid
	}
	// Reject anything explicitly typed as non-access (incl. legacy refresh JWTs that
	// carried "type": "refresh" and were signed with the same key).
	if typ, _ := claims["typ"].(string); typ != "" && typ != "access" {
		return nil, ErrInvalid
	}
	if typ, _ := claims["type"].(string); typ != "" && typ != "access" {
		return nil, ErrInvalid
	}
	return claims, nil
}

// Interface guards: the asymmetric private key types expose Public().
var (
	_ interface{ Public() crypto.PublicKey } = (*rsa.PrivateKey)(nil)
	_ interface{ Public() crypto.PublicKey } = (*ecdsa.PrivateKey)(nil)
)
