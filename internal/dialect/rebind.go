package dialect

import (
	"fmt"
	"strings"
)

// RebindQuestion rewrites $1,$2,… into ? placeholders (MySQL, SQLite).
func RebindQuestion(query string) string {
	var b strings.Builder
	b.Grow(len(query))
	argIdx := 0
	for i := 0; i < len(query); i++ {
		if query[i] == '$' && i+1 < len(query) {
			// Consume all digits after $
			j := i + 1
			for j < len(query) && query[j] >= '0' && query[j] <= '9' {
				j++
			}
			if j > i+1 {
				argIdx++
				b.WriteByte('?')
				i = j - 1
				continue
			}
		}
		b.WriteByte(query[i])
	}
	_ = argIdx // suppress unused warning
	return b.String()
}

// RebindDollar is a no-op for Postgres (already uses $N).
func RebindDollar(query string) string { return query }

// BuildPlaceholders returns "$1, $2, …, $n" (Postgres) or "?, ?, …" (others).
func BuildPlaceholders(n int, useQuestion bool) []string {
	ph := make([]string, n)
	for i := range ph {
		if useQuestion {
			ph[i] = "?"
		} else {
			ph[i] = fmt.Sprintf("$%d", i+1)
		}
	}
	return ph
}
