package schema

import "github.com/teleology-io/yayPI/pkg/types"

// Internal auth tables. They are registered (so migrations create them) whenever the auth
// endpoints are configured, but are marked Internal: never exposed through endpoints or
// OpenAPI. Times are unix seconds (bigint) so comparisons behave identically on every
// dialect, including SQLite where timestamps are stored as text.
const (
	RefreshTokenEntityName = "YaypiRefreshToken"
	RefreshTokenTable      = "yaypi_refresh_tokens"
	AuthTokenEntityName    = "YaypiAuthToken"
	AuthTokenTable         = "yaypi_auth_tokens"
)

func internalStr(name string, length int, nullable bool) Field {
	return Field{Name: name, ColumnName: name, Type: types.FieldTypeString, Length: length, Nullable: nullable}
}

func internalInt(name string, nullable bool) Field {
	return Field{Name: name, ColumnName: name, Type: types.FieldTypeBigint, Nullable: nullable}
}

// NewAuthTokenEntities returns the internal refresh-token and one-time-token entities.
func NewAuthTokenEntities() []*Entity {
	refresh := &Entity{
		Name:     RefreshTokenEntityName,
		Table:    RefreshTokenTable,
		Internal: true,
		Fields: []Field{
			{Name: "id", ColumnName: "id", Type: types.FieldTypeString, Length: 36, PrimaryKey: true},
			internalStr("user_id", 64, false),
			internalStr("family_id", 36, false),
			{Name: "token_hash", ColumnName: "token_hash", Type: types.FieldTypeString, Length: 64, Unique: true},
			internalInt("expires_at", false),
			internalInt("used_at", true),
			internalInt("revoked_at", true),
			internalInt("created_at", false),
		},
		Indexes: []Index{
			{Name: "idx_yaypi_refresh_user", Columns: []string{"user_id"}},
			{Name: "idx_yaypi_refresh_family", Columns: []string{"family_id"}},
		},
	}
	oneTime := &Entity{
		Name:     AuthTokenEntityName,
		Table:    AuthTokenTable,
		Internal: true,
		Fields: []Field{
			{Name: "id", ColumnName: "id", Type: types.FieldTypeString, Length: 36, PrimaryKey: true},
			internalStr("user_id", 64, false),
			internalStr("purpose", 16, false),
			{Name: "token_hash", ColumnName: "token_hash", Type: types.FieldTypeString, Length: 64, Unique: true},
			internalInt("expires_at", false),
			internalInt("used_at", true),
			internalInt("created_at", false),
		},
		Indexes: []Index{
			{Name: "idx_yaypi_auth_tokens_user", Columns: []string{"user_id", "purpose"}},
		},
	}
	return []*Entity{refresh, oneTime}
}

// Idempotency-key store for POST create requests.
const (
	IdempotencyEntityName = "YaypiIdempotencyKey"
	IdempotencyTable      = "yaypi_idempotency_keys"
)

// NewIdempotencyEntity returns the internal table backing the Idempotency-Key header.
func NewIdempotencyEntity() *Entity {
	return &Entity{
		Name:     IdempotencyEntityName,
		Table:    IdempotencyTable,
		Internal: true,
		Fields: []Field{
			{Name: "key_hash", ColumnName: "key_hash", Type: types.FieldTypeString, Length: 64, PrimaryKey: true},
			internalStr("fingerprint", 64, false),
			{Name: "status", ColumnName: "status", Type: types.FieldTypeInteger, Default: "0"},
			{Name: "body", ColumnName: "body", Type: types.FieldTypeText, Nullable: true},
			internalInt("created_at", false),
		},
		Indexes: []Index{{Name: "idx_yaypi_idempotency_created", Columns: []string{"created_at"}}},
	}
}

// Distributed cron lock table.
const (
	JobLockEntityName = "YaypiJobLock"
	JobLockTable      = "yaypi_job_locks"
)

// NewJobLockEntity returns the internal table backing cron's distributed lock.
func NewJobLockEntity() *Entity {
	return &Entity{
		Name:     JobLockEntityName,
		Table:    JobLockTable,
		Internal: true,
		Fields: []Field{
			{Name: "job_name", ColumnName: "job_name", Type: types.FieldTypeString, Length: 191, PrimaryKey: true},
			internalStr("owner", 128, false),
			internalInt("locked_until", false),
		},
	}
}

// Transactional outbox for webhooks and emails.
const (
	OutboxEntityName = "YaypiOutbox"
	OutboxTable      = "yaypi_outbox"
)

// NewOutboxEntity returns the internal outbox table: messages are written in the same
// transaction as the entity change and delivered (with retries) by a background worker.
func NewOutboxEntity() *Entity {
	return &Entity{
		Name:     OutboxEntityName,
		Table:    OutboxTable,
		Internal: true,
		Fields: []Field{
			{Name: "id", ColumnName: "id", Type: types.FieldTypeString, Length: 36, PrimaryKey: true},
			internalStr("kind", 16, false), // webhook | email
			internalStr("name", 191, false),
			{Name: "payload", ColumnName: "payload", Type: types.FieldTypeText},
			internalStr("status", 16, false), // pending | done | dead
			{Name: "attempts", ColumnName: "attempts", Type: types.FieldTypeInteger, Default: "0"},
			internalInt("next_attempt_at", false),
			internalInt("locked_until", false),
			{Name: "last_error", ColumnName: "last_error", Type: types.FieldTypeText, Nullable: true},
			internalInt("created_at", false),
		},
		Indexes: []Index{{Name: "idx_yaypi_outbox_due", Columns: []string{"status", "next_attempt_at"}}},
	}
}

// Audit log.
const (
	AuditEntityName = "YaypiAuditLog"
	AuditTable      = "yaypi_audit_log"
)

// NewAuditEntity returns the internal audit log table.
func NewAuditEntity() *Entity {
	return &Entity{
		Name:     AuditEntityName,
		Table:    AuditTable,
		Internal: true,
		Fields: []Field{
			{Name: "id", ColumnName: "id", Type: types.FieldTypeString, Length: 36, PrimaryKey: true},
			internalStr("entity", 128, false),
			internalStr("record_id", 191, false),
			internalStr("action", 16, false),
			internalStr("actor_id", 191, true),
			internalStr("actor_role", 64, true),
			internalStr("request_id", 128, true),
			{Name: "changes", ColumnName: "changes", Type: types.FieldTypeText},
			internalInt("created_at", false),
		},
		Indexes: []Index{
			{Name: "idx_yaypi_audit_record", Columns: []string{"entity", "record_id"}},
			{Name: "idx_yaypi_audit_created", Columns: []string{"created_at"}},
		},
	}
}
