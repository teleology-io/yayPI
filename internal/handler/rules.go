package handler

import (
	"fmt"
	"strconv"
	"time"

	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/internal/timefmt"
	"github.com/teleology-io/yayPI/pkg/sdk"
	"github.com/teleology-io/yayPI/pkg/types"
)

// applyRules runs an entity's stamp_when fields and rules for one write.
// before is the stored row (nil on create), data the column-keyed changes
// (stamps are written into it). A failed rule is a 422 naming the fields.
func applyRules(entity *schema.Entity, before, data map[string]any) error {
	if len(entity.Rules) == 0 && !hasStamps(entity) {
		return nil
	}
	merged := make(map[string]any, len(before)+len(data))
	for k, v := range before {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}
	now := time.Now().UTC()

	for i := range entity.Fields {
		f := &entity.Fields[i]
		if f.StampWhen == nil || !touches(f.StampWhen, data) {
			continue
		}
		after := holds(*f.StampWhen, merged, now)
		prev := before != nil && holds(*f.StampWhen, before, now)
		switch {
		case after && !prev:
			var v any = now
			if f.Type == types.FieldTypeDate {
				v = now.Format("2006-01-02")
			}
			data[f.ColumnName], merged[f.ColumnName] = v, v
		case !after && prev:
			data[f.ColumnName], merged[f.ColumnName] = nil, nil
		}
	}

	fields := map[string]string{}
	first := ""
	for _, r := range entity.Rules {
		ok, known := eval(r.Check, merged, now)
		if !known || ok {
			continue
		}
		if first == "" {
			first = r.Message
		}
		for _, col := range r.Check.Fields() {
			if _, set := fields[col]; !set {
				fields[col] = r.Message
			}
		}
	}
	if first != "" {
		return &sdk.HookError{Status: 422, Message: first, Code: "rule_failed", Fields: fields}
	}
	return nil
}

func hasStamps(e *schema.Entity) bool {
	for i := range e.Fields {
		if e.Fields[i].StampWhen != nil {
			return true
		}
	}
	return false
}

// touches reports whether the write changes a field the condition reads.
func touches(c *schema.Cond, data map[string]any) bool {
	for _, col := range c.Fields() {
		if _, ok := data[col]; ok {
			return true
		}
	}
	return false
}

// holds is eval with unknown (a null operand) as false.
func holds(c schema.Cond, row map[string]any, now time.Time) bool {
	ok, known := eval(c, row, now)
	return ok && known
}

// eval compares the condition's operands in row. known is false when an
// operand is null (unless the condition compares with null itself).
func eval(c schema.Cond, row map[string]any, now time.Time) (ok, known bool) {
	l := operandValue(c.Left, c.Right, row, now)
	r := operandValue(c.Right, c.Left, row, now)
	if l == nil || r == nil {
		if (c.Left.Column == "" && !c.Left.Now && c.Left.Lit == nil) || (c.Right.Column == "" && !c.Right.Now && c.Right.Lit == nil) {
			eq := l == nil && r == nil
			return eq == (c.Op == "=="), true // `x == null` / `x != null`
		}
		return false, false
	}
	cmp, comparable := compare(l, r)
	if !comparable {
		return false, false
	}
	switch c.Op {
	case "==":
		return cmp == 0, true
	case "!=":
		return cmp != 0, true
	case ">":
		return cmp > 0, true
	case "<":
		return cmp < 0, true
	case ">=":
		return cmp >= 0, true
	case "<=":
		return cmp <= 0, true
	}
	return false, false
}

// operandValue normalizes an operand to time.Time, float64, bool or string
// (nil for null). A quoted literal compared with a time field is parsed as
// a time.
func operandValue(o, other schema.Operand, row map[string]any, now time.Time) any {
	switch {
	case o.Now:
		return now
	case o.Column == "":
		if s, ok := o.Lit.(string); ok && other.Column != "" && schema.IsTimeType(other.Type) {
			if t, ok := toTime(s); ok {
				return t
			}
		}
		return o.Lit
	}
	v := row[o.Column]
	if v == nil {
		return nil
	}
	switch {
	case schema.IsTimeType(o.Type):
		if t, ok := toTime(v); ok {
			return t
		}
	case schema.IsNumberType(o.Type):
		if n, ok := toFloat(v); ok {
			return n
		}
	case o.Type == types.FieldTypeBoolean:
		switch b := v.(type) {
		case bool:
			return b
		case int64:
			return b != 0
		case int:
			return b != 0
		}
	}
	return fmt.Sprint(v)
}

func toTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		for _, layout := range []string{time.RFC3339Nano, timefmt.SQLiteLayout, "2006-01-02 15:04:05", "2006-01-02"} {
			if p, err := time.Parse(layout, t); err == nil {
				return p, true
			}
		}
	}
	return time.Time{}, false
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// compare orders two normalized values of the same kind.
func compare(a, b any) (int, bool) {
	switch x := a.(type) {
	case time.Time:
		y, ok := b.(time.Time)
		if !ok {
			return 0, false
		}
		return x.Compare(y), true
	case float64:
		y, ok := b.(float64)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case bool:
		y, ok := b.(bool)
		if !ok {
			return 0, false
		}
		if x == y {
			return 0, true
		}
		return 1, true
	case string:
		y, ok := b.(string)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	}
	return 0, false
}
