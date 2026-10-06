package migration

import "strings"

// SplitStatements splits a SQL script into individual statements on top-level
// semicolons, respecting single/double quotes, backticks, Postgres dollar-quoted bodies
// ($$…$$ / $tag$…$tag$), and -- / /* */ comments. Transaction control statements
// (BEGIN/COMMIT/ROLLBACK/START TRANSACTION) are dropped — the runner manages the
// transaction itself. Empty / comment-only statements are omitted.
func SplitStatements(script string) []string {
	var out []string
	var cur strings.Builder
	n := len(script)

	flush := func() {
		stmt := strings.TrimSpace(cur.String())
		cur.Reset()
		if stmt == "" || onlyComments(stmt) || isTxControl(stmt) {
			return
		}
		out = append(out, stmt)
	}

	for i := 0; i < n; i++ {
		c := script[i]
		switch {
		case c == '-' && i+1 < n && script[i+1] == '-':
			j := strings.IndexByte(script[i:], '\n')
			if j < 0 {
				j = n - i
			}
			cur.WriteString(script[i : i+j])
			i += j - 1
		case c == '/' && i+1 < n && script[i+1] == '*':
			j := strings.Index(script[i+2:], "*/")
			end := n
			if j >= 0 {
				end = i + 2 + j + 2
			}
			cur.WriteString(script[i:end])
			i = end - 1
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < n {
				if script[j] == c {
					if j+1 < n && script[j+1] == c { // doubled quote escape
						j += 2
						continue
					}
					break
				}
				if script[j] == '\\' && c != '"' {
					j++
				}
				j++
			}
			end := min(j+1, n)
			cur.WriteString(script[i:end])
			i = end - 1
		case c == '$':
			// Dollar quote: $tag$ … $tag$ where tag is [A-Za-z0-9_]* (not a $1 placeholder).
			j := i + 1
			for j < n && (script[j] == '_' || script[j] >= 'a' && script[j] <= 'z' || script[j] >= 'A' && script[j] <= 'Z' || (j > i+1 && script[j] >= '0' && script[j] <= '9')) {
				j++
			}
			if j < n && script[j] == '$' {
				tag := script[i : j+1]
				k := strings.Index(script[j+1:], tag)
				end := n
				if k >= 0 {
					end = j + 1 + k + len(tag)
				}
				cur.WriteString(script[i:end])
				i = end - 1
			} else {
				cur.WriteByte(c)
			}
		case c == ';':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

func onlyComments(stmt string) bool {
	for _, line := range strings.Split(stmt, "\n") {
		l := strings.TrimSpace(line)
		if l != "" && !strings.HasPrefix(l, "--") {
			return false
		}
	}
	return true
}

func isTxControl(stmt string) bool {
	// Strip leading comment lines before checking.
	var lines []string
	for _, line := range strings.Split(stmt, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			lines = append(lines, l)
		}
	}
	s := strings.ToUpper(strings.Join(lines, " "))
	switch s {
	case "BEGIN", "BEGIN TRANSACTION", "START TRANSACTION", "COMMIT", "END", "ROLLBACK":
		return true
	}
	return false
}
