package middleware

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// TrustedProxies is a set of CIDRs whose forwarding headers are believed.
type TrustedProxies []*net.IPNet

// ParseTrustedProxies parses CIDRs or bare IPs ("10.0.0.0/8", "127.0.0.1").
func ParseTrustedProxies(entries []string) (TrustedProxies, error) {
	var out TrustedProxies
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", e)
			}
			if ip.To4() != nil {
				e += "/32"
			} else {
				e += "/128"
			}
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: %w", e, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func (tp TrustedProxies) contains(ip net.IP) bool {
	for _, n := range tp {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ResolveClientIP returns the originating client IP. Forwarding headers are honoured only
// when the direct peer is a trusted proxy; X-Forwarded-For is walked right-to-left,
// skipping trusted hops, so a client cannot spoof its address by prepending entries.
func ResolveClientIP(r *http.Request, trusted TrustedProxies) string {
	peer := r.RemoteAddr
	if h, _, err := net.SplitHostPort(peer); err == nil {
		peer = h
	}
	peerIP := net.ParseIP(peer)
	if peerIP == nil || len(trusted) == 0 || !trusted.contains(peerIP) {
		return peer
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		hops := strings.Split(xff, ",")
		for i := len(hops) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(hops[i]))
			if ip == nil {
				break // malformed — stop trusting the chain
			}
			if !trusted.contains(ip) {
				return ip.String()
			}
		}
	}
	if xr := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); xr != nil {
		return xr.String()
	}
	return peer
}

// ClientIP resolves the client IP once per request and stores it in the context for the
// rate limiter and logger.
func ClientIP(trusted TrustedProxies) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ResolveClientIP(r, trusted)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyClientIP, ip)))
		})
	}
}

// GetClientIP returns the IP stored by ClientIP, falling back to the raw peer address.
func GetClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(ctxKeyClientIP).(string); ok {
		return ip
	}
	return ResolveClientIP(r, nil)
}
