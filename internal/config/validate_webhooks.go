package config

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

// signingSchemes are the signature schemes that sign/verify with webhooks[].secret.
var signingSchemes = map[string]bool{
	"yaypi_v1": true, "hmac_sha256": true, "hmac_sha1": true, "github": true, "stripe": true, "slack": true,
}

// requestPathRe is a request value path: body.a.b, headers.X-Event-Id, query.token.
var requestPathRe = regexp.MustCompile(`^(body|headers|query)(\.[A-Za-z0-9_\-]+)*$`)

// templateRe matches {{ path }} placeholders in inbound map/key values.
var templateRe = regexp.MustCompile(`\{\{\s*([^}]*?)\s*\}\}`)

// EffectiveSignatureScheme mirrors signature.Effective (config cannot import it): the
// configured scheme, else yaypi_v1 with a secret, else none.
func EffectiveSignatureScheme(w WebhookDef) string {
	if w.Signature != nil && w.Signature.Scheme != "" {
		return w.Signature.Scheme
	}
	if w.Secret != "" {
		return "yaypi_v1"
	}
	return "none"
}

// InboundGuarded reports whether an inbound webhook has a non-signature guard.
func InboundGuarded(w WebhookDef) bool {
	return authDeclared(w.Auth) || (w.Token != nil && w.Token.Value != "") || len(w.AllowedIPs) > 0
}

func validateWebhooks(cfg *RootConfig, entities map[string]*EntityDef) []ValidationError {
	var errs []ValidationError
	add := func(file, name, format string, a ...any) {
		errs = append(errs, ValidationError{File: file, Message: fmt.Sprintf("webhook %q: ", name) + fmt.Sprintf(format, a...)})
	}
	endpointPaths := map[string]bool{}
	for _, ef := range cfg.Endpoints {
		for _, ep := range ef.Endpoints {
			endpointPaths[ep.Path] = true
		}
	}
	names, paths := map[string]bool{}, map[string]string{}

	for _, wf := range cfg.WebhookFiles {
		for _, w := range wf.Webhooks {
			file := wf.FilePath
			switch {
			case w.Name == "":
				add(file, w.Name, "name is required")
			case names[w.Name]:
				add(file, w.Name, "duplicate webhook name")
			case len(w.Name) > 100:
				add(file, w.Name, "name must be at most 100 characters")
			}
			names[w.Name] = true

			scheme := EffectiveSignatureScheme(w)
			if w.Signature != nil {
				if w.Signature.Scheme == "" {
					add(file, w.Name, "signature.scheme is required when a signature block is given")
				}
				if _, err := ParseDuration(w.Signature.Tolerance); err != nil {
					add(file, w.Name, "signature.tolerance: %v", err)
				}
				if e := w.Signature.Encoding; e != "" && e != "hex" && e != "base64" {
					add(file, w.Name, "signature.encoding %q must be hex or base64", e)
				}
			}
			switch {
			case scheme == "none" && w.Secret != "":
				add(file, w.Name, "signature.scheme none with a secret: the secret would never be checked; remove one")
			case signingSchemes[scheme] && w.Secret == "":
				add(file, w.Name, "signature scheme %s needs secret (is the env var set?)", scheme)
			case (scheme == "hmac_sha256" || scheme == "hmac_sha1") && w.Signature.Header == "":
				add(file, w.Name, "signature scheme %s needs signature.header", scheme)
			case scheme != "none" && scheme != "plugin" && !signingSchemes[scheme]:
				add(file, w.Name, "signature.scheme %q must be yaypi_v1, hmac_sha256, hmac_sha1, github, stripe, slack, plugin or none", scheme)
			}
			if _, err := ParseDuration(w.Timeout); err != nil {
				add(file, w.Name, "timeout: %v", err)
			}

			switch w.Mode {
			case "", WebhookOutbound:
				errs = append(errs, validateOutbound(file, w, scheme, entities)...)
			case WebhookInbound:
				errs = append(errs, validateInbound(file, w, scheme, entities)...)
				if w.Path != "" {
					if other, dup := paths[w.Path]; dup {
						add(file, w.Name, "path %s is already used by webhook %q", w.Path, other)
					}
					if endpointPaths[w.Path] {
						add(file, w.Name, "path %s is already used by an endpoint", w.Path)
					}
					paths[w.Path] = w.Name
				}
			default:
				add(file, w.Name, "mode %q must be outbound or inbound", w.Mode)
			}
		}
	}
	return errs
}

