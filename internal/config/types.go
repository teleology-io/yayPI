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
	// Loaded from included files:
	Entities  []*EntityConfig      `yaml:"-"`
	Endpoints []*EndpointFileConfig `yaml:"-"`
	Jobs      []*JobConfig         `yaml:"-"`
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
}

// AuthRequirement describes authentication/authorization requirements.
type AuthRequirement struct {
	Require bool     `yaml:"require"`
	Roles   []string `yaml:"roles"`
}

// ListConfig describes list endpoint configuration.
type ListConfig struct {
	AllowFilterBy []string          `yaml:"allow_filter_by"`
	AllowSortBy   []string          `yaml:"allow_sort_by"`
	DefaultSort   string            `yaml:"default_sort"`
	Pagination    *PaginationConfig `yaml:"pagination"`
	Include       []string          `yaml:"include"`
	Auth          *AuthRequirement  `yaml:"auth"`
}

// PaginationConfig describes pagination settings.
type PaginationConfig struct {
	Style        string `yaml:"style"`
	DefaultLimit int    `yaml:"default_limit"`
	MaxLimit     int    `yaml:"max_limit"`
}

// GetConfig describes get endpoint configuration.
type GetConfig struct {
	Include []string         `yaml:"include"`
	Auth    *AuthRequirement `yaml:"auth"`
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
}

// DeleteConfig describes delete endpoint configuration.
type DeleteConfig struct {
	Auth       *AuthRequirement `yaml:"auth"`
	SoftDelete bool             `yaml:"soft_delete"`
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
