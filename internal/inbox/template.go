package inbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

// placeholderRe matches {{ path }} in map/key values.
var placeholderRe = regexp.MustCompile(`\{\{\s*([^}]*?)\s*\}\}`)

// scope is what request paths resolve against: body (decoded JSON or form), headers and
// query.
type scope map[string]any

// newScope decodes body as JSON (numbers kept exact) or, for form posts, as a flat map.
func newScope(body []byte, contentType string, h http.Header, q url.Values) scope {
	var decoded any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&decoded); err != nil {
		decoded = nil
		if mt, _, _ := mime.ParseMediaType(contentType); mt == "application/x-www-form-urlencoded" {
			if form, err := url.ParseQuery(string(body)); err == nil {
				decoded = flatten(form)
			}
		}
	}
	headers := make(map[string]any, len(h))
	for k, v := range h {
		if len(v) > 0 {
			headers[http.CanonicalHeaderKey(k)] = v[0]
		}
	}
	return scope{"body": decoded, "headers": headers, "query": flatten(q)}
}

func flatten(v url.Values) map[string]any {
	out := make(map[string]any, len(v))
	for k, vals := range v {
		if len(vals) > 0 {
			out[k] = vals[0]
		}
	}
	return out
}

// lookup resolves a path like body.data.items.0.id or headers.X-Event-Id.
func (s scope) lookup(path string) (any, bool) {
	parts := strings.Split(strings.TrimSpace(path), ".")
	cur, ok := s[parts[0]]
	if !ok {
		return nil, false
	}
	for i, p := range parts[1:] {
		switch node := cur.(type) {
		case map[string]any:
			if parts[0] == "headers" && i == 0 {
				p = http.CanonicalHeaderKey(p)
			}
			if cur, ok = node[p]; !ok {
				return nil, false
			}
		case []any:
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n >= len(node) {
				return nil, false
			}
			cur = node[n]
		default:
			return nil, false
		}
	}
	return cur, true
}

// text renders a path's value as plain text ("" when missing or null).
func (s scope) text(path string) string {
	if path == "" {
		return ""
	}
	v, _ := s.lookup(path)
	return plain(v)
}

func plain(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case map[string]any, []any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

// render evaluates a map/key value. A string that is exactly one placeholder keeps the
// value's JSON type (number, bool, object, null when missing); mixed text renders as a
// string. Non-string YAML values are literals.
func (s scope) render(v any) any {
	str, ok := v.(string)
	if !ok {
		return v
	}
	if m := placeholderRe.FindStringSubmatchIndex(str); m != nil && m[0] == 0 && m[1] == len(str) {
		val, _ := s.lookup(str[m[2]:m[3]])
		return val
	}
	return placeholderRe.ReplaceAllStringFunc(str, func(ph string) string {
		return s.text(placeholderRe.FindStringSubmatch(ph)[1])
	})
}

// renderFields renders a map/key block and adapts values to the entity's field types, so
// a provider's numeric id can land in a string column and unix seconds in a timestamp.
func (s scope) renderFields(entity *schema.Entity, block map[string]any) map[string]any {
	out := make(map[string]any, len(block))
	for field, tmpl := range block {
		v := s.render(tmpl)
		if f, ok := entity.FieldByName(field); ok {
			v = adapt(f, v)
		}
		out[field] = v
	}
	return out
}

// adapt bridges JSON shapes that the strict create/update coercion would reject but whose
// meaning is unambiguous for a machine-to-machine payload.
func adapt(f *schema.Field, v any) any {
	switch f.Type {
	case types.FieldTypeString, types.FieldTypeText, types.FieldTypeEnum, types.FieldTypeUUID:
		switch x := v.(type) {
		case json.Number:
			return x.String()
		case bool:
			return strconv.FormatBool(x)
		}
	case types.FieldTypeInteger, types.FieldTypeBigint, types.FieldTypeFloat:
		if x, ok := v.(string); ok && x != "" {
			if _, err := strconv.ParseFloat(x, 64); err == nil {
				return json.Number(x)
			}
		}
	case types.FieldTypeBoolean:
		if x, ok := v.(string); ok {
			if b, err := strconv.ParseBool(x); err == nil {
				return b
			}
		}
	case types.FieldTypeTimestamptz:
		// Unix seconds (common in webhook payloads) → RFC 3339.
		if n, ok := v.(json.Number); ok {
			if sec, err := n.Int64(); err == nil {
				return time.Unix(sec, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	return v
}
