package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Asset statuses.
const (
	StatusActive    = "active"
	StatusDefective = "defective"
	StatusRetired   = "retired"
	StatusLost      = "lost"
)

// Statuses lists the valid asset statuses.
var Statuses = []string{StatusActive, StatusDefective, StatusRetired, StatusLost}

// ValidStatus reports whether s is a known status.
func ValidStatus(s string) bool {
	for _, v := range Statuses {
		if s == v {
			return true
		}
	}
	return false
}

// Asset is one physical item.
type Asset struct {
	ID             int64
	InventoryNo    string
	CategoryID     int64
	CategoryName   string // read only
	CategoryPrefix string // read only
	Name           string
	SerialNo       string
	Manufacturer   string
	Model          string
	// LocationID is 0 for "no location".
	LocationID   int64
	LocationName string // read only
	Status       string
	Note         string
	// PurchaseDate is YYYY-MM-DD or empty.
	PurchaseDate string
	// DGUVIntervalMonths overrides the category's interval; 0 = none.
	DGUVIntervalMonths int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// Values holds the custom field values by field ID. On input they are raw
	// form values; Create and Update normalize them (empty = no value).
	Values map[int64]string
}

const assetFrom = ` FROM assets a JOIN categories c ON c.id = a.category_id LEFT JOIN locations l ON l.id = a.location_id`

const assetCols = `SELECT a.id, a.inventory_no, a.category_id, c.name, c.prefix, a.name, a.serial_no, a.manufacturer, a.model,
	COALESCE(a.location_id, 0), COALESCE(l.name, ''), a.status, a.note, COALESCE(a.purchase_date, ''),
	COALESCE(a.dguv_interval_months, 0), a.created_at, a.updated_at`

const assetSelect = assetCols + assetFrom

func scanAsset(row interface{ Scan(...any) error }) (*Asset, error) {
	var a Asset
	var cr, up int64
	err := row.Scan(&a.ID, &a.InventoryNo, &a.CategoryID, &a.CategoryName, &a.CategoryPrefix, &a.Name, &a.SerialNo,
		&a.Manufacturer, &a.Model, &a.LocationID, &a.LocationName, &a.Status, &a.Note, &a.PurchaseDate,
		&a.DGUVIntervalMonths, &cr, &up)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.CreatedAt, a.UpdatedAt = fromUnix(cr), fromUnix(up)
	return &a, nil
}

