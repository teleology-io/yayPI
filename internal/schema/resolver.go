package schema

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/pkg/types"
)

// buildEntity converts a config.EntityConfig to a schema.Entity.
func buildEntity(ec *config.EntityConfig) (*Entity, error) {
	def := ec.Entity

	table := def.Table
	if table == "" {
		table = toSnakeCase(def.Name) + "s"
	}

	entity := &Entity{
		Name:       def.Name,
		Table:      table,
		Database:   def.Database,
		SoftDelete: def.SoftDelete,
		Timestamps: def.Timestamps,
	}

	// Build fields
	for _, fd := range def.Fields {
		field, err := buildField(fd)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", fd.Name, err)
		}
		entity.Fields = append(entity.Fields, field)
	}

	// Add timestamp fields if enabled
	if def.Timestamps {
		entity.Fields = append(entity.Fields,
			Field{Name: "created_at", ColumnName: "created_at", Type: types.FieldTypeTimestamptz, Default: "now()", Nullable: false},
			Field{Name: "updated_at", ColumnName: "updated_at", Type: types.FieldTypeTimestamptz, Default: "now()", Nullable: false},
		)
	}

	// Add soft delete field if enabled
	if def.SoftDelete {
		entity.Fields = append(entity.Fields,
			Field{Name: "deleted_at", ColumnName: "deleted_at", Type: types.FieldTypeTimestamptz, Nullable: true},
		)
	}

	// Build relations
	for _, rd := range def.Relations {
		rel, err := buildRelation(rd)
		if err != nil {
			return nil, fmt.Errorf("relation %q: %w", rd.Name, err)
		}
		entity.Relations = append(entity.Relations, rel)
	}

	// Build indexes
	for _, id := range def.Indexes {
		entity.Indexes = append(entity.Indexes, Index{
			Name:    id.Name,
			Columns: id.Columns,
			Unique:  id.Unique,
			Type:    id.Type,
		})
	}

	// Build constraints
	for _, cd := range def.Constraints {
		entity.Constraints = append(entity.Constraints, Constraint{
			Name:    cd.Name,
			Type:    cd.Type,
			Check:   cd.Check,
			Columns: cd.Columns,
		})
	}

	// Build hooks
	entity.Hooks = EntityHooks{
		BeforeCreate: def.Hooks.BeforeCreate,
		AfterCreate:  def.Hooks.AfterCreate,
		BeforeUpdate: def.Hooks.BeforeUpdate,
		AfterUpdate:  def.Hooks.AfterUpdate,
		BeforeDelete: def.Hooks.BeforeDelete,
		AfterDelete:  def.Hooks.AfterDelete,
	}

	return entity, nil
}

// buildField converts a config.FieldDef to a schema.Field.
func buildField(fd config.FieldDef) (Field, error) {
	ft, err := resolveFieldType(fd.Type)
	if err != nil {
		return Field{}, err
	}

	colName := fd.Name
	if colName == "" {
		return Field{}, fmt.Errorf("field name is required")
	}
	// Field name may already be snake_case; ensure it is.
	colName = toSnakeCase(colName)

	f := Field{
		Name:         fd.Name,
		ColumnName:   colName,
		Type:         ft,
		Nullable:     fd.Nullable,
		Unique:       fd.Unique,
		PrimaryKey:   fd.PrimaryKey,
		Default:      fd.Default,
		OmitResponse: fd.Serialization.OmitResponse,
		OmitLog:      fd.Serialization.OmitLog,
		EnumValues:   fd.Values,
		Length:       fd.Length,
		Precision:    fd.Precision,
		Scale:        fd.Scale,
		Index:        fd.Index,
		Immutable:    fd.Immutable,
	}

	if fd.References != nil {
		f.Reference = &Reference{
			Entity:   fd.References.Entity,
			Field:    fd.References.Field,
			OnDelete: resolveReferentialAction(fd.References.OnDelete),
			OnUpdate: resolveReferentialAction(fd.References.OnUpdate),
		}
	}

	if fd.Access != nil {
		f.ReadRoles = fd.Access.ReadRoles
		f.WriteRoles = fd.Access.WriteRoles
	}

	if fd.Validate != nil {
		f.Validate = &FieldValidation{
			Required:  fd.Validate.Required,
			MinLength: fd.Validate.MinLength,
			MaxLength: fd.Validate.MaxLength,
			Min:       fd.Validate.Min,
			Max:       fd.Validate.Max,
			Pattern:   fd.Validate.Pattern,
			Format:    fd.Validate.Format,
			Message:   fd.Validate.Message,
		}
	}

	return f, nil
}

// buildRelation converts a config.RelationDef to a schema.Relation.
func buildRelation(rd config.RelationDef) (Relation, error) {
	rt, err := resolveRelationType(rd.Type)
	if err != nil {
		return Relation{}, err
	}
	return Relation{
		Name:       rd.Name,
		Type:       rt,
		Entity:     rd.Entity,
		ForeignKey: rd.ForeignKey,
		Through:    rd.Through,
		OtherKey:   rd.OtherKey,
	}, nil
}

// resolveFieldType converts a string to a FieldType.
func resolveFieldType(s string) (types.FieldType, error) {
	switch types.FieldType(strings.ToLower(s)) {
	case types.FieldTypeUUID, types.FieldTypeString, types.FieldTypeText,
		types.FieldTypeInteger, types.FieldTypeBigint, types.FieldTypeFloat,
		types.FieldTypeDecimal, types.FieldTypeBoolean, types.FieldTypeTimestamptz,
		types.FieldTypeDate, types.FieldTypeJSONB, types.FieldTypeEnum,
		types.FieldTypeArray, types.FieldTypeBytea:
		return types.FieldType(strings.ToLower(s)), nil
	default:
		return "", fmt.Errorf("unknown field type %q", s)
	}
}

// resolveRelationType converts a string to a RelationType.
func resolveRelationType(s string) (types.RelationType, error) {
	switch types.RelationType(strings.ToLower(s)) {
	case types.RelationBelongsTo, types.RelationHasMany, types.RelationHasOne, types.RelationManyToMany:
		return types.RelationType(strings.ToLower(s)), nil
	default:
		return "", fmt.Errorf("unknown relation type %q", s)
	}
}

// resolveReferentialAction converts a string to a ReferentialAction.
func resolveReferentialAction(s string) types.ReferentialAction {
	switch types.ReferentialAction(strings.ToUpper(s)) {
	case types.ActionCascade, types.ActionSetNull, types.ActionRestrict, types.ActionNoAction:
		return types.ReferentialAction(strings.ToUpper(s))
	default:
		if s == "" {
			return types.ActionNoAction
		}
		return types.ActionNoAction
	}
}

// toSnakeCase converts a CamelCase or PascalCase string to snake_case.
func toSnakeCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteRune('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