func validateOutbound(file string, w WebhookDef, scheme string, entities map[string]*EntityDef) []ValidationError {
	var msgs []string
	if scheme == "plugin" {
		msgs = append(msgs, "signature.scheme plugin is inbound only")
	}
	if w.Entity == "" {
		msgs = append(msgs, "entity is required")
	} else if _, ok := entities[w.Entity]; !ok && w.Entity != BuiltinUserEntityName {
		msgs = append(msgs, fmt.Sprintf("unknown entity %q", w.Entity))
	}
	if w.URL == "" {
		msgs = append(msgs, "url is required")
	}
	for field, set := range map[string]bool{
		"path": w.Path != "", "challenge": w.Challenge != nil, "dedupe_key": w.DedupeKey != "", "event": w.Event != "",
		"routes": len(w.Routes) > 0, "handler": w.Handler != "", "auth": w.Auth != nil, "token": w.Token != nil,
		"allowed_ips": len(w.AllowedIPs) > 0, "rate_limit": w.RateLimit != nil, "as_role": w.AsRole != "",
		"max_body_size": w.MaxBodySize != "", "spec": w.Spec != nil,
	} {
		if set {
			msgs = append(msgs, field+" is only valid with mode: inbound")
		}
	}
	return webhookErrs(file, w.Name, msgs)
}

func validateInbound(file string, w WebhookDef, scheme string, entities map[string]*EntityDef) []ValidationError {
	var msgs []string
	bad := func(format string, a ...any) { msgs = append(msgs, fmt.Sprintf(format, a...)) }

	for field, set := range map[string]bool{
		"entity": w.Entity != "", "trigger": w.Trigger != "", "condition": w.Condition != "", "url": w.URL != "",
		"method": w.Method != "", "payload": w.Payload != "", "headers": len(w.Headers) > 0,
		"allow_private_network": w.AllowPrivateNetwork,
	} {
		if set {
			bad("%s is only valid with mode: outbound", field)
		}
	}
	if !strings.HasPrefix(w.Path, "/") {
		bad("path is required and must start with /")
	}
	if _, err := ParseByteSize(w.MaxBodySize); err != nil {
		bad("max_body_size: %v", err)
	}

	// Guards: a receiver must say how it is protected.
	if scheme == "none" {
		if !InboundGuarded(w) {
			bad("unsigned receiver: set secret (signature) or a guard (auth, token or allowed_ips)")
		}
		if w.DedupeKey == "" {
			bad("dedupe_key is required on an unsigned receiver (repeats are the only replay protection)")
		}
	}
	if scheme == "plugin" && (w.Signature == nil || !strings.Contains(w.Signature.Handler, ".")) {
		bad("signature.scheme plugin needs signature.handler: <plugin>.<verifier>")
	}
	if t := w.Token; t != nil {
		if t.Value == "" {
			bad("token.value is required (is the env var set?)")
		}
		if (t.Header == "") == (t.QueryParam == "") {
			bad("token needs exactly one of header or query_param")
		}
	}
	for _, ip := range w.AllowedIPs {
		if !validIPOrCIDR(ip) {
			bad("allowed_ips: %q is not an IP or CIDR", ip)
		}
	}
	if rl := w.RateLimit; rl != nil && rl.KeyBy != "" && rl.KeyBy != "ip" && rl.KeyBy != "user" {
		bad("rate_limit.key_by %q must be ip or user", rl.KeyBy)
	}

	for name, p := range map[string]string{"dedupe_key": w.DedupeKey, "event": w.Event} {
		if p != "" && !requestPathRe.MatchString(p) {
			bad("%s %q must be a request path like body.id or headers.X-Event-Id", name, p)
		}
	}
	if c := w.Challenge; c != nil {
		switch strings.ToUpper(c.Method) {
		case "", "GET":
			if c.QueryParam == "" {
				bad("challenge.query_param is required for a GET challenge")
			}
		case "POST":
			if !requestPathRe.MatchString(c.BodyPath) || !strings.HasPrefix(c.BodyPath, "body.") {
				bad("challenge.body_path must be a body path like body.challenge")
			}
			if c.When == "" || w.Event == "" {
				bad("a POST challenge needs challenge.when and event (the event value that marks the handshake)")
			}
		default:
			bad("challenge.method %q must be GET or POST", c.Method)
		}
	}

	routes := w.Routes
	switch {
	case w.Handler != "" && len(routes) > 0:
		bad("use routes[\"*\"].handler instead of handler together with routes")
	case w.Handler != "":
		routes = map[string]WebhookRouteDef{"*": {Handler: w.Handler}}
	case len(routes) == 0:
		bad("routes (or handler) is required")
	}
	keys := make([]string, 0, len(routes))
	for k := range routes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, ev := range keys {
		if ev != "*" && w.Event == "" {
			bad("route %q never matches: set event (e.g. body.type) to route by event", ev)
		}
		for _, m := range validateRoute(routes[ev], entities) {
			bad("route %q: %s", ev, m)
		}
	}
	return webhookErrs(file, w.Name, msgs)
}

