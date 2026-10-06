package query

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/teleology-io/yayPI/internal/schema"
	"github.com/teleology-io/yayPI/pkg/types"
)

const includeBatch = 500

// RowHook post-processes a related row before it is attached (field stripping etc.).
type RowHook func(entity *schema.Entity, row map[string]any)

// Include loads relation rel for rows (batched IN queries, no N+1) and attaches the
// result under row[rel.Name]: an object or null for belongs_to/has_one, an array for
// has_many/many_to_many. Soft-deleted related rows are excluded.
func (b *Builder) Include(ctx context.Context, reg *schema.Registry, rows []map[string]any, rel schema.Relation, hook RowHook) error {
	if len(rows) == 0 {
		return nil
	}
	target, ok := reg.GetEntity(rel.Entity)
	if !ok {
		return fmt.Errorf("relation %q: unknown entity %q", rel.Name, rel.Entity)
	}
	if target.Database != b.entity.Database {
		return fmt.Errorf("relation %q: cross-database includes are not supported", rel.Name)
	}
	tb := NewBuilder(target, b.db, b.dialect)

	switch rel.Type {
	case types.RelationBelongsTo:
		fk := rel.ForeignKey
		related, err := tb.fetchIn(ctx, target.PKColumn(), distinctKeys(rows, fk), "", "")
		if err != nil {
			return err
		}
		byKey := indexBy(related, target.PKColumn(), hook, target)
		for _, r := range rows {
			if list := byKey[keyString(r[fk])]; len(list) > 0 {
				r[rel.Name] = list[0]
			} else {
				r[rel.Name] = nil
			}
		}
	case types.RelationHasOne, types.RelationHasMany:
		pk := b.pk()
		related, err := tb.fetchIn(ctx, rel.ForeignKey, distinctKeys(rows, pk), "", "")
		if err != nil {
			return err
		}
		byKey := indexBy(related, rel.ForeignKey, hook, target)
		for _, r := range rows {
			list := byKey[keyString(r[pk])]
			if rel.Type == types.RelationHasOne {
				if len(list) > 0 {
					r[rel.Name] = list[0]
				} else {
					r[rel.Name] = nil
				}
				continue
			}
			if list == nil {
				list = []map[string]any{}
			}
			r[rel.Name] = list
		}
	case types.RelationManyToMany:
		through, ok := reg.GetEntity(rel.Through)
		if !ok {
			return fmt.Errorf("relation %q: unknown through entity %q", rel.Name, rel.Through)
		}
		pk := b.pk()
		related, err := tb.fetchIn(ctx, "", distinctKeys(rows, pk), through.Table, rel.ForeignKey+"\x00"+rel.OtherKey)
		if err != nil {
			return err
		}
		byKey := indexBy(related, parentKeyCol, hook, target)
		for _, r := range rows {
			list := byKey[keyString(r[pk])]
			if list == nil {
				list = []map[string]any{}
			}
			r[rel.Name] = list
		}
	default:
		return fmt.Errorf("relation %q: unsupported type %q", rel.Name, rel.Type)
	}
	return nil
}

// parentKeyCol carries the owning row's key through a many-to-many join.
const parentKeyCol = "__yaypi_parent"

// fetchIn selects this entity's rows whose col is in keys. For many-to-many, col is
// empty and joinTable/joinKeys ("fk\x00otherKey") describe the junction table.
func (b *Builder) fetchIn(ctx context.Context, col string, keys []any, joinTable, joinKeys string) ([]map[string]any, error) {
	var out []map[string]any
	for start := 0; start < len(keys); start += includeBatch {
		end := min(start+includeBatch, len(keys))
		a := &args{}
		ph := make([]string, 0, end-start)
		for _, k := range keys[start:end] {
			ph = append(ph, a.add(k))
		}
		var sqlStr string
		if joinTable == "" {
			sqlStr = fmt.Sprintf("SELECT %s FROM %s WHERE %s IN (%s)",
				b.selectColumns(), b.qi(b.entity.Table), b.qi(col), strings.Join(ph, ", "))
			if b.entity.SoftDelete {
				sqlStr += " AND " + b.qi("deleted_at") + " IS NULL"
			}
		} else {
			parts := strings.SplitN(joinKeys, "\x00", 2)
			fk, other := parts[0], parts[1]
			cols := make([]string, 0, len(b.entity.Fields)+1)
			for _, f := range b.entity.Fields {
				cols = append(cols, "o."+b.qi(f.ColumnName))
			}
			cols = append(cols, "j."+b.qi(fk)+" AS "+b.qi(parentKeyCol))
			sqlStr = fmt.Sprintf("SELECT %s FROM %s o JOIN %s j ON j.%s = o.%s WHERE j.%s IN (%s)",
				strings.Join(cols, ", "), b.qi(b.entity.Table), b.qi(joinTable),
				b.qi(other), b.qi(b.pk()), b.qi(fk), strings.Join(ph, ", "))
			if b.entity.SoftDelete {
				sqlStr += " AND o." + b.qi("deleted_at") + " IS NULL"
			}
		}
		rows, err := b.db.QueryContext(ctx, b.dialect.Rebind(sqlStr), a.vals...)
		if err != nil {
			return nil, fmt.Errorf("include query: %w", err)
		}
		batch, err := b.scanRows(rows)
		rows.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, batch...)
	}
	return out, nil
}

// indexBy groups rows by keyString(row[col]), applying hook to each row. The internal
// parent-key column is removed after grouping.
func indexBy(rows []map[string]any, col string, hook RowHook, entity *schema.Entity) map[string][]map[string]any {
	out := make(map[string][]map[string]any)
	for _, r := range rows {
		k := keyString(r[col])
		delete(r, parentKeyCol)
		if hook != nil {
			hook(entity, r)
		}
		out[k] = append(out[k], r)
	}
	return out
}

func distinctKeys(rows []map[string]any, col string) []any {
	seen := make(map[string]bool)
	var keys []any
	for _, r := range rows {
		v := r[col]
		if v == nil {
			continue
		}
		k := keyString(v)
		if !seen[k] {
			seen[k] = true
			keys = append(keys, v)
		}
	}
	return keys
}

// keyString normalises key values of differing Go types (int64 vs float64, uuid bytes vs
// string) so they compare equal across queries.
func keyString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []byte:
		if len(x) == 16 {
			if u, err := uuid.FromBytes(x); err == nil {
				return u.String()
			}
		}
		return string(x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprint(int64(x))
		}
	}
	return fmt.Sprint(v)
}
