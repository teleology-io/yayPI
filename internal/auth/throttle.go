package auth

import (
	"sync"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
)

// loginThrottle counts failed logins per credential and per client IP inside a fixed
// window. Once a key reaches its limit, further attempts are refused until the window
// (started by the first failure) expires. State is per process: with N replicas an
// attacker gets at most N× the budget, which still turns online guessing from millions
// of attempts per hour into a handful.
type loginThrottle struct {
	mu        sync.Mutex
	window    time.Duration
	perCred   int
	perIP     int
	entries   map[string]*failWindow
	lastSweep time.Time
}

type failWindow struct {
	count int
	start time.Time
}

func newLoginThrottle(cfg *config.LoginDef) *loginThrottle {
	maxAttempts, window := 5, 15*time.Minute
	if cfg != nil {
		if cfg.MaxAttempts != 0 {
			maxAttempts = cfg.MaxAttempts
		}
		if d, err := parseDuration(cfg.LockoutWindow); cfg.LockoutWindow != "" && err == nil && d > 0 {
			window = d
		}
	}
	if maxAttempts < 0 {
		return nil // disabled
	}
	return &loginThrottle{
		window:    window,
		perCred:   maxAttempts,
		perIP:     maxAttempts * 4,
		entries:   make(map[string]*failWindow),
		lastSweep: time.Now(),
	}
}

// blocked reports whether either key is over its limit, and how long until it clears.
func (t *loginThrottle) blocked(credential, ip string) (time.Duration, bool) {
	if t == nil {
		return 0, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for key, limit := range map[string]int{"c:" + credential: t.perCred, "i:" + ip: t.perIP} {
		if e := t.live(key, now); e != nil && e.count >= limit {
			return e.start.Add(t.window).Sub(now), true
		}
	}
	return 0, false
}

// fail records a failed attempt for both keys.
func (t *loginThrottle) fail(credential, ip string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.sweep(now)
	for _, key := range []string{"c:" + credential, "i:" + ip} {
		if e := t.live(key, now); e != nil {
			e.count++
		} else {
			t.entries[key] = &failWindow{count: 1, start: now}
		}
	}
}

// reset clears the credential's counter after a successful login. The IP counter is
// kept so one valid account can't be used to launder guesses against others.
func (t *loginThrottle) reset(credential string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.entries, "c:"+credential)
	t.mu.Unlock()
}

// live returns the entry for key if its window is still open (caller holds the lock).
func (t *loginThrottle) live(key string, now time.Time) *failWindow {
	e := t.entries[key]
	if e == nil || now.Sub(e.start) >= t.window {
		delete(t.entries, key)
		return nil
	}
	return e
}

// sweep drops expired windows at most once per window (caller holds the lock).
func (t *loginThrottle) sweep(now time.Time) {
	if now.Sub(t.lastSweep) < t.window {
		return
	}
	t.lastSweep = now
	for k, e := range t.entries {
		if now.Sub(e.start) >= t.window {
			delete(t.entries, k)
		}
	}
}
