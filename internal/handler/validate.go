package handler

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

// ValidationErrors holds per-field validation error messages.
type ValidationErrors map[string]string

func (ve ValidationErrors) Error() string {
	parts := make([]string, 0, len(ve))
	for k, v := range ve {
		parts = append(parts, fmt.Sprintf("%s: %s", k, v))
	}
	return strings.Join(parts, "; ")
}

// validateFields checks all entity field validation rules against the incoming data.
// For partial updates (isUpdate=true) only fields present in data are validated.
// Returns nil if all rules pass, otherwise a ValidationErrors map.
func validateFields(entity *schema.Entity, data map[string]interface{}, isUpdate bool) ValidationErrors {
	errs := ValidationErrors{}

	for _, f := range entity.Fields {
		if f.Validate == nil {
			continue
		}
		v := f.Validate
		val, present := data[f.Name]

		// required check (only on create, or if the field is explicitly provided on update)
		if v.Required && !isUpdate && !present {
			errs[f.Name] = message(v, fmt.Sprintf("%s is required", f.Name))
			continue
		}

		if !present {
			continue // nothing provided — skip remaining rules
		}

		if val == nil {
			if v.Required {
				errs[f.Name] = message(v, fmt.Sprintf("%s is required"))
			}
			continue
		}

		switch f.Type {
		case types.FieldTypeString, types.FieldTypeText, types.FieldTypeEnum:
			s, ok := val.(string)
			if !ok {
				errs[f.Name] = fmt.Sprintf("%s must be a string", f.Name)
				continue
			}
			if v.MinLength > 0 && len(s) < v.MinLength {
				errs[f.Name] = message(v, fmt.Sprintf("%s must be at least %d characters", f.Name, v.MinLength))
				continue
			}
			if v.MaxLength > 0 && len(s) > v.MaxLength {
				errs[f.Name] = message(v, fmt.Sprintf("%s must be at most %d characters", f.Name, v.MaxLength))
				continue
			}
			if v.Pattern != "" {
				re, err := regexp.Compile(v.Pattern)
				if err == nil && !re.MatchString(s) {
					errs[f.Name] = message(v, fmt.Sprintf("%s has an invalid format", f.Name))
					continue
				}
			}
			if v.Format != "" {
				if msg := checkFormat(f.Name, s, v.Format); msg != "" {
					errs[f.Name] = message(v, msg)
					continue
				}
			}

		case types.FieldTypeInteger, types.FieldTypeBigint, types.FieldTypeFloat, types.FieldTypeDecimal:
			num, ok := toFloat64(val)
			if !ok {
				errs[f.Name] = fmt.Sprintf("%s must be a number", f.Name)
				continue
			}
			if v.Min != nil && num < *v.Min {
				errs[f.Name] = message(v, fmt.Sprintf("%s must be at least %g", f.Name, *v.Min))
				continue
			}
			if v.Max != nil && num > *v.Max {
				errs[f.Name] = message(v, fmt.Sprintf("%s must be at most %g", f.Name, *v.Max))
				continue
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// checkFormat validates well-known format names and returns an error message or "".
func checkFormat(field, s, format string) string {
	switch format {
	case "email":
		if !isValidEmail(s) {
			return fmt.Sprintf("%s must be a valid email address", field)
		}
	case "url":
		u, err := url.ParseRequestURI(s)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Sprintf("%s must be a valid URL", field)
		}
	case "uuid":
		if _, err := uuid.Parse(s); err != nil {
			return fmt.Sprintf("%s must be a valid UUID", field)
		}
	case "slug":
		if !regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`).MatchString(s) {
			return fmt.Sprintf("%s must be a valid slug (lowercase letters, numbers, hyphens)", field)
		}
	}
	return ""
}

// isValidEmail does a simple RFC 5322-ish email check without a regex library.
func isValidEmail(s string) bool {
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 {
		return false
	}
	domain := s[at+1:]
	if !strings.Contains(domain, ".") {
		return false
	}
	return true
}

// toFloat64 converts JSON numbers (float64, int, int64, etc.) to float64.
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	}
	return 0, false
}

// message returns the custom message if set, otherwise the default.
func message(v *schema.FieldValidation, defaultMsg string) string {
	if v.Message != "" {
		return v.Message
	}
	return defaultMsg
}
