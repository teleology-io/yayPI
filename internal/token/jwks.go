package token

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
)

// JWK is a single JSON Web Key (public parts only).
type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Kid string `json:"kid,omitempty"`
	// RSA
	N string `json:"n,omitempty"`
	E string `json:"e,omitempty"`
	// EC
	Crv string `json:"crv,omitempty"`
	X   string `json:"x,omitempty"`
	Y   string `json:"y,omitempty"`
}

// JWKS returns the public key set for asymmetric algorithms, or nil for HMAC (a shared
// secret must never be published).
func (k *Keys) JWKS() map[string][]JWK {
	b64 := base64.RawURLEncoding.EncodeToString
	var key JWK
	switch pub := k.publicKey.(type) {
	case *rsa.PublicKey:
		key = JWK{Kty: "RSA", N: b64(pub.N.Bytes()), E: b64(big.NewInt(int64(pub.E)).Bytes())}
	case *ecdsa.PublicKey:
		size := (pub.Curve.Params().BitSize + 7) / 8
		key = JWK{Kty: "EC", Crv: pub.Curve.Params().Name, X: b64(pub.X.FillBytes(make([]byte, size))), Y: b64(pub.Y.FillBytes(make([]byte, size)))}
	default:
		return nil
	}
	key.Use, key.Alg, key.Kid = "sig", k.Algorithm(), k.kid
	return map[string][]JWK{"keys": {key}}
}
