package config

import "time"

// BuiltinUserEntityName is the canonical name of the always-present User entity.
// Defined here (in config) to avoid an import cycle with the schema package.
const BuiltinUserEntityName = "User"

// RootConfig is the top-level configuration structure loaded from yaypi.yaml.
type RootConfig struct {
	Version     string         `yaml:"version"`
	Project     ProjectConfig  `yaml:"project"`
	Server      ServerConfig   `yaml:"server"`
	Databases   []DBConfig     `yaml:"databases"`
	Auth        AuthConfig     `yaml:"auth"`
	Policy      PolicyConfig   `yaml:"policy"`
	AutoMigrate bool           `yaml:"auto_migrate"`
	Plugins     []PluginConfig `yaml:"plugins"`
	Include     []string       `yaml:"include"`
	Specs       []SpecConfig   `yaml:"spec"`
	Log         LogConfig      `yaml:"log"`
	SMTP        *SMTPConfig    `yaml:"smtp"`
	Outbox      OutboxConfig   `yaml:"outbox"`
	Cron        CronConfig     `yaml:"cron"`
	Audit       AuditConfig    `yaml:"audit"`
	Metrics     *MetricsConfig `yaml:"metrics"`
	// Loaded from included files:
	Entities     []*EntityConfig         `yaml:"-"`
	Endpoints    []*EndpointFileConfig   `yaml:"-"`
	Jobs         []*JobConfig            `yaml:"-"`
	SeedFiles    []*SeedFileConfig       `yaml:"-"`
	EmailFiles   []*EmailFileConfig      `yaml:"-"`
	WebhookFiles []*WebhookFileConfig    `yaml:"-"`
	AuthEndpoint *AuthEndpointFileConfig `yaml:"-"`
}

// SMTPConfig configures outgoing email (entity email triggers, password reset, email
// verification). Any field left empty falls back to the matching SMTP_* env var
// (SMTP_HOST, SMTP_PORT, SMTP_USER, SMTP_PASS, SMTP_SENDER_NAME, SMTP_SENDER_EMAIL).
type SMTPConfig struct {
	Host      string `yaml:"host"`
	Port      int    `yaml:"port"` // default 587
	Username  string `yaml:"username"`
	Password  string `yaml:"password"` // use ${ENV_VAR}
	FromName  string `yaml:"from_name"`
	FromEmail string `yaml:"from_email"`
	// Retry is the default delivery retry policy for entity email triggers (emails[].retry
	// overrides it). Default: 5 attempts, exponential from 10s up to 1h.
	Retry *RetryConfig `yaml:"retry"`
}

// OutboxConfig tunes webhook/email delivery.
type OutboxConfig struct {
	PollInterval string `yaml:"poll_interval"` // how often due messages are picked up (default 1s)
	Retention    string `yaml:"retention"`     // delivered messages are deleted after this (default 7d)
}

// CronConfig controls job scheduling across replicas.
type CronConfig struct {
	// Distributed (default true when a database is configured) coordinates runs through
	// the yaypi_job_locks table so each tick executes on one replica. Set false for
	// single-instance deployments that should not touch the lock table.
	Distributed *bool `yaml:"distributed"`
}

// AuditConfig controls the audit log (entities with audit: true).
type AuditConfig struct {
	Retention string `yaml:"retention"` // delete rows older than this, e.g. 365d (default: keep forever)
}

// LogConfig controls process logging.
type LogConfig struct {
	Level  string `yaml:"level"`  // debug | info (default) | warn | error
	Format string `yaml:"format"` // json | console (default: console on a terminal, json otherwise)
}

