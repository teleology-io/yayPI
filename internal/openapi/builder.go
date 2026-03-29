package openapi

import (
	"fmt"
	"strings"

	"github.com/teleology-io/yayPI/internal/schema"
)

// Build generates one OpenAPI 3.1 Spec per named spec in the registry.
// All endpoints are included in all specs by default unless ExcludeFromSpec is set.
// projectName is used as a fallback tag when no tags are defined on the endpoint.
func Build(reg *schema.Registry, projectName string, authConfigured bool) map[string]*Spec {
	specs := make(map[string]*Spec, len(reg.Specs))

	for _, sm := range reg.Specs {
		s := &Spec{
			OpenAPI: "3.1.0",
			Info: Info{
				Title:       sm.Title,
				Description: sm.Description,
				Version:     sm.Version,
			},
			Paths: make(map[string]*PathItem),
			Components: Components{
				Schemas: make(map[string]*Schema),
			},
		}
		for _, srv := range sm.Servers {
			s.Servers = append(s.Servers, Server{URL: srv.URL, Description: srv.Description})
		}
		if authConfigured {
			s.Components.SecuritySchemes = map[string]SecurityScheme{
				"bearerAuth": {
					Type:         "http",
					Scheme:       "bearer",
					BearerFormat: "JWT",
					Description:  "JWT bearer token",
				},
			}
		}
		// Reusable error schema
		s.Components.Schemas["Error"] = &Schema{
			Type: "object",
			Properties: map[string]*Schema{
				"error": {Type: "string"},
			},
			Required: []string{"error"},
		}
		specs[sm.Name] = s
	}

	if len(specs) == 0 {
		return specs
	}

	// Build entity component schemas and register in all specs.
	entitySchemas := make(map[string]*Schema)
	for _, entity := range reg.Entities() {
		entitySchemas[entity.Name] = entityToSchema(entity)
	}
	for _, s := range specs {
		for name, schema := range entitySchemas {
			s.Components.Schemas[name] = schema
		}
	}

	// Collect all tag names per spec for the Tags block.
	tagSeen := make(map[string]map[string]struct{})
	for name := range specs {
		tagSeen[name] = make(map[string]struct{})
	}

	// Process endpoints.
	for _, ep := range reg.Endpoints() {
		if ep.ExcludeFromSpec {
			continue
		}
		// Only CRUD endpoints with a known entity are supported.
		if len(ep.CRUD) == 0 || ep.Entity == "" {
			continue
		}
		entity, ok := reg.GetEntity(ep.Entity)
		if !ok {
			continue
		}

		// Determine target spec names.
		var targetNames []string
		if ep.Specs != nil && len(ep.Specs.Names) > 0 {
			targetNames = ep.Specs.Names
		} else {
			for name := range specs {
				targetNames = append(targetNames, name)
			}
		}

		for _, specName := range targetNames {
			s, ok := specs[specName]
			if !ok {
				continue
			}

			for _, op := range ep.CRUD {
				oaiPath := resolvePath(ep.Path, op)
				operation := buildOperation(op, ep, entity, projectName, authConfigured)

				if s.Paths[oaiPath] == nil {
					s.Paths[oaiPath] = &PathItem{}
				}
				attachOperation(s.Paths[oaiPath], op, operation)

				for _, tag := range operation.Tags {
					tagSeen[specName][tag] = struct{}{}
				}
			}
		}
	}

	// Populate Tags block in each spec.
	for name, s := range specs {
		for tag := range tagSeen[name] {
			s.Tags = append(s.Tags, Tag{Name: tag})
		}
	}

	return specs
}

// entityToSchema builds an OpenAPI Schema for a given entity's response shape.
func entityToSchema(entity *schema.Entity) *Schema {
	s := &Schema{
		Type:       "object",
		Properties: make(map[string]*Schema),
	}
	var required []string

	for _, f := range entity.Fields {
		if f.OmitResponse {
			continue
		}
		prop := fieldSchema(f.Type, f.EnumValues)
		if f.Nullable {
			prop.Nullable = true
		}
		isTimestamp := f.Name == "created_at" || f.Name == "updated_at" || f.Name == "deleted_at"
		if isTimestamp {
			prop.ReadOnly = true
		}
		s.Properties[f.Name] = prop

		if !f.Nullable && !f.PrimaryKey && f.Default == "" && !isTimestamp {
			required = append(required, f.Name)
		}
	}
	if len(required) > 0 {
		s.Required = required
	}
	return s
}

