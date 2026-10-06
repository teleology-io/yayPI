// Package netsafe provides an HTTP client for outbound calls to user-influenced URLs
// (webhooks, cron HTTP jobs) that refuses to connect to internal addresses.
//
// The check runs in the dialer's Control hook, i.e. against the IP actually being
// connected to after DNS resolution. That closes the gaps a pre-flight URL check leaves
// open: hostnames that resolve to private IPs, DNS rebinding between check and connect,
// and redirects to internal hosts (every redirect hop dials through the same hook).
package netsafe

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// ErrBlocked is returned (wrapped) when a connection target is a disallowed address.
var ErrBlocked = errors.New("destination address is not allowed")

// blockedPrefixes covers loopback, private, link-local (incl. cloud metadata
// 169.254.169.254), CGNAT, unspecified, multicast, IPv6 ULA, and other special ranges.
var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
	"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "64:ff9b::/96", "100::/64", "2001:db8::/32", "fc00::/7",
	"fe80::/10", "ff00::/8",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// IsBlockedIP reports whether ip falls in a range outbound calls may not reach.
// IPv4-mapped IPv6 addresses are unmapped first.
func IsBlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// CheckAddr validates a dial address ("host:port", host already an IP literal).
func CheckAddr(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrBlocked, address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || IsBlockedIP(ip) {
		return fmt.Errorf("%w: %s", ErrBlocked, host)
	}
	return nil
}

// Options configures NewClient.
type Options struct {
	Timeout      time.Duration // overall request timeout (default 10s)
	AllowPrivate bool          // disable the address check (trusted internal targets)
	MaxRedirects int           // default 3; negative disables redirects
}

// NewClient returns an *http.Client whose connections are restricted to public
// addresses unless AllowPrivate is set. Proxy env vars are ignored so a proxy can't be
// used to reach what the dialer blocks.
func NewClient(opts Options) *http.Client {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.MaxRedirects == 0 {
		opts.MaxRedirects = 3
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	if !opts.AllowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			return CheckAddr(address)
		}
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: opts.Timeout,
	}
	maxRedirects := opts.MaxRedirects
	return &http.Client{
		Timeout:   opts.Timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if maxRedirects < 0 || len(via) > maxRedirects {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}