// MetricsConfig exposes Prometheus metrics.
type MetricsConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`  // default: /metrics (mounted outside base_url, like /health)
	Token   string `yaml:"token"` // optional bearer token required to scrape; use ${ENV_VAR}
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
	Port               int              `yaml:"port"`
	ReadTimeout        time.Duration    `yaml:"read_timeout"`
	WriteTimeout       time.Duration    `yaml:"write_timeout"`
	ShutdownTimeout    time.Duration    `yaml:"shutdown_timeout"`
	ReadHeaderTimeout  time.Duration    `yaml:"read_header_timeout"` // default: 10s
	IdleTimeout        time.Duration    `yaml:"idle_timeout"`        // default: 120s
	MaxRequestBodySize string           `yaml:"max_request_body_size"`
	MaxHeaderBytes     string           `yaml:"max_header_bytes"`
	TLS                *TLSConfig       `yaml:"tls"`
	AllowedOrigins     []string         `yaml:"allowed_origins"`
	CORS               *CORSConfig      `yaml:"cors"`
	Health             *HealthConfig    `yaml:"health"`
	RateLimit          *RateLimitConfig `yaml:"rate_limit"`
	// TrustedProxies lists CIDRs/IPs of reverse proxies whose X-Forwarded-For and
	// X-Real-IP headers are believed. Empty = use the TCP peer address only.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// StrictStartup (default true) makes any degraded subsystem at boot — cron jobs that
	// fail to register, OpenAPI build errors, email triggers without SMTP — stop the
	// process instead of logging a warning. Security-relevant failures (auth, policy,
	// migrations) are always fatal regardless.
	StrictStartup *bool `yaml:"strict_startup"`
	// SecurityHeaders (default true) adds nosniff / frame-deny / no-referrer / CSP headers.
	SecurityHeaders *bool `yaml:"security_headers"`
	// HSTSMaxAge > 0 adds Strict-Transport-Security. Set it (e.g. 31536000) when the API
	// is only served over HTTPS, including behind a TLS-terminating proxy.
	HSTSMaxAge int `yaml:"hsts_max_age"`
	// DrainDelay is how long to keep serving after SIGTERM while /ready reports draining,
	// so load balancers stop sending traffic before connections close (e.g. 5s on k8s).
	DrainDelay time.Duration `yaml:"drain_delay"`
	// IdempotencyTTL is how long an Idempotency-Key response is replayed (default 24h).
	IdempotencyTTL string `yaml:"idempotency_ttl"`
	// RequestTimeout bounds each request's context (and so every query it runs);
	// default: write_timeout. Exceeding it returns 504.
	RequestTimeout time.Duration `yaml:"request_timeout"`
}

// CORSConfig tunes CORS beyond server.allowed_origins.
type CORSConfig struct {
	AllowedHeaders []string `yaml:"allowed_headers"` // default: Authorization, Content-Type, Accept, X-API-Key, X-Request-ID, If-Match, Idempotency-Key
	AllowedMethods []string `yaml:"allowed_methods"` // default: GET, POST, PUT, PATCH, DELETE, OPTIONS
	ExposedHeaders []string `yaml:"exposed_headers"` // response headers readable by browser JS
	MaxAge         int      `yaml:"max_age"`         // preflight cache seconds (default: 600)
}

// TLSConfig holds TLS certificate settings.
type TLSConfig struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// HealthConfig holds health/readiness endpoint settings.
type HealthConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Path          string `yaml:"path"`           // default: /health
	ReadinessPath string `yaml:"readiness_path"` // default: /ready
}

// RateLimitConfig holds global rate limiting settings.
type RateLimitConfig struct {
	RequestsPerMinute int    `yaml:"requests_per_minute"`
	Burst             int    `yaml:"burst"`
	KeyBy             string `yaml:"key_by"` // ip | user (default: ip)
}

// DBConfig holds database connection settings.
type DBConfig struct {
	Name            string        `yaml:"name"`
	Driver          string        `yaml:"driver"`
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`     // default: 25
	MaxIdleConns    int           `yaml:"max_idle_conns"`     // default: max_open_conns
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`  // default: 30m
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"` // default: 5m
	Default         bool          `yaml:"default"`
	ReadOnly        bool          `yaml:"read_only"`
	Schema          string        `yaml:"schema"`
}

