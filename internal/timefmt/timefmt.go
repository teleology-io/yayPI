// Package timefmt formats and parses timestamptz/date values for the API.
//
// A format is a preset name or a pattern of tokens:
//
//	rfc3339       2026-10-23T22:38:07Z         (default for timestamptz)
//	rfc3339_ms    2026-10-23T22:38:07.123Z
//	rfc3339_nano  2026-10-23T22:38:07.123456789Z
//	unix          1792795087                    (JSON number, seconds)
//	unix_ms       1792795087123                 (JSON number, milliseconds)
//	date          2026-10-23                    (default for date)
//
//	YYYY YY       year (2026, 26)
//	MMMM MMM MM M month (October, Oct, 10, 10)
//	DD D          day of month (03, 3)
//	dddd ddd      weekday (Friday, Fri)
//	HH H hh h     hour, 24h (09, 9) and 12h (09, 9)
//	mm m ss s     minute, second
//	SSS           milliseconds
//	A a           AM/PM, am/pm
//	Z ZZ          offset (+02:00 or Z, +0200)
//	[text]        literal text
//
// Patterns are interpreted token by token (not as a Go reference layout), so
// any other character is copied as is.
package timefmt

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type kind int

const (
	kindLayout kind = iota // a Go layout (presets)
	kindUnix
	kindUnixMs
	kindPattern
)

// Format renders time values in one configured format and location.
type Format struct {
	// Spec is the preset name or pattern as configured.
	Spec   string
	kind   kind
	layout string
	tokens []token
	loc    *time.Location
}

var presets = map[string]string{
	"rfc3339":      "2006-01-02T15:04:05Z07:00",
	"rfc3339_ms":   "2006-01-02T15:04:05.000Z07:00",
	"rfc3339_nano": time.RFC3339Nano,
	"date":         "2006-01-02",
}

// New parses a format spec. loc is where timestamps are rendered (nil = UTC);
// date-only formats ignore it.
func New(spec string, loc *time.Location) (*Format, error) {
	if loc == nil {
		loc = time.UTC
	}
	f := &Format{Spec: spec, loc: loc}
	switch spec {
	case "unix":
		f.kind = kindUnix
		return f, nil
	case "unix_ms":
		f.kind = kindUnixMs
		return f, nil
	}
	if l, ok := presets[spec]; ok {
		f.kind, f.layout = kindLayout, l
		return f, nil
	}
	if isWord(spec) {
		return nil, fmt.Errorf("unknown time format %q (presets: rfc3339, rfc3339_ms, rfc3339_nano, unix, unix_ms, date; or a pattern like \"YYYY-MM-DD HH:mm:ss\")", spec)
	}
	toks, err := tokenize(spec)
	if err != nil {
		return nil, err
	}
	f.kind, f.tokens = kindPattern, toks
	return f, nil
}

// Numeric reports whether values render as JSON numbers.
func (f *Format) Numeric() bool { return f.kind == kindUnix || f.kind == kindUnixMs }

// DateOnly reports whether the format carries no time of day (rendered without
// a timezone shift).
func (f *Format) DateOnly() bool {
	if f.kind == kindLayout {
		return f.Spec == "date"
	}
	if f.kind != kindPattern {
		return false
	}
	for _, t := range f.tokens {
		switch t.tok {
		case "HH", "H", "hh", "h", "mm", "m", "ss", "s", "SSS", "A", "a", "Z", "ZZ":
			return false
		}
	}
	return true
}

// IsRFC3339 reports whether output is RFC 3339 (OpenAPI format date-time).
func (f *Format) IsRFC3339() bool {
	return f.kind == kindLayout && strings.HasPrefix(f.Spec, "rfc3339")
}

// Render formats t: a string, or an int64 for the unix formats.
func (f *Format) Render(t time.Time) any {
	if !f.DateOnly() {
		t = t.In(f.loc)
	}
	switch f.kind {
	case kindUnix:
		return t.Unix()
	case kindUnixMs:
		return t.UnixMilli()
	case kindLayout:
		return t.Format(f.layout)
	}
	var b strings.Builder
	for _, tk := range f.tokens {
		b.WriteString(tk.render(t))
	}
	return b.String()
}

// Parse reads a client value written in this format.
func (f *Format) Parse(v any) (time.Time, bool) {
	switch f.kind {
	case kindUnix, kindUnixMs:
		var n int64
		switch x := v.(type) {
		case json.Number:
			i, err := x.Int64()
			if err != nil {
				return time.Time{}, false
			}
			n = i
		case float64:
			n = int64(x)
		case int64:
			n = x
		case int:
			n = int64(x)
		case string:
			i, err := strconv.ParseInt(x, 10, 64)
			if err != nil {
				return time.Time{}, false
			}
			n = i
		default:
			return time.Time{}, false
		}
		if f.kind == kindUnix {
			return time.Unix(n, 0).UTC(), true
		}
		return time.UnixMilli(n).UTC(), true
	}
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	if f.kind == kindLayout {
		t, err := time.ParseInLocation(f.layout, s, f.loc)
		return t, err == nil
	}
	return f.parsePattern(s)
}

