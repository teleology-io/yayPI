package schema

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/pkg/types"
)

// defaultSQL turns a field's `default:` into its SQL default expression
// (dialect-neutral; dialects translate now() and gen_random_uuid()).
//
// Literals are checked against the field type and quoted here, so
// `default: ""` and `default: draft` just work. Two shorthands name the
// common expressions: `uuid` on a uuid field and `now` on a timestamptz or
// date field. Anything else that needs SQL is written {sql: "..."}.
func defaultSQL(d *config.DefaultDef, ft types.FieldType) (string, error) {
	if !d.IsSet() {
		return "", nil
	}
	if d.SQL != "" {
		return d.SQL, nil
	}
	v, tag := d.Literal, d.LiteralTag
	if tag == "!!null" {
		return "", nil
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	bad := func(want string) error {
		return fmt.Errorf("default %q is not a valid %s for a %s field%s", v, want, ft, sqlHint(v))
	}
	switch ft {
	case types.FieldTypeUUID:
		if v == "uuid" {
			return "gen_random_uuid()", nil
		}
		if _, err := uuid.Parse(v); err != nil || tag != "!!str" {
			return "", fmt.Errorf("default %q on a uuid field must be `uuid` (generate one) or a UUID%s", v, sqlHint(v))
		}
		return quote(v), nil
	case types.FieldTypeTimestamptz, types.FieldTypeDate:
		if v == "now" {
			return "now()", nil
		}
		if tag != "!!str" && tag != "!!timestamp" {
			return "", bad("time")
		}
		if h := sqlHint(v); h != "" {
			return "", fmt.Errorf("default %q looks like SQL%s", v, h)
		}
		return quote(v), nil
	case types.FieldTypeInteger, types.FieldTypeBigint:
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return "", bad("integer")
		}
		return v, nil
	case types.FieldTypeFloat, types.FieldTypeDecimal:
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "", bad("number")
		}
		return v, nil
	case types.FieldTypeBoolean:
		if v != "true" && v != "false" {
			return "", bad("boolean (true or false)")
		}
		return v, nil
	case types.FieldTypeString, types.FieldTypeText, types.FieldTypeEnum:
		if h := sqlHint(v); h != "" {
			return "", fmt.Errorf("default %q looks like SQL%s", v, h)
		}
		return quote(v), nil
	}
	return "", fmt.Errorf("a %s field takes its default as {sql: \"expression\"}", ft)
}

// sqlHint explains the old SQL-expression spelling of a default, which
// would now be taken literally (quotes included).
func sqlHint(v string) string {
	switch {
	case len(v) >= 2 && strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'"):
		inner := strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		return fmt.Sprintf(" (defaults are literal values now: write %q, without the SQL quotes)", inner)
	case strings.HasSuffix(v, ")") && strings.Contains(v, "("):
		return " (for an SQL expression write {sql: \"" + v + "\"}; `uuid` and `now` are shorthands for gen_random_uuid() and now())"
	}
	return ""
}

// DefaultValue is the JSON value of a literal default, for the OpenAPI
// schema (nil when there is none or it is an expression).
func defaultValue(d *config.DefaultDef, ft types.FieldType) any {
	if !d.IsSet() || d.SQL != "" || d.LiteralTag == "!!null" {
		return nil
	}
	v := d.Literal
	switch ft {
	case types.FieldTypeInteger, types.FieldTypeBigint:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	case types.FieldTypeFloat, types.FieldTypeDecimal:
		n, _ := strconv.ParseFloat(v, 64)
		return n
	case types.FieldTypeBoolean:
		return v == "true"
	case types.FieldTypeUUID, types.FieldTypeTimestamptz, types.FieldTypeDate:
		if v == "uuid" || v == "now" {
			return nil
		}
	}
	return v
}
