package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Category groups similar assets and defines their custom fields.
type Category struct {
	ID                 int64
	Name               string
	Prefix             string
	DGUVRequired       bool
	DGUVIntervalMonths int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// AssetCount is filled by ListCategories and CategoryByID.
	AssetCount int
}

// Field is the definition of a custom field of a category.
type Field struct {
	ID         int64
	CategoryID int64
	Key        string
	Label      string
	Type       string
	Unit       string
	Options    []string
	Required   bool
	Position   int
}

var (
	prefixRE = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)
	keyRE    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

const (
	maxOptions      = 50
	maxOptionLength = 64
)

func (c *Category) validate() *ValidationError {
	ve := newVE()
	var code string
	if c.Name, code = cleanLine(c.Name, 64); code != "" {
		ve.add("name", code)
	} else if c.Name == "" {
		ve.add("name", "required")
	}
	c.Prefix = strings.ToUpper(strings.TrimSpace(c.Prefix))
	if c.Prefix != "" && !prefixRE.MatchString(c.Prefix) {
		ve.add("prefix", "invalid_prefix")
	}
	if c.DGUVIntervalMonths == 0 {
		c.DGUVIntervalMonths = 12
	}
	if c.DGUVIntervalMonths < 1 || c.DGUVIntervalMonths > 120 {
		ve.add("interval", "invalid_interval")
	}
	return ve
}

func uniqueCategory(err error) *ValidationError {
	ve := newVE()
	if strings.Contains(err.Error(), "prefix") {
		ve.add("prefix", "taken")
	} else {
		ve.add("name", "taken")
	}
	return ve
}

// CreateCategory inserts a category.
func (db *DB) CreateCategory(ctx context.Context, c *Category) error {
	if ve := c.validate(); ve.err() != nil {
		return ve
	}
	return insertCategory(ctx, db.sql, c)
}

