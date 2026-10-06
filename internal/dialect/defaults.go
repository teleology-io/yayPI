package dialect

import "strings"

// normalizeDefault lowercases and strips whitespace for matching well-known defaults.
func normalizeDefault(expr string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(expr), " ", ""))
}

func isNowDefault(expr string) bool {
	switch normalizeDefault(expr) {
	case "now()", "current_timestamp", "current_timestamp()", "localtimestamp":
		return true
	}
	return false
}

func isUUIDDefault(expr string) bool {
	switch normalizeDefault(expr) {
	case "gen_random_uuid()", "uuid_generate_v4()", "uuid()":
		return true
	}
	return false
}

// errContainsAny reports whether err's message contains any of the substrings
// (case-insensitive). Used where drivers expose no typed error codes.
func errContainsAny(err error, subs ...string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range subs {
		if strings.Contains(msg, strings.ToLower(s)) {
			return true
		}
	}
	return false
}
