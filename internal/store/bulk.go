package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// MaxBulkAssets is the most assets one bulk operation may touch.
const MaxBulkAssets = 1000

var (
	// ErrBulkTooMany is returned when more than MaxBulkAssets assets are selected.
	ErrBulkTooMany = errors.New("too many assets for one bulk change")
	// ErrBulkEmpty is returned when no asset is selected.
	ErrBulkEmpty = errors.New("no assets selected")
)

// BulkNotFoundError lists selected asset IDs that do not exist.
type BulkNotFoundError struct{ IDs []int64 }

// Error implements error.
func (e *BulkNotFoundError) Error() string {
	parts := make([]string, len(e.IDs))
	for i, id := range e.IDs {
		parts[i] = fmt.Sprint(id)
	}
	return "unknown asset ids: " + strings.Join(parts, ", ")
}

// BulkChange describes what a bulk update changes. Every part is optional
// (nil = leave unchanged). Name, serial number and inventory number are
// deliberately not bulk-editable.
type BulkChange struct {
	CategoryID *int64
	// LocationID 0 clears the location.
	LocationID   *int64
	Status       *string
	Manufacturer *string
	Model        *string
	// PurchaseDate is YYYY-MM-DD; empty clears it.
	PurchaseDate *string
	// DGUVIntervalMonths 0 means "category default".
	DGUVIntervalMonths *int
	// Note replaces the note, or is appended as a new line when NoteAppend is set.
	Note       *string
	NoteAppend bool
	// Values sets custom field values by field KEY on the asset's (target)
	// category; a nil pointer clears the value.
	Values map[string]*string
}

// Empty reports whether the change modifies nothing.
func (c BulkChange) Empty() bool {
	return c.CategoryID == nil && c.LocationID == nil && c.Status == nil && c.Manufacturer == nil && c.Model == nil &&
		c.PurchaseDate == nil && c.DGUVIntervalMonths == nil && c.Note == nil && len(c.Values) == 0
}

// BulkError is one validation problem of one asset. Field uses the form
// field names of ValidationError ("category", "location", "f_<key>", …).
type BulkError struct {
	AssetID     int64
	InventoryNo string
	Field       string
	Code        string
}

// BulkValidationError is returned (together with the result) when at least
// one asset is invalid; nothing is written then.
type BulkValidationError struct{ Errors []BulkError }

// Error implements error.
func (e *BulkValidationError) Error() string {
	return fmt.Sprintf("bulk change invalid for %d problem(s)", len(e.Errors))
}

// BulkLost is a custom value dropped by a category move.
type BulkLost struct {
	Key   string
	Label string
	Value string
	Unit  string
	Type  string
}

// BulkItem is the outcome for one asset.
type BulkItem struct {
	ID          int64
	InventoryNo string
	Name        string
	// Changes is field → [old, new] as for the audit log; empty = unchanged.
	Changes map[string][2]string
	Lost    []BulkLost
	// Asset is the asset as saved (Values keyed by the target category's field IDs).
	Asset *Asset
}

// BulkResult is the outcome of BulkUpdateAssets.
type BulkResult struct {
	Items []BulkItem
	// Errors lists validation problems; when set nothing was written.
	Errors []BulkError
	// Changed counts the items with at least one change.
	Changed int
	DryRun  bool
}

// BulkUpdateAssets applies ch to the assets ids in one transaction: all or
// nothing. With dryRun everything is done and rolled back. Validation
// problems are collected per asset; the result then carries them and the
// returned error is a *BulkValidationError. At most MaxBulkAssets assets are
// allowed (ErrBulkTooMany); unknown ids yield a *BulkNotFoundError.
func (db *DB) BulkUpdateAssets(ctx context.Context, ids []int64, ch BulkChange, dryRun bool) (*BulkResult, error) {
	seen := map[int64]bool{}
	var uniq []int64
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	if len(uniq) == 0 {
		return nil, ErrBulkEmpty
	}
	if len(uniq) > MaxBulkAssets {
		return nil, ErrBulkTooMany
	}
	res := &BulkResult{DryRun: dryRun}
	errRollback := errors.New("dry run")
	err := db.InTx(ctx, func(tx *Tx) error {
		q := tx.tx
		var missing []int64
		var olds []*Asset
		for _, id := range uniq {
			a, err := getAsset(ctx, q, id)
			if errors.Is(err, ErrNotFound) {
				missing = append(missing, id)
				continue
			}
			if err != nil {
				return err
			}
			olds = append(olds, a)
		}
		if len(missing) > 0 {
			return &BulkNotFoundError{IDs: missing}
		}
		cache := map[int64][]Field{}
		fieldsOf := func(cid int64) ([]Field, error) {
			if f, ok := cache[cid]; ok {
				return f, nil
			}
			f, err := categoryFields(ctx, q, cid)
			cache[cid] = f
			return f, err
		}
		for _, old := range olds {
			item, verrs, err := bulkOne(ctx, q, old, ch, fieldsOf)
			if err != nil {
				return err
			}
			res.Errors = append(res.Errors, verrs...)
			if len(verrs) == 0 {
				res.Items = append(res.Items, *item)
				if len(item.Changes) > 0 {
					res.Changed++
				}
			}
		}
		if len(res.Errors) > 0 {
			return &BulkValidationError{Errors: res.Errors}
		}
		if dryRun {
			return errRollback
		}
		return nil
	})
	if errors.Is(err, errRollback) {
		return res, nil
	}
	if err != nil {
		var ve *BulkValidationError
		if errors.As(err, &ve) {
			return res, err
		}
		return nil, err
	}
	return res, nil
}

