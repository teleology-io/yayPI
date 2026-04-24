package schema

import (
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/pkg/types"
)

// BuiltinUserEntityName is the canonical name of the always-present User entity.
const BuiltinUserEntityName = config.BuiltinUserEntityName

// builtinUserFields returns the fixed core fields of the built-in User entity.
// These cannot be overridden by developer-supplied extension fields.
func builtinUserFields() []Field {
	return []Field{
		{
			Name:       "id",
			ColumnName: "id",
			Type:       types.FieldTypeUUID,
			PrimaryKey: true,
			Default:    "gen_random_uuid()",
			Nullable:   false,
		},
		{
			Name:       "email",
			ColumnName: "email",
			Type:       types.FieldTypeString,
			Length:     255,
			Unique:     true,
			Nullable:   false,
			Validate:   &FieldValidation{Required: true, Format: "email"},
		},
		{
			Name:         "password_hash",
			ColumnName:   "password_hash",
			Type:         types.FieldTypeString,
			Length:       255,
			Nullable:     true,
			OmitResponse: true,
			OmitLog:      true,
		},
		{
			Name:       "role",
			ColumnName: "role",
			Type:       types.FieldTypeString,
			Length:     64,
			Nullable:   false,
			Default:    "'member'",
		},
		{
			Name:       "oauth_provider",
			ColumnName: "oauth_provider",
			Type:       types.FieldTypeString,
			Length:     64,
			Nullable:   true,
		},
		{
			Name:       "oauth_id",
			ColumnName: "oauth_id",
			Type:       types.FieldTypeString,
			Length:     256,
			Nullable:   true,
		},
		{
			Name:       "created_at",
			ColumnName: "created_at",
			Type:       types.FieldTypeTimestamptz,
			Default:    "now()",
			Nullable:   false,
		},
		{
			Name:       "updated_at",
			ColumnName: "updated_at",
			Type:       types.FieldTypeTimestamptz,
			Default:    "now()",
			Nullable:   false,
		},
		{
			Name:       "deleted_at",
			ColumnName: "deleted_at",
			Type:       types.FieldTypeTimestamptz,
			Nullable:   true,
		},
	}
}

// NewBuiltinUser constructs the built-in User entity, extended with any developer-supplied
// fields from auth.yaml under auth.user.fields. Built-in field names take precedence;
// extension fields whose column names collide with built-in fields are dropped with a warning.
func NewBuiltinUser(extensions []config.FieldDef) (*Entity, error) {
	base := builtinUserFields()

	reserved := make(map[string]bool, len(base))
	for _, f := range base {
		reserved[f.ColumnName] = true
	}

	fields := make([]Field, len(base))
	copy(fields, base)

	for _, fd := range extensions {
		f, err := buildField(fd)
		if err != nil {
			return nil, fmt.Errorf("user extension field %q: %w", fd.Name, err)
		}
		if reserved[f.ColumnName] {
			log.Warn().Str("field", fd.Name).Msg("user extension field collides with built-in User field; ignoring")
			continue
		}
		fields = append(fields, f)
		reserved[f.ColumnName] = true
	}

	return &Entity{
		Name:       BuiltinUserEntityName,
		Table:      "users",
		SoftDelete: true,
		Timestamps: true,
		Fields:     fields,
		Indexes: []Index{
			{Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
			{Name: "idx_users_oauth", Columns: []string{"oauth_provider", "oauth_id"}},
		},
	}, nil
}
