package schema

import (
	"regexp"

	"github.com/teleology-io/yayPI/pkg/types"
)

// Entity represents a fully resolved entity from YAML configuration.
type Entity struct {
	Name         string
	Table        string
	Database     string
	Fields       []Field
	Relations    []Relation
	Indexes      []Index
	Constraints  []Constraint
	Hooks        EntityHooks
	SoftDelete   bool
	Timestamps   bool
	Internal     bool   // framework-owned table: migrated, but never exposed via endpoints/OpenAPI
	Audit        bool   // write an audit log row for every change
	TenantColumn string // non-empty: rows are isolated by this column = subject.tenant
}

// PrimaryKey returns the primary key field, or nil when none is declared.
func (e *Entity) PrimaryKey() *Field {
	for i := range e.Fields {
		if e.Fields[i].PrimaryKey {
			return &e.Fields[i]
		}
	}
	return nil
}

// PKColumn returns the primary key column name ("id" when none is declared).
func (e *Entity) PKColumn() string {
	if pk := e.PrimaryKey(); pk != nil {
		return pk.ColumnName
	}
	return "id"
}

// HasColumn reports whether the entity has a column with the given name.
func (e *Entity) HasColumn(col string) bool {
	for _, f := range e.Fields {
		if f.ColumnName == col {
			return true
		}
	}
	return false
}

// Field represents a single column on an entity.
type Field struct {
	Name         string
	ColumnName   string // snake_case of Name if not overridden
	Type         types.FieldType
	Nullable     bool
	Unique       bool
	PrimaryKey   bool
	Default      string
	Reference    *Reference
	OmitResponse bool
	OmitLog      bool
	EnumValues   []string
	Length       int
	Precision    int
	Scale        int
	Index        bool
	Immutable    bool     // stripped from PATCH body; accepted on create only
	ReadRoles    []string // ABAC: nil = no restriction; set = only these roles can read this field
	WriteRoles   []string // ABAC: nil = no restriction; set = only these roles can write this field
	Validate     *FieldValidation
	DefaultFrom  string // subject.id | subject.email | subject.role — server-set on create, immutable after
}

// FieldValidation holds validation rules for a field.
type FieldValidation struct {
	Required  bool
	MinLength int
	MaxLength int
	Min       *float64
	Max       *float64
	Pattern   string
	Format    string         // email, url, uuid, slug
	Message   string         // custom error message override
	Regexp    *regexp.Regexp // Pattern, compiled once at schema build
}

// Reference represents a foreign key reference from a field.
type Reference struct {
	Entity   string
	Field    string
	OnDelete types.ReferentialAction
	OnUpdate types.ReferentialAction
}

// Relation represents a relationship between entities.
type Relation struct {
	Name       string
	Type       types.RelationType
	Entity     string
	ForeignKey string
	Through    string
	OtherKey   string
}

// Index represents a database index on an entity.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Type    string // btree, brin, hash, etc.
}

// Constraint represents a database constraint on an entity.
type Constraint struct {
	Name    string
	Type    string // check, primary_key, unique
	Check   string
	Columns []string
}

// EntityHooks holds plugin hook names for each lifecycle event.
type EntityHooks struct {
	BeforeCreate []string
	AfterCreate  []string
	BeforeUpdate []string
	AfterUpdate  []string
	BeforeDelete []string
	AfterDelete  []string
}