// AuthConfig holds JWT authentication settings.
type AuthConfig struct {
	Provider         string   `yaml:"provider"`
	Secret           string   `yaml:"secret"`            // HMAC key for HS* algorithms (>= 32 bytes)
	Expiry           string   `yaml:"expiry"`            // access token TTL (default: 15m with refresh enabled, else 1h)
	Algorithm        string   `yaml:"algorithm"`         // HS256 (default) | HS384 | HS512 | RS256 | RS384 | RS512 | ES256 | ES384 | ES512
	RejectAlgorithms []string `yaml:"reject_algorithms"` // deprecated: only the configured algorithm is ever accepted
	PrivateKeyFile   string   `yaml:"private_key_file"`  // PEM key for RS*/ES* signing
	PublicKeyFile    string   `yaml:"public_key_file"`   // PEM key for RS*/ES* verification (derived from private key if unset)
	KeyID            string   `yaml:"key_id"`            // "kid" header and JWKS key id
	Issuer           string   `yaml:"issuer"`            // "iss" on issued tokens (default: project.name); verified only when set
	Audience         string   `yaml:"audience"`          // "aud" claim; verified when set
	// RevocationCheck re-reads the user on every authenticated request so deleted users,
	// role changes, logout-all and password resets take effect immediately instead of at
	// token expiry. Costs one indexed query per request.
	RevocationCheck bool          `yaml:"revocation_check"`
	APIKeys         *APIKeyConfig `yaml:"api_keys"`
}

// APIKeyConfig holds API key authentication settings.
type APIKeyConfig struct {
	Header     string         `yaml:"header"`      // default: X-API-Key
	QueryParam string         `yaml:"query_param"` // optional: also accept ?<param>=
	Keys       []StaticAPIKey `yaml:"keys"`        // static key list
	// DB-backed alternative (used when Keys is empty)
	Entity       string `yaml:"entity"`        // entity name in registry
	KeyField     string `yaml:"key_field"`     // column holding the key digest, default: token
	RoleField    string `yaml:"role_field"`    // column holding the role, default: role
	SubjectField string `yaml:"subject_field"` // column used as subject id (default: user_id if present, else the PK)
	KeyHash      string `yaml:"key_hash"`      // sha256 (default; column stores hex digest) | plain (legacy)
}

// StaticAPIKey is a single hard-coded API key entry.
type StaticAPIKey struct {
	Key  string `yaml:"key"`
	Role string `yaml:"role"`
	Name string `yaml:"name"` // subject id for this key (default: apikey:<index>)
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
	// Audit records every create/update/delete (who, what, field-level diff) in the
	// yaypi_audit_log table, in the same transaction as the change. omit_log fields are
	// redacted.
	Audit bool `yaml:"audit"`
	// TenantScoped isolates rows per tenant: every query is restricted to
	// tenant_field = the caller's "tenant" token claim, and the field is set from it on
	// create. Requests without a tenant claim are refused.
	TenantScoped bool   `yaml:"tenant_scoped"`
	TenantField  string `yaml:"tenant_field"` // default: tenant_id
}

// FieldDef describes a single field on an entity.
type FieldDef struct {
	Name          string            `yaml:"name"`
	Type          string            `yaml:"type"`
	Length        int               `yaml:"length"`
	Precision     int               `yaml:"precision"`
	Scale         int               `yaml:"scale"`
	Nullable      bool              `yaml:"nullable"`
	Unique        bool              `yaml:"unique"`
	PrimaryKey    bool              `yaml:"primary_key"`
	Default       string            `yaml:"default"`
	Index         bool              `yaml:"index"`
	Immutable     bool              `yaml:"immutable"` // stripped from PATCH body; accepted on create only
	Values        []string          `yaml:"values"`    // for enum
	References    *ReferenceDef     `yaml:"references"`
	Serialization SerializationDef  `yaml:"serialization"`
	Access        *FieldAccessDef   `yaml:"access"`   // ABAC: per-role read/write access
	Validate      *FieldValidateDef `yaml:"validate"` // input validation rules
	// DefaultFrom sets the field from the authenticated caller on create, ignoring any
	// client value, and makes it read-only on update. Values: subject.id, subject.email,
	// subject.role. Typical use: an owner column (author_id: default_from: subject.id).
	DefaultFrom string `yaml:"default_from"`
}

