package outbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	htmltemplate "html/template"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// recordFieldRe matches {{record.field}} placeholders.
var recordFieldRe = regexp.MustCompile(`\{\{\s*record\.([A-Za-z0-9_]+)\s*\}\}`)

// Escaper turns a record value into template text for one output context.
type Escaper func(v any) string

// JSONEscaper renders values safely inside a JSON document: strings are JSON-escaped
// without surrounding quotes (the template supplies them), everything else is literal
// JSON. A value can therefore never break out of its string or inject keys.
func JSONEscaper(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		b, _ := json.Marshal(x)
		return string(b[1 : len(b)-1])
	case []byte:
		b, _ := json.Marshal(string(x))
		return string(b[1 : len(b)-1])
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case json.RawMessage:
		return string(x)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// URLEscaper path-escapes values substituted into a URL.
func URLEscaper(v any) string { return url.PathEscape(PlainString(v)) }

// HeaderEscaper strips CR/LF so values cannot inject headers.
func HeaderEscaper(v any) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(PlainString(v))
}

// PlainString renders a value as plain text.
func PlainString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case json.RawMessage:
		return string(x)
	}
	return fmt.Sprint(v)
}

// Render substitutes {{record.field}} placeholders, escaping each value with esc.
func Render(tmpl string, record map[string]any, esc Escaper) string {
	return recordFieldRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		field := recordFieldRe.FindStringSubmatch(m)[1]
		return esc(record[field])
	})
}

// RenderHTML renders an email body; record values are HTML-escaped.
func RenderHTML(tmpl string, record map[string]any) (string, error) {
	goTmpl := recordFieldRe.ReplaceAllString(tmpl, `{{index .record "$1"}}`)
	t, err := htmltemplate.New("").Option("missingkey=zero").Parse(goTmpl)
	if err != nil {
		return "", err
	}
	plain := make(map[string]any, len(record))
	for k, v := range record {
		plain[k] = PlainString(v)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{"record": plain}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// CheckTemplate reports a template parse error early (at boot).
func CheckTemplate(tmpl string) error {
	_, err := template.New("").Parse(recordFieldRe.ReplaceAllString(tmpl, `{{.x}}`))
	return err
}

// ── conditions ────────────────────────────────────────────────────────────────

// Condition is a compiled record predicate.
type Condition func(record map[string]any) bool

var condRe = regexp.MustCompile(`^record\.([A-Za-z0-9_]+)\s*(==|!=|>=|<=|>|<)\s*(.+)$`)

// CompileCondition parses expressions like:
//
//	record.status == "published"     record.deleted_at == null
//	record.score >= 10               record.email != ""
//
// joined with "and" / "&&". Anything else is an error at boot (previously an unparsable
// condition silently fired every time).
func CompileCondition(expr string) (Condition, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return func(map[string]any) bool { return true }, nil
	}
	parts := regexp.MustCompile(`\s+(?:and|&&)\s+`).Split(expr, -1)
	var conds []Condition
	for _, p := range parts {
		c, err := compileOne(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		conds = append(conds, c)
	}
	return func(r map[string]any) bool {
		for _, c := range conds {
			if !c(r) {
				return false
			}
		}
		return true
	}, nil
}

func compileOne(expr string) (Condition, error) {
	m := condRe.FindStringSubmatch(expr)
	if m == nil {
		return nil, fmt.Errorf("unsupported condition %q (expected record.<field> <op> <value>)", expr)
	}
	field, op, rawVal := m[1], m[2], strings.TrimSpace(m[3])

	var want any
	switch {
	case rawVal == "null" || rawVal == "nil":
		want = nil
	case len(rawVal) >= 2 && (rawVal[0] == '"' && rawVal[len(rawVal)-1] == '"' || rawVal[0] == '\'' && rawVal[len(rawVal)-1] == '\''):
		want = rawVal[1 : len(rawVal)-1]
	case rawVal == "true" || rawVal == "false":
		want = rawVal == "true"
	default:
		f, err := strconv.ParseFloat(rawVal, 64)
		if err != nil {
			return nil, fmt.Errorf("condition %q: value must be a quoted string, number, true/false or null", expr)
		}
		want = f
	}
	if want == nil && op != "==" && op != "!=" {
		return nil, fmt.Errorf("condition %q: null only supports == and !=", expr)
	}

	return func(r map[string]any) bool {
		got := r[field]
		if want == nil {
			isNull := got == nil
			return (op == "==") == isNull
		}
		if wf, ok := want.(float64); ok {
			gf, ok := toFloat(got)
			if !ok {
				return op == "!="
			}
			switch op {
			case "==":
				return gf == wf
			case "!=":
				return gf != wf
			case ">":
				return gf > wf
			case ">=":
				return gf >= wf
			case "<":
				return gf < wf
			case "<=":
				return gf <= wf
			}
		}
		if wb, ok := want.(bool); ok {
			gb, _ := got.(bool)
			if gi, ok := toFloat(got); ok { // SQLite/MySQL booleans are 0/1
				gb = gi != 0
			}
			return (op == "==") == (gb == wb)
		}
		// Missing/NULL compares as "" so `record.token != ""` is false when unset.
		gs, ws := PlainString(got), want.(string)
		switch op {
		case "==":
			return gs == ws
		case "!=":
			return gs != ws
		case ">":
			return gs > ws
		case ">=":
			return gs >= ws
		case "<":
			return gs < ws
		case "<=":
			return gs <= ws
		}
		return false
	}, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	case []byte:
		f, err := strconv.ParseFloat(string(n), 64)
		return f, err == nil
	}
	return 0, false
}
