package policy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
)

// ErrRowAccessDenied is returned when row_access rules are defined but none match the subject.
var ErrRowAccessDenied = errors.New("row access denied")

// ResolveRowFilter evaluates the row_access rules against the subject and returns
// the SQL fragment and bound argument values to append to a WHERE clause.
//
// Rules are evaluated in order; the first matching rule wins:
//   - If the filter is empty ("") → no extra WHERE condition (allow all rows).
//   - If the filter is non-empty → inject it as an AND condition with bound params.
//
// If rules is nil/empty → ("", nil, nil) — open access, no filter applied.
// If rules are defined but none match → ErrRowAccessDenied (caller should return 403).
//
// Bind variables in the filter string use the syntax :subject.id, :subject.role,
// :subject.email and are replaced with positional $N placeholders.
func ResolveRowFilter(rules []schema.RowAccessRule, sub *middleware.Subject) (string, []any, error) {
	if len(rules) == 0 {
		return "", nil, nil
	}

	for _, rule := range rules {
		matched, err := MatchCondition(rule.When, sub)
		if err != nil {
			return "", nil, fmt.Errorf("row_access rule %q: %w", rule.When, err)
		}
		if !matched {
			continue
		}
		// Rule matched.
		if rule.Filter == "" {
			return "", nil, nil // allow all rows, no extra filter
		}
		sql, args := bindSubjectParams(rule.Filter, sub)
		return sql, args, nil
	}

	// No rule matched — deny access.
	return "", nil, ErrRowAccessDenied
}

// bindSubjectParams replaces :subject.id, :subject.role, :subject.email with
// positional $1, $2, … placeholders and returns the modified SQL and argument slice.
//
// The placeholder index starts at 1 and increments for each distinct binding found
// in left-to-right order. Repeated uses of the same binding each get a new placeholder.
func bindSubjectParams(filter string, sub *middleware.Subject) (string, []any) {
	bindings := []struct {
		placeholder string
		value       func() string
	}{
		{":subject.id", func() string {
			if sub == nil {
				return ""
			}
			return sub.ID
		}},
		{":subject.role", func() string {
			if sub == nil {
				return ""
			}
			return sub.Role
		}},
		{":subject.email", func() string {
			if sub == nil {
				return ""
			}
			return sub.Email
		}},
	}

	result := filter
	var args []any
	idx := 1

	// Replace each occurrence left-to-right by scanning the string.
	// We do a single pass using strings.Builder to handle overlapping cases cleanly.
	var b strings.Builder
	remaining := filter
	for len(remaining) > 0 {
		// Find the earliest binding match.
		bestPos := -1
		bestLen := 0
		var bestVal string
		for _, bnd := range bindings {
			pos := strings.Index(remaining, bnd.placeholder)
			if pos >= 0 && (bestPos < 0 || pos < bestPos) {
				bestPos = pos
				bestLen = len(bnd.placeholder)
				bestVal = bnd.value()
			}
		}
		if bestPos < 0 {
			// No more placeholders.
			b.WriteString(remaining)
			break
		}
		b.WriteString(remaining[:bestPos])
		b.WriteString(fmt.Sprintf("$%d", idx))
		args = append(args, bestVal)
		idx++
		remaining = remaining[bestPos+bestLen:]
	}
	result = b.String()

	return result, args
}