// FieldValidateDef holds validation rules for a field.
type FieldValidateDef struct {
	Required  bool     `yaml:"required"`
	MinLength int      `yaml:"min_length"`
	MaxLength int      `yaml:"max_length"`
	Min       *float64 `yaml:"min"`
	Max       *float64 `yaml:"max"`
	Pattern   string   `yaml:"pattern"`
	Format    string   `yaml:"format"`  // email, url, uuid, slug
	Message   string   `yaml:"message"` // custom error message override
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
	RateLimit  *RateLimitConfig `yaml:"rate_limit"` // per-endpoint rate limit (applies in addition to global)
	// MaxBodySize overrides server.max_request_body_size for this endpoint (e.g. "64MB"
	// for an upload route).
	MaxBodySize string           `yaml:"max_body_size"`
	List        *ListConfig      `yaml:"list"`
	Get         *GetConfig       `yaml:"get"`
	Create      *CreateConfig    `yaml:"create"`
	Update      *UpdateConfig    `yaml:"update"`
	Delete      *DeleteConfig    `yaml:"delete"`
	Spec        *bool            `yaml:"spec"`  // nil = include in all specs; false = exclude
	Specs       *EndpointSpecRef `yaml:"specs"` // optional metadata / per-spec filter
}

// EndpointSpecRef holds per-endpoint OpenAPI documentation overrides.
type EndpointSpecRef struct {
	Names       []string `yaml:"names"` // restrict to these spec names; empty = all
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
	Search        []string          `yaml:"search"` // text fields matched (case-insensitive substring) by ?q=
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
	IncludeTotal bool   `yaml:"include_total"` // offset style: run COUNT(*) query
}

// GetConfig describes get endpoint configuration.
type GetConfig struct {
	Include   []string         `yaml:"include"`
	Auth      *AuthRequirement `yaml:"auth"`
	RowAccess []RowAccessRule  `yaml:"row_access"` // ABAC: row-level filter rules
}

// CreateConfig describes create endpoint configuration.
type CreateConfig struct {
	Auth          *AuthRequirement `yaml:"auth"`
	BeforeHooks   []string         `yaml:"before_hooks"`
	AfterHooks    []string         `yaml:"after_hooks"`
	Bulk          bool             `yaml:"bulk"`            // accept array body
	BulkMax       int              `yaml:"bulk_max"`        // default: 500
	BulkErrorMode string           `yaml:"bulk_error_mode"` // abort | partial (default: abort)
	Strict        bool             `yaml:"strict"`          // reject unknown body fields with 400 (default: drop them)
}

// UpdateConfig describes update endpoint configuration.
type UpdateConfig struct {
	AllowedFields []string         `yaml:"allowed_fields"`
	Strict        bool             `yaml:"strict"` // reject unknown body fields with 400 (default: drop them)
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
	Version  string          `yaml:"version"`
	Kind     string          `yaml:"kind"`
	Auth     AuthEndpointDef `yaml:"auth"`
	FilePath string          `yaml:"-"`
}

// UserExtensionDef allows extending the built-in User entity with custom fields.
type UserExtensionDef struct {
	Fields []FieldDef `yaml:"fields"`
}

// AuthEndpointDef is the auth block inside an AuthEndpointFileConfig.
type AuthEndpointDef struct {
	BasePath   string            `yaml:"base_path"`   // URL prefix, default: /auth
	UserEntity string            `yaml:"user_entity"` // deprecated: built-in User is always registered
	User       *UserExtensionDef `yaml:"user"`        // optional: extend built-in User with custom fields
	Register   *RegisterDef      `yaml:"register"`
	Login      *LoginDef         `yaml:"login"`
	Me         *MeDef            `yaml:"me"`
	Refresh    *RefreshDef       `yaml:"refresh"`
	OAuth2     *OAuth2Def        `yaml:"oauth2"`
	Cookie     *CookieDef        `yaml:"cookie"`

	PasswordReset     *PasswordResetDef     `yaml:"password_reset"`
	EmailVerification *EmailVerificationDef `yaml:"email_verification"`
}

