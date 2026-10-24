package openapi

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/teleology-io/yayPI/internal/schema"
)

var pathParamRe = regexp.MustCompile(`\{([^}/]+)\}`)

// buildCustomOperation documents a custom route (method + handler) from its
// declared contract: query parameters, body and the `data` it returns. An
// undeclared route is still listed, with no body or response schema.
func buildCustomOperation(ep *schema.Endpoint, entity *schema.Entity, projectName string, authConfigured bool) *Operation {
	c := ep.Contract
	if c == nil {
		c = &schema.RouteContract{}
	}
	id := c.OperationID
	if id == "" {
		id = ep.Handler
		if i := strings.LastIndex(id, "."); i >= 0 {
			id = id[i+1:]
		}
	}
	op := &Operation{OperationID: id, Responses: map[string]Response{}}
	op.Summary = strings.ToUpper(ep.Method) + " " + ep.Path
	if ep.Specs != nil && ep.Specs.Summary != "" {
		op.Summary = ep.Specs.Summary
	}
	if ep.Specs != nil {
		op.Description = ep.Specs.Description
	}
	op.Tags = []string{entity.Name}
	if ep.Specs != nil && len(ep.Specs.Tags) > 0 {
		op.Tags = append(op.Tags, ep.Specs.Tags...)
	} else {
		op.Tags = append(op.Tags, projectName)
	}
	authed := authConfigured && requiresAuth("", ep)
	if authed {
		op.Security = []SecurityRequirement{{"bearerAuth": []string{}}}
	}

	for _, m := range pathParamRe.FindAllStringSubmatch(ep.Path, -1) {
		op.Parameters = append(op.Parameters, Parameter{Name: m[1], In: "path", Required: true, Schema: &Schema{Type: "string"}})
	}
	for i := range c.Params {
		f := &c.Params[i]
		op.Parameters = append(op.Parameters, Parameter{
			Name: f.Name, In: "query", Required: f.Validate != nil && f.Validate.Required,
			Description: f.Description, Schema: fieldSchemaFor(f),
		})
	}
	if len(c.Body) > 0 {
		body := objectSchema(c.Body)
		op.RequestBody = &RequestBody{
			Required: len(body.Required) > 0,
			Content:  map[string]MediaType{"application/json": {Schema: body}},
		}
	}

	status := http.StatusOK
	if r := c.Response; r != nil && r.Status != 0 {
		status = r.Status
	}
	resp := Response{Description: http.StatusText(status)}
	if r := c.Response; r != nil && status != http.StatusNoContent && r.Shape != nil {
		resp.Content = map[string]MediaType{"application/json": {Schema: &Schema{
			Type: "object", Properties: map[string]*Schema{"data": shapeSchema(r.Shape)}, Required: []string{"data"},
		}}}
	}
	op.Responses[strconv.Itoa(status)] = resp
	addErrorResponses(op, "custom", authed)
	return op
}

// shapeSchema documents a response shape: an entity $ref or an object, or
// an array of either; object members may nest.
func shapeSchema(sh *schema.RouteShape) *Schema {
	item := &Schema{Ref: "#/components/schemas/" + sh.Entity}
	if sh.Entity == "" {
		item = &Schema{Type: "object", Properties: map[string]*Schema{}}
		for i := range sh.Members {
			m := &sh.Members[i]
			var p *Schema
			if m.Shape != nil {
				p = shapeSchema(m.Shape)
			} else {
				p = fieldSchemaFor(&m.Field)
			}
			if m.Field.Description != "" {
				p.Description = m.Field.Description
			}
			p.Nullable = m.Field.Nullable
			item.Properties[m.Field.Name] = p
			if !m.Field.Nullable {
				item.Required = append(item.Required, m.Field.Name) // a response always carries its members
			}
		}
	}
	if sh.List {
		return &Schema{Type: "array", Items: item}
	}
	return item
}

// objectSchema is an object with the given fields (required as declared).
func objectSchema(fields []schema.Field) *Schema {
	s := &Schema{Type: "object", Properties: map[string]*Schema{}}
	for i := range fields {
		f := &fields[i]
		p := fieldSchemaFor(f)
		p.Description = f.Description
		p.Nullable = f.Nullable
		s.Properties[f.Name] = p
		if f.Validate != nil && f.Validate.Required {
			s.Required = append(s.Required, f.Name)
		}
	}
	return s
}

// attachMethod sets operation on item under an HTTP method.
func attachMethod(item *PathItem, method string, operation *Operation) {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		item.Get = operation
	case http.MethodPost:
		item.Post = operation
	case http.MethodPatch:
		item.Patch = operation
	case http.MethodPut:
		item.Put = operation
	case http.MethodDelete:
		item.Delete = operation
	}
}