func insertCategory(ctx context.Context, q queryer, c *Category) error {
	now := Now()
	res, err := q.ExecContext(ctx, `INSERT INTO categories (name, prefix, dguv_required, dguv_interval_months, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, c.Name, c.Prefix, boolInt(c.DGUVRequired), c.DGUVIntervalMonths, unix(now), unix(now))
	if isUnique(err) {
		return uniqueCategory(err)
	}
	if err != nil {
		return err
	}
	c.ID, err = res.LastInsertId()
	c.CreatedAt, c.UpdatedAt = now, now
	return err
}

// UpdateCategory saves name, prefix and DGUV settings. A changed prefix does
// not renumber existing assets.
func (db *DB) UpdateCategory(ctx context.Context, c *Category) error {
	if ve := c.validate(); ve.err() != nil {
		return ve
	}
	res, err := db.sql.ExecContext(ctx, `UPDATE categories SET name = ?, prefix = ?, dguv_required = ?,
		dguv_interval_months = ?, updated_at = ? WHERE id = ?`,
		c.Name, c.Prefix, boolInt(c.DGUVRequired), c.DGUVIntervalMonths, unix(Now()), c.ID)
	if isUnique(err) {
		return uniqueCategory(err)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCategory removes a category and its field definitions; ErrInUse while
// assets use it.
func (db *DB) DeleteCategory(ctx context.Context, id int64) error {
	return db.InTx(ctx, func(tx *Tx) error {
		var n int
		if err := tx.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE category_id = ?`, id).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrInUse
		}
		res, err := tx.tx.ExecContext(ctx, `DELETE FROM categories WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

const categoryCols = `c.id, c.name, c.prefix, c.dguv_required, c.dguv_interval_months, c.created_at, c.updated_at,
	(SELECT COUNT(*) FROM assets a WHERE a.category_id = c.id)`

func scanCategory(row interface{ Scan(...any) error }) (*Category, error) {
	var c Category
	var cr, up int64
	var req int
	err := row.Scan(&c.ID, &c.Name, &c.Prefix, &req, &c.DGUVIntervalMonths, &cr, &up, &c.AssetCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.DGUVRequired = req == 1
	c.CreatedAt, c.UpdatedAt = fromUnix(cr), fromUnix(up)
	return &c, nil
}

// CategoryByID returns a category.
func (db *DB) CategoryByID(ctx context.Context, id int64) (*Category, error) {
	return getCategory(ctx, db.sql, id)
}

func getCategory(ctx context.Context, q queryer, id int64) (*Category, error) {
	return scanCategory(q.QueryRowContext(ctx, `SELECT `+categoryCols+` FROM categories c WHERE c.id = ?`, id))
}

// ListCategories returns all categories by name with their asset counts.
func (db *DB) ListCategories(ctx context.Context) ([]*Category, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+categoryCols+` FROM categories c ORDER BY c.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Category
	for rows.Next() {
		c, err := scanCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- fields ---

const fieldCols = `id, category_id, key, label, type, unit, options, required, position`

func scanField(row interface{ Scan(...any) error }) (*Field, error) {
	var f Field
	var opts string
	var req int
	err := row.Scan(&f.ID, &f.CategoryID, &f.Key, &f.Label, &f.Type, &f.Unit, &opts, &req, &f.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f.Required = req == 1
	if opts != "" {
		_ = json.Unmarshal([]byte(opts), &f.Options) // a malformed row has no options
	}
	return &f, nil
}

func categoryFields(ctx context.Context, q queryer, categoryID int64) ([]Field, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+fieldCols+` FROM category_fields WHERE category_id = ? ORDER BY position, id`, categoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Field
	for rows.Next() {
		f, err := scanField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// CategoryFields returns the field definitions of a category in order.
func (db *DB) CategoryFields(ctx context.Context, categoryID int64) ([]Field, error) {
	return categoryFields(ctx, db.sql, categoryID)
}

// FieldByID returns one field definition.
func (db *DB) FieldByID(ctx context.Context, id int64) (*Field, error) {
	return scanField(db.sql.QueryRowContext(ctx, `SELECT `+fieldCols+` FROM category_fields WHERE id = ?`, id))
}

func (f *Field) validate(requireKey bool) *ValidationError {
	ve := newVE()
	var code string
	if requireKey {
		f.Key = strings.TrimSpace(f.Key)
		if !keyRE.MatchString(f.Key) {
			ve.add("key", "invalid_key")
		}
	}
	if f.Label, code = cleanLine(f.Label, 64); code != "" {
		ve.add("label", code)
	} else if f.Label == "" {
		ve.add("label", "required")
	}
	if !ValidFieldType(f.Type) {
		ve.add("type", "invalid_type")
		return ve
	}
	if f.Unit, code = cleanLine(f.Unit, 16); code != "" {
		ve.add("unit", code)
	}
	if f.Type != FieldNumber {
		f.Unit = ""
	}
	if f.Type != FieldSelect {
		f.Options = nil
		return ve
	}
	seen := map[string]bool{}
	var opts []string
	for _, o := range f.Options {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if utf8.RuneCountInString(o) > maxOptionLength || seen[o] {
			ve.add("options", "invalid_options")
			break
		}
		seen[o] = true
		opts = append(opts, o)
	}
	if len(opts) == 0 {
		ve.add("options", "invalid_options")
	}
	if len(opts) > maxOptions {
		ve.add("options", "invalid_options")
	}
	f.Options = opts
	return ve
}

func optionsJSON(f *Field) string {
	if f.Type != FieldSelect {
		return ""
	}
	b, _ := json.Marshal(f.Options) // a string slice always marshals
	return string(b)
}

// CreateField adds a field at the end of the category's list.
func (db *DB) CreateField(ctx context.Context, f *Field) error {
	if ve := f.validate(true); ve.err() != nil {
		return ve
	}
	return insertField(ctx, db.sql, f)
}

func insertField(ctx context.Context, q queryer, f *Field) error {
	var pos int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM category_fields WHERE category_id = ?`,
		f.CategoryID).Scan(&pos); err != nil {
		return err
	}
	f.Position = pos
	res, err := q.ExecContext(ctx, `INSERT INTO category_fields (category_id, key, label, type, unit, options, required, position)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, f.CategoryID, f.Key, f.Label, f.Type, f.Unit, optionsJSON(f), boolInt(f.Required), pos)
	if isUnique(err) {
		ve := newVE()
		ve.add("key", "taken")
		return ve
	}
	if err != nil && strings.Contains(err.Error(), "FOREIGN KEY") {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	f.ID, err = res.LastInsertId()
	return err
}

// UpdateField saves label, type, unit, options and the required flag (the key
// and position stay). The type cannot change while values exist for the
// field, and an option that is in use cannot be removed.
func (db *DB) UpdateField(ctx context.Context, f *Field) error {
	if ve := f.validate(false); ve.err() != nil {
		return ve
	}
	return db.InTx(ctx, func(tx *Tx) error {
		old, err := scanField(tx.tx.QueryRowContext(ctx, `SELECT `+fieldCols+` FROM category_fields WHERE id = ?`, f.ID))
		if err != nil {
			return err
		}
		ve := newVE()
		if old.Type != f.Type {
			var n int
			if err := tx.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_field_values WHERE field_id = ?`, f.ID).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				ve.add("type", "type_locked")
				return ve
			}
		} else if f.Type == FieldSelect {
			keep := map[string]bool{}
			for _, o := range f.Options {
				keep[o] = true
			}
			for _, o := range old.Options {
				if keep[o] {
					continue
				}
				var n int
				if err := tx.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM asset_field_values WHERE field_id = ? AND value = ?`,
					f.ID, o).Scan(&n); err != nil {
					return err
				}
				if n > 0 {
					ve.add("options", "option_in_use")
					return ve
				}
			}
		}
		_, err = tx.tx.ExecContext(ctx, `UPDATE category_fields SET label = ?, type = ?, unit = ?, options = ?, required = ? WHERE id = ?`,
			f.Label, f.Type, f.Unit, optionsJSON(f), boolInt(f.Required), f.ID)
		if err != nil {
			return err
		}
		f.CategoryID, f.Key, f.Position = old.CategoryID, old.Key, old.Position
		return nil
	})
}

// DeleteField removes a field definition and all values stored for it.
func (db *DB) DeleteField(ctx context.Context, id int64) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM category_fields WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MoveField moves a field one place up (dir < 0) or down (dir > 0) within its category.
func (db *DB) MoveField(ctx context.Context, id int64, dir int) error {
	return db.InTx(ctx, func(tx *Tx) error {
		f, err := scanField(tx.tx.QueryRowContext(ctx, `SELECT `+fieldCols+` FROM category_fields WHERE id = ?`, id))
		if err != nil {
			return err
		}
		fields, err := categoryFields(ctx, tx.tx, f.CategoryID)
		if err != nil {
			return err
		}
		idx := -1
		for i := range fields {
			if fields[i].ID == id {
				idx = i
			}
		}
		to := idx + 1
		if dir < 0 {
			to = idx - 1
		}
		if idx < 0 || to < 0 || to >= len(fields) {
			return nil
		}
		fields[idx], fields[to] = fields[to], fields[idx]
		for i := range fields {
			if _, err := tx.tx.ExecContext(ctx, `UPDATE category_fields SET position = ? WHERE id = ?`, i, fields[i].ID); err != nil {
				return err
			}
		}
		return nil
	})
}
