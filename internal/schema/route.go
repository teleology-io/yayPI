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
	List   bool
	Entity string  // entity schema, or
	Fields []Field // an object with these fields
}

// AllFields lists every declared field (for time formats).
func (c *RouteContract) AllFields() []*Field {
	var out []*Field
	for _, fs := range [][]Field{c.Params, c.Body} {
		for i := range fs {
			out = append(out, &fs[i])
		}
	}
	if c.Response != nil {
		for i := range c.Response.Fields {
			out = append(out, &c.Response.Fields[i])
		}
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
		c.Response = &RouteResponse{Status: r.Status, List: r.List, Entity: r.Entity}
		if c.Response.Fields, err = buildRouteFields(r.Fields, "response"); err != nil {
			return nil, err
		}
		if r.Status != 0 && (r.Status < 200 || r.Status > 299) {
			return nil, fmt.Errorf("response.status %d: declare the success status (2xx); errors use the standard envelope", r.Status)
		}
		if r.Status == http.StatusNoContent && (r.Entity != "" || len(r.Fields) > 0) {
			return nil, fmt.Errorf("response: status 204 has no body")
		}
	}
	return c, nil
}

// checkRouteContracts verifies references once every entity is registered.
func checkRouteContracts(reg *Registry) error {
	for _, ep := range reg.endpoints {
		if ep.Contract == nil || ep.Contract.Response == nil || ep.Contract.Response.Entity == "" {
			continue
		}
		if _, ok := reg.GetEntity(ep.Contract.Response.Entity); !ok {
			return fmt.Errorf("endpoint %s %s: response entity %q is not defined", strings.ToUpper(ep.Method), ep.Path, ep.Contract.Response.Entity)
		}
	}
	return nil
}
