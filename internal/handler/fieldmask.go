package handler

import (
	"github.com/csullivan/yaypi/internal/middleware"
	"github.com/csullivan/yaypi/internal/schema"
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

// applyWriteRoles removes write-restricted fields from the decoded request body
// based on the caller's role. Fields with no WriteRoles set are always writable.
// A nil subject is treated as having an empty role.
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

func sliceContainsStr(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
