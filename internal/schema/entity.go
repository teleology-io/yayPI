package schema

import "github.com/csullivan/yaypi/pkg/types"

// Entity represents a fully resolved entity from YAML configuration.
type Entity struct {
	Name        string
	Table       string
	Database    string
	Fields      []Field
	Relations   []Relation
	Indexes     []Index
	Constraints []Constraint
	Hooks       EntityHooks
	SoftDelete  bool
	Timestamps  bool
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