// bulkOne applies ch to one asset inside the transaction.
func bulkOne(ctx context.Context, q queryer, old *Asset, ch BulkChange, fieldsOf func(int64) ([]Field, error)) (*BulkItem, []BulkError, error) {
	a := *old
	a.Values = make(map[int64]string, len(old.Values))
	for k, v := range old.Values {
		a.Values[k] = v
	}
	fail := func(field, code string) []BulkError {
		return []BulkError{{AssetID: old.ID, InventoryNo: old.InventoryNo, Field: field, Code: code}}
	}
	fields, err := fieldsOf(old.CategoryID)
	if err != nil {
		return nil, nil, err
	}
	var lost []Field
	if ch.CategoryID != nil && *ch.CategoryID != old.CategoryID {
		newFields, err := fieldsOf(*ch.CategoryID)
		if err != nil {
			return nil, nil, err
		}
		if _, err := getCategory(ctx, q, *ch.CategoryID); errors.Is(err, ErrNotFound) {
			return nil, fail("category", "invalid_category"), nil
		} else if err != nil {
			return nil, nil, err
		}
		a.Values, lost = CarryValues(old, fields, newFields)
		a.CategoryID = *ch.CategoryID
		fields = newFields
	}
	if len(ch.Values) > 0 {
		byKey := map[string]*Field{}
		for i := range fields {
			byKey[fields[i].Key] = &fields[i]
		}
		var errs []BulkError
		keys := make([]string, 0, len(ch.Values))
		for k := range ch.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			f := byKey[k]
			if f == nil {
				errs = append(errs, fail("f_"+k, "unknown_field")...)
				continue
			}
			if v := ch.Values[k]; v == nil {
				delete(a.Values, f.ID)
			} else {
				a.Values[f.ID] = *v
			}
		}
		if len(errs) > 0 {
			return nil, errs, nil
		}
	}
	if ch.LocationID != nil {
		a.LocationID = *ch.LocationID
	}
	if ch.Status != nil {
		a.Status = *ch.Status
	}
	if ch.Manufacturer != nil {
		a.Manufacturer = *ch.Manufacturer
	}
	if ch.Model != nil {
		a.Model = *ch.Model
	}
	if ch.PurchaseDate != nil {
		a.PurchaseDate = *ch.PurchaseDate
	}
	if ch.DGUVIntervalMonths != nil {
		a.DGUVIntervalMonths = *ch.DGUVIntervalMonths
	}
	if ch.Note != nil {
		switch {
		case !ch.NoteAppend:
			a.Note = *ch.Note
		case strings.TrimSpace(*ch.Note) == "":
		case old.Note == "":
			a.Note = *ch.Note
		default:
			a.Note = old.Note + "\n" + *ch.Note
		}
	}
	changes, err := updateAsset(ctx, q, &a)
	if ve, ok := asValidation(err); ok {
		keys := make([]string, 0, len(ve.Fields))
		for k := range ve.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var errs []BulkError
		for _, k := range keys {
			errs = append(errs, fail(k, ve.Fields[k])...)
		}
		return nil, errs, nil
	}
	if err != nil {
		return nil, nil, err
	}
	item := &BulkItem{ID: old.ID, InventoryNo: old.InventoryNo, Name: a.Name, Changes: changes, Asset: &a}
	for _, f := range lost {
		if v := ch.Values[f.Key]; v != nil && strings.TrimSpace(*v) != "" {
			continue // overwritten explicitly
		}
		item.Lost = append(item.Lost, BulkLost{Key: f.Key, Label: f.Label, Value: old.Values[f.ID], Unit: f.Unit, Type: f.Type})
	}
	return item, nil, nil
}

// asValidation unwraps a *ValidationError.
func asValidation(err error) (*ValidationError, bool) {
	var ve *ValidationError
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}

// AssetIDsByFilter returns the IDs of all assets matching the filter (page
// and page size are ignored), in list order. More than max matches yield
// ErrBulkTooMany.
func (db *DB) AssetIDsByFilter(ctx context.Context, f AssetFilter, max int) ([]int64, error) {
	cond, args := f.where()
	rows, err := db.sql.QueryContext(ctx, `SELECT a.id`+assetFrom+cond+f.orderBy()+` LIMIT ?`, append(args, max+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > max {
		return nil, ErrBulkTooMany
	}
	return ids, nil
}

// AssetsByIDs returns the assets (without custom values) in the order of
// ids; unknown IDs yield a *BulkNotFoundError.
func (db *DB) AssetsByIDs(ctx context.Context, ids []int64) ([]*Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > MaxBulkAssets {
		return nil, ErrBulkTooMany
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.sql.QueryContext(ctx, assetSelect+` WHERE a.id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*Asset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, err
		}
		byID[a.ID] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Asset, 0, len(ids))
	var missing []int64
	for _, id := range ids {
		if a := byID[id]; a != nil {
			out = append(out, a)
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, &BulkNotFoundError{IDs: missing}
	}
	return out, nil
}
