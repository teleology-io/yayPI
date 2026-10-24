package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// DefaultDef is a field's `default:`. A plain YAML value is a literal of the
// field's type (`default: ""`, `default: draft`, `default: 0`, `default: false`);
// an expression is written `default: {sql: "..."}`. The schema resolver turns
// it into the column's SQL default and checks it against the field type.
type DefaultDef struct {
	// Literal is the scalar's text; LiteralTag its YAML tag (!!str, !!int,
	// !!float, !!bool, !!null). Empty when SQL is set.
	Literal    string
	LiteralTag string
	// SQL is a raw SQL default expression ({sql: ...}).
	SQL string
	// Line is the YAML line of the value, for error messages.
	Line int
}

// IsSet reports whether a default was written.
func (d *DefaultDef) IsSet() bool { return d != nil && (d.SQL != "" || d.LiteralTag != "") }

// UnmarshalYAML accepts a scalar (literal) or a {sql: expr} mapping.
func (d *DefaultDef) UnmarshalYAML(n *yaml.Node) error {
	d.Line = n.Line
	switch n.Kind {
	case yaml.ScalarNode:
		d.Literal, d.LiteralTag = n.Value, n.ShortTag()
		return nil
	case yaml.MappingNode:
		var m struct {
			SQL string `yaml:"sql"`
		}
		if err := n.Decode(&m); err != nil {
			return err
		}
		if len(n.Content) != 2 || m.SQL == "" {
			return fmt.Errorf("line %d: default must be a value or {sql: \"expression\"}", n.Line)
		}
		d.SQL = m.SQL
		return nil
	}
	return fmt.Errorf("line %d: default must be a value or {sql: \"expression\"}", n.Line)
}

// MarshalYAML writes the same shapes back.
func (d DefaultDef) MarshalYAML() (any, error) {
	if d.SQL != "" {
		return map[string]string{"sql": d.SQL}, nil
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: d.LiteralTag, Value: d.Literal}, nil
}
