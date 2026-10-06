package config

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultMaxRequestBodyBytes is used when server.max_request_body_size is unset.
const DefaultMaxRequestBodyBytes int64 = 1 << 20 // 1 MiB

// ParseByteSize parses sizes like "4MB", "512kb", "1gb", or a plain byte count.
// Units are binary (1KB = 1024 bytes). Empty input returns 0.
func ParseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10}, {"g", 1 << 30}, {"m", 1 << 20}, {"k", 1 << 10}, {"b", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.mult
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	return n * mult, nil
}
