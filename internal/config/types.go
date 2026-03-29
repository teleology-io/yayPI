package config

import "time"

// RootConfig is the top-level configuration structure loaded from yaypi.yaml.
type RootConfig struct {
	Version     string          `yaml:"version"`
	Project     ProjectConfig   `yaml:"project"`
	Server      ServerConfig    `yaml:"server"`
	Databases   []DBConfig      `yaml:"databases"`
	Auth        AuthConfig      `yaml:"auth"`
	Policy      PolicyConfig    `yaml:"policy"`
	AutoMigrate bool            `yaml:"auto_migrate"`
	Plugins     []PluginConfig  `yaml:"plugins"`
	Include     []string        `yaml:"include"`
	Specs       []SpecConfig    `yaml:"spec"`
	// Loaded from included files:
	Entities     []*EntityConfig          `yaml:"-"`
	Endpoints    []*EndpointFileConfig    `yaml:"-"`
	Jobs         []*JobConfig             `yaml:"-"`
	AuthEndpoint *AuthEndpointFileConfig  `yaml:"-"`
}

// SpecConfig defines a named OpenAPI spec in yaypi.yaml under spec:.
type SpecConfig struct {
	Name        string       `yaml:"name"`
	Title       string       `yaml:"title"`
	Description string       `yaml:"description"`
	Version     string       `yaml:"version"`
	Servers     []SpecServer `yaml:"servers"`
}

// SpecServer is a single server entry in an OpenAPI spec.
type SpecServer struct {
	URL         string `yaml:"url"`
	Description string `yaml:"description"`
}

// ProjectConfig holds project-level metadata.
type ProjectConfig struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port               int           `yaml:"port"`
	ReadTimeout        time.Duration `yaml:"read_timeout"`
	WriteTimeout       time.Duration `yaml:"write_timeout"`
	ShutdownTimeout    time.Duration `yaml:"shutdown_timeout"`
	MaxRequestBodySize string        `yaml:"max_request_body_size"`
	MaxHeaderBytes     string        `yaml:"max_header_bytes"`
	TLS                *TLSConfig    `yaml:"tls"`
	AllowedOrigins     []string      `yaml:"allowed_origins"`
}

// TLSConfig holds TLS certificate settings.
type TLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// DBConfig holds database connection settings.
type DBConfig struct {
	Name            string        `yaml:"name"`
	Driver          string        `yaml:"driver"`
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	Default         bool          `yaml:"default"`
	ReadOnly        bool          `yaml:"read_only"`
	Schema          string        `yaml:"schema"`
}

// AuthConfig holds JWT authentication settings.
type AuthConfig struct {
	Provider         string   `yaml:"provider"`
	Secret           string   `yaml:"secret"`
	Expiry           string   `yaml:"expiry"`
	Algorithm        string   `yaml:"algorithm"`
	RejectAlgorithms []string `yaml:"reject_algorithms"`
}

// PolicyConfig holds RBAC policy engine settings.
type PolicyConfig struct {
	Engine       string `yaml:"engine"`
	Model        string `yaml:"model"`
	Adapter      string `yaml:"adapter"`
	AdapterTable string `yaml:"adapter_table"`
}

// PluginConfig holds plugin configuration.
type PluginConfig struct {
	Name     string                 `yaml:"name"`
	Path     string                 `yaml:"path"`
	Checksum string                 `yaml:"checksum"`
	Config   map[string]interface{} `yaml:"config"`
}

// EntityConfig represents an entity YAML file.
type EntityConfig struct {
	Version  string    `yaml:"version"`
	Kind     string    `yaml:"kind"`
	Entity   EntityDef `yaml:"entity"`
	FilePath string    `yaml:"-"`
}

