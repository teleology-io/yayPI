package schema

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/pkg/types"
)

// Rule is an entity-level check across fields (`rules:`), evaluated on every
// create and update against the row as it will be stored.
type Rule struct {
	Check   Cond
	Message string
}

// Cond is one comparison over a row: `ends_at > starts_at`, `qty >= 1`,
// `status == "published"`, `ends_at > now`, or a bare boolean field (`done`).
type Cond struct {
	Text  string
	Left  Operand
	Op    string // == != > < >= <= (a bare boolean field is `== true`)
	Right Operand
}

// Operand is a field reference (Column set) or a literal.
type Operand struct {
	Column string          // field's column; "" for a literal
	Type   types.FieldType // the field's type
	Lit    any             // string, float64, bool or nil
	Now    bool            // the literal `now`
}

// Fields lists the fields a condition reads.
func (c Cond) Fields() []string {
	var out []string
	for _, o := range []Operand{c.Left, c.Right} {
		if o.Column != "" {
			out = append(out, o.Column)
		}
	}
	return out
}

var condOps = []string{">=", "<=", "!=", "==", ">", "<"}

// parseCond parses a comparison over e's fields.
func parseCond(e *Entity, text string) (Cond, error) {
	c := Cond{Text: strings.TrimSpace(text)}
	if c.Text == "" {
		return c, fmt.Errorf("empty condition")
	}
	for _, op := range condOps {
		if i := strings.Index(c.Text, " "+op+" "); i >= 0 {
			l, r := strings.TrimSpace(c.Text[:i]), strings.TrimSpace(c.Text[i+len(op)+2:])
			var err error
			if c.Left, err = parseOperand(e, l); err != nil {
				return c, err
			}
			if c.Right, err = parseOperand(e, r); err != nil {
				return c, err
			}
			c.Op = op
			if c.Left.Column == "" && c.Right.Column == "" {
				return c, fmt.Errorf("%q compares two constants", c.Text)
			}
			return c, checkOperandTypes(c)
		}
	}
	// A bare boolean field.
	o, err := parseOperand(e, c.Text)
	if err != nil {
		return c, fmt.Errorf("%q: expected `field`, or `a <op> b` with one of %s (spaces around the operator)", c.Text, strings.Join(condOps, " "))
	}
	if o.Type != types.FieldTypeBoolean {
		return c, fmt.Errorf("%q: a condition without an operator must be a boolean field", c.Text)
	}
	c.Left, c.Op, c.Right = o, "==", Operand{Lit: true}
	return c, nil
}

func parseOperand(e *Entity, s string) (Operand, error) {
	switch {
	case s == "now":
		return Operand{Now: true}, nil
	case s == "null":
		return Operand{}, nil
	case s == "true" || s == "false":
		return Operand{Lit: s == "true"}, nil
	case len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\''):
		return Operand{Lit: s[1 : len(s)-1]}, nil
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return Operand{Lit: n}, nil
	}
	for i := range e.Fields {
		f := &e.Fields[i]
		if f.Name == s || f.ColumnName == s {
			return Operand{Column: f.ColumnName, Type: f.Type}, nil
		}
	}
	return Operand{}, fmt.Errorf("unknown field %q (strings need quotes)", s)
}

// IsTimeType reports whether t holds times (timestamptz, date).
func IsTimeType(t types.FieldType) bool {
	return t == types.FieldTypeTimestamptz || t == types.FieldTypeDate
}

// IsNumberType reports whether t holds numbers.
func IsNumberType(t types.FieldType) bool {
	switch t {
	case types.FieldTypeInteger, types.FieldTypeBigint, types.FieldTypeFloat, types.FieldTypeDecimal:
		return true
	}
	return false
}

// checkOperandTypes refuses comparisons that can't mean anything (a number
// with a time, `now` with a string...).
func checkOperandTypes(c Cond) error {
	kind := func(o Operand) string {
		switch {
		case o.Now:
			return "time"
		case o.Column != "" && IsTimeType(o.Type):
			return "time"
		case o.Column != "" && IsNumberType(o.Type):
			return "number"
		case o.Type == types.FieldTypeBoolean:
			return "bool"
		case o.Column != "":
			return "text"
		}
		switch o.Lit.(type) {
		case float64:
			return "number"
		case bool:
			return "bool"
		case string:
			return "text?" // a quoted time literal compares with a time field
		}
		return "null"
	}
	a, b := kind(c.Left), kind(c.Right)
	if a == "null" || b == "null" {
		if c.Op != "==" && c.Op != "!=" {
			return fmt.Errorf("%q: null only compares with == or !=", c.Text)
		}
		return nil
	}
	if a == "text?" {
		a = b
	}
	if b == "text?" {
		b = a
	}
	if a != b {
		return fmt.Errorf("%q compares a %s with a %s", c.Text, a, b)
	}
	if a == "bool" && c.Op != "==" && c.Op != "!=" {
		return fmt.Errorf("%q: booleans only compare with == or !=", c.Text)
	}
	return nil
}

// buildRules resolves `rules:` and fields' `stamp_when:` once the entity's
// fields are known.
func buildRules(e *Entity, def config.EntityDef) error {
	for i, r := range def.Rules {
		c, err := parseCond(e, r.Check)
		if err != nil {
			return fmt.Errorf("rules[%d].check: %w", i, err)
		}
		msg := r.Message
		if msg == "" {
			msg = "must satisfy " + c.Text
		}
		e.Rules = append(e.Rules, Rule{Check: c, Message: msg})
	}
	for _, fd := range def.Fields {
		if fd.StampWhen == "" {
			continue
		}
		f, ok := e.FieldByName(fd.Name)
		if !ok {
			continue
		}
		if !IsTimeType(f.Type) {
			return fmt.Errorf("field %q: stamp_when needs a timestamptz or date field", fd.Name)
		}
		if !f.Nullable {
			return fmt.Errorf("field %q: stamp_when clears the field when the condition turns false, so it must be nullable: true", fd.Name)
		}
		c, err := parseCond(e, fd.StampWhen)
		if err != nil {
			return fmt.Errorf("field %q: stamp_when: %w", fd.Name, err)
		}
		f.StampWhen = &c
	}
	return nil
}
