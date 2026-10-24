package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// RouteFieldDef is one declared query parameter or body field of a custom
// route (`params:` / `body:`). Written as `name: type` or as a mapping:
//
//	params:
//	  date: date
//	  limit: {type: integer, validate: {min: 1, max: 100}}
type RouteFieldDef struct {
	Name        string
	Type        string            `yaml:"type"`
	Required    bool              `yaml:"required"`
	Nullable    bool              `yaml:"nullable"`
	Values      []string          `yaml:"values"` // enum
	Description string            `yaml:"description"`
	Validate    *FieldValidateDef `yaml:"validate"`
}

// RouteFields keeps declaration order (the spec and error messages follow it).
type RouteFields []RouteFieldDef

func (f *RouteFields) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: expected a mapping of field name to type", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		d := RouteFieldDef{Name: k.Value}
		switch v.Kind {
		case yaml.ScalarNode:
			d.Type = v.Value
		case yaml.MappingNode:
			type plain RouteFieldDef
			var p plain
			if err := v.Decode(&p); err != nil {
				return err
			}
			p.Name = k.Value
			d = RouteFieldDef(p)
		default:
			return fmt.Errorf("line %d: field %q is a type name or a mapping", v.Line, k.Value)
		}
		*f = append(*f, d)
	}
	return nil
}

// RouteResponseDef declares what a custom route returns in `data`: one
// entity or object (`one:`), a list of them (`list:`), or nothing
// (`status: 204`). `one`/`list` take an entity name or a field mapping.
//
//	response: {list: Event}
//	response: {one: {fields: {ok: boolean}}}
type RouteResponseDef struct {
	Status int
	List   bool
	Entity string
	Fields RouteFields
	Line   int
}

func (r *RouteResponseDef) UnmarshalYAML(n *yaml.Node) error {
	r.Line = n.Line
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: response is a mapping ({list: Entity}, {one: Entity}, {status: 204})", n.Line)
	}
	var shape *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "status":
			if err := v.Decode(&r.Status); err != nil {
				return err
			}
		case "one", "list":
			if shape != nil {
				return fmt.Errorf("line %d: response takes either one or list, not both", n.Line)
			}
			shape, r.List = v, k.Value == "list"
		default:
			return fmt.Errorf("line %d: unknown response key %q (status, one, list)", k.Line, k.Value)
		}
	}
	if shape == nil {
		return nil
	}
	switch shape.Kind {
	case yaml.ScalarNode:
		r.Entity = shape.Value
	case yaml.MappingNode:
		var m struct {
			Fields RouteFields `yaml:"fields"`
		}
		if err := shape.Decode(&m); err != nil {
			return err
		}
		if len(m.Fields) == 0 {
			return fmt.Errorf("line %d: give an entity name or {fields: {name: type}}", shape.Line)
		}
		r.Fields = m.Fields
	default:
		return fmt.Errorf("line %d: give an entity name or {fields: {name: type}}", shape.Line)
	}
	return nil
}
