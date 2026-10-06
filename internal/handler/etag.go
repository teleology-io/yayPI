package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// computeETag returns a strong ETag for the stored row (before any per-caller field
// stripping, so every caller sees the same tag for the same version).
func computeETag(row map[string]interface{}) string {
	b, _ := json.Marshal(row) // map keys are marshalled in sorted order: deterministic
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// etagMatches implements If-Match / If-None-Match list matching ("*" matches anything).
func etagMatches(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		p := strings.TrimSpace(part)
		if p == "*" || p == etag || strings.TrimPrefix(p, "W/") == etag {
			return true
		}
	}
	return false
}
