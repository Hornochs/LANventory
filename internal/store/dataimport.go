package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Conflict handling of ImportData for inventory numbers that already exist.
const (
	// OnConflictSkip skips existing inventory numbers and reports them.
	OnConflictSkip = "skip"
	// OnConflictAbort fails the whole import on the first existing number.
	OnConflictAbort = "abort"
)

// maxDataIssues bounds the problems listed in a result.
const maxDataIssues = 200

// DataImportOptions control ImportData.
type DataImportOptions struct {
	// DryRun does everything and rolls back.
	DryRun bool
	// OnConflict is OnConflictSkip (default) or OnConflictAbort.
	OnConflict string
	// ActorID is the importing user; inspections whose recorder is unknown
	// are attributed to them. 0 (CLI) records no user.
	ActorID int64
	// UpdateCategories adopts the settings of categories that exist in both
	// (prefix, DGUV flag and interval; label, unit and required flag of their
	// fields) from the file. Otherwise the target's settings stay and the
	// differences are reported as warnings. A fresh instance has seeded
	// template categories, so a transfer usually wants this.
	UpdateCategories bool
}

// DataImportIssue is one problem or warning. Path names the place in the file
// (for example "assets[12].fields.length_m"), Code is a short code (the
// codes of ValidationError plus the ones documented in dataimport.go), Detail
// is optional extra information such as the name of a conflicting category.
type DataImportIssue struct {
	Path   string
	Code   string
	Detail string
}

// String formats the issue for logs and the CLI.
func (i DataImportIssue) String() string {
	s := i.Code
	if i.Path != "" {
		s = i.Path + ": " + s
	}
	if i.Detail != "" {
		s += " (" + i.Detail + ")"
	}
	return s
}

// DataImportedAsset is an asset created by the import (for the audit log).
type DataImportedAsset struct {
	ID          int64
	InventoryNo string
	Name        string
	Changes     map[string][2]string
}

// DataImportResult reports what an import did (or, for a dry run, would do).
type DataImportResult struct {
	DryRun bool
	// Committed is true when the data was written.
	Committed bool

	LocationsCreated   int
	CategoriesCreated  int
	CategoriesUpdated  int
	FieldsCreated      int
	FieldsUpdated      int
	OptionsAdded       int
	AssetsCreated      int
	InspectionsCreated int

	// SkippedAssets lists existing inventory numbers that were not imported.
	SkippedAssets []string
	CreatedAssets []DataImportedAsset
	Warnings      []DataImportIssue
	// Errors lists the problems found (at most 200; ErrorCount is the total).
	// Any error means that nothing was written.
	Errors     []DataImportIssue
	ErrorCount int
}

// DataImportError is returned by ImportData and ParseDataExport when the data
// cannot be imported; the issues are also in DataImportResult.Errors.
type DataImportError struct {
	Issues []DataImportIssue
	Total  int
}

// Error implements error.
func (e *DataImportError) Error() string {
	if len(e.Issues) == 0 {
		return "data import failed"
	}
	return fmt.Sprintf("data import failed: %d problem(s), first: %s", e.Total, e.Issues[0])
}

func (r *DataImportResult) addError(path, code, detail string) {
	r.ErrorCount++
	if len(r.Errors) < maxDataIssues {
		r.Errors = append(r.Errors, DataImportIssue{path, code, detail})
	}
}

func (r *DataImportResult) addVE(prefix string, ve *ValidationError, rename func(string) string) {
	keys := make([]string, 0, len(ve.Fields))
	for k := range ve.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r.addError(prefix+rename(k), ve.Fields[k], "")
	}
}

func (r *DataImportResult) warn(path, code, detail string) {
	if len(r.Warnings) < maxDataIssues {
		r.Warnings = append(r.Warnings, DataImportIssue{path, code, detail})
	}
}

// ParseDataExport decodes an export file. Unknown JSON keys are rejected so
// that a typo cannot silently drop data.
func ParseDataExport(b []byte) (*DataExport, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var d DataExport
	if err := dec.Decode(&d); err != nil {
		return nil, &DataImportError{Issues: []DataImportIssue{{Code: "parse", Detail: err.Error()}}, Total: 1}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, &DataImportError{Issues: []DataImportIssue{{Code: "parse", Detail: "unexpected data after the JSON document"}}, Total: 1}
	}
	return &d, nil
}