// EntityDef is the entity definition block within an EntityConfig.
type EntityDef struct {
	Name        string          `yaml:"name"`
	Table       string          `yaml:"table"`
	Database    string          `yaml:"database"`
	Timestamps  bool            `yaml:"timestamps"`
	SoftDelete  bool            `yaml:"soft_delete"`
	Fields      []FieldDef      `yaml:"fields"`
	Relations   []RelationDef   `yaml:"relations"`
	Indexes     []IndexDef      `yaml:"indexes"`
	Constraints []ConstraintDef `yaml:"constraints"`
	Hooks       EntityHooksDef  `yaml:"hooks"`
}

// FieldDef describes a single field on an entity.
type FieldDef struct {
	Name          string           `yaml:"name"`
	Type          string           `yaml:"type"`
	Length        int              `yaml:"length"`
	Precision     int              `yaml:"precision"`
	Scale         int              `yaml:"scale"`
	Nullable      bool             `yaml:"nullable"`
	Unique        bool             `yaml:"unique"`
	PrimaryKey    bool             `yaml:"primary_key"`
	Default       string           `yaml:"default"`
	Index         bool             `yaml:"index"`
	Values        []string         `yaml:"values"` // for enum
	References    *ReferenceDef    `yaml:"references"`
	Serialization SerializationDef `yaml:"serialization"`
	Access        *FieldAccessDef  `yaml:"access"` // ABAC: per-role read/write access
}

// ReferenceDef describes a foreign key reference.
type ReferenceDef struct {
	Entity   string `yaml:"entity"`
	Field    string `yaml:"field"`
	OnDelete string `yaml:"on_delete"`
	OnUpdate string `yaml:"on_update"`
}

// SerializationDef controls how a field is serialized.
type SerializationDef struct {
	OmitResponse bool `yaml:"omit_response"`
	OmitLog      bool `yaml:"omit_log"`
}

// RelationDef describes a relationship between entities.
type RelationDef struct {
	Name       string `yaml:"name"`
	Type       string `yaml:"type"`
	Entity     string `yaml:"entity"`
	ForeignKey string `yaml:"foreign_key"`
	Through    string `yaml:"through"`
	OtherKey   string `yaml:"other_key"`
}

// IndexDef describes an index on an entity.
type IndexDef struct {
	Name    string   `yaml:"name"`
	Columns []string `yaml:"columns"`
	Unique  bool     `yaml:"unique"`
	Type    string   `yaml:"type"`
}

// ConstraintDef describes a constraint on an entity.
type ConstraintDef struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Check   string   `yaml:"check"`
	Columns []string `yaml:"columns"`
}

// EntityHooksDef describes lifecycle hooks for an entity.
type EntityHooksDef struct {
	BeforeCreate []string `yaml:"before_create"`
	AfterCreate  []string `yaml:"after_create"`
	BeforeUpdate []string `yaml:"before_update"`
	AfterUpdate  []string `yaml:"after_update"`
	BeforeDelete []string `yaml:"before_delete"`
	AfterDelete  []string `yaml:"after_delete"`
}

// EndpointFileConfig represents an endpoints YAML file.
type EndpointFileConfig struct {
	Version   string        `yaml:"version"`
	Kind      string        `yaml:"kind"`
	Endpoints []EndpointDef `yaml:"endpoints"`
	FilePath  string        `yaml:"-"`
}

// EndpointDef describes a single endpoint or group of CRUD endpoints.
type EndpointDef struct {
	Path       string           `yaml:"path"`
	Entity     string           `yaml:"entity"`
	CRUD       []string         `yaml:"crud"`
	Method     string           `yaml:"method"`
	Handler    string           `yaml:"handler"`
	Middleware []string         `yaml:"middleware"`
	Auth       *AuthRequirement `yaml:"auth"`
	List       *ListConfig      `yaml:"list"`
	Get        *GetConfig       `yaml:"get"`
	Create     *CreateConfig    `yaml:"create"`
	Update     *UpdateConfig    `yaml:"update"`
	Delete     *DeleteConfig    `yaml:"delete"`
	Spec       *bool            `yaml:"spec"`  // nil = include in all specs; false = exclude
	Specs      *EndpointSpecRef `yaml:"specs"` // optional metadata / per-spec filter
}

