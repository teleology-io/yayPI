package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Cursor represents an opaque pagination cursor.
type Cursor struct {
	ID        string `json:"id"`
	CreatedAt string `json:"created_at,omitempty"`
}

// EncodeCursor encodes a cursor to a signed, URL-safe base64 string.
// Format: base64url(json) + "." + base64url(hmac-sha256(payload, secret))
func EncodeCursor(c Cursor, secret []byte) string {
	payload, _ := json.Marshal(c)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	sig := computeHMAC([]byte(encoded), secret)
	sigEncoded := base64.RawURLEncoding.EncodeToString(sig)
	return encoded + "." + sigEncoded
}

// DecodeCursor decodes and verifies a cursor string.
// Returns an error if the signature is invalid.
func DecodeCursor(s string, secret []byte) (*Cursor, error) {
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid cursor format")
	}
	encoded, sigEncoded := parts[0], parts[1]

	// Verify signature
	expectedSig := computeHMAC([]byte(encoded), secret)
	gotSig, err := base64.RawURLEncoding.DecodeString(sigEncoded)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor signature encoding: %w", err)
	}
	if !hmac.Equal(expectedSig, gotSig) {
		return nil, fmt.Errorf("cursor signature mismatch")
	}

	// Decode payload
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor payload encoding: %w", err)
	}

	var c Cursor
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("invalid cursor payload: %w", err)
	}
	return &c, nil
}

// computeHMAC returns a SHA-256 HMAC of data using key.
func computeHMAC(data, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
