package schema

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/teleology-io/yayPI/internal/config"
)

// RouteContract is a custom route's declared interface (`params`, `body`,
// `response` on a method + handler endpoint).
type RouteContract struct {
	OperationID string
	Params      []Field // query parameters, checked and typed before the handler runs
	Body        []Field // JSON body fields, likewise (unknown keys are rejected)
	Response    *RouteResponse
}

// RouteResponse is what the route returns in `data` (nil Response: undeclared).
type RouteResponse struct {
	Status int
	Shape  *RouteShape // nil: no body
}

// RouteShape is one entity or object, or a list of them.
type RouteShape struct {
	List    bool
	Entity  string        // entity schema, or
	Members []RouteMember // an object with these members
}

// RouteMember is a scalar field or a nested shape of a response object.
type RouteMember struct {
	Field Field       // name, type, nullable, description (Type unset when Shape is)
	Shape *RouteShape // nested {one|list: …}; nil for a scalar
}

// AllFields lists every declared scalar field (for time formats).
func (c *RouteContract) AllFields() []*Field {
	var out []*Field
	for _, fs := range [][]Field{c.Params, c.Body} {
		for i := range fs {
			out = append(out, &fs[i])
		}
	}
	if c.Response != nil {
		out = c.Response.Shape.fields(out)
	}
	return out
}

func (s *RouteShape) fields(out []*Field) []*Field {
	if s == nil {
		return out
	}
	for i := range s.Members {
		m := &s.Members[i]
		if m.Shape != nil {
			out = m.Shape.fields(out)
		} else {
			out = append(out, &m.Field)
		}
	}
	return out
}

// entities lists the entity names a shape refers to.
func (s *RouteShape) entities(out []string) []string {
	if s == nil {
		return out
	}
	if s.Entity != "" {
		out = append(out, s.Entity)
	}
	for _, m := range s.Members {
		out = m.Shape.entities(out)
	}
	return out
}

func buildRouteFields(defs config.RouteFields, where string) ([]Field, error) {
	out := make([]Field, 0, len(defs))
	seen := map[string]bool{}
	for _, d := range defs {
		if seen[d.Name] {
			return nil, fmt.Errorf("%s: %q declared twice", where, d.Name)
		}
		seen[d.Name] = true
		if d.Shape != nil {
			return nil, fmt.Errorf("%s: %q: nested one/list members are for responses; input fields are scalars (or jsonb)", where, d.Name)
		}
		v := d.Validate
		if d.Required {
			c := config.FieldValidateDef{}
			if v != nil {
				c = *v
			}
			c.Required = true
			v = &c
		}
		f, err := buildField(config.FieldDef{Name: d.Name, Type: d.Type, Nullable: d.Nullable, Values: d.Values, Validate: v})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		f.ColumnName = d.Name // keys are used as written, not snake_cased
		f.Description = d.Description
		out = append(out, f)
	}
	return out, nil
}

// buildRouteContract resolves a custom route's declarations; nil when it
// declares none. CRUD endpoints may not declare them.
func buildRouteContract(def *config.EndpointDef) (*RouteContract, error) {
	if def.OperationID == "" && len(def.Params) == 0 && len(def.Body) == 0 && def.Response == nil {
		return nil, nil
	}
	if def.Handler == "" || len(def.CRUD) > 0 {
		return nil, fmt.Errorf("operation_id, params, body and response describe custom routes (method + handler); CRUD endpoints are described from their entity")
	}
	c := &RouteContract{OperationID: def.OperationID}
	var err error
	if c.Params, err = buildRouteFields(def.Params, "params"); err != nil {
		return nil, err
	}
	if c.Body, err = buildRouteFields(def.Body, "body"); err != nil {
		return nil, err
	}
	if len(c.Body) > 0 {
		switch strings.ToUpper(def.Method) {
		case http.MethodGet, http.MethodDelete, http.MethodHead:
			return nil, fmt.Errorf("body: a %s route takes params, not a body", strings.ToUpper(def.Method))
		}
	}
	if r := def.Response; r != nil {
		c.Response = &RouteResponse{Status: r.Status}
		if r.Shape != nil {
			if c.Response.Shape, err = buildShape(r.Shape, "response"); err != nil {
				return nil, err
			}
		}
		if r.Status != 0 && (r.Status < 200 || r.Status > 299) {
			return nil, fmt.Errorf("response.status %d: declare the success status (2xx); errors use the standard envelope", r.Status)
		}
		if r.Status == http.StatusNoContent && r.Shape != nil {
			return nil, fmt.Errorf("response: status 204 has no body")
		}
	}
	return c, nil
}

func buildShape(d *config.RouteShapeDef, where string) (*RouteShape, error) {
	s := &RouteShape{List: d.List, Entity: d.Entity}
	seen := map[string]bool{}
	for _, m := range d.Fields {
		at := where + "." + m.Name
		if seen[m.Name] {
			return nil, fmt.Errorf("%s declared twice", at)
		}
		seen[m.Name] = true
		if m.Shape != nil {
			inner, err := buildShape(m.Shape, at)
			if err != nil {
				return nil, err
			}
			s.Members = append(s.Members, RouteMember{Field: Field{Name: m.Name, ColumnName: m.Name, Nullable: m.Nullable, Description: m.Description}, Shape: inner})
			continue
		}
		fs, err := buildRouteFields(config.RouteFields{m}, where)
		if err != nil {
			return nil, err
		}
		s.Members = append(s.Members, RouteMember{Field: fs[0]})
	}
	return s, nil
}

// checkRouteContracts verifies references once every entity is registered.
func checkRouteContracts(reg *Registry) error {
	for _, ep := range reg.endpoints {
		if ep.Contract == nil || ep.Contract.Response == nil {
			continue
		}
		for _, name := range ep.Contract.Response.Shape.entities(nil) {
			if _, ok := reg.GetEntity(name); !ok {
				return fmt.Errorf("endpoint %s %s: response entity %q is not defined", strings.ToUpper(ep.Method), ep.Path, name)
			}
		}
	}
	return nil
}
