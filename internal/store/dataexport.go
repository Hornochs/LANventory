package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/lan-dot-party/lanventory/pkg/version"
)

// Format identification of the data export.
const (
	DataFormat        = "lanventory-export"
	DataFormatVersion = 1
)

// DataExport is the full-fidelity export of the inventory (see
// docs/concepts.md): locations, categories with their fields, assets with
// their custom field values and inspections. Users, sessions, tokens,
// settings and the audit log are not part of it.
type DataExport struct {
	Format     string         `json:"format"`
	Version    int            `json:"version"`
	ExportedAt string         `json:"exported_at"`
	AppVersion string         `json:"app_version"`
	Locations  []DataLocation `json:"locations"`
	Categories []DataCategory `json:"categories"`
	Assets     []DataAsset    `json:"assets"`
}

// DataLocation is a location in the export.
type DataLocation struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// DataCategory is a category with its field definitions in position order.
type DataCategory struct {
	Name               string      `json:"name"`
	Prefix             string      `json:"prefix"`
	DGUVRequired       bool        `json:"dguv_required"`
	DGUVIntervalMonths int         `json:"dguv_interval_months"`
	Fields             []DataField `json:"fields"`
}

// DataField is a field definition in the export.
type DataField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Unit     string   `json:"unit"`
	Options  []string `json:"options"`
	Required bool     `json:"required"`
}

// DataAsset is an asset with its field values and inspections. Values are
// exported exactly as stored.
type DataAsset struct {
	InventoryNo        string            `json:"inventory_no"`
	Category           string            `json:"category"`
	Name               string            `json:"name"`
	SerialNo           string            `json:"serial_no"`
	Manufacturer       string            `json:"manufacturer"`
	Model              string            `json:"model"`
	Location           string            `json:"location"`
	Status             string            `json:"status"`
	Note               string            `json:"note"`
	PurchaseDate       *string           `json:"purchase_date"`
	DGUVIntervalMonths *int              `json:"dguv_interval_months"`
	CreatedAt          string            `json:"created_at"`
	UpdatedAt          string            `json:"updated_at"`
	Fields             map[string]string `json:"fields"`
	Inspections        []DataInspection  `json:"inspections"`
}

// DataInspection is an inspection record in the export.
type DataInspection struct {
	InspectedOn     string   `json:"inspected_on"`
	InspectorName   string   `json:"inspector_name"`
	RecordedBy      string   `json:"recorded_by"`
	Result          string   `json:"result"`
	ProtectionClass string   `json:"protection_class"`
	VisualOK        bool     `json:"visual_ok"`
	FunctionOK      bool     `json:"function_ok"`
	PEResistanceOhm *float64 `json:"pe_resistance_ohm"`
	InsulationMOhm  *float64 `json:"insulation_mohm"`
	LeakageMA       *float64 `json:"leakage_ma"`
	TestDevice      string   `json:"test_device"`
	Remark          string   `json:"remark"`
	IntervalMonths  int      `json:"interval_months"`
	NextDue         string   `json:"next_due"`
	CreatedAt       string   `json:"created_at"`
	// Optional properties (absent in older files of format version 1).
	PEResistanceCmp           string   `json:"pe_resistance_cmp,omitempty"`
	InsulationCmp             string   `json:"insulation_cmp,omitempty"`
	LeakageCmp                string   `json:"leakage_cmp,omitempty"`
	LeakageMethod             string   `json:"leakage_method,omitempty"`
	TouchMA                   *float64 `json:"touch_ma,omitempty"`
	TouchCmp                  string   `json:"touch_cmp,omitempty"`
	Reason                    string   `json:"reason,omitempty"`
	TestDeviceID              string   `json:"test_device_id,omitempty"`
	TestDeviceCalibratedUntil string   `json:"test_device_calibrated_until,omitempty"`
}

func ts(v int64) string { return fromUnix(v).Format(time.RFC3339) }

