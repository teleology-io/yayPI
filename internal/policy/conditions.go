package policy

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/csullivan/yaypi/internal/middleware"
)

// EvalConditions returns true only if ALL conditions pass against the subject.
// An empty conditions list always passes.
func EvalConditions(conditions []string, sub *middleware.Subject) (bool, error) {
	for _, cond := range conditions {
		ok, err := MatchCondition(cond, sub)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// MatchCondition evaluates a single condition expression against the subject.
//
// Supported syntax:
//
//	*                                     → always true
//	subject.<attr> == "value"
//	subject.<attr> != "value"
//	subject.<attr> > "value"
//	subject.<attr> < "value"
//	subject.<attr> >= "value"
//	subject.<attr> <= "value"
//	subject.<attr> in ["a", "b", "c"]
//	subject.<attr> not_in ["a", "b"]
//	subject.<attr> ends_with "value"
//	subject.<attr> starts_with "value"
//
// Attributes: subject.id, subject.role, subject.email
//
// Comparison uses numeric ordering when both sides parse as floats; string ordering otherwise.
func MatchCondition(expr string, sub *middleware.Subject) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "*" {
		return true, nil
	}

	// Extract LHS: must start with "subject."
	if !strings.HasPrefix(expr, "subject.") {
		return false, fmt.Errorf("condition %q: left-hand side must start with subject.<attr>", expr)
	}
	rest := expr[len("subject."):]

	// Split attribute name from the rest
	spaceIdx := strings.IndexByte(rest, ' ')
	if spaceIdx < 0 {
		return false, fmt.Errorf("condition %q: missing operator", expr)
	}
	attr := rest[:spaceIdx]
	rest = strings.TrimSpace(rest[spaceIdx:])

	lhs, err := subjectAttr(sub, attr)
	if err != nil {
		return false, fmt.Errorf("condition %q: %w", expr, err)
	}

	// Detect operator
	for _, op := range []string{"not_in", "ends_with", "starts_with", ">=", "<=", "!=", "==", ">", "<", "in"} {
		if strings.HasPrefix(rest, op) {
			rhs := strings.TrimSpace(rest[len(op):])
			return applyOp(op, lhs, rhs, expr)
		}
	}

	return false, fmt.Errorf("condition %q: unknown operator", expr)
}

// subjectAttr returns the named attribute value from the subject.
func subjectAttr(sub *middleware.Subject, attr string) (string, error) {
	if sub == nil {
		return "", nil // unauthenticated — empty string for all attrs
	}
	switch attr {
	case "id":
		return sub.ID, nil
	case "role":
		return sub.Role, nil
	case "email":
		return sub.Email, nil
	default:
		return "", fmt.Errorf("unknown subject attribute %q", attr)
	}
}

// applyOp applies the operator to lhs and rhs, returning the boolean result.
func applyOp(op, lhs, rhs, expr string) (bool, error) {
	switch op {
	case "in":
		vals, err := parseList(rhs, expr)
		if err != nil {
			return false, err
		}
		for _, v := range vals {
			if lhs == v {
				return true, nil
			}
		}
		return false, nil

	case "not_in":
		vals, err := parseList(rhs, expr)
		if err != nil {
			return false, err
		}
		for _, v := range vals {
			if lhs == v {
				return false, nil
			}
		}
		return true, nil

	case "ends_with":
		rhsStr, err := parseString(rhs, expr)
		if err != nil {
			return false, err
		}
		return strings.HasSuffix(lhs, rhsStr), nil

	case "starts_with":
		rhsStr, err := parseString(rhs, expr)
		if err != nil {
			return false, err
		}
		return strings.HasPrefix(lhs, rhsStr), nil

	case "==", "!=", ">", "<", ">=", "<=":
		rhsStr, err := parseString(rhs, expr)
		if err != nil {
			return false, err
		}
		return compareValues(op, lhs, rhsStr), nil
	}

	return false, fmt.Errorf("condition %q: unhandled operator %q", expr, op)
}

// compareValues compares two values using numeric ordering when both parse as numbers,
// or lexicographic ordering otherwise.
func compareValues(op, lhs, rhs string) bool {
	lhsF, lhsErr := strconv.ParseFloat(lhs, 64)
	rhsF, rhsErr := strconv.ParseFloat(rhs, 64)

	if lhsErr == nil && rhsErr == nil {
		// Numeric comparison
		switch op {
		case "==":
			return lhsF == rhsF
		case "!=":
			return lhsF != rhsF
		case ">":
			return lhsF > rhsF
		case "<":
			return lhsF < rhsF
		case ">=":
			return lhsF >= rhsF
		case "<=":
			return lhsF <= rhsF
		}
	}

	// String comparison
	switch op {
	case "==":
		return lhs == rhs
	case "!=":
		return lhs != rhs
	case ">":
		return lhs > rhs
	case "<":
		return lhs < rhs
	case ">=":
		return lhs >= rhs
	case "<=":
		return lhs <= rhs
	}
	return false
}

// parseString extracts a quoted string value from s (e.g. `"hello"` → `hello`).
func parseString(s, expr string) (string, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1], nil
	}
	// Also accept unquoted single tokens for convenience
	if !strings.ContainsAny(s, " \t\"[]") {
		return s, nil
	}
	return "", fmt.Errorf("condition %q: expected quoted string, got %q", expr, s)
}

// parseList extracts a list of quoted strings from s (e.g. `["a", "b"]` → ["a", "b"]).
func parseList(s, expr string) ([]string, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil, fmt.Errorf("condition %q: in/not_in requires a list like [\"a\", \"b\"]", expr)
	}
	inner := s[1 : len(s)-1]
	var vals []string
	for _, part := range strings.Split(inner, ",") {
		part = strings.TrimSpace(part)
		v, err := parseString(part, expr)
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	return vals, nil
}