func validateRoute(r WebhookRouteDef, entities map[string]*EntityDef) []string {
	var msgs []string
	bad := func(format string, a ...any) { msgs = append(msgs, fmt.Sprintf(format, a...)) }
	if r.Handler != "" {
		if r.Entity != "" || r.Op != "" || len(r.Map) > 0 || len(r.Key) > 0 {
			bad("handler cannot be combined with entity/op/map/key")
		}
		if !strings.Contains(r.Handler, ".") {
			bad("handler %q must be <plugin>.<handler>", r.Handler)
		}
		return msgs
	}
	ent, ok := entities[r.Entity]
	if !ok {
		bad("unknown entity %q (set entity and op, or handler)", r.Entity)
		return msgs
	}
	switch r.Op {
	case "create", "upsert":
		if len(r.Map) == 0 {
			bad("op %s needs map", r.Op)
		}
		if len(r.Key) > 0 {
			bad("op %s takes no key (upsert matches on the entity's natural_key)", r.Op)
		}
		if r.Op == "upsert" && len(ent.NaturalKey) == 0 {
			bad("op upsert needs entity %s to declare natural_key", ent.Name)
		}
	case "update", "delete":
		if len(r.Key) == 0 {
			bad("op %s needs key (the primary key or the natural_key fields)", r.Op)
		} else if !keyIsUnique(ent, r.Key) {
			bad("key must be the primary key or exactly the natural_key fields of %s", ent.Name)
		}
		if r.Op == "update" && len(r.Map) == 0 {
			bad("op update needs map")
		}
		if r.Op == "delete" && len(r.Map) > 0 {
			bad("op delete takes no map")
		}
	default:
		bad("op %q must be create, update, upsert or delete", r.Op)
	}
	for _, block := range []map[string]any{r.Map, r.Key} {
		for field, v := range block {
			if !entityHasField(ent, field) {
				bad("%s has no field %q", ent.Name, field)
			}
			if s, ok := v.(string); ok {
				for _, m := range templateRe.FindAllStringSubmatch(s, -1) {
					if !requestPathRe.MatchString(m[1]) {
						bad("field %s: {{%s}} must reference body.*, headers.* or query.*", field, m[1])
					}
				}
			}
		}
	}
	return msgs
}

func entityHasField(e *EntityDef, name string) bool {
	for _, f := range e.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// keyIsUnique: key is the primary key field alone, or exactly the natural key.
func keyIsUnique(e *EntityDef, key map[string]any) bool {
	if len(key) == 1 {
		for name := range key {
			for _, f := range e.Fields {
				if f.Name == name && f.PrimaryKey {
					return true
				}
			}
		}
	}
	if len(key) != len(e.NaturalKey) {
		return false
	}
	for _, n := range e.NaturalKey {
		if _, ok := key[n]; !ok {
			return false
		}
	}
	return true
}

func validIPOrCIDR(s string) bool {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		return err == nil
	}
	return net.ParseIP(s) != nil
}

func webhookErrs(file, name string, msgs []string) []ValidationError {
	sort.Strings(msgs) // map iteration above; keep output stable
	out := make([]ValidationError, len(msgs))
	for i, m := range msgs {
		out[i] = ValidationError{File: file, Message: fmt.Sprintf("webhook %q: %s", name, m)}
	}
	return out
}

func validateJobs(cfg *RootConfig) []ValidationError {
	var errs []ValidationError
	for _, jf := range cfg.Jobs {
		for _, j := range jf.Jobs {
			bad := func(format string, a ...any) {
				errs = append(errs, ValidationError{Message: fmt.Sprintf("job %q: ", j.Name) + fmt.Sprintf(format, a...)})
			}
			switch strings.ToLower(j.Handler) {
			case "sql", "http":
				if j.Plugin != "" {
					bad("plugin is only used with handler: plugin")
				}
			case "plugin":
				if !strings.Contains(j.Plugin, ".") {
					bad("handler: plugin needs plugin: <plugin>.<job>")
				}
			default:
				bad("handler %q must be sql, http or plugin", j.Handler)
			}
			if j.OnFailure != "" && j.OnFailure != "log" && !strings.Contains(j.OnFailure, ".") {
				bad("on_failure %q must be log or <plugin>.<job>", j.OnFailure)
			}
			if _, err := ParseDuration(j.Timeout); err != nil {
				bad("timeout: %v", err)
			}
		}
	}
	return errs
}

// validateUpserts checks the upsert crud op and create.on_conflict against natural keys.
func validateUpserts(cfg *RootConfig, entities map[string]*EntityDef) []ValidationError {
	var errs []ValidationError
	for _, ef := range cfg.Endpoints {
		for _, ep := range ef.Endpoints {
			ent := entities[ep.Entity]
			hasKey := ent != nil && len(ent.NaturalKey) > 0
			for _, op := range ep.CRUD {
				if op == "upsert" && !hasKey {
					errs = append(errs, ValidationError{File: ef.FilePath,
						Message: fmt.Sprintf("endpoint %q: crud upsert needs entity %q to declare natural_key", ep.Path, ep.Entity)})
				}
			}
			if ep.Create == nil {
				continue
			}
			switch ep.Create.OnConflict {
			case "", "error":
			case "update", "ignore":
				if !hasKey {
					errs = append(errs, ValidationError{File: ef.FilePath,
						Message: fmt.Sprintf("endpoint %q: create.on_conflict %s needs entity %q to declare natural_key", ep.Path, ep.Create.OnConflict, ep.Entity)})
				}
			default:
				errs = append(errs, ValidationError{File: ef.FilePath,
					Message: fmt.Sprintf("endpoint %q: create.on_conflict %q must be error, update or ignore", ep.Path, ep.Create.OnConflict)})
			}
		}
	}
	return errs
}
