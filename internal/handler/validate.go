package handler

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

// ValidationErrors holds per-field validation error messages.
type ValidationErrors map[string]string

func (ve ValidationErrors) Error() string {
	parts := make([]string, 0, len(ve))
	for k, v := range ve {
		parts = append(parts, fmt.Sprintf("%s: %s", k, v))
	}
	return strings.Join(parts, "; ")
}

var slugRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// prepareInput type-checks and coerces every provided field, applies validation rules,
// and returns a body keyed by column name containing only known fields. Unknown keys are
// dropped, or rejected when strict. For partial updates (isUpdate) only provided fields
// are checked and "required" applies only to explicit nulls.
func prepareInput(entity *schema.Entity, data map[string]any, isUpdate, strict bool, dialectName string) (map[string]any, ValidationErrors) {
	errs := ValidationErrors{}
	out := make(map[string]any, len(data))
	byKey := make(map[string]*schema.Field, len(entity.Fields)*2)
	for i := range entity.Fields {
		f := &entity.Fields[i]
		byKey[f.Name] = f
		byKey[f.ColumnName] = f
	}

	for k, raw := range data {
		f, ok := byKey[k]
		if !ok {
			if strict {
				errs[k] = "unknown field"
			}
			continue
		}
		if raw == nil {
			if !f.Nullable && !f.PrimaryKey {
				errs[f.Name] = message(f.Validate, fmt.Sprintf("%s cannot be null", f.Name))
				continue
			}
			out[f.ColumnName] = nil
			continue
		}
		v, msg := coerceValue(f, raw, dialectName)
		if msg != "" {
			errs[f.Name] = msg
			continue
		}
		if msg := checkRules(f, v); msg != "" {
			errs[f.Name] = msg
			continue
		}
		out[f.ColumnName] = v
	}

	if !isUpdate {
		for _, f := range entity.Fields {
			if f.Validate != nil && f.Validate.Required {
				if _, ok := out[f.ColumnName]; !ok {
					if _, already := errs[f.Name]; !already {
						errs[f.Name] = message(f.Validate, fmt.Sprintf("%s is required", f.Name))
					}
				}
			}
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

// passThroughExtras copies body keys that are not entity fields into data so Before*
// hooks can read them (e.g. a hook that turns an "email" into a user_id). They never
// reach SQL — the query builder only writes declared columns. JSON numbers are handed
// over as float64, matching encoding/json's default. No-op for strict endpoints, which
// reject unknown keys instead.
func passThroughExtras(entity *schema.Entity, raw, data map[string]any, strict bool) {
	if strict {
		return
	}
	for k, v := range raw {
		if entity.HasColumn(k) || hasFieldName(entity, k) {
			continue
		}
		if n, ok := v.(json.Number); ok {
			if f, err := n.Float64(); err == nil {
				v = f
			}
		}
		data[k] = v
	}
}

func hasFieldName(entity *schema.Entity, name string) bool {
	for _, f := range entity.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// coerceValue converts a decoded JSON value to the Go type the driver expects for the
// field. It returns a message when the value has the wrong type.
func coerceValue(f *schema.Field, v any, dialectName string) (any, string) {
	bad := func(kind string) (any, string) { return nil, fmt.Sprintf("%s must be %s", f.Name, kind) }
	switch f.Type {
	case types.FieldTypeString, types.FieldTypeText:
		s, ok := v.(string)
		if !ok {
			return bad("a string")
		}
		return s, ""
	case types.FieldTypeEnum:
		s, ok := v.(string)
		if !ok {
			return bad("a string")
		}
		if len(f.EnumValues) > 0 && !sliceContainsStr(f.EnumValues, s) {
			return bad("one of " + strings.Join(f.EnumValues, ", "))
		}
		return s, ""
	case types.FieldTypeInteger, types.FieldTypeBigint:
		switch n := v.(type) {
		case json.Number:
			i, err := n.Int64()
			if err != nil {
				return bad("an integer")
			}
			return i, ""
		case float64:
			if n != math.Trunc(n) || math.Abs(n) > 1<<53 {
				return bad("an integer")
			}
			return int64(n), ""
		case int, int32, int64:
			return n, ""
		}
		return bad("an integer")
	case types.FieldTypeFloat:
		switch n := v.(type) {
		case json.Number:
			x, err := n.Float64()
			if err != nil {
				return bad("a number")
			}
			return x, ""
		case float64:
			return n, ""
		case int, int64:
			return n, ""
		}
		return bad("a number")
	case types.FieldTypeDecimal:
		// Keep decimals as their exact decimal string; the database parses it without
		// float rounding.
		switch n := v.(type) {
		case json.Number:
			if _, err := n.Float64(); err != nil {
				return bad("a number")
			}
			return n.String(), ""
		case float64:
			return fmt.Sprint(n), ""
		case string:
			if _, err := json.Number(n).Float64(); err != nil {
				return bad("a number")
			}
			return n, ""
		}
		return bad("a number")
	case types.FieldTypeBoolean:
		b, ok := v.(bool)
		if !ok {
			return bad("true or false")
		}
		return b, ""
	case types.FieldTypeUUID:
		s, ok := v.(string)
		if !ok {
			return bad("a UUID")
		}
		u, err := uuid.Parse(s)
		if err != nil {
			return bad("a UUID")
		}
		return u.String(), ""
	case types.FieldTypeTimestamptz:
		s, ok := v.(string)
		if !ok {
			return bad("an RFC 3339 timestamp")
		}
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return bad("an RFC 3339 timestamp")
		}
		return t.UTC(), ""
	case types.FieldTypeDate:
		s, ok := v.(string)
		if !ok {
			return bad("a YYYY-MM-DD date")
		}
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return bad("a YYYY-MM-DD date")
		}
		return s, ""
	case types.FieldTypeJSONB:
		b, err := json.Marshal(v)
		if err != nil {
			return bad("valid JSON")
		}
		if dialectName == "postgres" {
			return json.RawMessage(b), "" // pgx sends RawMessage verbatim as jsonb
		}
		return string(b), ""
	case types.FieldTypeArray:
		items, ok := v.([]any)
		if !ok {
			return bad("an array")
		}
		strs := make([]string, len(items))
		for i, it := range items {
			switch x := it.(type) {
			case string:
				strs[i] = x
			case json.Number:
				strs[i] = x.String()
			case bool:
				strs[i] = fmt.Sprint(x)
			default:
				return bad("an array of scalars")
			}
		}
		if dialectName == "postgres" {
			return strs, ""
		}
		b, _ := json.Marshal(strs)
		return string(b), ""
	case types.FieldTypeBytea:
		s, ok := v.(string)
		if !ok {
			return bad("base64-encoded bytes")
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return bad("base64-encoded bytes")
		}
		return b, ""
	}
	return v, ""
}

// checkRules applies the field's validate: block to an already-coerced value.
func checkRules(f *schema.Field, v any) string {
	rules := f.Validate
	if rules == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		n := utf8.RuneCountInString(val)
		if rules.MinLength > 0 && n < rules.MinLength {
			return message(rules, fmt.Sprintf("%s must be at least %d characters", f.Name, rules.MinLength))
		}
		if rules.MaxLength > 0 && n > rules.MaxLength {
			return message(rules, fmt.Sprintf("%s must be at most %d characters", f.Name, rules.MaxLength))
		}
		if rules.Regexp != nil && !rules.Regexp.MatchString(val) {
			return message(rules, fmt.Sprintf("%s has an invalid format", f.Name))
		}
		if rules.Format != "" {
			if msg := checkFormat(f.Name, val, rules.Format); msg != "" {
				return message(rules, msg)
			}
		}
		if (rules.Min != nil || rules.Max != nil) && f.Type == types.FieldTypeDecimal {
			var num float64
			if _, err := fmt.Sscan(val, &num); err == nil {
				return checkRange(f, rules, num)
			}
		}
	case int64:
		return checkRange(f, rules, float64(val))
	case float64:
		return checkRange(f, rules, val)
	}
	return ""
}

func checkRange(f *schema.Field, rules *schema.FieldValidation, num float64) string {
	if rules.Min != nil && num < *rules.Min {
		return message(rules, fmt.Sprintf("%s must be at least %g", f.Name, *rules.Min))
	}
	if rules.Max != nil && num > *rules.Max {
		return message(rules, fmt.Sprintf("%s must be at most %g", f.Name, *rules.Max))
	}
	return ""
}

// checkFormat validates well-known format names and returns an error message or "".
func checkFormat(field, s, format string) string {
	switch format {
	case "email":
		if !isValidEmail(s) {
			return fmt.Sprintf("%s must be a valid email address", field)
		}
	case "url":
		u, err := url.ParseRequestURI(s)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Sprintf("%s must be a valid URL", field)
		}
	case "uuid":
		if _, err := uuid.Parse(s); err != nil {
			return fmt.Sprintf("%s must be a valid UUID", field)
		}
	case "slug":
		if !slugRe.MatchString(s) {
			return fmt.Sprintf("%s must be a valid slug (lowercase letters, numbers, hyphens)", field)
		}
	}
	return ""
}

// isValidEmail does a simple structural email check.
func isValidEmail(s string) bool {
	if strings.ContainsAny(s, " \t\r\n<>,;") {
		return false
	}
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 {
		return false
	}
	return strings.Contains(s[at+1:], ".")
}

// message returns the custom message if set, otherwise the default.
func message(v *schema.FieldValidation, defaultMsg string) string {
	if v != nil && v.Message != "" {
		return v.Message
	}
	return defaultMsg
}
