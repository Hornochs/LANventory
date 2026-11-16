package store

import (
	"context"
	"encoding/json"
	"strings"
)

// DefaultPerPage is the page size of the asset list.
const DefaultPerPage = 50

// AssetSorts lists the sort keys of the asset list.
var AssetSorts = []string{"inventory_no", "name", "category", "location", "status", "updated"}

var sortExpr = map[string]string{
	"inventory_no": "a.inventory_no COLLATE NOCASE",
	"name":         "a.name COLLATE NOCASE",
	"category":     "c.name COLLATE NOCASE",
	"location":     "l.name COLLATE NOCASE",
	"status":       "a.status",
	"updated":      "a.updated_at",
}

// AssetFilter selects assets for the list.
type AssetFilter struct {
	// Q is a search text; every whitespace-separated word must match the
	// inventory number, name, serial number, manufacturer, model or a custom
	// field value (case-insensitive for ASCII).
	Q          string
	CategoryID int64
	LocationID int64
	Status     string
	// Fields filters by custom field key → exact value; only used together with CategoryID.
	Fields  map[string]string
	Sort    string
	Desc    bool
	Page    int
	PerPage int
}

// AssetPage is one page of the asset list.
type AssetPage struct {
	Assets []*Asset
	Total  int
	Page   int
	Pages  int
}

// escapeLike escapes the wildcards of a LIKE pattern (ESCAPE '\').
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// where builds the WHERE clause (with leading " WHERE ", or empty) shared by
// the list and the CSV export.
func (f AssetFilter) where() (string, []any) {
	var where []string
	var args []any
	for _, term := range strings.Fields(f.Q) {
		p := "%" + escapeLike(term) + "%"
		where = append(where, `(a.inventory_no LIKE ? ESCAPE '\' OR a.name LIKE ? ESCAPE '\' OR a.serial_no LIKE ? ESCAPE '\'
			OR a.manufacturer LIKE ? ESCAPE '\' OR a.model LIKE ? ESCAPE '\'
			OR EXISTS (SELECT 1 FROM asset_field_values v WHERE v.asset_id = a.id AND v.value LIKE ? ESCAPE '\'))`)
		args = append(args, p, p, p, p, p, p)
	}
	if f.CategoryID != 0 {
		where = append(where, `a.category_id = ?`)
		args = append(args, f.CategoryID)
		for key, val := range f.Fields {
			where = append(where, `EXISTS (SELECT 1 FROM asset_field_values v JOIN category_fields cf ON cf.id = v.field_id
				WHERE v.asset_id = a.id AND cf.category_id = a.category_id AND cf.key = ? AND v.value = ?)`)
			args = append(args, key, val)
		}
	}
	if f.LocationID != 0 {
		where = append(where, `a.location_id = ?`)
		args = append(args, f.LocationID)
	}
	if f.Status != "" {
		where = append(where, `a.status = ?`)
		args = append(args, f.Status)
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// orderBy returns the ORDER BY clause for the filter's sort key and direction.
func (f AssetFilter) orderBy() string {
	expr, ok := sortExpr[f.Sort]
	if !ok {
		expr = sortExpr["inventory_no"]
	}
	dir := "ASC"
	if f.Desc {
		dir = "DESC"
	}
	return ` ORDER BY ` + expr + ` ` + dir + `, a.id ` + dir
}

// ListAssets returns one page of assets matching the filter.
func (db *DB) ListAssets(ctx context.Context, f AssetFilter) (*AssetPage, error) {
	cond, args := f.where()
	const from = assetFrom
	var total int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)`+from+cond, args...).Scan(&total); err != nil {
		return nil, err
	}
	per := f.PerPage
	if per < 1 {
		per = DefaultPerPage
	}
	pages := (total + per - 1) / per
	if pages < 1 {
		pages = 1
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	q := assetCols + from + cond + f.orderBy() + ` LIMIT ? OFFSET ?`
	rows, err := db.sql.QueryContext(ctx, q, append(args, per, (page-1)*per)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &AssetPage{Total: total, Page: page, Pages: pages}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		out.Assets = append(out.Assets, a)
	}
	return out, rows.Err()
}

// CountAssetsByStatus returns the number of assets per status.
func (db *DB) CountAssetsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT status, COUNT(*) FROM assets GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

// AuditByObject returns the audit entries of one object, newest first.
func (db *DB) AuditByObject(ctx context.Context, objectType, objectID string, limit int) ([]AuditEntry, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT a.id, a.ts, COALESCE(a.user_id, 0), COALESCE(u.username, ''),
		a.action, a.object_type, a.object_id, a.details
		FROM audit_log a LEFT JOIN users u ON u.id = a.user_id
		WHERE a.object_type = ? AND a.object_id = ? ORDER BY a.id DESC LIMIT ?`, objectType, objectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.UserID, &e.Username, &e.Action, &e.ObjectType, &e.ObjectID, &e.RawDetails); err != nil {
			return nil, err
		}
		e.TS = fromUnix(ts)
		_ = json.Unmarshal([]byte(e.RawDetails), &e.Details) // a malformed row is shown without details
		out = append(out, e)
	}
	return out, rows.Err()
}