// RegisterDef configures the POST /auth/register endpoint.
type RegisterDef struct {
	Enabled           bool   `yaml:"enabled"`
	CredentialField   string `yaml:"credential_field"`    // entity field used as login ID (e.g. "email")
	PasswordField     string `yaml:"password_field"`      // field sent in the request (e.g. "password") — never stored
	HashField         string `yaml:"hash_field"`          // entity field that stores the bcrypt hash (e.g. "password_hash")
	DefaultRole       string `yaml:"default_role"`        // role assigned to every new user (callers can never choose a role)
	MinPasswordLength int    `yaml:"min_password_length"` // default: 8 (max is always 72 bytes — the bcrypt limit)
}

// LoginDef configures the POST /auth/login endpoint.
type LoginDef struct {
	Enabled         bool   `yaml:"enabled"`
	CredentialField string `yaml:"credential_field"`
	PasswordField   string `yaml:"password_field"`
	HashField       string `yaml:"hash_field"`
	// Brute-force protection: after MaxAttempts failed logins for one credential (or
	// 4x that from one IP) within LockoutWindow, further attempts get 429 until the
	// window passes. Per instance (in memory).
	MaxAttempts   int    `yaml:"max_attempts"`   // default: 5; -1 disables
	LockoutWindow string `yaml:"lockout_window"` // default: 15m
}

// CookieDef configures auth cookies (refresh token, OAuth state).
type CookieDef struct {
	Secure   *bool  `yaml:"secure"`    // default: true — set false only for local http development
	SameSite string `yaml:"same_site"` // lax (default) | strict | none
	Domain   string `yaml:"domain"`
}

// PasswordResetDef configures POST /auth/password/forgot and /auth/password/reset.
type PasswordResetDef struct {
	Enabled  bool   `yaml:"enabled"`
	Expiry   string `yaml:"expiry"`    // reset token TTL (default: 1h)
	ResetURL string `yaml:"reset_url"` // link sent by email; {{token}} is replaced, e.g. https://app/reset?token={{token}}
	Subject  string `yaml:"subject"`
	Body     string `yaml:"body"` // HTML; {{link}} is replaced with the reset URL
}

// EmailVerificationDef configures email verification.
type EmailVerificationDef struct {
	Enabled   bool   `yaml:"enabled"`
	Required  bool   `yaml:"required"`   // block password login until verified
	Expiry    string `yaml:"expiry"`     // verification token TTL (default: 24h)
	VerifyURL string `yaml:"verify_url"` // link sent by email; {{token}} is replaced
	Subject   string `yaml:"subject"`
	Body      string `yaml:"body"` // HTML; {{link}} is replaced with the verify URL
}

// MeDef configures the GET /auth/me endpoint.
type MeDef struct {
	Enabled bool `yaml:"enabled"`
}

// RefreshDef configures the POST /auth/refresh endpoint.
type RefreshDef struct {
	Enabled bool   `yaml:"enabled"`
	Expiry  string `yaml:"expiry"` // refresh token TTL, e.g. "30d" (default: 30d)
	Store   string `yaml:"store"`  // cookie | body (default: cookie)
}

// OAuth2Def holds OAuth2 provider configurations.
type OAuth2Def struct {
	Providers []OAuth2ProviderDef `yaml:"providers"`
}

// EmailFileConfig represents a "kind: email" YAML file.
type EmailFileConfig struct {
	Version  string     `yaml:"version"`
	Kind     string     `yaml:"kind"`
	Emails   []EmailDef `yaml:"emails"`
	FilePath string     `yaml:"-"`
}

