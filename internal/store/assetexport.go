package store

import (
	"context"
	"strings"
)

// MaxExportRows is the hard cap of the asset CSV export; AssetExport.Truncated
// reports that more assets matched.
const MaxExportRows = 50000

// AssetExportRow is one asset of the CSV export.
type AssetExportRow struct {
	Asset *Asset
	// LastInspected is the date of the latest inspection, empty if never inspected.
	LastInspected string
	// NextDue is the next due date of the latest inspection; empty when the
	// category does not require DGUV V3 or the asset was never inspected.
	NextDue string
	// Values holds the custom field values by field key.
	Values map[string]string
}

// AssetExport is the result of ExportAssets.
type AssetExport struct {
	Rows []AssetExportRow
	// FieldKeys is the union of the custom field keys of the categories in the
	// result (sorted by category name, then position; a key shared by several
	// categories appears once).
	FieldKeys []string
	// Truncated is set when more than MaxExportRows assets matched.
	Truncated bool
}

// rowPlus scans extra columns after the asset columns.
type rowPlus struct {
	row   interface{ Scan(...any) error }
	extra []any
}

func (r rowPlus) Scan(dest ...any) error { return r.row.Scan(append(dest, r.extra...)...) }

// ExportAssets returns all assets matching the filter (paging is ignored,
// the sort is honoured) with their latest inspection and custom values, at
// most MaxExportRows.
func (db *DB) ExportAssets(ctx context.Context, f AssetFilter) (*AssetExport, error) {
	cond, args := f.where()
	q := assetCols + `, COALESCE(i.inspected_on, ''), COALESCE(i.next_due, ''), c.dguv_required` + assetFrom +
		` LEFT JOIN dguv_inspections i ON i.id = (SELECT x.id FROM dguv_inspections x WHERE x.asset_id = a.id
			ORDER BY x.inspected_on DESC, x.id DESC LIMIT 1)` + cond + f.orderBy() + ` LIMIT ?`
	rows, err := db.sql.QueryContext(ctx, q, append(args, MaxExportRows+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &AssetExport{}
	byID := map[int64]*AssetExportRow{}
	cats := map[int64]bool{}
	var catOrder []int64
	for rows.Next() {
		var last, next string
		var required bool
		a, err := scanAsset(rowPlus{rows, []any{&last, &next, &required}})
		if err != nil {
			return nil, err
		}
		if len(out.Rows) == MaxExportRows {
			out.Truncated = true
			break
		}
		if !required {
			next = ""
		}
		out.Rows = append(out.Rows, AssetExportRow{Asset: a, LastInspected: last, NextDue: next, Values: map[string]string{}})
		if !cats[a.CategoryID] {
			cats[a.CategoryID] = true
			catOrder = append(catOrder, a.CategoryID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out.Rows {
		byID[out.Rows[i].Asset.ID] = &out.Rows[i]
	}
	if len(out.Rows) > 0 {
		vq := `SELECT xv.asset_id, xf.key, xv.value FROM asset_field_values xv JOIN category_fields xf ON xf.id = xv.field_id
			JOIN assets a ON a.id = xv.asset_id JOIN categories c ON c.id = a.category_id
			LEFT JOIN locations l ON l.id = a.location_id` + cond
		vr, err := db.sql.QueryContext(ctx, vq, args...)
		if err != nil {
			return nil, err
		}
		defer vr.Close()
		for vr.Next() {
			var id int64
			var key, val string
			if err := vr.Scan(&id, &key, &val); err != nil {
				return nil, err
			}
			if r := byID[id]; r != nil {
				r.Values[key] = val
			}
		}
		if err := vr.Err(); err != nil {
			return nil, err
		}
	}
	if f.CategoryID != 0 && !cats[f.CategoryID] {
		catOrder = append(catOrder, f.CategoryID)
	}
	if len(catOrder) > 0 {
		ph := strings.TrimSuffix(strings.Repeat("?,", len(catOrder)), ",")
		ids := make([]any, len(catOrder))
		for i, id := range catOrder {
			ids[i] = id
		}
		kr, err := db.sql.QueryContext(ctx, `SELECT cf.key FROM category_fields cf JOIN categories c ON c.id = cf.category_id
			WHERE cf.category_id IN (`+ph+`) ORDER BY c.name COLLATE NOCASE, c.id, cf.position, cf.id`, ids...)
		if err != nil {
			return nil, err
		}
		defer kr.Close()
		seen := map[string]bool{}
		for kr.Next() {
			var k string
			if err := kr.Scan(&k); err != nil {
				return nil, err
			}
			if !seen[k] {
				seen[k] = true
				out.FieldKeys = append(out.FieldKeys, k)
			}
		}
		if err := kr.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