// buildOperation creates an Operation for the given CRUD op.
func buildOperation(op string, ep *schema.Endpoint, entity *schema.Entity, projectName string, authConfigured bool) *Operation {
	operation := &Operation{
		OperationID: fmt.Sprintf("%s%s", op, entity.Name),
		Responses:   make(map[string]Response),
	}

	// Summary
	if ep.Specs != nil && ep.Specs.Summary != "" {
		operation.Summary = ep.Specs.Summary
	} else {
		operation.Summary = fmt.Sprintf("%s %s", strings.Title(op), entity.Name) //nolint:staticcheck
	}

	// Description
	if ep.Specs != nil && ep.Specs.Description != "" {
		operation.Description = ep.Specs.Description
	}

	// Tags: entity name first, then user tags or project name.
	tags := []string{entity.Name}
	if ep.Specs != nil && len(ep.Specs.Tags) > 0 {
		tags = append(tags, ep.Specs.Tags...)
	} else {
		tags = append(tags, projectName)
	}
	operation.Tags = tags

	entityRef := fmt.Sprintf("#/components/schemas/%s", entity.Name)
	errorRef := &Schema{Ref: "#/components/schemas/Error"}

	// Determine if auth is required for this op.
	if authConfigured && requiresAuth(op, ep) {
		operation.Security = []SecurityRequirement{{"bearerAuth": []string{}}}
	}

	// ID path parameter (shared by get/update/delete).
	idParam := buildIDParam(entity)

	switch op {
	case "list":
		var params []Parameter
		if ep.List != nil {
			for _, field := range ep.List.AllowFilterBy {
				params = append(params, Parameter{
					Name:   field,
					In:     "query",
					Schema: &Schema{Type: "string"},
				})
			}
			if len(ep.List.AllowSortBy) > 0 {
				params = append(params, Parameter{
					Name:        "sort",
					In:          "query",
					Description: fmt.Sprintf("Sort field and direction. Allowed: %s", strings.Join(ep.List.AllowSortBy, ", ")),
					Schema:      &Schema{Type: "string"},
				})
			}
			switch ep.List.Pagination.Style {
			case "cursor":
				params = append(params,
					Parameter{Name: "limit", In: "query", Schema: &Schema{Type: "integer", Format: "int32"}},
					Parameter{Name: "cursor", In: "query", Schema: &Schema{Type: "string"}},
				)
			case "offset":
				params = append(params,
					Parameter{Name: "limit", In: "query", Schema: &Schema{Type: "integer", Format: "int32"}},
					Parameter{Name: "offset", In: "query", Schema: &Schema{Type: "integer", Format: "int32"}},
				)
			default:
				params = append(params,
					Parameter{Name: "limit", In: "query", Schema: &Schema{Type: "integer", Format: "int32"}},
					Parameter{Name: "page", In: "query", Schema: &Schema{Type: "integer", Format: "int32"}},
				)
			}
		}
		operation.Parameters = params
		operation.Responses["200"] = Response{
			Description: "OK",
			Content: map[string]MediaType{
				"application/json": {Schema: &Schema{
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Type: "array", Items: &Schema{Ref: entityRef}},
						"meta": {Type: "object", Properties: map[string]*Schema{
							"count":       {Type: "integer"},
							"next_cursor": {Type: "string", Nullable: true},
						}},
					},
				}},
			},
		}

	case "get":
		operation.Parameters = []Parameter{idParam}
		operation.Responses["200"] = Response{
			Description: "OK",
			Content: map[string]MediaType{
				"application/json": {Schema: &Schema{
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Ref: entityRef},
					},
				}},
			},
		}
		operation.Responses["404"] = Response{
			Description: "Not Found",
			Content:     map[string]MediaType{"application/json": {Schema: errorRef}},
		}

	case "create":
		operation.RequestBody = &RequestBody{
			Required: true,
			Content: map[string]MediaType{
				"application/json": {Schema: buildCreateSchema(entity)},
			},
		}
		operation.Responses["201"] = Response{
			Description: "Created",
			Content: map[string]MediaType{
				"application/json": {Schema: &Schema{
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Ref: entityRef},
					},
				}},
			},
		}
		operation.Responses["422"] = Response{
			Description: "Unprocessable Entity",
			Content:     map[string]MediaType{"application/json": {Schema: errorRef}},
		}

	case "update":
		operation.Parameters = []Parameter{idParam}
		operation.RequestBody = &RequestBody{
			Required: true,
			Content: map[string]MediaType{
				"application/json": {Schema: buildUpdateSchema(entity, ep)},
			},
		}
		operation.Responses["200"] = Response{
			Description: "OK",
			Content: map[string]MediaType{
				"application/json": {Schema: &Schema{
					Type: "object",
					Properties: map[string]*Schema{
						"data": {Ref: entityRef},
					},
				}},
			},
		}
		operation.Responses["404"] = Response{
			Description: "Not Found",
			Content:     map[string]MediaType{"application/json": {Schema: errorRef}},
		}

	case "delete":
		operation.Parameters = []Parameter{idParam}
		operation.Responses["204"] = Response{Description: "No Content"}
		operation.Responses["404"] = Response{
			Description: "Not Found",
			Content:     map[string]MediaType{"application/json": {Schema: errorRef}},
		}
	}

	return operation
}