// EmailDef defines a single email trigger.
type EmailDef struct {
	Name    string `yaml:"name"`
	Entity  string `yaml:"entity"`
	Trigger string `yaml:"trigger"` // after_create | after_update | after_delete
	// Condition is an optional CEL-lite expression; if set, email only fires when it evaluates true.
	// Supported bindings: record.<field>
	Condition string       `yaml:"condition"`
	To        string       `yaml:"to"`      // may contain {{record.field}}
	Retry     *RetryConfig `yaml:"retry"`   // overrides smtp.retry for this email
	Subject   string       `yaml:"subject"` // may contain {{record.field}} and ${ENV_VAR}
	Body      string       `yaml:"body"`    // HTML; may contain {{record.field}} and ${ENV_VAR}
}

// WebhookFileConfig represents a "kind: webhooks" YAML file.
type WebhookFileConfig struct {
	Version  string       `yaml:"version"`
	Kind     string       `yaml:"kind"`
	Webhooks []WebhookDef `yaml:"webhooks"`
	FilePath string       `yaml:"-"`
}

// WebhookDef defines a single outbound webhook trigger.
type WebhookDef struct {
	Name      string            `yaml:"name"`
	Entity    string            `yaml:"entity"`
	Trigger   string            `yaml:"trigger"` // after_create | after_update | after_delete
	Condition string            `yaml:"condition"`
	URL       string            `yaml:"url"`
	Method    string            `yaml:"method"` // default: POST
	Headers   map[string]string `yaml:"headers"`
	Payload   string            `yaml:"payload"` // JSON template with {{record.field}}
	Timeout   string            `yaml:"timeout"` // default: 5s
	Retry     *RetryConfig      `yaml:"retry"`
	// Secret signs every delivery: X-Yaypi-Signature: v1=<hex HMAC-SHA256(secret,
	// "<X-Yaypi-Timestamp>.<body>")>. Use ${ENV_VAR}.
	Secret string `yaml:"secret"`
	// AllowPrivateNetwork permits delivery to private/loopback/link-local addresses.
	// Off by default: webhook URLs can be templated from record data (SSRF risk).
	AllowPrivateNetwork bool `yaml:"allow_private_network"`
}

// SeedFileConfig represents a "kind: seed" YAML file.
type SeedFileConfig struct {
	Version  string    `yaml:"version"`
	Kind     string    `yaml:"kind"`
	Seeds    []SeedDef `yaml:"seeds"`
	FilePath string    `yaml:"-"`
}

// SeedDef defines a set of rows to insert into an entity table.
type SeedDef struct {
	Entity   string                   `yaml:"entity"`
	KeyField string                   `yaml:"key_field"` // field used to detect existing rows
	Data     []map[string]interface{} `yaml:"data"`
}

// OAuth2ProviderDef configures a single OAuth2 provider.
type OAuth2ProviderDef struct {
	Name            string   `yaml:"name"` // "google", "github", or custom
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
	EmailField    string `yaml:"email_field"`    // default: "email"
	NameField     string `yaml:"name_field"`     // default: "name"
	UsernameField string `yaml:"username_field"` // provider field to use as username
	IDField       string `yaml:"id_field"`       // stable provider user id (default: "id"; "sub" for OIDC)
	// EmailVerifiedField names the boolean userinfo field asserting the email is verified
	// (default: "email_verified"; Google v2 uses "verified_email"; GitHub is checked via
	// /user/emails). Unverified emails are never linked to or used to create accounts.
	EmailVerifiedField string `yaml:"email_verified_field"`
	// TrustEmail skips the verified check — only for providers that guarantee verified
	// addresses (e.g. a corporate IdP).
	TrustEmail bool  `yaml:"trust_email"`
	PKCE       *bool `yaml:"pkce"` // default: true (S256)
	// TokenDelivery controls how the token reaches success_redirect: "fragment" (default,
	// #token=… never hits server logs or Referer) or "query" (legacy).
	TokenDelivery string `yaml:"token_delivery"`
}
