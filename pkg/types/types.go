package types

// FieldType represents the type of an entity field.
type FieldType string

const (
	FieldTypeUUID        FieldType = "uuid"
	FieldTypeString      FieldType = "string"
	FieldTypeText        FieldType = "text"
	FieldTypeInteger     FieldType = "integer"
	FieldTypeBigint      FieldType = "bigint"
	FieldTypeFloat       FieldType = "float"
	FieldTypeDecimal     FieldType = "decimal"
	FieldTypeBoolean     FieldType = "boolean"
	FieldTypeTimestamptz FieldType = "timestamptz"
	FieldTypeDate        FieldType = "date"
	FieldTypeJSONB       FieldType = "jsonb"
	FieldTypeEnum        FieldType = "enum"
	FieldTypeArray       FieldType = "array"
	FieldTypeBytea       FieldType = "bytea"
)

// RelationType represents the type of a relationship between entities.
type RelationType string

const (
	RelationBelongsTo  RelationType = "belongs_to"
	RelationHasMany    RelationType = "has_many"
	RelationHasOne     RelationType = "has_one"
	RelationManyToMany RelationType = "many_to_many"
)

// ReferentialAction represents the action taken when a referenced record is updated or deleted.
type ReferentialAction string

const (
	ActionCascade  ReferentialAction = "CASCADE"
	ActionSetNull  ReferentialAction = "SET NULL"
	ActionRestrict ReferentialAction = "RESTRICT"
	ActionNoAction ReferentialAction = "NO ACTION"
)
