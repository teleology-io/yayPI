package cron

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/netsafe"
)

// sqlHandler executes a SQL statement on a named database.
func sqlHandler(jobCfg config.JobDef, dbManager *db.Manager) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		sqlStr, ok := jobCfg.Config["sql"].(string)
		if !ok || sqlStr == "" {
			return fmt.Errorf("job %q: config.sql is required for sql handler", jobCfg.Name)
		}

		// Validate: reject DDL and multi-statement
		upper := strings.ToUpper(strings.TrimSpace(sqlStr))
		for _, forbidden := range []string{"CREATE ", "DROP ", "ALTER ", "TRUNCATE ", "GRANT ", "REVOKE "} {
			if strings.HasPrefix(upper, forbidden) {
				return fmt.Errorf("job %q: DDL statements are not allowed in sql handler", jobCfg.Name)
			}
		}
		// Detect multi-statement (naive: reject semicolons except trailing)
		trimmed := strings.TrimRight(sqlStr, " \t\n\r;")
		if strings.Contains(trimmed, ";") {
			return fmt.Errorf("job %q: multi-statement SQL is not allowed in sql handler", jobCfg.Name)
		}

		dbName, _ := jobCfg.Config["database"].(string)
		// Use the db manager to get the pool
		var execErr error
		if dbName != "" {
			p, err := dbManager.Get(dbName)
			if err != nil {
				return fmt.Errorf("job %q: database %q not found: %w", jobCfg.Name, dbName, err)
			}
			_, execErr = p.SQL.ExecContext(ctx, sqlStr)
		} else {
			_, execErr = dbManager.Default().SQL.ExecContext(ctx, sqlStr)
		}

		return execErr
	}
}

// httpHandler executes an HTTP request against an allowed host.
func httpHandler(jobCfg config.JobDef) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		rawURL, _ := jobCfg.Config["url"].(string)
		if rawURL == "" {
			return fmt.Errorf("job %q: config.url is required for http handler", jobCfg.Name)
		}

		method, _ := jobCfg.Config["method"].(string)
		if method == "" {
			method = http.MethodGet
		}

		// Validate URL
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return fmt.Errorf("job %q: invalid URL: %w", jobCfg.Name, err)
		}

		// Check against allowlist
		if allowedRaw, ok := jobCfg.Config["allowed_hosts"]; ok {
			allowed := toStringSlice(allowedRaw)
			if len(allowed) > 0 && !hostAllowed(parsed.Hostname(), allowed) {
				return fmt.Errorf("job %q: host %q not in allowed_hosts", jobCfg.Name, parsed.Hostname())
			}
		}

		// Apply timeout
		timeout := 30 * time.Second
		if jobCfg.Timeout != "" {
			if d, err := time.ParseDuration(jobCfg.Timeout); err == nil {
				timeout = d
			}
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), rawURL, nil)
		if err != nil {
			return fmt.Errorf("job %q: creating request: %w", jobCfg.Name, err)
		}

		allowPrivate, _ := jobCfg.Config["allow_private_network"].(bool)
		client := netsafe.NewClient(netsafe.Options{Timeout: timeout, AllowPrivate: allowPrivate})
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("job %q: HTTP request failed: %w", jobCfg.Name, err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)

		if resp.StatusCode >= 400 {
			return fmt.Errorf("job %q: HTTP %d response", jobCfg.Name, resp.StatusCode)
		}
		return nil
	}
}

// hostAllowed checks if host matches any pattern in the allowlist.
func hostAllowed(host string, allowed []string) bool {
	for _, h := range allowed {
		if h == host {
			return true
		}
	}
	return false
}

// toStringSlice converts an interface{} to []string.
func toStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []interface{}:
		result := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	}
	return nil
}
