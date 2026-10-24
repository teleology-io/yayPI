package openapi

import (
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

// fieldSchemaFor is fieldSchema plus the field's configured time format: RFC 3339
// stays date-time, unix formats are integers, patterns are strings described by
// their pattern.
func fieldSchemaFor(f *schema.Field) *Schema {
	s := fieldSchema(f.Type, f.EnumValues)
	if f.Time == nil {
		return s
	}
	switch {
	case f.Time.Numeric():
		return &Schema{Type: "integer", Format: "int64", Description: f.Time.Describe()}
	case f.Type == types.FieldTypeTimestamptz && f.Time.IsRFC3339():
	case f.Type == types.FieldTypeDate && f.Time.Spec == "date":
	default:
		return &Schema{Type: "string", Description: "Format: " + f.Time.Spec}
	}
	return s
}

// fieldSchema returns an OpenAPI Schema for the given field type and metadata.
func fieldSchema(ft types.FieldType, enumValues []string) *Schema {
	switch ft {
	case types.FieldTypeUUID:
		return &Schema{Type: "string", Format: "uuid"}
	case types.FieldTypeString:
		return &Schema{Type: "string"}
	case types.FieldTypeText:
		return &Schema{Type: "string"}
	case types.FieldTypeInteger:
		return &Schema{Type: "integer", Format: "int32"}
	case types.FieldTypeBigint:
		return &Schema{Type: "integer", Format: "int64"}
	case types.FieldTypeFloat:
		return &Schema{Type: "number", Format: "float"}
	case types.FieldTypeDecimal:
		return &Schema{Type: "number", Format: "double"}
	case types.FieldTypeBoolean:
		return &Schema{Type: "boolean"}
	case types.FieldTypeTimestamptz:
		return &Schema{Type: "string", Format: "date-time"}
	case types.FieldTypeDate:
		return &Schema{Type: "string", Format: "date"}
	case types.FieldTypeJSONB:
		return &Schema{Type: "object"}
	case types.FieldTypeEnum:
		return &Schema{Type: "string", Enum: enumValues}
	case types.FieldTypeArray:
		return &Schema{Type: "array", Items: &Schema{Type: "string"}}
	case types.FieldTypeBytea:
		return &Schema{Type: "string", Format: "byte"}
	default:
		return &Schema{Type: "string"}
	}
}
