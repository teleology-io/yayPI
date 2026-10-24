package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/teleology-io/yayPI/internal/middleware"
	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

// RouteInput checks a custom route's declared query parameters and JSON body
// before its handler runs, with the same type checks and validate rules as
// CRUD input. Values come back typed: integers int64, floats float64,
// decimals their exact decimal string, booleans bool, timestamptz time.Time
// (UTC), date "YYYY-MM-DD", jsonb and arrays as decoded JSON. On failure it
// writes the standard 400 (or 413/415) and returns ok=false. Undeclared query
// parameters are ignored; undeclared body keys are rejected.
func RouteInput(w http.ResponseWriter, r *http.Request, c *schema.RouteContract) (params, body map[string]any, ok bool) {
	params, body = map[string]any{}, map[string]any{}
	if len(c.Params) > 0 {
		raw := map[string]any{}
		q := r.URL.Query()
		for _, f := range c.Params {
			if !q.Has(f.Name) {
				continue
			}
			raw[f.Name] = queryValue(f.Type, q.Get(f.Name))
		}
		out, errs := prepareInput(&schema.Entity{Fields: c.Params}, raw, false, false, "")
		if len(errs) > 0 {
			writeValidation(w, r, errs)
			return nil, nil, false
		}
		params = plainValues(c.Params, out, raw)
	}
	if len(c.Body) > 0 {
		raw := map[string]any{}
		if r.Body != nil && r.ContentLength != 0 {
			if !isJSONContentType(r) {
				writeError(w, r, http.StatusUnsupportedMediaType, "expected application/json")
				return nil, nil, false
			}
			dec := json.NewDecoder(r.Body)
			dec.UseNumber()
			if err := dec.Decode(&raw); err != nil && err != io.EOF {
				if !decodeFailed(w, r, err) {
					return nil, nil, false
				}
			}
		}
		out, errs := prepareInput(&schema.Entity{Fields: c.Body}, raw, false, true, "")
		if len(errs) > 0 {
			writeValidation(w, r, errs)
			return nil, nil, false
		}
		body = plainValues(c.Body, out, raw)
	}
	return params, body, true
}

// decodeFailed writes the decode error response; always false.
func decodeFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	if middleware.IsBodyTooLarge(err) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "request body too large")
	} else {
		writeError(w, r, http.StatusBadRequest, "invalid JSON body (expected an object)")
	}
	return false
}

// queryValue turns a query string into the JSON-ish value the type checks
// expect (numbers and booleans are written bare in a URL).
func queryValue(ft types.FieldType, s string) any {
	switch ft {
	case types.FieldTypeInteger, types.FieldTypeBigint, types.FieldTypeFloat, types.FieldTypeDecimal:
		return json.Number(s)
	case types.FieldTypeBoolean:
		switch s {
		case "true", "1":
			return true
		case "false", "0":
			return false
		}
	}
	return s
}

// plainValues hands handlers Go values: jsonb and array fields keep their
// decoded JSON rather than the database encoding.
func plainValues(fields []schema.Field, out, raw map[string]any) map[string]any {
	for _, f := range fields {
		v, ok := out[f.ColumnName]
		if !ok || v == nil {
			continue
		}
		switch f.Type {
		case types.FieldTypeJSONB, types.FieldTypeArray:
			out[f.ColumnName] = normalizeNumbers(raw[f.Name])
		}
	}
	return out
}

func normalizeNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = normalizeNumbers(e)
		}
	}
	return v
}
