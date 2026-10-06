package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teleology-io/yayPI/internal/schema"
)

// Cursor is an opaque keyset pagination position: the sort column/direction it was
// produced under and the last row's sort value and primary key.
type Cursor struct {
	Sort  string `json:"s"`           // sort column
	Desc  bool   `json:"d,omitempty"` // sort direction
	Value any    `json:"v"`           // last row's sort value (nil for NULL)
	Kind  string `json:"k,omitempty"` // type tag for Value: time | int | float | str
	ID    any    `json:"id"`          // last row's primary key
}

// ErrCursorMismatch means the cursor was issued for a different sort order.
var ErrCursorMismatch = errors.New("cursor does not match the requested sort")

// NewCursor builds the cursor that continues after row under sort.
func NewCursor(entity *schema.Entity, sort Sort, row map[string]any) Cursor {
	c := Cursor{Sort: sort.Column, Desc: sort.Desc, ID: row[entity.PKColumn()]}
	if sort.Column != entity.PKColumn() {
		c.Value, c.Kind = tagValue(row[sort.Column])
	}
	if id, kind := tagValue(c.ID); kind == "time" {
		c.ID = id
	}
	return c
}

func tagValue(v any) (any, string) {
	switch x := v.(type) {
	case nil:
		return nil, ""
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano), "time"
	case int64, int32, int:
		return x, "int"
	case float64, float32:
		return x, "float"
	case []byte:
		return string(x), "str"
	default:
		return fmt.Sprint(x), "str"
	}
}

// untag restores the Go type a tagged cursor value had when it was read.
func untag(v any, kind string) any {
	switch kind {
	case "time":
		if s, ok := v.(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t
			}
		}
	case "int":
		if f, ok := v.(float64); ok {
			return int64(f)
		}
		if n, ok := v.(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				return i
			}
		}
	}
	return v
}

// EncodeCursor encodes a cursor to a signed, URL-safe string:
// base64url(json) + "." + base64url(hmac-sha256(payload, secret)).
func EncodeCursor(c Cursor, secret []byte) string {
	payload, _ := json.Marshal(c)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	sig := computeHMAC([]byte(encoded), secret)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// DecodeCursor decodes and verifies a cursor string.
func DecodeCursor(s string, secret []byte) (*Cursor, error) {
	parts := strings.SplitN(s, ".", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid cursor format")
	}
	encoded, sigEncoded := parts[0], parts[1]

	expectedSig := computeHMAC([]byte(encoded), secret)
	gotSig, err := base64.RawURLEncoding.DecodeString(sigEncoded)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor signature encoding: %w", err)
	}
	if !hmac.Equal(expectedSig, gotSig) {
		return nil, fmt.Errorf("cursor signature mismatch")
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor payload encoding: %w", err)
	}
	var c Cursor
	dec := json.NewDecoder(strings.NewReader(string(payload)))
	dec.UseNumber()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("invalid cursor payload: %w", err)
	}
	c.Value = untag(c.Value, c.Kind)
	if n, ok := c.ID.(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			c.ID = i
		} else {
			c.ID = n.String()
		}
	}
	return &c, nil
}

func computeHMAC(data, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// ── ordering & keyset ─────────────────────────────────────────────────────────

// resolveSort validates the sort column (default: primary key ascending).
func (b *Builder) resolveSort(s Sort) (Sort, error) {
	if s.Column == "" {
		return Sort{Column: b.pk(), Desc: s.Desc}, nil
	}
	f, ok := b.field(s.Column)
	if !ok {
		return Sort{}, fmt.Errorf("sort column %q is not a known entity field", s.Column)
	}
	return Sort{Column: f.ColumnName, Desc: s.Desc}, nil
}

func (b *Builder) sortNullable(col string) bool {
	f, ok := b.field(col)
	return ok && f.Nullable && !f.PrimaryKey
}

// orderBy renders ORDER BY with the primary key as tiebreaker. NULLs sort last in both
// directions on every dialect via a leading "(col IS NULL)" key.
func (b *Builder) orderBy(s Sort) string {
	dir := " ASC"
	if s.Desc {
		dir = " DESC"
	}
	pk := b.qi(b.pk())
	if s.Column == b.pk() {
		return pk + dir
	}
	col := b.qi(s.Column)
	order := col + dir + ", " + pk + dir
	if b.sortNullable(s.Column) {
		order = "(" + col + " IS NULL) ASC, " + order
	}
	return order
}

// keysetClause renders "rows strictly after the cursor" for the given ordering.
func (b *Builder) keysetClause(a *args, s Sort, c *Cursor) (string, error) {
	if c.Sort != s.Column || c.Desc != s.Desc {
		return "", ErrCursorMismatch
	}
	cmp := " > "
	if s.Desc {
		cmp = " < "
	}
	pk := b.qi(b.pk())
	if s.Column == b.pk() {
		return pk + cmp + a.add(c.ID), nil
	}
	col := b.qi(s.Column)
	if c.Value == nil {
		// Already inside the trailing NULL block: only NULL rows after this id remain.
		return "(" + col + " IS NULL AND " + pk + cmp + a.add(c.ID) + ")", nil
	}
	v1 := a.add(c.Value)
	v2 := a.add(c.Value)
	id := a.add(c.ID)
	clause := "(" + col + cmp + v1 + " OR (" + col + " = " + v2 + " AND " + pk + cmp + id + "))"
	if b.sortNullable(s.Column) {
		clause = "(" + clause + " OR " + col + " IS NULL)"
	}
	return clause, nil
}
