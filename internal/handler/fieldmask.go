package handler

import (
	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
)

// applyFieldAccess strips read-restricted fields from a record map based on the
// caller's role. Fields with no ReadRoles set are always included (opt-in restriction).
// A nil subject (unauthenticated) is treated as having an empty role.
func applyFieldAccess(entity *schema.Entity, record map[string]interface{}, sub *middleware.Subject) {
	if record == nil {
		return
	}
	role := ""
	if sub != nil {
		role = sub.Role
	}
	for _, f := range entity.Fields {
		if len(f.ReadRoles) == 0 {
			continue // no restriction — always included
		}
		if !sliceContainsStr(f.ReadRoles, role) {
			delete(record, f.ColumnName)
			delete(record, f.Name)
		}
	}
}

// applyWriteRoles removes write-restricted fields from the request body based on the
// caller's role. Fields with no WriteRoles set are always writable.
func applyWriteRoles(entity *schema.Entity, data map[string]interface{}, sub *middleware.Subject) {
	if data == nil {
		return
	}
	role := ""
	if sub != nil {
		role = sub.Role
	}
	for _, f := range entity.Fields {
		if len(f.WriteRoles) == 0 {
			continue // no restriction
		}
		if !sliceContainsStr(f.WriteRoles, role) {
			delete(data, f.ColumnName)
			delete(data, f.Name)
		}
	}
}

// applySubjectDefaults sets default_from fields from the caller on create, overriding any
// client-supplied value. It returns the name of a required field that could not be set
// because the request is anonymous ("" when fine).
func applySubjectDefaults(entity *schema.Entity, data map[string]interface{}, sub *middleware.Subject) string {
	for _, f := range entity.Fields {
		if f.DefaultFrom == "" {
			continue
		}
		delete(data, f.Name)
		v := middleware.SubjectAttr(sub, f.DefaultFrom[len("subject."):])
		if v == "" {
			if !f.Nullable {
				return f.Name
			}
			continue
		}
		data[f.ColumnName] = v
	}
	return ""
}

// stripServerManaged removes columns clients may never write directly: the primary key
// on update, timestamps, soft-delete marker, immutable fields (update only) and
// default_from fields (set from the caller on create, frozen after).
func stripServerManaged(entity *schema.Entity, data map[string]interface{}, isUpdate bool) {
	for _, f := range entity.Fields {
		drop := false
		switch {
		case f.ColumnName == "created_at" && entity.Timestamps,
			f.ColumnName == "updated_at" && entity.Timestamps,
			f.ColumnName == "deleted_at" && entity.SoftDelete:
			drop = true
		case isUpdate && (f.PrimaryKey || f.Immutable || f.DefaultFrom != ""):
			drop = true
		}
		if drop {
			delete(data, f.Name)
			delete(data, f.ColumnName)
		}
	}
}

// presentRecord applies omit_response and read-role stripping before a row is returned.
func presentRecord(entity *schema.Entity, row map[string]interface{}, sub *middleware.Subject) {
	stripOmitFields(entity, row)
	applyFieldAccess(entity, row, sub)
}

// stripOmitFields removes fields marked omit_response from a row.
func stripOmitFields(entity *schema.Entity, row map[string]interface{}) {
	for _, f := range entity.Fields {
		if f.OmitResponse {
			delete(row, f.ColumnName)
			delete(row, f.Name)
		}
	}
}

func sliceContainsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
