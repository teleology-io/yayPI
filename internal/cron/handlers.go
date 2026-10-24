package cron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/db"
	"github.com/teleology-io/yayPI/internal/netsafe"
	"github.com/teleology-io/yayPI/internal/signature"
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

// httpHandler executes an HTTP request against an allowed host. config: url, method,
// headers, body (string, or a map/list sent as JSON), allowed_hosts,
// allow_private_network, and optionally secret + signature to sign the body like an
// outbound webhook. Static settings are checked here, at boot.
func httpHandler(jobCfg config.JobDef) (func(ctx context.Context) error, error) {
	rawURL, _ := jobCfg.Config["url"].(string)
	if rawURL == "" {
		return nil, fmt.Errorf("config.url is required for http handler")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if allowed := toStringSlice(jobCfg.Config["allowed_hosts"]); len(allowed) > 0 && !hostAllowed(parsed.Hostname(), allowed) {
		return nil, fmt.Errorf("host %q not in allowed_hosts", parsed.Hostname())
	}
	method, _ := jobCfg.Config["method"].(string)
	if method == "" {
		method = http.MethodGet
	}
	headers := map[string]string{}
	if raw, ok := jobCfg.Config["headers"].(map[string]any); ok {
		for k, v := range raw {
			headers[k] = fmt.Sprint(v)
		}
	} else if jobCfg.Config["headers"] != nil {
		return nil, fmt.Errorf("config.headers must be a map")
	}
	var body []byte
	switch b := jobCfg.Config["body"].(type) {
	case nil:
	case string:
		body = []byte(b)
	default:
		if body, err = json.Marshal(b); err != nil {
			return nil, fmt.Errorf("config.body: %w", err)
		}
		if _, ok := headers["Content-Type"]; !ok {
			headers["Content-Type"] = "application/json"
		}
	}
	secret, _ := jobCfg.Config["secret"].(string)
	var sigDef *config.WebhookSignatureDef
	if raw := jobCfg.Config["signature"]; raw != nil {
		sigDef = &config.WebhookSignatureDef{}
		b, _ := yaml.Marshal(raw)
		if err := yaml.Unmarshal(b, sigDef); err != nil {
			return nil, fmt.Errorf("config.signature: %w", err)
		}
	}
	scheme, err := signature.Resolve(sigDef, secret)
	if err != nil {
		return nil, fmt.Errorf("config.%w", err)
	}
	if scheme != nil && (secret == "" || scheme.IsPlugin()) {
		return nil, fmt.Errorf("config.signature: scheme %s needs config.secret (plugin signing is inbound only)", scheme.Name)
	}

	timeout := 30 * time.Second
	if d, err := time.ParseDuration(jobCfg.Timeout); jobCfg.Timeout != "" && err == nil {
		timeout = d
	}
	allowPrivate, _ := jobCfg.Config["allow_private_network"].(bool)
	client := netsafe.NewClient(netsafe.Options{Timeout: timeout, AllowPrivate: allowPrivate})

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), rawURL, rd)
		if err != nil {
			return fmt.Errorf("job %q: creating request: %w", jobCfg.Name, err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if scheme != nil {
			for k, v := range scheme.Sign(secret, body, time.Now()) {
				req.Header.Set(k, v)
			}
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("job %q: HTTP request failed: %w", jobCfg.Name, err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

		if resp.StatusCode >= 400 {
			return fmt.Errorf("job %q: HTTP %d response", jobCfg.Name, resp.StatusCode)
		}
		return nil
	}, nil
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