// EndpointSpecRef holds per-endpoint OpenAPI documentation overrides.
type EndpointSpecRef struct {
	Names       []string `yaml:"names"`       // restrict to these spec names; empty = all
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
	Summary     string   `yaml:"summary"`
}

// AuthRequirement describes authentication/authorization requirements.
type AuthRequirement struct {
	Require    bool     `yaml:"require"`
	Roles      []string `yaml:"roles"`
	Conditions []string `yaml:"conditions"` // ABAC: CEL-lite expressions evaluated against subject
}

// RowAccessRule is a single rule in a row_access list.
// Rules are evaluated in order; the first matching rule is applied.
// If filter is empty the row set is unrestricted; if no rule matches the request is denied.
type RowAccessRule struct {
	When   string `yaml:"when"`   // condition expression or "*" (always matches)
	Filter string `yaml:"filter"` // SQL fragment with :subject.id/:subject.role/:subject.email; "" = no filter
}

// FieldAccessDef controls per-role read/write access to a single field.
// Omitting either list means no restriction for that direction.
type FieldAccessDef struct {
	ReadRoles  []string `yaml:"read_roles"`  // roles that may read this field
	WriteRoles []string `yaml:"write_roles"` // roles that may write this field on create/update
}

// ListConfig describes list endpoint configuration.
type ListConfig struct {
	AllowFilterBy []string          `yaml:"allow_filter_by"`
	AllowSortBy   []string          `yaml:"allow_sort_by"`
	DefaultSort   string            `yaml:"default_sort"`
	Pagination    *PaginationConfig `yaml:"pagination"`
	Include       []string          `yaml:"include"`
	Auth          *AuthRequirement  `yaml:"auth"`
	RowAccess     []RowAccessRule   `yaml:"row_access"` // ABAC: row-level filter rules
}

// PaginationConfig describes pagination settings.
type PaginationConfig struct {
	Style        string `yaml:"style"`
	DefaultLimit int    `yaml:"default_limit"`
	MaxLimit     int    `yaml:"max_limit"`
}

// GetConfig describes get endpoint configuration.
type GetConfig struct {
	Include   []string        `yaml:"include"`
	Auth      *AuthRequirement `yaml:"auth"`
	RowAccess []RowAccessRule  `yaml:"row_access"` // ABAC: row-level filter rules
}

// CreateConfig describes create endpoint configuration.
type CreateConfig struct {
	Auth        *AuthRequirement `yaml:"auth"`
	BeforeHooks []string         `yaml:"before_hooks"`
	AfterHooks  []string         `yaml:"after_hooks"`
}

// UpdateConfig describes update endpoint configuration.
type UpdateConfig struct {
	AllowedFields []string         `yaml:"allowed_fields"`
	Auth          *AuthRequirement `yaml:"auth"`
	RowAccess     []RowAccessRule  `yaml:"row_access"` // ABAC: row-level filter rules
}

// DeleteConfig describes delete endpoint configuration.
type DeleteConfig struct {
	Auth       *AuthRequirement `yaml:"auth"`
	SoftDelete bool             `yaml:"soft_delete"`
	RowAccess  []RowAccessRule  `yaml:"row_access"` // ABAC: row-level filter rules
}

// JobConfig represents a jobs YAML file.
type JobConfig struct {
	Version string   `yaml:"version"`
	Kind    string   `yaml:"kind"`
	Jobs    []JobDef `yaml:"jobs"`
}

// JobDef describes a single background job.
type JobDef struct {
	Name        string                 `yaml:"name"`
	Description string                 `yaml:"description"`
	Schedule    string                 `yaml:"schedule"`
	Timezone    string                 `yaml:"timezone"`
	Handler     string                 `yaml:"handler"`
	Plugin      string                 `yaml:"plugin"`
	Config      map[string]interface{} `yaml:"config"`
	Retry       *RetryConfig           `yaml:"retry"`
	Timeout     string                 `yaml:"timeout"`
	OnFailure   string                 `yaml:"on_failure"`
}

