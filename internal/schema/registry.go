package schema

import (
	"fmt"

	"github.com/csullivan/yaypi/internal/config"
)

// Registry holds all resolved entities and endpoints.
type Registry struct {
	entities  map[string]*Entity
	endpoints []*Endpoint
}

// Endpoint represents a resolved API endpoint.
type Endpoint struct {
	Path       string
	Entity     string
	CRUD       []string
	Method     string
	Handler    string
	Middleware []string
	Auth       *Auth
	List       *ListOpts
	Get        *GetOpts
	Create     *CreateOpts
	Update     *UpdateOpts
	Delete     *DeleteOpts
}

// Auth holds authentication/authorization requirements for an endpoint.
type Auth struct {
	Require bool
	Roles   []string
}

// ListOpts holds list endpoint options.
type ListOpts struct {
	AllowFilterBy []string
	AllowSortBy   []string
	DefaultSort   string
	Pagination    Pagination
	Include       []string
	Auth          *Auth
}

// Pagination holds pagination settings.
type Pagination struct {
	Style        string // cursor, offset, page
	DefaultLimit int
	MaxLimit     int
}

// GetOpts holds get endpoint options.
type GetOpts struct {
	Include []string
	Auth    *Auth
}

// CreateOpts holds create endpoint options.
type CreateOpts struct {
	Auth        *Auth
	BeforeHooks []string
	AfterHooks  []string
}

// UpdateOpts holds update endpoint options.
type UpdateOpts struct {
	AllowedFields []string
	Auth          *Auth
}

// DeleteOpts holds delete endpoint options.
type DeleteOpts struct {
	Auth       *Auth
	SoftDelete bool
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		entities: make(map[string]*Entity),
	}
}

// RegisterEntity adds an entity to the registry.
func (r *Registry) RegisterEntity(e *Entity) {
	r.entities[e.Name] = e
}

// GetEntity returns an entity by name.
func (r *Registry) GetEntity(name string) (*Entity, bool) {
	e, ok := r.entities[name]
	return e, ok
}

// Entities returns all registered entities.
func (r *Registry) Entities() []*Entity {
	out := make([]*Entity, 0, len(r.entities))
	for _, e := range r.entities {
		out = append(out, e)
	}
	return out
}

// RegisterEndpoint adds an endpoint to the registry.
func (r *Registry) RegisterEndpoint(ep *Endpoint) {
	r.endpoints = append(r.endpoints, ep)
}

// Endpoints returns all registered endpoints.
func (r *Registry) Endpoints() []*Endpoint {
	return r.endpoints
}

// Build constructs a Registry from a loaded RootConfig.
func Build(cfg *config.RootConfig) (*Registry, error) {
	reg := NewRegistry()

	// Register entities
	for _, ec := range cfg.Entities {
		entity, err := buildEntity(ec)
		if err != nil {
			return nil, fmt.Errorf("building entity %q from %s: %w", ec.Entity.Name, ec.FilePath, err)
		}
		reg.RegisterEntity(entity)
	}

	// Register endpoints
	for _, ef := range cfg.Endpoints {
		for i := range ef.Endpoints {
			ep := buildEndpoint(&ef.Endpoints[i])
			reg.RegisterEndpoint(ep)
		}
	}

	return reg, nil
}

// buildEndpoint converts a config.EndpointDef to a schema.Endpoint.
func buildEndpoint(def *config.EndpointDef) *Endpoint {
	ep := &Endpoint{
		Path:       def.Path,
		Entity:     def.Entity,
		CRUD:       def.CRUD,
		Method:     def.Method,
		Handler:    def.Handler,
		Middleware: def.Middleware,
		Auth:       buildAuth(def.Auth),
	}
	if def.List != nil {
		ep.List = buildListOpts(def.List)
	}
	if def.Get != nil {
		ep.Get = buildGetOpts(def.Get)
	}
	if def.Create != nil {
		ep.Create = buildCreateOpts(def.Create)
	}
	if def.Update != nil {
		ep.Update = buildUpdateOpts(def.Update)
	}
	if def.Delete != nil {
		ep.Delete = buildDeleteOpts(def.Delete)
	}
	return ep
}

func buildAuth(a *config.AuthRequirement) *Auth {
	if a == nil {
		return nil
	}
	return &Auth{Require: a.Require, Roles: a.Roles}
}

func buildListOpts(l *config.ListConfig) *ListOpts {
	opts := &ListOpts{
		AllowFilterBy: l.AllowFilterBy,
		AllowSortBy:   l.AllowSortBy,
		DefaultSort:   l.DefaultSort,
		Include:       l.Include,
		Auth:          buildAuth(l.Auth),
	}
	if l.Pagination != nil {
		opts.Pagination = Pagination{
			Style:        l.Pagination.Style,
			DefaultLimit: l.Pagination.DefaultLimit,
			MaxLimit:     l.Pagination.MaxLimit,
		}
	}
	if opts.Pagination.DefaultLimit == 0 {
		opts.Pagination.DefaultLimit = 20
	}
	if opts.Pagination.MaxLimit == 0 {
		opts.Pagination.MaxLimit = 100
	}
	return opts
}

func buildGetOpts(g *config.GetConfig) *GetOpts {
	return &GetOpts{Include: g.Include, Auth: buildAuth(g.Auth)}
}

func buildCreateOpts(c *config.CreateConfig) *CreateOpts {
	return &CreateOpts{
		Auth:        buildAuth(c.Auth),
		BeforeHooks: c.BeforeHooks,
		AfterHooks:  c.AfterHooks,
	}
}

func buildUpdateOpts(u *config.UpdateConfig) *UpdateOpts {
	return &UpdateOpts{AllowedFields: u.AllowedFields, Auth: buildAuth(u.Auth)}
}

func buildDeleteOpts(d *config.DeleteConfig) *DeleteOpts {
	return &DeleteOpts{Auth: buildAuth(d.Auth), SoftDelete: d.SoftDelete}
}