// Describe is a short human description for docs and error messages.
func (f *Format) Describe() string {
	switch f.kind {
	case kindUnix:
		return "unix seconds"
	case kindUnixMs:
		return "unix milliseconds"
	case kindLayout:
		if f.Spec == "date" {
			return "a YYYY-MM-DD date"
		}
		return "an RFC 3339 timestamp"
	}
	return "a timestamp like " + f.Spec
}

// SQLiteLayout is how yayPI stores times in SQLite (always UTC). A whole second
// matches CURRENT_TIMESTAMP ("2006-01-02 15:04:05") exactly, and fractions sort
// textually in time order.
const SQLiteLayout = "2006-01-02 15:04:05.999999"

// driverLayouts are the shapes database drivers hand back as text: SQLite's
// CURRENT_TIMESTAMP and stored values (including Go's time.Time String() form,
// written by older versions), MySQL DATETIME without parseTime.
var driverLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999 -0700 MST",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02",
}

// FromDriver turns a scanned column value into a time. Values without an
// offset are UTC (how yayPI writes them). Anything else is not a time.
func FromDriver(v any) (time.Time, bool) {
	switch x := v.(type) {
	case time.Time:
		return x, true
	case []byte:
		return FromDriver(string(x))
	case string:
		s := strings.TrimSpace(x)
		for _, l := range driverLayouts {
			if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// ── patterns ────────────────────────────────────────────────────────────────

type token struct {
	tok string // "" for literal text
	lit string
}

// Longest first so "YYYY" wins over "YY", "MMMM" over "MM".
var tokenNames = []string{"YYYY", "MMMM", "dddd", "MMM", "ddd", "SSS", "YY", "MM", "DD", "HH", "hh", "mm", "ss", "ZZ", "M", "D", "H", "h", "m", "s", "A", "a", "Z"}

func tokenize(p string) ([]token, error) {
	var out []token
	lit := func(s string) {
		if n := len(out); n > 0 && out[n-1].tok == "" {
			out[n-1].lit += s
			return
		}
		out = append(out, token{lit: s})
	}
	anchored := false
	for i := 0; i < len(p); {
		if p[i] == '[' {
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				return nil, fmt.Errorf("format %q: unclosed '['", p)
			}
			lit(p[i+1 : i+end])
			i += end + 1
			continue
		}
		matched := false
		for _, n := range tokenNames {
			if strings.HasPrefix(p[i:], n) {
				out = append(out, token{tok: n})
				i += len(n)
				matched = true
				anchored = anchored || len(n) > 1
				break
			}
		}
		if !matched {
			lit(p[i : i+1])
			i++
		}
	}
	if !anchored {
		return nil, fmt.Errorf("time format %q has no YYYY, MM, DD, HH, mm or ss token (put literal text in [brackets])", p)
	}
	return out, nil
}

// isWord reports a bare identifier: a preset name, possibly misspelled.
func isWord(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return s != ""
}

func pad(n, w int) string {
	s := strconv.Itoa(n)
	for len(s) < w {
		s = "0" + s
	}
	return s
}

func hour12(h int) int {
	if h%12 == 0 {
		return 12
	}
	return h % 12
}

func (tk token) render(t time.Time) string {
	switch tk.tok {
	case "":
		return tk.lit
	case "YYYY":
		return pad(t.Year(), 4)
	case "YY":
		return pad(t.Year()%100, 2)
	case "MMMM":
		return t.Month().String()
	case "MMM":
		return t.Month().String()[:3]
	case "MM":
		return pad(int(t.Month()), 2)
	case "M":
		return strconv.Itoa(int(t.Month()))
	case "DD":
		return pad(t.Day(), 2)
	case "D":
		return strconv.Itoa(t.Day())
	case "dddd":
		return t.Weekday().String()
	case "ddd":
		return t.Weekday().String()[:3]
	case "HH":
		return pad(t.Hour(), 2)
	case "H":
		return strconv.Itoa(t.Hour())
	case "hh":
		return pad(hour12(t.Hour()), 2)
	case "h":
		return strconv.Itoa(hour12(t.Hour()))
	case "mm":
		return pad(t.Minute(), 2)
	case "m":
		return strconv.Itoa(t.Minute())
	case "ss":
		return pad(t.Second(), 2)
	case "s":
		return strconv.Itoa(t.Second())
	case "SSS":
		return pad(t.Nanosecond()/1e6, 3)
	case "A":
		if t.Hour() < 12 {
			return "AM"
		}
		return "PM"
	case "a":
		if t.Hour() < 12 {
			return "am"
		}
		return "pm"
	case "Z":
		return t.Format("Z07:00")
	case "ZZ":
		return t.Format("-0700")
	}
	return ""
}

// parsePattern reads s token by token. Weekday names are checked for shape
// only; the date comes from the year, month and day.
func (f *Format) parsePattern(s string) (time.Time, bool) {
	year, month, day, hour, min, sec, ms := 1970, 1, 1, 0, 0, 0, 0
	pm, hasAMPM := false, false
	loc := f.loc
	if f.DateOnly() {
		loc = time.UTC
	}
	num := func(min, max int) (int, bool) {
		n := 0
		for n < max && n < len(s) && s[n] >= '0' && s[n] <= '9' {
			n++
		}
		if n < min {
			return 0, false
		}
		v, _ := strconv.Atoi(s[:n])
		s = s[n:]
		return v, true
	}
	name := func(names []string) (int, bool) {
		for i, n := range names {
			if strings.HasPrefix(strings.ToLower(s), strings.ToLower(n)) {
				s = s[len(n):]
				return i, true
			}
		}
		return 0, false
	}
	months, shortMonths, days, shortDays := []string{}, []string{}, []string{}, []string{}
	for m := time.January; m <= time.December; m++ {
		months = append(months, m.String())
		shortMonths = append(shortMonths, m.String()[:3])
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		days = append(days, d.String())
		shortDays = append(shortDays, d.String()[:3])
	}
	var ok bool
	for _, tk := range f.tokens {
		switch tk.tok {
		case "":
			if !strings.HasPrefix(s, tk.lit) {
				return time.Time{}, false
			}
			s = s[len(tk.lit):]
			ok = true
		case "YYYY":
			year, ok = num(4, 4)
		case "YY":
			year, ok = num(2, 2)
			year += 2000
		case "MMMM":
			month, ok = name(months)
			month++
		case "MMM":
			month, ok = name(shortMonths)
			month++
		case "MM":
			month, ok = num(2, 2)
		case "M":
			month, ok = num(1, 2)
		case "DD":
			day, ok = num(2, 2)
		case "D":
			day, ok = num(1, 2)
		case "dddd":
			_, ok = name(days)
		case "ddd":
			_, ok = name(shortDays)
		case "HH", "hh":
			hour, ok = num(2, 2)
		case "H", "h":
			hour, ok = num(1, 2)
		case "mm":
			min, ok = num(2, 2)
		case "m":
			min, ok = num(1, 2)
		case "ss":
			sec, ok = num(2, 2)
		case "s":
			sec, ok = num(1, 2)
		case "SSS":
			ms, ok = num(3, 3)
		case "A", "a":
			var i int
			i, ok = name([]string{"AM", "PM"})
			pm, hasAMPM = i == 1, true
		case "Z", "ZZ":
			loc, ok = parseOffset(&s)
		}
		if !ok {
			return time.Time{}, false
		}
	}
	if s != "" {
		return time.Time{}, false
	}
	if hasAMPM {
		if hour < 1 || hour > 12 {
			return time.Time{}, false
		}
		hour %= 12
		if pm {
			hour += 12
		}
	}
	t := time.Date(year, time.Month(month), day, hour, min, sec, ms*1e6, loc)
	// time.Date normalizes out-of-range values (Feb 30 → Mar 2); refuse them.
	if t.Year() != year || int(t.Month()) != month || t.Day() != day || t.Hour() != hour || t.Minute() != min || t.Second() != sec {
		return time.Time{}, false
	}
	return t, true
}

func parseOffset(s *string) (*time.Location, bool) {
	if strings.HasPrefix(*s, "Z") {
		*s = (*s)[1:]
		return time.UTC, true
	}
	if len(*s) < 5 || ((*s)[0] != '+' && (*s)[0] != '-') {
		return nil, false
	}
	sign := 1
	if (*s)[0] == '-' {
		sign = -1
	}
	rest := (*s)[1:]
	hh, err1 := strconv.Atoi(rest[:2])
	rest = rest[2:]
	rest = strings.TrimPrefix(rest, ":")
	if len(rest) < 2 {
		return nil, false
	}
	mm, err2 := strconv.Atoi(rest[:2])
	if err1 != nil || err2 != nil {
		return nil, false
	}
	*s = rest[2:]
	return time.FixedZone("", sign*(hh*3600+mm*60)), true
}