// fold lowercases ASCII only, matching SQLite's NOCASE collation.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

type dataCat struct {
	id         int64
	name       string
	prefix     string
	required   bool
	interval   int
	assetCount int
	fields     []Field // loaded lazily by importer.fields
}

type dataImporter struct {
	ctx  context.Context
	tx   *sql.Tx
	data *DataExport
	opts DataImportOptions
	res  *DataImportResult
	now  time.Time

	locations map[string]int64
	cats      map[string]*dataCat
	prefixes  map[string]string // prefix -> category name
	users     map[string]int64
}

// ImportData writes an export into this database in one transaction: nothing
// is written when a problem is found. Locations and categories are matched by
// name (case-insensitive) and created when missing; assets whose inventory
// number exists are skipped or abort the import (opts.OnConflict). Problems
// are returned in the result and as *DataImportError; other errors (database)
// come without a result.
func (db *DB) ImportData(ctx context.Context, data *DataExport, opts DataImportOptions) (*DataImportResult, error) {
	switch opts.OnConflict {
	case "":
		opts.OnConflict = OnConflictSkip
	case OnConflictSkip, OnConflictAbort:
	default:
		return nil, fmt.Errorf("invalid on-conflict mode %q", opts.OnConflict)
	}
	res := &DataImportResult{DryRun: opts.DryRun, SkippedAssets: []string{}}
	switch {
	case data == nil || data.Format != DataFormat:
		res.addError("format", "unknown_format", "")
	case data.Version != DataFormatVersion:
		res.addError("version", "unsupported_version", strconv.Itoa(data.Version))
	}
	if res.ErrorCount > 0 {
		return res, res.failure()
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // after commit this is a no-op
	imp := &dataImporter{ctx: ctx, tx: tx, data: data, opts: opts, res: res, now: Now(),
		locations: map[string]int64{}, cats: map[string]*dataCat{}, prefixes: map[string]string{}, users: map[string]int64{}}
	if err := imp.run(); err != nil {
		return nil, err
	}
	if res.ErrorCount > 0 {
		return res, res.failure()
	}
	if opts.DryRun {
		return res, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	res.Committed = true
	return res, nil
}

func (r *DataImportResult) failure() error {
	return &DataImportError{Issues: r.Errors, Total: r.ErrorCount}
}

func (m *dataImporter) run() error {
	if err := m.locationsStep(); err != nil {
		return err
	}
	if err := m.categoriesStep(); err != nil {
		return err
	}
	if m.res.ErrorCount > 0 {
		return nil
	}
	return m.assetsStep()
}

func (m *dataImporter) locationsStep() error {
	rows, err := m.tx.QueryContext(m.ctx, `SELECT id, name FROM locations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return err
		}
		m.locations[fold(n)] = id
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, dl := range m.data.Locations {
		path := fmt.Sprintf("locations[%d].", i)
		l := Location{Name: dl.Name, Description: dl.Description}
		if ve := l.validate(); ve.err() != nil {
			m.res.addVE(path, ve, func(s string) string { return s })
			continue
		}
		key := fold(l.Name)
		if seen[key] {
			m.res.addError(path+"name", "duplicate", "")
			continue
		}
		seen[key] = true
		if _, ok := m.locations[key]; ok {
			continue // keep the existing one, including its description
		}
		r, err := m.tx.ExecContext(m.ctx, `INSERT INTO locations (name, description, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			l.Name, l.Description, unix(m.now), unix(m.now))
		if err != nil {
			return err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return err
		}
		m.locations[key] = id
		m.res.LocationsCreated++
	}
	return nil
}

func (m *dataImporter) categoriesStep() error {
	rows, err := m.tx.QueryContext(m.ctx, `SELECT c.id, c.name, c.prefix, c.dguv_required, c.dguv_interval_months,
		(SELECT COUNT(*) FROM assets a WHERE a.category_id = c.id) FROM categories c`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c dataCat
		var req int
		if err := rows.Scan(&c.id, &c.name, &c.prefix, &req, &c.interval, &c.assetCount); err != nil {
			rows.Close()
			return err
		}
		c.required = req == 1
		m.cats[fold(c.name)] = &c
		if c.prefix != "" {
			m.prefixes[c.prefix] = c.name
		}
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range m.data.Categories {
		dc := &m.data.Categories[i]
		path := fmt.Sprintf("categories[%d].", i)
		c := Category{Name: dc.Name, Prefix: dc.Prefix, DGUVRequired: dc.DGUVRequired, DGUVIntervalMonths: dc.DGUVIntervalMonths}
		if ve := c.validate(); ve.err() != nil {
			m.res.addVE(path, ve, func(s string) string {
				if s == "interval" {
					return "dguv_interval_months"
				}
				return s
			})
			continue
		}
		key := fold(c.Name)
		if seen[key] {
			m.res.addError(path+"name", "duplicate", "")
			continue
		}
		seen[key] = true
		if ex, ok := m.cats[key]; ok {
			if err := m.existingCategory(path, ex, &c, dc); err != nil {
				return err
			}
			continue
		}
		if other, ok := m.prefixes[c.Prefix]; ok && c.Prefix != "" {
			m.res.addError(path+"prefix", "prefix_conflict", fmt.Sprintf("%s / %s", c.Name, other))
			continue
		}
		if err := insertCategory(m.ctx, m.tx, &c); err != nil {
			var ve *ValidationError
			if errors.As(err, &ve) {
				m.res.addVE(path, ve, func(s string) string { return s })
				continue
			}
			return err
		}
		m.res.CategoriesCreated++
		cat := &dataCat{id: c.ID, name: c.Name, prefix: c.Prefix, required: c.DGUVRequired, interval: c.DGUVIntervalMonths}
		m.cats[key] = cat
		if c.Prefix != "" {
			m.prefixes[c.Prefix] = c.Name
		}
		for j := range dc.Fields {
			if err := m.addField(fmt.Sprintf("%sfields[%d].", path, j), cat, &dc.Fields[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// addField validates and inserts a field definition into a category.
func (m *dataImporter) addField(path string, cat *dataCat, df *DataField) error {
	f := Field{CategoryID: cat.id, Key: df.Key, Label: df.Label, Type: df.Type, Unit: df.Unit, Options: df.Options, Required: df.Required}
	if ve := f.validate(true); ve.err() != nil {
		m.res.addVE(path, ve, func(s string) string { return s })
		return nil
	}
	if f.Required && cat.assetCount > 0 {
		// Existing assets have no value for a new field; do not make them invalid.
		f.Required = false
		m.res.warn(path+"required", "required_dropped", cat.name+"/"+f.Key)
	}
	if err := insertField(m.ctx, m.tx, &f); err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			m.res.addVE(path, ve, func(s string) string { return s })
			return nil
		}
		return err
	}
	m.res.FieldsCreated++
	return nil
}

// existingCategory merges the fields of the file into an existing category and
// reports differing settings as warnings; the category's own settings stay.
func (m *dataImporter) existingCategory(path string, ex *dataCat, c *Category, dc *DataCategory) error {
	differs := ex.prefix != c.Prefix || ex.required != c.DGUVRequired || ex.interval != c.DGUVIntervalMonths
	switch {
	case differs && m.opts.UpdateCategories:
		if err := m.updateCategory(path, ex, c); err != nil {
			return err
		}
	case differs:
		if ex.prefix != c.Prefix {
			m.res.warn(path+"prefix", "category_differs", fmt.Sprintf("%s: %q / %q", ex.name, ex.prefix, c.Prefix))
		}
		if ex.required != c.DGUVRequired {
			m.res.warn(path+"dguv_required", "category_differs", fmt.Sprintf("%s: %t / %t", ex.name, ex.required, c.DGUVRequired))
		}
		if ex.interval != c.DGUVIntervalMonths {
			m.res.warn(path+"dguv_interval_months", "category_differs", fmt.Sprintf("%s: %d / %d", ex.name, ex.interval, c.DGUVIntervalMonths))
		}
	}
	existing, err := categoryFields(m.ctx, m.tx, ex.id)
	if err != nil {
		return err
	}
	byKey := map[string]*Field{}
	for i := range existing {
		byKey[existing[i].Key] = &existing[i]
	}
	seen := map[string]bool{}
	for j := range dc.Fields {
		df := &dc.Fields[j]
		fp := fmt.Sprintf("%sfields[%d].", path, j)
		if seen[df.Key] {
			m.res.addError(fp+"key", "duplicate", "")
			continue
		}
		seen[df.Key] = true
		old := byKey[df.Key]
		if old == nil {
			if err := m.addField(fp, ex, df); err != nil {
				return err
			}
			continue
		}
		if old.Type != df.Type {
			m.res.addError(fp+"type", "field_type_conflict", fmt.Sprintf("%s/%s: %s / %s", ex.name, old.Key, old.Type, df.Type))
			continue
		}
		nf := *old
		added := 0
		if old.Type == FieldSelect {
			have := map[string]bool{}
			for _, o := range old.Options {
				have[o] = true
			}
			nf.Options = append([]string(nil), old.Options...)
			for _, o := range df.Options {
				if o = strings.TrimSpace(o); o != "" && !have[o] {
					have[o] = true
					nf.Options = append(nf.Options, o)
					added++
				}
			}
		}
		settings := false
		if m.opts.UpdateCategories {
			nf.Label, nf.Unit, nf.Required = df.Label, df.Unit, df.Required
			if nf.Required && !old.Required && ex.assetCount > 0 {
				nf.Required = false // existing assets may lack the value
				m.res.warn(fp+"required", "required_dropped", ex.name+"/"+old.Key)
			}
			settings = nf.Label != old.Label || nf.Unit != old.Unit || nf.Required != old.Required
		}
		if added == 0 && !settings {
			continue
		}
		if ve := nf.validate(false); ve.err() != nil {
			m.res.addVE(fp, ve, func(s string) string { return s })
			continue
		}
		if _, err := m.tx.ExecContext(m.ctx, `UPDATE category_fields SET label = ?, unit = ?, required = ?, options = ? WHERE id = ?`,
			nf.Label, nf.Unit, boolInt(nf.Required), optionsJSON(&nf), old.ID); err != nil {
			return err
		}
		m.res.OptionsAdded += added
		if settings {
			m.res.FieldsUpdated++
		}
	}
	return nil
}

// updateCategory adopts prefix and DGUV settings of the file for an existing
// category (UpdateCategories).
func (m *dataImporter) updateCategory(path string, ex *dataCat, c *Category) error {
	if c.Prefix != "" && c.Prefix != ex.prefix {
		if other, ok := m.prefixes[c.Prefix]; ok && other != ex.name {
			m.res.addError(path+"prefix", "prefix_conflict", fmt.Sprintf("%s / %s", c.Name, other))
			return nil
		}
	}
	_, err := m.tx.ExecContext(m.ctx, `UPDATE categories SET prefix = ?, dguv_required = ?, dguv_interval_months = ?, updated_at = ?
		WHERE id = ?`, c.Prefix, boolInt(c.DGUVRequired), c.DGUVIntervalMonths, unix(m.now), ex.id)
	if isUnique(err) {
		m.res.addError(path+"prefix", "prefix_conflict", c.Name)
		return nil
	}
	if err != nil {
		return err
	}
	if ex.prefix != "" {
		delete(m.prefixes, ex.prefix)
	}
	if c.Prefix != "" {
		m.prefixes[c.Prefix] = ex.name
	}
	ex.prefix, ex.required, ex.interval = c.Prefix, c.DGUVRequired, c.DGUVIntervalMonths
	m.res.CategoriesUpdated++
	return nil
}

func (m *dataImporter) catFields(c *dataCat) ([]Field, error) {
	if c.fields == nil {
		f, err := categoryFields(m.ctx, m.tx, c.id)
		if err != nil {
			return nil, err
		}
		if f == nil {
			f = []Field{}
		}
		c.fields = f
	}
	return c.fields, nil
}

func (m *dataImporter) userID(name string) (int64, error) {
	if name == "" {
		return m.opts.ActorID, nil
	}
	key := fold(name)
	if id, ok := m.users[key]; ok {
		return id, nil
	}
	var id int64
	err := m.tx.QueryRowContext(m.ctx, `SELECT id FROM users WHERE username = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = m.opts.ActorID, nil
	}
	if err != nil {
		return 0, err
	}
	m.users[key] = id
	return id, nil
}

func parseDataTime(s string, def time.Time) (time.Time, bool) {
	if s == "" {
		return def, true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.Year() < 1970 || t.Year() > 2200 {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func assetPath() func(string) string {
	return func(s string) string {
		switch {
		case strings.HasPrefix(s, "f_"):
			return "fields." + s[2:]
		case s == "dguv_interval":
			return "dguv_interval_months"
		}
		return s
	}
}

func (m *dataImporter) assetsStep() error {
	seen := map[string]bool{}
	for i := range m.data.Assets {
		da := &m.data.Assets[i]
		path := fmt.Sprintf("assets[%d].", i)
		no, code := cleanLine(da.InventoryNo, 64)
		switch {
		case code != "":
			m.res.addError(path+"inventory_no", code, "")
			continue
		case no == "":
			m.res.addError(path+"inventory_no", "required", "")
			continue
		case seen[fold(no)]:
			m.res.addError(path+"inventory_no", "duplicate", no)
			continue
		}
		seen[fold(no)] = true
		var existing int64
		err := m.tx.QueryRowContext(m.ctx, `SELECT id FROM assets WHERE inventory_no = ?`, no).Scan(&existing)
		if err == nil {
			if m.opts.OnConflict == OnConflictAbort {
				m.res.addError(path+"inventory_no", "exists", no)
			} else {
				m.res.SkippedAssets = append(m.res.SkippedAssets, no)
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := m.createAsset(i, path, no, da); err != nil {
			return err
		}
	}
	return nil
}

func (m *dataImporter) createAsset(i int, path, no string, da *DataAsset) error {
	cat, ok := m.cats[fold(strings.TrimSpace(da.Category))]
	if !ok {
		m.res.addError(path+"category", "unknown_category", da.Category)
		return nil
	}
	fields, err := m.catFields(cat)
	if err != nil {
		return err
	}
	a := Asset{InventoryNo: no, CategoryID: cat.id, CategoryName: cat.name, CategoryPrefix: cat.prefix, Name: da.Name,
		SerialNo: da.SerialNo, Manufacturer: da.Manufacturer, Model: da.Model, Status: da.Status, Note: da.Note,
		Values: map[int64]string{}}
	if da.PurchaseDate != nil {
		a.PurchaseDate = *da.PurchaseDate
	}
	if da.DGUVIntervalMonths != nil {
		a.DGUVIntervalMonths = *da.DGUVIntervalMonths
		if a.DGUVIntervalMonths == 0 {
			a.DGUVIntervalMonths = -1 // 0 means "none" internally; an exported 0 is invalid
		}
	}
	nerr := m.res.ErrorCount
	if loc := strings.TrimSpace(da.Location); loc != "" {
		if id, ok := m.locations[fold(loc)]; ok {
			a.LocationID = id
		} else {
			m.res.addError(path+"location", "unknown_location", da.Location)
		}
	}
	byKey := map[string]*Field{}
	for j := range fields {
		byKey[fields[j].Key] = &fields[j]
	}
	keys := make([]string, 0, len(da.Fields))
	for k := range da.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if f := byKey[k]; f != nil {
			a.Values[f.ID] = da.Fields[k]
		} else {
			m.res.addError(path+"fields."+k, "unknown_field", cat.name)
		}
	}
	created, ok1 := parseDataTime(da.CreatedAt, m.now)
	if !ok1 {
		m.res.addError(path+"created_at", "invalid_timestamp", "")
	}
	updated, ok2 := parseDataTime(da.UpdatedAt, created)
	if !ok2 {
		m.res.addError(path+"updated_at", "invalid_timestamp", "")
	}
	// Required flags are not enforced: an export may contain assets that
	// predate a required field.
	relaxed := make([]Field, len(fields))
	copy(relaxed, fields)
	for j := range relaxed {
		relaxed[j].Required = false
	}
	if ve := validateAsset(m.ctx, m.tx, &a, relaxed); ve.err() != nil {
		m.res.addVE(path, ve, assetPath())
	}
	type pendingInspection struct {
		in       Inspection
		created  time.Time
		recorder int64
	}
	var pending []pendingInspection
	for k := range da.Inspections {
		di := &da.Inspections[k]
		ip := fmt.Sprintf("%sinspections[%d].", path, k)
		in := Inspection{InspectedOn: di.InspectedOn, InspectorName: di.InspectorName, Result: di.Result,
			ProtectionClass: di.ProtectionClass, VisualOK: di.VisualOK, FunctionOK: di.FunctionOK, TestDevice: di.TestDevice,
			Remark: di.Remark, IntervalMonths: di.IntervalMonths, NextDue: strings.TrimSpace(di.NextDue),
			PEInput: fmtMeasure(di.PEResistanceOhm), InsulationInput: fmtMeasure(di.InsulationMOhm), LeakageInput: fmtMeasure(di.LeakageMA),
			TouchInput: fmtMeasure(di.TouchMA), PECmp: di.PEResistanceCmp, InsulationCmp: di.InsulationCmp, LeakageCmp: di.LeakageCmp,
			TouchCmp: di.TouchCmp, LeakageMethod: di.LeakageMethod, Reason: di.Reason, TestDeviceID: di.TestDeviceID,
			TestDeviceCalibratedUntil: di.TestDeviceCalibratedUntil}
		before := m.res.ErrorCount
		if ve := in.validate(m.now); ve.err() != nil {
			m.res.addVE(ip, ve, func(s string) string { return s })
		}
		if in.IntervalMonths < 1 || in.IntervalMonths > 120 {
			m.res.addError(ip+"interval_months", "invalid_interval", "")
		}
		if t, err := time.Parse(DateLayout, in.NextDue); err != nil || t.Year() < 1900 || t.Year() > 2300 {
			m.res.addError(ip+"next_due", "invalid_date", "")
		}
		ct, ok := parseDataTime(di.CreatedAt, m.now)
		if !ok {
			m.res.addError(ip+"created_at", "invalid_timestamp", "")
		}
		rec, err := m.userID(strings.TrimSpace(di.RecordedBy))
		if err != nil {
			return err
		}
		if m.res.ErrorCount == before {
			pending = append(pending, pendingInspection{in, ct, rec})
		}
	}
	if m.res.ErrorCount > nerr {
		return nil
	}
	r, err := m.tx.ExecContext(m.ctx, `INSERT INTO assets (inventory_no, category_id, name, serial_no, manufacturer, model,
		location_id, status, note, purchase_date, dguv_interval_months, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.InventoryNo, a.CategoryID, a.Name, a.SerialNo, a.Manufacturer, a.Model, nullInt(a.LocationID), a.Status, a.Note,
		nullStr(a.PurchaseDate), nullInt(int64(a.DGUVIntervalMonths)), unix(created), unix(updated))
	if isUnique(err) {
		m.res.addError(path+"inventory_no", "taken", a.InventoryNo)
		return nil
	}
	if err != nil {
		return err
	}
	if a.ID, err = r.LastInsertId(); err != nil {
		return err
	}
	for fid, v := range a.Values {
		if _, err := m.tx.ExecContext(m.ctx, `INSERT INTO asset_field_values (asset_id, field_id, value) VALUES (?, ?, ?)`, a.ID, fid, v); err != nil {
			return err
		}
	}
	for _, p := range pending {
		in := &p.in
		if _, err := insertInspection(m.ctx, m.tx, a.ID, in, p.recorder, p.created); err != nil {
			return err
		}
		m.res.InspectionsCreated++
	}
	m.res.AssetsCreated++
	m.res.CreatedAssets = append(m.res.CreatedAssets, DataImportedAsset{ID: a.ID, InventoryNo: a.InventoryNo, Name: a.Name,
		Changes: AssetChanges(&Asset{}, &a, fields)})
	return nil
}

func fmtMeasure(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}
