package schema

import (
	"fmt"

	"github.com/teleology-io/yayPI/internal/config"
)

// Registry holds all resolved entities and endpoints.
type Registry struct {
	entities  map[string]*Entity
	endpoints []*Endpoint
	Specs     []SpecMeta
}

// SpecMeta holds the resolved metadata for a named OpenAPI spec.
type SpecMeta struct {
	Name        string
	Title       string
	Description string
	Version     string
	Servers     []SpecServerMeta
}

// SpecServerMeta is a single server entry in a SpecMeta.
type SpecServerMeta struct {
	URL         string
	Description string
}

// EndpointSpecMembership holds per-endpoint OpenAPI documentation overrides.
type EndpointSpecMembership struct {
	Names       []string // empty = all specs
	Description string
	Tags        []string
	Summary     string
}

// Endpoint represents a resolved API endpoint.
type Endpoint struct {
	Path            string
	Entity          string
	CRUD            []string
	Method          string
	Handler         string
	Middleware      []string
	Auth            *Auth
	RateLimit       *RateLimit              // optional per-endpoint rate limit
	List            *ListOpts
	Get             *GetOpts
	Create          *CreateOpts
	Update          *UpdateOpts
	Delete          *DeleteOpts
	ExcludeFromSpec bool                    // true when spec: false
	Specs           *EndpointSpecMembership // nil = include in all specs with no metadata override
}

// RateLimit holds rate limiting settings for an endpoint.
type RateLimit struct {
	RequestsPerMinute int
	Burst             int
	KeyBy             string // ip | user
}

// Auth holds authentication/authorization requirements for an endpoint.
type Auth struct {
	Require    bool
	Roles      []string
	Conditions []string // ABAC: CEL-lite expressions evaluated against the subject
}

// RowAccessRule is a single rule in a row_access list.
// The first matching rule wins. Filter "" means no restriction; no match means 403.
type RowAccessRule struct {
	When   string // condition expression or "*"
	Filter string // SQL fragment with :subject.* bindings; "" = no filter
}

// ListOpts holds list endpoint options.
type ListOpts struct {
	AllowFilterBy []string
	AllowSortBy   []string
	DefaultSort   string
	Pagination    Pagination
	Include       []string
	Auth          *Auth
	RowAccess     []RowAccessRule // ABAC: row-level filter rules
}

// Pagination holds pagination settings.
type Pagination struct {
	Style        string // cursor | offset
	DefaultLimit int
	MaxLimit     int
	IncludeTotal bool // offset style: emit meta.total via COUNT(*) query
}

// GetOpts holds get endpoint options.
type GetOpts struct {
	Include   []string
	Auth      *Auth
	RowAccess []RowAccessRule // ABAC: row-level filter rules
}

// CreateOpts holds create endpoint options.
type CreateOpts struct {
	Auth          *Auth
	BeforeHooks   []string
	AfterHooks    []string
	Bulk          bool
	BulkMax       int
	BulkErrorMode string // abort | partial
}

// UpdateOpts holds update endpoint options.
type UpdateOpts struct {
	AllowedFields []string
	Auth          *Auth
	RowAccess     []RowAccessRule // ABAC: row-level filter rules
}

// DeleteOpts holds delete endpoint options.
type DeleteOpts struct {
	Auth       *Auth
	SoftDelete bool
	RowAccess  []RowAccessRule // ABAC: row-level filter rules
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

	// Populate named spec metadata
	for _, sc := range cfg.Specs {
		sm := SpecMeta{
			Name:        sc.Name,
			Title:       sc.Title,
			Description: sc.Description,
			Version:     sc.Version,
		}
		for _, srv := range sc.Servers {
			sm.Servers = append(sm.Servers, SpecServerMeta{URL: srv.URL, Description: srv.Description})
		}
		reg.Specs = append(reg.Specs, sm)
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
		RateLimit:  buildRateLimit(def.RateLimit),
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
	if def.Spec != nil && !*def.Spec {
		ep.ExcludeFromSpec = true
	}
	if def.Specs != nil {
		ep.Specs = &EndpointSpecMembership{
			Names:       def.Specs.Names,
			Description: def.Specs.Description,
			Tags:        def.Specs.Tags,
			Summary:     def.Specs.Summary,
		}
	}
	return ep
}

func buildRateLimit(r *config.RateLimitConfig) *RateLimit {
	if r == nil {
		return nil
	}
	return &RateLimit{
		RequestsPerMinute: r.RequestsPerMinute,
		Burst:             r.Burst,
		KeyBy:             r.KeyBy,
	}
}

func buildAuth(a *config.AuthRequirement) *Auth {
	if a == nil {
		return nil
	}
	return &Auth{Require: a.Require, Roles: a.Roles, Conditions: a.Conditions}
}

func buildRowAccess(rules []config.RowAccessRule) []RowAccessRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]RowAccessRule, len(rules))
	for i, r := range rules {
		out[i] = RowAccessRule{When: r.When, Filter: r.Filter}
	}
	return out
}

func buildListOpts(l *config.ListConfig) *ListOpts {
	opts := &ListOpts{
		AllowFilterBy: l.AllowFilterBy,
		AllowSortBy:   l.AllowSortBy,
		DefaultSort:   l.DefaultSort,
		Include:       l.Include,
		Auth:          buildAuth(l.Auth),
		RowAccess:     buildRowAccess(l.RowAccess),
	}
	if l.Pagination != nil {
		opts.Pagination = Pagination{
			Style:        l.Pagination.Style,
			DefaultLimit: l.Pagination.DefaultLimit,
			MaxLimit:     l.Pagination.MaxLimit,
			IncludeTotal: l.Pagination.IncludeTotal,
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
	return &GetOpts{Include: g.Include, Auth: buildAuth(g.Auth), RowAccess: buildRowAccess(g.RowAccess)}
}

func buildCreateOpts(c *config.CreateConfig) *CreateOpts {
	bulkMax := c.BulkMax
	if bulkMax <= 0 {
		bulkMax = 500
	}
	mode := c.BulkErrorMode
	if mode == "" {
		mode = "abort"
	}
	return &CreateOpts{
		Auth:          buildAuth(c.Auth),
		BeforeHooks:   c.BeforeHooks,
		AfterHooks:    c.AfterHooks,
		Bulk:          c.Bulk,
		BulkMax:       bulkMax,
		BulkErrorMode: mode,
	}
}

func buildUpdateOpts(u *config.UpdateConfig) *UpdateOpts {
	return &UpdateOpts{AllowedFields: u.AllowedFields, Auth: buildAuth(u.Auth), RowAccess: buildRowAccess(u.RowAccess)}
}

func buildDeleteOpts(d *config.DeleteConfig) *DeleteOpts {
	return &DeleteOpts{Auth: buildAuth(d.Auth), SoftDelete: d.SoftDelete, RowAccess: buildRowAccess(d.RowAccess)}
}
