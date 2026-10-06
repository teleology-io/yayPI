package query

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

// Filter is one WHERE condition from a list request: `?col=v` (eq) or `?col[op]=v`.
type Filter struct {
	Column string
	Op     string // eq ne gt gte lt lte in nin contains starts_with is_null
	Value  any    // []any for in/nin, bool for is_null
}

// FilterOps lists the supported operators.
var FilterOps = map[string]bool{
	"eq": true, "ne": true, "gt": true, "gte": true, "lt": true, "lte": true,
	"in": true, "nin": true, "contains": true, "starts_with": true, "is_null": true,
}

const maxInValues = 100

// ParseFilter builds a Filter for field from a raw query-string value, coercing the value
// to the field's type so bad input is a 400 rather than a database error.
func ParseFilter(f *schema.Field, op, raw string) (Filter, error) {
	if op == "" {
		op = "eq"
	}
	if !FilterOps[op] {
		return Filter{}, fmt.Errorf("unsupported filter operator %q", op)
	}
	flt := Filter{Column: f.ColumnName, Op: op}
	switch op {
	case "is_null":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return Filter{}, fmt.Errorf("%s[is_null] must be true or false", f.Name)
		}
		flt.Value = b
	case "in", "nin":
		parts := strings.Split(raw, ",")
		if len(parts) > maxInValues {
			return Filter{}, fmt.Errorf("%s[%s] accepts at most %d values", f.Name, op, maxInValues)
		}
		vals := make([]any, 0, len(parts))
		for _, p := range parts {
			v, err := CoerceString(f, strings.TrimSpace(p))
			if err != nil {
				return Filter{}, err
			}
			vals = append(vals, v)
		}
		flt.Value = vals
	case "contains", "starts_with":
		if !isTextual(f.Type) {
			return Filter{}, fmt.Errorf("%s[%s] only applies to text fields", f.Name, op)
		}
		flt.Value = raw
	default:
		v, err := CoerceString(f, raw)
		if err != nil {
			return Filter{}, err
		}
		flt.Value = v
	}
	return flt, nil
}

func isTextual(t types.FieldType) bool {
	return t == types.FieldTypeString || t == types.FieldTypeText || t == types.FieldTypeEnum
}

// CoerceString converts a query-string value to the Go type matching the field.
func CoerceString(f *schema.Field, raw string) (any, error) {
	bad := func(kind string) error { return fmt.Errorf("%s must be %s", f.Name, kind) }
	switch f.Type {
	case types.FieldTypeInteger, types.FieldTypeBigint:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, bad("an integer")
		}
		return n, nil
	case types.FieldTypeFloat, types.FieldTypeDecimal:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, bad("a number")
		}
		return n, nil
	case types.FieldTypeBoolean:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, bad("true or false")
		}
		return b, nil
	case types.FieldTypeUUID:
		u, err := uuid.Parse(raw)
		if err != nil {
			return nil, bad("a UUID")
		}
		return u.String(), nil
	case types.FieldTypeTimestamptz:
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, bad("an RFC 3339 timestamp")
		}
		return t.UTC(), nil
	case types.FieldTypeDate:
		if _, err := time.Parse("2006-01-02", raw); err != nil {
			return nil, bad("a YYYY-MM-DD date")
		}
		return raw, nil
	case types.FieldTypeEnum:
		if len(f.EnumValues) > 0 {
			for _, v := range f.EnumValues {
				if v == raw {
					return raw, nil
				}
			}
			return nil, bad("one of " + strings.Join(f.EnumValues, ", "))
		}
	}
	return raw, nil
}

// escapeLike escapes LIKE wildcards so user input matches literally. '!' is the escape
// character because a backslash literal is itself an escape in MySQL strings.
func escapeLike(s string) string {
	r := strings.NewReplacer(`!`, `!!`, `%`, `!%`, `_`, `!_`)
	return r.Replace(s)
}

// filterClause renders one Filter.
func (b *Builder) filterClause(a *args, f Filter) (string, error) {
	field, ok := b.field(f.Column)
	if !ok {
		return "", fmt.Errorf("unknown filter column %q", f.Column)
	}
	col := b.qi(field.ColumnName)
	switch f.Op {
	case "", "eq":
		return col + " = " + a.add(f.Value), nil
	case "ne":
		return "(" + col + " <> " + a.add(f.Value) + " OR " + col + " IS NULL)", nil
	case "gt":
		return col + " > " + a.add(f.Value), nil
	case "gte":
		return col + " >= " + a.add(f.Value), nil
	case "lt":
		return col + " < " + a.add(f.Value), nil
	case "lte":
		return col + " <= " + a.add(f.Value), nil
	case "in", "nin":
		vals, _ := f.Value.([]any)
		if len(vals) == 0 {
			if f.Op == "in" {
				return "1 = 0", nil
			}
			return "1 = 1", nil
		}
		ph := make([]string, len(vals))
		for i, v := range vals {
			ph[i] = a.add(v)
		}
		kw := " IN "
		if f.Op == "nin" {
			kw = " NOT IN "
		}
		return col + kw + "(" + strings.Join(ph, ", ") + ")", nil
	case "contains", "starts_with":
		s, _ := f.Value.(string)
		pattern := escapeLike(strings.ToLower(s)) + "%"
		if f.Op == "contains" {
			pattern = "%" + pattern
		}
		return "LOWER(" + col + ") LIKE " + a.add(pattern) + " ESCAPE '!'", nil
	case "is_null":
		if v, _ := f.Value.(bool); v {
			return col + " IS NULL", nil
		}
		return col + " IS NOT NULL", nil
	}
	return "", fmt.Errorf("unsupported filter operator %q", f.Op)
}
