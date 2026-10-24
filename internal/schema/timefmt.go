package schema

import (
	"fmt"
	"time"

	"github.com/teleology-io/yayPI/internal/config"
	"github.com/teleology-io/yayPI/internal/timefmt"
	"github.com/teleology-io/yayPI/pkg/types"
)

// resolveTimeFormats gives every timestamptz and date field its output format:
// the field's serialization.format, else the project default.
func resolveTimeFormats(reg *Registry, sc config.RootSerializationConfig) error {
	loc := time.UTC
	if sc.Timezone != "" {
		l, err := time.LoadLocation(sc.Timezone)
		if err != nil {
			return fmt.Errorf("serialization.timezone: %w", err)
		}
		loc = l
	}
	defaults := map[types.FieldType]string{
		types.FieldTypeTimestamptz: or(sc.TimestampFormat, "rfc3339"),
		types.FieldTypeDate:        or(sc.DateFormat, "date"),
	}
	for name, d := range map[string]string{"timestamp_format": defaults[types.FieldTypeTimestamptz], "date_format": defaults[types.FieldTypeDate]} {
		if _, err := timefmt.New(d, loc); err != nil {
			return fmt.Errorf("serialization.%s: %w", name, err)
		}
	}
	for _, e := range reg.entities {
		for i := range e.Fields {
			f := &e.Fields[i]
			def, isTime := defaults[f.Type]
			if !isTime {
				if f.FormatSpec != "" {
					return fmt.Errorf("entity %s field %s: serialization.format applies to timestamptz and date fields only", e.Name, f.Name)
				}
				continue
			}
			tf, err := timefmt.New(or(f.FormatSpec, def), loc)
			if err != nil {
				return fmt.Errorf("entity %s field %s: serialization.format: %w", e.Name, f.Name, err)
			}
			f.Time = tf
		}
	}
	return nil
}

// Defaults for fields whose registry wasn't built by Build (tests, plugins).
var (
	defaultTimestamp, _ = timefmt.New("rfc3339", time.UTC)
	defaultDate, _      = timefmt.New("date", time.UTC)
)

// TimeFormat is the field's output format, or the default for its type; nil for
// fields that aren't timestamptz or date.
func (f *Field) TimeFormat() *timefmt.Format {
	switch {
	case f.Time != nil:
		return f.Time
	case f.Type == types.FieldTypeTimestamptz:
		return defaultTimestamp
	case f.Type == types.FieldTypeDate:
		return defaultDate
	}
	return nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// RenderTimes formats the entity's timestamptz and date values in row for a
// response. Values that aren't recognizable times are left as they are.
func (e *Entity) RenderTimes(row map[string]any) {
	for i := range e.Fields {
		f := &e.Fields[i]
		tf := f.TimeFormat()
		if tf == nil {
			continue
		}
		v, ok := row[f.ColumnName]
		if !ok || v == nil {
			continue
		}
		if t, ok := timefmt.FromDriver(v); ok {
			row[f.ColumnName] = tf.Render(t)
		}
	}
}

// ParseTime reads a client-supplied timestamptz or date value: RFC 3339 (or
// YYYY-MM-DD for dates) always, plus the field's configured format.
func (f *Field) ParseTime(v any) (time.Time, bool) {
	if s, ok := v.(string); ok {
		layout := time.RFC3339Nano
		if f.Type == types.FieldTypeDate {
			layout = "2006-01-02"
		}
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	if f.Time != nil {
		return f.Time.Parse(v)
	}
	return time.Time{}, false
}

// TimeExpectation describes accepted input for error messages.
func (f *Field) TimeExpectation() string {
	base := "an RFC 3339 timestamp"
	if f.Type == types.FieldTypeDate {
		base = "a YYYY-MM-DD date"
	}
	if f.Time == nil || f.Time.Describe() == base {
		return base
	}
	return base + " or " + f.Time.Describe()
}
