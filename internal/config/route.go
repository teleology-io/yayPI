package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// RouteFieldDef is one declared query parameter or body field of a custom
// route (`params:` / `body:`), or a member of a response object. Written as
// `name: type` or as a mapping:
//
//	params:
//	  date: date
//	  limit: {type: integer, validate: {min: 1, max: 100}}
//
// Response members may also nest: `events: {list: Event}`,
// `totals: {one: {fields: {count: integer}}}`.
type RouteFieldDef struct {
	Name        string
	Type        string            `yaml:"type"`
	Required    bool              `yaml:"required"`
	Nullable    bool              `yaml:"nullable"`
	Values      []string          `yaml:"values"` // enum
	Description string            `yaml:"description"`
	Validate    *FieldValidateDef `yaml:"validate"`
	// Shape is set for a nested member ({one: …} or {list: …}).
	Shape *RouteShapeDef `yaml:"-"`
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
		switch {
		case v.Kind == yaml.ScalarNode:
			d.Type = v.Value
		case v.Kind == yaml.MappingNode && (hasKey(v, "one") || hasKey(v, "list")):
			shape, err := parseShape(v)
			if err != nil {
				return err
			}
			d.Shape = shape
			if desc := keyValue(v, "description"); desc != nil {
				d.Description = desc.Value
			}
			if nb := keyValue(v, "nullable"); nb != nil {
				d.Nullable = nb.Value == "true"
			}
		case v.Kind == yaml.MappingNode:
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

// RouteShapeDef is `{one: X}` or `{list: X}` where X is an entity name or
// `{fields: {...}}` (whose members may nest again).
type RouteShapeDef struct {
	List   bool
	Entity string
	Fields RouteFields
}

func hasKey(n *yaml.Node, key string) bool { return keyValue(n, key) != nil }

func keyValue(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// parseShape reads the one/list key of mapping n (other keys are the
// caller's: status, description, nullable).
func parseShape(n *yaml.Node) (*RouteShapeDef, error) {
	one, list := keyValue(n, "one"), keyValue(n, "list")
	if one != nil && list != nil {
		return nil, fmt.Errorf("line %d: either one or list, not both", n.Line)
	}
	shape := &RouteShapeDef{List: list != nil}
	v := one
	if list != nil {
		v = list
	}
	switch v.Kind {
	case yaml.ScalarNode:
		shape.Entity = v.Value
	case yaml.MappingNode:
		var m struct {
			Fields RouteFields `yaml:"fields"`
		}
		if err := v.Decode(&m); err != nil {
			return nil, err
		}
		if len(m.Fields) == 0 || len(v.Content) != 2 {
			return nil, fmt.Errorf("line %d: give an entity name or {fields: {name: type}}", v.Line)
		}
		shape.Fields = m.Fields
	default:
		return nil, fmt.Errorf("line %d: give an entity name or {fields: {name: type}}", v.Line)
	}
	return shape, nil
}

// RouteResponseDef declares what a custom route returns in `data`: one
// entity or object (`one:`), a list of them (`list:`), or nothing
// (`status: 204`). Object members may nest further.
//
//	response: {list: Event}
//	response:
//	  one:
//	    fields:
//	      day: date
//	      events: {list: Event}
//	      doses: {list: {fields: {at: timestamptz, taken: boolean}}}
type RouteResponseDef struct {
	Status int
	Shape  *RouteShapeDef // nil: no body declared
	Line   int
}

func (r *RouteResponseDef) UnmarshalYAML(n *yaml.Node) error {
	r.Line = n.Line
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: response is a mapping ({list: Entity}, {one: Entity}, {status: 204})", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch k.Value {
		case "status":
			if err := v.Decode(&r.Status); err != nil {
				return err
			}
		case "one", "list":
		default:
			return fmt.Errorf("line %d: unknown response key %q (status, one, list)", k.Line, k.Value)
		}
	}
	if hasKey(n, "one") || hasKey(n, "list") {
		shape, err := parseShape(n)
		if err != nil {
			return err
		}
		r.Shape = shape
	}
	return nil
}