// buildCreateSchema returns an inline request body schema for create — all writable fields.
func buildCreateSchema(entity *schema.Entity) *Schema {
	s := &Schema{
		Type:       "object",
		Properties: make(map[string]*Schema),
	}
	var required []string
	for _, f := range entity.Fields {
		if f.PrimaryKey || f.OmitResponse {
			continue
		}
		if f.Name == "created_at" || f.Name == "updated_at" || f.Name == "deleted_at" {
			continue
		}
		prop := fieldSchema(f.Type, f.EnumValues)
		if f.Nullable {
			prop.Nullable = true
		}
		s.Properties[f.Name] = prop
		if !f.Nullable && f.Default == "" {
			required = append(required, f.Name)
		}
	}
	if len(required) > 0 {
		s.Required = required
	}
	return s
}

// buildUpdateSchema returns an inline request body schema for update — all fields optional.
// Respects AllowedFields if set on the endpoint.
func buildUpdateSchema(entity *schema.Entity, ep *schema.Endpoint) *Schema {
	var allowedSet map[string]struct{}
	if ep.Update != nil && len(ep.Update.AllowedFields) > 0 {
		allowedSet = make(map[string]struct{}, len(ep.Update.AllowedFields))
		for _, f := range ep.Update.AllowedFields {
			allowedSet[f] = struct{}{}
		}
	}

	s := &Schema{
		Type:       "object",
		Properties: make(map[string]*Schema),
	}
	for _, f := range entity.Fields {
		if f.PrimaryKey || f.OmitResponse {
			continue
		}
		if f.Name == "created_at" || f.Name == "updated_at" || f.Name == "deleted_at" {
			continue
		}
		if allowedSet != nil {
			if _, ok := allowedSet[f.Name]; !ok {
				continue
			}
		}
		prop := fieldSchema(f.Type, f.EnumValues)
		if f.Nullable {
			prop.Nullable = true
		}
		s.Properties[f.Name] = prop
	}
	return s
}

// buildIDParam returns a path parameter for the entity's primary key.
func buildIDParam(entity *schema.Entity) Parameter {
	schema := &Schema{Type: "string", Format: "uuid"}
	for _, f := range entity.Fields {
		if f.PrimaryKey {
			schema = fieldSchema(f.Type, nil)
			break
		}
	}
	return Parameter{
		Name:     "id",
		In:       "path",
		Required: true,
		Schema:   schema,
	}
}

// resolvePath converts a chi path + crud op into an OpenAPI path.
// list/create use the base path; get/update/delete append /{id} if not already present.
func resolvePath(path, op string) string {
	switch op {
	case "get", "update", "delete":
		if strings.Contains(path, "{") {
			return path
		}
		return path + "/{id}"
	default:
		return path
	}
}

// attachOperation sets the operation on the correct method of the PathItem.
func attachOperation(item *PathItem, op string, operation *Operation) {
	switch op {
	case "list":
		item.Get = operation
	case "get":
		item.Get = operation
	case "create":
		item.Post = operation
	case "update":
		item.Patch = operation
	case "delete":
		item.Delete = operation
	}
}

// requiresAuth returns true if the given op requires authentication.
func requiresAuth(op string, ep *schema.Endpoint) bool {
	// Check op-level auth first, then fall back to endpoint-level auth.
	var opAuth *schema.Auth
	switch op {
	case "list":
		if ep.List != nil {
			opAuth = ep.List.Auth
		}
	case "get":
		if ep.Get != nil {
			opAuth = ep.Get.Auth
		}
	case "create":
		if ep.Create != nil {
			opAuth = ep.Create.Auth
		}
	case "update":
		if ep.Update != nil {
			opAuth = ep.Update.Auth
		}
	case "delete":
		if ep.Delete != nil {
			opAuth = ep.Delete.Auth
		}
	}
	if opAuth != nil {
		return opAuth.Require
	}
	return ep.Auth != nil && ep.Auth.Require
}