// ExportData reads the whole inventory in one consistent snapshot. Assets are
// sorted by inventory number, inspections by date.
func (db *DB) ExportData(ctx context.Context) (*DataExport, error) {
	tx, err := db.sql.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // read only
	out := &DataExport{Format: DataFormat, Version: DataFormatVersion, ExportedAt: Now().Format(time.RFC3339),
		AppVersion: version.Version, Locations: []DataLocation{}, Categories: []DataCategory{}, Assets: []DataAsset{}}

	rows, err := tx.QueryContext(ctx, `SELECT name, description FROM locations ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var l DataLocation
		if err := rows.Scan(&l.Name, &l.Description); err != nil {
			rows.Close()
			return nil, err
		}
		out.Locations = append(out.Locations, l)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	catIdx := map[int64]int{}
	rows, err = tx.QueryContext(ctx, `SELECT id, name, prefix, dguv_required, dguv_interval_months FROM categories ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var c DataCategory
		var req int
		if err := rows.Scan(&id, &c.Name, &c.Prefix, &req, &c.DGUVIntervalMonths); err != nil {
			rows.Close()
			return nil, err
		}
		c.DGUVRequired = req == 1
		c.Fields = []DataField{}
		catIdx[id] = len(out.Categories)
		out.Categories = append(out.Categories, c)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	fieldKey := map[int64]string{}
	rows, err = tx.QueryContext(ctx, `SELECT `+fieldCols+` FROM category_fields ORDER BY category_id, position, id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		f, err := scanField(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		fieldKey[f.ID] = f.Key
		opts := f.Options
		if opts == nil {
			opts = []string{}
		}
		c := &out.Categories[catIdx[f.CategoryID]]
		c.Fields = append(c.Fields, DataField{Key: f.Key, Label: f.Label, Type: f.Type, Unit: f.Unit, Options: opts, Required: f.Required})
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	assetIdx := map[int64]int{}
	rows, err = tx.QueryContext(ctx, `SELECT a.id, a.inventory_no, c.name, a.name, a.serial_no, a.manufacturer, a.model,
		COALESCE(l.name, ''), a.status, a.note, a.purchase_date, a.dguv_interval_months, a.created_at, a.updated_at
		FROM assets a JOIN categories c ON c.id = a.category_id LEFT JOIN locations l ON l.id = a.location_id
		ORDER BY a.inventory_no COLLATE NOCASE, a.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, cr, up int64
		var a DataAsset
		var pd sql.NullString
		var iv sql.NullInt64
		if err := rows.Scan(&id, &a.InventoryNo, &a.Category, &a.Name, &a.SerialNo, &a.Manufacturer, &a.Model, &a.Location,
			&a.Status, &a.Note, &pd, &iv, &cr, &up); err != nil {
			rows.Close()
			return nil, err
		}
		if pd.Valid {
			a.PurchaseDate = &pd.String
		}
		if iv.Valid {
			n := int(iv.Int64)
			a.DGUVIntervalMonths = &n
		}
		a.CreatedAt, a.UpdatedAt = ts(cr), ts(up)
		a.Fields = map[string]string{}
		a.Inspections = []DataInspection{}
		assetIdx[id] = len(out.Assets)
		out.Assets = append(out.Assets, a)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT asset_id, field_id, value FROM asset_field_values`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var aid, fid int64
		var v string
		if err := rows.Scan(&aid, &fid, &v); err != nil {
			rows.Close()
			return nil, err
		}
		out.Assets[assetIdx[aid]].Fields[fieldKey[fid]] = v
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}

	rows, err = tx.QueryContext(ctx, `SELECT i.asset_id, i.inspected_on, i.inspector_name, COALESCE(u.username, ''), i.result,
		COALESCE(i.protection_class, ''), i.visual_ok, i.function_ok, i.pe_resistance_ohm, i.insulation_mohm, i.leakage_ma,
		i.test_device, i.remark, i.interval_months, i.next_due, i.created_at,
		i.pe_resistance_cmp, i.insulation_cmp, i.leakage_cmp, i.leakage_method, i.touch_ma, i.touch_cmp, i.reason, i.test_device_id,
		COALESCE(i.test_device_calibrated_until, '')
		FROM dguv_inspections i LEFT JOIN users u ON u.id = i.recorded_by ORDER BY i.inspected_on, i.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var aid, cr int64
		var in DataInspection
		var vis, fun int
		var pe, ins, leak, touch sql.NullFloat64
		if err := rows.Scan(&aid, &in.InspectedOn, &in.InspectorName, &in.RecordedBy, &in.Result, &in.ProtectionClass, &vis, &fun,
			&pe, &ins, &leak, &in.TestDevice, &in.Remark, &in.IntervalMonths, &in.NextDue, &cr,
			&in.PEResistanceCmp, &in.InsulationCmp, &in.LeakageCmp, &in.LeakageMethod, &touch, &in.TouchCmp, &in.Reason,
			&in.TestDeviceID, &in.TestDeviceCalibratedUntil); err != nil {
			rows.Close()
			return nil, err
		}
		in.VisualOK, in.FunctionOK = vis != 0, fun != 0
		in.PEResistanceOhm, in.InsulationMOhm, in.LeakageMA = nullFloat(pe), nullFloat(ins), nullFloat(leak)
		in.TouchMA = nullFloat(touch)
		in.CreatedAt = ts(cr)
		a := &out.Assets[assetIdx[aid]]
		a.Inspections = append(a.Inspections, in)
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return out, nil
}

func closeRows(rows *sql.Rows) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}

func nullFloat(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	f := n.Float64
	return &f
}

// Summary returns the counts of an export, for the CLI and the UI.
func (d *DataExport) Summary() (locations, categories, assets, inspections int) {
	for i := range d.Assets {
		inspections += len(d.Assets[i].Inspections)
	}
	return len(d.Locations), len(d.Categories), len(d.Assets), inspections
}