func assetValues(ctx context.Context, q queryer, assetID int64) (map[int64]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT field_id, value FROM asset_field_values WHERE asset_id = ?`, assetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}

func getAsset(ctx context.Context, q queryer, id int64) (*Asset, error) {
	a, err := scanAsset(q.QueryRowContext(ctx, assetSelect+` WHERE a.id = ?`, id))
	if err != nil {
		return nil, err
	}
	a.Values, err = assetValues(ctx, q, id)
	return a, err
}

// AssetByID returns an asset with its custom field values.
func (db *DB) AssetByID(ctx context.Context, id int64) (*Asset, error) {
	return getAsset(ctx, db.sql, id)
}

// AssetByNumber returns an asset by its inventory number (case-insensitive)
// with its custom field values.
func (db *DB) AssetByNumber(ctx context.Context, inventoryNo string) (*Asset, error) {
	a, err := scanAsset(db.sql.QueryRowContext(ctx, assetSelect+` WHERE a.inventory_no = ?`, strings.TrimSpace(inventoryNo)))
	if err != nil {
		return nil, err
	}
	a.Values, err = assetValues(ctx, db.sql, a.ID)
	return a, err
}

// LoadAssetValues fills Values of the given assets (list results come without them).
func (db *DB) LoadAssetValues(ctx context.Context, assets []*Asset) error {
	for _, a := range assets {
		v, err := assetValues(ctx, db.sql, a.ID)
		if err != nil {
			return err
		}
		a.Values = v
	}
	return nil
}

// validateAsset normalizes the core fields and the custom field values of a
// (in place) and reports problems per form field name; custom fields are
// named "f_<key>". It also resolves the location name.
func validateAsset(ctx context.Context, q queryer, a *Asset, fields []Field) *ValidationError {
	ve := newVE()
	var code string
	if a.Name, code = cleanLine(a.Name, 200); code != "" {
		ve.add("name", code)
	} else if a.Name == "" {
		ve.add("name", "required")
	}
	for _, p := range []struct {
		field string
		v     *string
	}{{"serial_no", &a.SerialNo}, {"manufacturer", &a.Manufacturer}, {"model", &a.Model}} {
		if *p.v, code = cleanLine(*p.v, 100); code != "" {
			ve.add(p.field, code)
		}
	}
	if a.Note, code = cleanText(a.Note, 2000); code != "" {
		ve.add("note", code)
	}
	if a.Status == "" {
		a.Status = StatusActive
	}
	if !ValidStatus(a.Status) {
		ve.add("status", "invalid_status")
	}
	a.PurchaseDate = strings.TrimSpace(a.PurchaseDate)
	if a.PurchaseDate != "" {
		t, err := time.Parse(DateLayout, a.PurchaseDate)
		if err != nil || t.Year() < 1900 || t.Year() > 2200 {
			ve.add("purchase_date", "invalid_date")
		} else {
			a.PurchaseDate = t.Format(DateLayout)
		}
	}
	if a.DGUVIntervalMonths != 0 && (a.DGUVIntervalMonths < 1 || a.DGUVIntervalMonths > 120) {
		ve.add("dguv_interval", "invalid_interval")
	}
	a.LocationName = ""
	if a.LocationID != 0 {
		err := q.QueryRowContext(ctx, `SELECT name FROM locations WHERE id = ?`, a.LocationID).Scan(&a.LocationName)
		if err != nil {
			ve.add("location", "invalid_location")
		}
	}
	norm := map[int64]string{}
	for i := range fields {
		f := &fields[i]
		v, code := NormalizeValue(f, a.Values[f.ID])
		name := "f_" + f.Key
		switch {
		case code != "":
			ve.add(name, code)
		case v == "" && f.Required:
			ve.add(name, "required")
		case v != "":
			norm[f.ID] = v
		}
	}
	a.Values = norm
	return ve
}

func fieldVE(field, code string) error {
	ve := newVE()
	ve.add(field, code)
	return ve
}

// CreateAsset validates and inserts an asset with its field values. When
// a.InventoryNo is empty the next number for the category prefix is generated
// from numberFormat in the same transaction; a manual number that is taken
// yields a ValidationError on "inventory_no" with code "taken". It returns
// the changes (field → [old, new]) for the audit log.
func (db *DB) CreateAsset(ctx context.Context, a *Asset, numberFormat string) (map[string][2]string, error) {
	var changes map[string][2]string
	err := db.InTx(ctx, func(tx *Tx) error {
		var err error
		changes, err = createAsset(ctx, tx.tx, a, numberFormat)
		return err
	})
	if err != nil {
		return nil, err
	}
	return changes, nil
}

// createAsset is CreateAsset inside the caller's transaction (also used by the CSV import).
func createAsset(ctx context.Context, q queryer, a *Asset, numberFormat string) (map[string][2]string, error) {
	cat, err := scanCategory(q.QueryRowContext(ctx, `SELECT `+categoryCols+` FROM categories c WHERE c.id = ?`, a.CategoryID))
	if errors.Is(err, ErrNotFound) {
		return nil, fieldVE("category", "invalid_category")
	}
	if err != nil {
		return nil, err
	}
	fields, err := categoryFields(ctx, q, cat.ID)
	if err != nil {
		return nil, err
	}
	ve := validateAsset(ctx, q, a, fields)
	manual := strings.TrimSpace(a.InventoryNo)
	if manual != "" {
		var code string
		if manual, code = cleanLine(manual, 64); code != "" {
			ve.add("inventory_no", code)
		}
	}
	if e := ve.err(); e != nil {
		return nil, e
	}
	a.CategoryName, a.CategoryPrefix = cat.Name, cat.Prefix
	if manual != "" {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE inventory_no = ?`, manual).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, fieldVE("inventory_no", "taken")
		}
		a.InventoryNo = manual
	} else {
		if a.InventoryNo, err = nextInventoryNo(ctx, q, numberFormat, cat.Prefix); err != nil {
			return nil, err
		}
	}
	now := Now()
	res, err := q.ExecContext(ctx, `INSERT INTO assets (inventory_no, category_id, name, serial_no, manufacturer, model,
		location_id, status, note, purchase_date, dguv_interval_months, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.InventoryNo, a.CategoryID, a.Name, a.SerialNo, a.Manufacturer, a.Model, nullInt(a.LocationID), a.Status, a.Note,
		nullStr(a.PurchaseDate), nullInt(int64(a.DGUVIntervalMonths)), unix(now), unix(now))
	if isUnique(err) {
		return nil, fieldVE("inventory_no", "taken")
	}
	if err != nil {
		return nil, err
	}
	if a.ID, err = res.LastInsertId(); err != nil {
		return nil, err
	}
	a.CreatedAt, a.UpdatedAt = now, now
	for fid, v := range a.Values {
		if _, err := q.ExecContext(ctx, `INSERT INTO asset_field_values (asset_id, field_id, value) VALUES (?, ?, ?)`,
			a.ID, fid, v); err != nil {
			return nil, err
		}
	}
	return AssetChanges(&Asset{}, a, fields), nil
}

// UpdateAsset saves the editable data of an asset. The inventory number does
// not change. When a.CategoryID names another category, the asset moves
// there: a.Values must then be keyed by the new category's fields (see
// CarryValues), and values of the old category's fields are removed. It
// returns the changes (field → [old, new]) for the audit log; the map is
// empty when nothing changed.
func (db *DB) UpdateAsset(ctx context.Context, a *Asset) (map[string][2]string, error) {
	var changes map[string][2]string
	err := db.InTx(ctx, func(tx *Tx) error {
		var err error
		changes, err = updateAsset(ctx, tx.tx, a)
		return err
	})
	if err != nil {
		return nil, err
	}
	return changes, nil
}

// updateAsset is UpdateAsset inside the caller's transaction (also used by the CSV import).
func updateAsset(ctx context.Context, q queryer, a *Asset) (map[string][2]string, error) {
	old, err := getAsset(ctx, q, a.ID)
	if err != nil {
		return nil, err
	}
	oldFields, err := categoryFields(ctx, q, old.CategoryID)
	if err != nil {
		return nil, err
	}
	fields := oldFields
	if a.CategoryID == 0 || a.CategoryID == old.CategoryID {
		a.CategoryID, a.CategoryName, a.CategoryPrefix = old.CategoryID, old.CategoryName, old.CategoryPrefix
	} else {
		cat, err := getCategory(ctx, q, a.CategoryID)
		if errors.Is(err, ErrNotFound) {
			return nil, fieldVE("category", "invalid_category")
		} else if err != nil {
			return nil, err
		}
		a.CategoryName, a.CategoryPrefix = cat.Name, cat.Prefix
		if fields, err = categoryFields(ctx, q, a.CategoryID); err != nil {
			return nil, err
		}
	}
	a.InventoryNo = old.InventoryNo
	if e := validateAsset(ctx, q, a, fields).err(); e != nil {
		return nil, e
	}
	changes := assetChangesAcross(old, oldFields, a, fields)
	if len(changes) == 0 {
		return changes, nil
	}
	now := Now()
	if _, err := q.ExecContext(ctx, `UPDATE assets SET category_id = ?, name = ?, serial_no = ?, manufacturer = ?, model = ?,
		location_id = ?, status = ?, note = ?, purchase_date = ?, dguv_interval_months = ?, updated_at = ? WHERE id = ?`,
		a.CategoryID, a.Name, a.SerialNo, a.Manufacturer, a.Model, nullInt(a.LocationID), a.Status, a.Note, nullStr(a.PurchaseDate),
		nullInt(int64(a.DGUVIntervalMonths)), unix(now), a.ID); err != nil {
		return nil, err
	}
	a.UpdatedAt = now
	if a.CategoryID != old.CategoryID {
		if _, err := q.ExecContext(ctx, `DELETE FROM asset_field_values WHERE asset_id = ?
			AND field_id NOT IN (SELECT id FROM category_fields WHERE category_id = ?)`, a.ID, a.CategoryID); err != nil {
			return nil, err
		}
	}
	for i := range fields {
		fid := fields[i].ID
		if v, ok := a.Values[fid]; ok {
			_, err = q.ExecContext(ctx, `INSERT INTO asset_field_values (asset_id, field_id, value) VALUES (?, ?, ?)
				ON CONFLICT (asset_id, field_id) DO UPDATE SET value = excluded.value`, a.ID, fid, v)
		} else {
			_, err = q.ExecContext(ctx, `DELETE FROM asset_field_values WHERE asset_id = ? AND field_id = ?`, a.ID, fid)
		}
		if err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// DeleteAsset removes an asset and its values.
func (db *DB) DeleteAsset(ctx context.Context, id int64) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AssetChanges lists what differs between old and new as field → [old, new].
// Core fields use their column names (the location by name); custom fields
// are "field:<key>". Bool values are "1"/"0". For a new asset pass an empty
// Asset as old.
func AssetChanges(old, new *Asset, fields []Field) map[string][2]string {
	out := map[string][2]string{}
	cmp := func(k, o, n string) {
		if o != n {
			out[k] = [2]string{o, n}
		}
	}
	cmp("inventory_no", old.InventoryNo, new.InventoryNo)
	cmp("category", old.CategoryName, new.CategoryName)
	cmp("name", old.Name, new.Name)
	cmp("serial_no", old.SerialNo, new.SerialNo)
	cmp("manufacturer", old.Manufacturer, new.Manufacturer)
	cmp("model", old.Model, new.Model)
	cmp("location", old.LocationName, new.LocationName)
	cmp("status", old.Status, new.Status)
	cmp("note", old.Note, new.Note)
	cmp("purchase_date", old.PurchaseDate, new.PurchaseDate)
	cmp("dguv_interval", itoaNonZero(old.DGUVIntervalMonths), itoaNonZero(new.DGUVIntervalMonths))
	for i := range fields {
		f := &fields[i]
		cmp("field:"+f.Key, old.Values[f.ID], new.Values[f.ID])
	}
	return out
}

// assetChangesAcross is AssetChanges for an asset that may have moved to
// another category: custom fields are compared by key.
func assetChangesAcross(old *Asset, oldFields []Field, new *Asset, newFields []Field) map[string][2]string {
	out := AssetChanges(old, new, nil)
	vals := map[string][2]string{}
	for i := range oldFields {
		f := &oldFields[i]
		vals[f.Key] = [2]string{old.Values[f.ID], ""}
	}
	for i := range newFields {
		f := &newFields[i]
		v := vals[f.Key]
		v[1] = new.Values[f.ID]
		vals[f.Key] = v
	}
	for k, v := range vals {
		if v[0] != v[1] {
			out["field:"+k] = v
		}
	}
	return out
}

// CarryValues maps the custom values of an asset to the fields of another
// category: a value moves to the field with the same key when it is valid
// for that field's type. It returns the new values (by new field ID) and the
// old fields whose values are lost.
func CarryValues(a *Asset, oldFields, newFields []Field) (map[int64]string, []Field) {
	byKey := map[string]*Field{}
	for i := range newFields {
		byKey[newFields[i].Key] = &newFields[i]
	}
	out := map[int64]string{}
	var lost []Field
	for i := range oldFields {
		f := &oldFields[i]
		v := a.Values[f.ID]
		if v == "" {
			continue
		}
		if nf := byKey[f.Key]; nf != nil {
			if nv, code := NormalizeValue(nf, v); code == "" && nv != "" {
				out[nf.ID] = nv
				continue
			}
		}
		lost = append(lost, *f)
	}
	return out, lost
}

func itoaNonZero(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