// RetryConfig describes retry settings for a job.
type RetryConfig struct {
	MaxAttempts  int    `yaml:"max_attempts"`
	Backoff      string `yaml:"backoff"`
	InitialDelay string `yaml:"initial_delay"`
	MaxDelay     string `yaml:"max_delay"`
}

// RoleConfig describes a role in policies/roles.yaml.
type RoleConfig struct {
	Name        string             `yaml:"name"`
	Inherits    []string           `yaml:"inherits"`
	Permissions []PermissionConfig `yaml:"permissions"`
}

// PermissionConfig describes a permission entry for a role.
type PermissionConfig struct {
	Resource string   `yaml:"resource"`
	Actions  []string `yaml:"actions"`
}

// PolicyFileConfig represents a policies YAML file.
type PolicyFileConfig struct {
	Version string       `yaml:"version"`
	Kind    string       `yaml:"kind"`
	Roles   []RoleConfig `yaml:"roles"`
}

// AuthEndpointFileConfig represents a "kind: auth" YAML file.
type AuthEndpointFileConfig struct {
	Version  string         `yaml:"version"`
	Kind     string         `yaml:"kind"`
	Auth     AuthEndpointDef `yaml:"auth"`
	FilePath string         `yaml:"-"`
}

// AuthEndpointDef is the auth block inside an AuthEndpointFileConfig.
type AuthEndpointDef struct {
	BasePath   string          `yaml:"base_path"`   // URL prefix, default: /auth
	UserEntity string          `yaml:"user_entity"` // entity name (e.g. "User")
	Register   *RegisterDef    `yaml:"register"`
	Login      *LoginDef       `yaml:"login"`
	Me         *MeDef          `yaml:"me"`
	OAuth2     *OAuth2Def      `yaml:"oauth2"`
}

// RegisterDef configures the POST /auth/register endpoint.
type RegisterDef struct {
	Enabled         bool   `yaml:"enabled"`
	CredentialField string `yaml:"credential_field"` // entity field used as login ID (e.g. "email")
	PasswordField   string `yaml:"password_field"`   // field sent in the request (e.g. "password") — never stored
	HashField       string `yaml:"hash_field"`       // entity field that stores the bcrypt hash (e.g. "password_hash")
	DefaultRole     string `yaml:"default_role"`     // role assigned to new users (e.g. "member")
}

// LoginDef configures the POST /auth/login endpoint.
type LoginDef struct {
	Enabled         bool   `yaml:"enabled"`
	CredentialField string `yaml:"credential_field"`
	PasswordField   string `yaml:"password_field"`
	HashField       string `yaml:"hash_field"`
}

// MeDef configures the GET /auth/me endpoint.
type MeDef struct {
	Enabled bool `yaml:"enabled"`
}

// OAuth2Def holds OAuth2 provider configurations.
type OAuth2Def struct {
	Providers []OAuth2ProviderDef `yaml:"providers"`
}

// OAuth2ProviderDef configures a single OAuth2 provider.
type OAuth2ProviderDef struct {
	Name            string   `yaml:"name"`             // "google", "github", or custom
	ClientID        string   `yaml:"client_id"`
	ClientSecret    string   `yaml:"client_secret"`
	Scopes          []string `yaml:"scopes"`
	RedirectURI     string   `yaml:"redirect_uri"`     // where the provider sends the auth code
	SuccessRedirect string   `yaml:"success_redirect"` // where to redirect after success (optional)
	ErrorRedirect   string   `yaml:"error_redirect"`   // where to redirect on failure (optional)
	// For custom providers (not needed for "google" or "github"):
	AuthURL     string `yaml:"auth_url"`
	TokenURL    string `yaml:"token_url"`
	UserInfoURL string `yaml:"userinfo_url"`
	// Mapping from provider userinfo JSON to entity fields:
	EmailField      string `yaml:"email_field"`       // default: "email"
	NameField       string `yaml:"name_field"`        // default: "name"
	UsernameField   string `yaml:"username_field"`    // provider field to use as username
}
