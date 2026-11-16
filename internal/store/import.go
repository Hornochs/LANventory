package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Import actions of a row.
const (
	ImportCreate = "create"
	ImportUpdate = "update"
	ImportSkip   = "skip"
	ImportError  = "error"
)

// ImportCoreTargets lists the asset columns a CSV column can be mapped to
// (custom fields are "field:<key>"). Their names are the export's headers.
var ImportCoreTargets = []string{"inventory_no", "category", "name", "serial_no", "manufacturer", "model", "location",
	"status", "purchase_date", "dguv_interval_months", "note"}

// ImportInspectionTargets lists the optional inspection columns. A row with
// inspected_on and result also records an inspection of its asset.
var ImportInspectionTargets = []string{"inspected_on", "inspector_name", "result", "reason", "protection_class", "visual_ok",
	"function_ok", "pe_resistance_ohm", "insulation_mohm", "leakage_ma", "leakage_method", "touch_ma", "test_device",
	"test_device_id", "test_device_calibrated_until", "remark"}

// IsInspectionTarget reports whether target is one of ImportInspectionTargets.
func IsInspectionTarget(target string) bool { return oneOf(target, ImportInspectionTargets) }

// FieldPrefix starts the target and the export header of a custom field.
const FieldPrefix = "field:"

// FieldTarget is a custom field offered as import target. Fields of
// different categories with the same key are one target.
type FieldTarget struct {
	Key        string
	Label      string
	Categories []string
}

// ImportFieldTargets lists the custom fields of all categories, unique by
// key (the first label wins), ordered by category name and position.
func (db *DB) ImportFieldTargets(ctx context.Context) ([]FieldTarget, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT cf.key, cf.label, c.name FROM category_fields cf JOIN categories c ON c.id = cf.category_id
		ORDER BY c.name COLLATE NOCASE, c.id, cf.position, cf.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FieldTarget
	idx := map[string]int{}
	for rows.Next() {
		var k, l, c string
		if err := rows.Scan(&k, &l, &c); err != nil {
			return nil, err
		}
		i, ok := idx[k]
		if !ok {
			idx[k] = len(out)
			out = append(out, FieldTarget{Key: k, Label: l})
			i = len(out) - 1
		}
		out[i].Categories = append(out[i].Categories, c)
	}
	return out, rows.Err()
}

// headerAliases maps normalized header names (see normHeader) to targets:
// our export headers plus common German and English names.
var headerAliases = map[string]string{
	"inventoryno": "inventory_no", "inventorynumber": "inventory_no", "inventarnummer": "inventory_no", "inventarnr": "inventory_no",
	"inventarno": "inventory_no", "invnr": "inventory_no", "nummer": "inventory_no", "nr": "inventory_no", "id": "inventory_no",
	"category": "category", "kategorie": "category", "categoryname": "category", "kategoriename": "category",
	"name": "name", "bezeichnung": "name", "geraet": "name", "artikel": "name", "titel": "name", "title": "name",
	"serialno": "serial_no", "serial": "serial_no", "serialnumber": "serial_no", "seriennummer": "serial_no",
	"seriennr": "serial_no", "sn": "serial_no",
	"manufacturer": "manufacturer", "hersteller": "manufacturer", "marke": "manufacturer", "brand": "manufacturer",
	"model": "model", "modell": "model",
	"location": "location", "standort": "location", "lager": "location", "lagerort": "location", "ort": "location",
	"status": "status", "zustand": "status",
	"purchasedate": "purchase_date", "kaufdatum": "purchase_date", "anschaffungsdatum": "purchase_date",
	"anschaffung": "purchase_date", "gekauftam": "purchase_date",
	"dguvintervalmonths": "dguv_interval_months", "pruefintervall": "dguv_interval_months",
	"pruefintervallmonate": "dguv_interval_months", "dguvintervall": "dguv_interval_months", "dguvinterval": "dguv_interval_months",
	"note": "note", "notes": "note", "notiz": "note", "notizen": "note", "bemerkung": "note", "bemerkungen": "note",
	"anmerkung": "note", "kommentar": "note", "comment": "note",

	"inspectedon": "inspected_on", "inspectiondate": "inspected_on", "pruefdatum": "inspected_on", "pruefungsdatum": "inspected_on",
	"geprueftam": "inspected_on", "datumderpruefung": "inspected_on", "pruefungam": "inspected_on",
	"inspectorname": "inspector_name", "inspector": "inspector_name", "pruefer": "inspector_name", "pruefername": "inspector_name",
	"pruefperson": "inspector_name", "geprueftvon": "inspector_name",
	"result": "result", "ergebnis": "result", "pruefergebnis": "result", "gesamtergebnis": "result", "bewertung": "result",
	"reason": "reason", "pruefanlass": "reason", "anlass": "reason", "pruefungsart": "reason",
	"protectionclass": "protection_class", "schutzklasse": "protection_class", "sk": "protection_class",
	"visualok": "visual_ok", "visualinspection": "visual_ok", "sichtpruefung": "visual_ok", "besichtigung": "visual_ok",
	"sichtkontrolle": "visual_ok",
	"functionok":     "function_ok", "functiontest": "function_ok", "funktionspruefung": "function_ok", "funktionstest": "function_ok",
	"peresistanceohm": "pe_resistance_ohm", "peresistance": "pe_resistance_ohm", "rpe": "pe_resistance_ohm",
	"schutzleiterwiderstand": "pe_resistance_ohm",
	"insulationmohm":         "insulation_mohm", "riso": "insulation_mohm", "isolationswiderstand": "insulation_mohm",
	"leakagema": "leakage_ma", "schutzleiterstrom": "leakage_ma", "ableitstrom": "leakage_ma", "ipe": "leakage_ma", "isl": "leakage_ma",
	"leakagemethod": "leakage_method", "messverfahren": "leakage_method", "messmethode": "leakage_method", "messart": "leakage_method",
	"verfahren": "leakage_method", "islverfahren": "leakage_method", "ableitstromverfahren": "leakage_method",
	"touchma": "touch_ma", "beruehrungsstrom": "touch_ma", "ib": "touch_ma",
	"testdevice": "test_device", "pruefgeraet": "test_device", "pruefmittel": "test_device", "messgeraet": "test_device",
	"testdeviceid": "test_device_id", "pruefmittelid": "test_device_id", "pruefgeraetid": "test_device_id", "messgeraetid": "test_device_id",
	"pruefgeraetnummer":         "test_device_id",
	"testdevicecalibrateduntil": "test_device_calibrated_until", "kalibriertbis": "test_device_calibrated_until",
	"kalibrierung": "test_device_calibrated_until", "kalibrierdatum": "test_device_calibrated_until",
	"kalibrierungbis": "test_device_calibrated_until",
	"remark":          "remark", "remarks": "remark",
}

// remarkAliases are headers that mean the asset's note, except in a file with
// inspection columns, where they mean the inspection's remark (as in an
// inspection protocol).
var remarkAliases = map[string]bool{"bemerkung": true, "bemerkungen": true, "anmerkung": true, "kommentar": true, "comment": true}

var umlauts = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "Ä", "ae", "Ö", "oe", "Ü", "ue")

// normHeader lowercases a header, folds German umlauts (ä to ae) and drops
// everything but letters and digits.
func normHeader(s string) string {
	s = umlauts.Replace(strings.ToLower(strings.TrimSpace(s)))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SuggestMapping proposes a target for every header: "" (ignore) or one of
// ImportCoreTargets or "field:<key>". Matching uses our export headers,
// common German and English names, "field:<key>" headers and field labels
// and keys. Every target is suggested at most once (the first column wins).
func SuggestMapping(header []string, fields []FieldTarget) []string {
	byLabel := map[string]string{}
	byKey := map[string]string{}
	for _, f := range fields {
		if _, ok := byLabel[normHeader(f.Label)]; !ok {
			byLabel[normHeader(f.Label)] = f.Key
		}
		byKey[strings.ToLower(f.Key)] = f.Key
	}
	out := make([]string, len(header))
	used := map[string]bool{}
	hasInspection := false
	for _, h := range header {
		if t := headerAliases[normHeader(h)]; t != "remark" && IsInspectionTarget(t) {
			hasInspection = true
		}
	}
	for i, h := range header {
		target := ""
		lh := strings.ToLower(strings.TrimSpace(h))
		if k, ok := strings.CutPrefix(lh, FieldPrefix); ok {
			if key, ok := byKey[strings.TrimSpace(k)]; ok {
				target = FieldPrefix + key
			}
		} else if t, ok := headerAliases[normHeader(h)]; ok {
			target = t
			if hasInspection && remarkAliases[normHeader(h)] && !used["remark"] {
				target = "remark"
			}
		} else if key, ok := byLabel[normHeader(h)]; ok && normHeader(h) != "" {
			target = FieldPrefix + key
		} else if key, ok := byKey[lh]; ok {
			target = FieldPrefix + key
		}
		if target != "" && !used[target] {
			used[target] = true
			out[i] = target
		}
	}
	return out
}

// ImportOptions control an import run.
type ImportOptions struct {
	// Mapping gives the target of every CSV column ("" = ignore).
	Mapping []string
	// DefaultCategoryID is used for rows without a category (0 = none).
	DefaultCategoryID int64
	// DefaultLocationID is used for new assets without a location (0 = none).
	DefaultLocationID int64
	// SkipInvalid imports the valid rows and leaves out the invalid ones;
	// otherwise a single invalid row aborts the whole import.
	SkipInvalid bool
	// CreateLocations creates locations that do not exist yet; otherwise a
	// row with an unknown location is invalid.
	CreateLocations bool
	// UpdateExisting updates assets whose inventory number exists; otherwise
	// such rows are skipped.
	UpdateExisting bool
	// NumberFormat is inventory.number_format for generated numbers.
	NumberFormat string
	// AllowInspections permits the inspection columns (users with the
	// inspection right); otherwise mapping one is an *ImportFileError with
	// code inspection_forbidden.
	AllowInspections bool
	// RecordedBy is the importing user, stored as the recorder of inspections.
	RecordedBy int64
}

// ImportProblem is one problem of a row. Column is the CSV column index
// (-1 for the row as a whole), Code a validation code (translated as
// verr.<code>) and Value the offending cell, shortened.
type ImportProblem struct {
	Column int
	Target string
	Code   string
	Value  string
}

// ImportRowResult is the outcome of one data row.
type ImportRowResult struct {
	// Row is the 1-based number of the data row (the header is not counted).
	Row    int
	Action string
	// Note explains a skip: "exists" (inventory number exists, update off) or
	// "unchanged" (update without differences).
	Note        string
	AssetID     int64
	InventoryNo string
	Name        string
	Category    string
	Problems    []ImportProblem
	// Changes are the audit changes of created and updated assets.
	Changes map[string][2]string
	// Inspection is set when the row carries an inspection.
	Inspection *ImportInspection
}

// ImportInspection is the inspection of an import row.
type ImportInspection struct {
	// Recorded is false when an identical inspection (same asset, date and
	// result) already exists; ID is then that inspection's ID.
	Recorded    bool
	ID          int64
	InspectedOn string
	Result      string
	NextDue     string
}

// ImportResult is the outcome of an import run.
type ImportResult struct {
	Rows   []ImportRowResult
	Create int
	Update int
	Skip   int
	Errors int
	// Inspections is the number of inspections recorded, InspectionsExisting
	// the number of rows whose inspection was already present.
	Inspections         int
	InspectionsExisting int
	// Committed reports that the changes were written.
	Committed bool
}

var errRollback = errors.New("rollback")

// RunImport plans or performs an import of tbl. Without commit nothing is
// written: the run happens in a transaction that is rolled back, so the
// result (including generated inventory numbers) is what a real run would
// do. With commit everything is written in one transaction; when rows are
// invalid and opts.SkipInvalid is not set nothing is written and Committed
// stays false. An *ImportFileError with code bad_mapping or no_category
// reports unusable options.
func (db *DB) RunImport(ctx context.Context, tbl *ImportTable, opts ImportOptions, commit bool) (*ImportResult, error) {
	var res *ImportResult
	err := db.InTx(ctx, func(tx *Tx) error {
		run, err := newImportRun(ctx, db, tx.tx, tbl, opts)
		if err != nil {
			return err
		}
		res = &ImportResult{}
		for i := range tbl.Rows {
			rr, err := run.row(i)
			if err != nil {
				return err
			}
			res.Rows = append(res.Rows, rr)
			if rr.Inspection != nil {
				if rr.Inspection.Recorded {
					res.Inspections++
				} else {
					res.InspectionsExisting++
				}
			}
			switch rr.Action {
			case ImportCreate:
				res.Create++
			case ImportUpdate:
				res.Update++
			case ImportSkip:
				res.Skip++
			default:
				res.Errors++
			}
		}
		if !commit || (res.Errors > 0 && !opts.SkipInvalid) {
			return errRollback
		}
		res.Committed = true
		return nil
	})
	if errors.Is(err, errRollback) {
		return res, nil
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

type importRun struct {
	ctx    context.Context
	q      *sql.Tx
	tbl    *ImportTable
	opts   ImportOptions
	col    map[string]int // target → column
	catsBy map[string]*Category
	fields map[int64][]Field
	locs   map[string]int64 // lower-case name → id
	seen   map[string]bool  // lower-case inventory numbers of earlier rows
}

func newImportRun(ctx context.Context, db *DB, q *sql.Tx, tbl *ImportTable, opts ImportOptions) (*importRun, error) {
	r := &importRun{ctx: ctx, q: q, tbl: tbl, opts: opts, col: map[string]int{}, catsBy: map[string]*Category{},
		fields: map[int64][]Field{}, locs: map[string]int64{}, seen: map[string]bool{}}
	targets, err := db.ImportFieldTargets(ctx)
	if err != nil {
		return nil, err
	}
	valid := map[string]bool{}
	for _, t := range ImportCoreTargets {
		valid[t] = true
	}
	for _, t := range ImportInspectionTargets {
		valid[t] = true
	}
	for _, f := range targets {
		valid[FieldPrefix+f.Key] = true
	}
	if len(opts.Mapping) != len(tbl.Header) {
		return nil, fileErr("bad_mapping", nil)
	}
	for i, t := range opts.Mapping {
		if t == "" {
			continue
		}
		if _, dup := r.col[t]; dup || !valid[t] {
			return nil, fileErr("bad_mapping", nil)
		}
		if IsInspectionTarget(t) && !opts.AllowInspections {
			return nil, fileErr("inspection_forbidden", nil)
		}
		r.col[t] = i
	}
	cats, err := db.ListCategories(ctx)
	if err != nil {
		return nil, err
	}
	for _, c := range cats {
		r.catsBy["n:"+strings.ToLower(c.Name)] = c
		if c.Prefix != "" {
			r.catsBy["p:"+strings.ToLower(c.Prefix)] = c
		}
	}
	_, hasCat := r.col["category"]
	if !hasCat && opts.DefaultCategoryID == 0 {
		return nil, fileErr("no_category", nil)
	}
	lrows, err := q.QueryContext(ctx, `SELECT id, name FROM locations`)
	if err != nil {
		return nil, err
	}
	defer lrows.Close()
	for lrows.Next() {
		var id int64
		var name string
		if err := lrows.Scan(&id, &name); err != nil {
			return nil, err
		}
		r.locs[strings.ToLower(name)] = id
	}
	return r, lrows.Err()
}

// unguardCell removes the apostrophe that the exports put in front of cells
// starting with = + - @ tab or CR, and trims the cell.
func unguardCell(s string) string {
	if len(s) >= 2 && s[0] == '\'' && strings.IndexByte("=+-@\t\r", s[1]) >= 0 {
		s = s[1:]
	}
	return strings.TrimSpace(s)
}

func shorten(s string) string {
	if utf8.RuneCountInString(s) <= 60 {
		return s
	}
	return string([]rune(s)[:60]) + "…"
}

var statusWords = map[string]string{
	"active": StatusActive, "aktiv": StatusActive,
	"defective": StatusDefective, "defekt": StatusDefective,
	"retired": StatusRetired, "ausgemustert": StatusRetired,
	"lost": StatusLost, "verloren": StatusLost, "verschollen": StatusLost,
}

func parseImportStatus(s string) (string, bool) {
	v, ok := statusWords[strings.ToLower(strings.TrimSpace(s))]
	return v, ok
}

var boolWords = map[string]string{
	"1": "1", "true": "1", "yes": "1", "ja": "1", "x": "1", "y": "1", "j": "1", "on": "1", "wahr": "1",
	"0": "0", "false": "0", "no": "0", "nein": "0", "n": "0", "off": "0", "falsch": "0",
}

func parseImportBool(s string) (string, bool) {
	v, ok := boolWords[strings.ToLower(strings.TrimSpace(s))]
	return v, ok
}

// parseImportDate accepts YYYY-MM-DD and DD.MM.YYYY and returns YYYY-MM-DD.
func parseImportDate(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{DateLayout, "2006-1-2", "2.1.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(DateLayout), true
		}
	}
	return "", false
}

// veTarget maps a validation field name of validateAsset to an import target.
func veTarget(field string) string {
	switch {
	case strings.HasPrefix(field, "f_"):
		return FieldPrefix + strings.TrimPrefix(field, "f_")
	case field == "dguv_interval":
		return "dguv_interval_months"
	}
	return field
}

func (r *importRun) cell(row []string, target string) (string, bool) {
	i, ok := r.col[target]
	if !ok {
		return "", false
	}
	return unguardCell(row[i]), true
}

func (r *importRun) catFields(id int64) ([]Field, error) {
	if f, ok := r.fields[id]; ok {
		return f, nil
	}
	f, err := categoryFields(r.ctx, r.q, id)
	if err != nil {
		return nil, err
	}
	r.fields[id] = f
	return f, nil
}

func (r *importRun) assetByNumber(no string) (*Asset, error) {
	a, err := scanAsset(r.q.QueryRowContext(r.ctx, assetSelect+` WHERE a.inventory_no = ?`, no))
	if err != nil {
		return nil, err
	}
	a.Values, err = assetValues(r.ctx, r.q, a.ID)
	return a, err
}

func copyValues(m map[int64]string) map[int64]string {
	out := make(map[int64]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// row evaluates (and, in a committing run, writes) one data row.
func (r *importRun) row(i int) (ImportRowResult, error) {
	row := r.tbl.Rows[i]
	rr := ImportRowResult{Row: i + 1}
	problem := func(target, code string) {
		p := ImportProblem{Column: -1, Target: target, Code: code}
		if c, ok := r.col[target]; ok {
			p.Column = c
			p.Value = shorten(unguardCell(row[c]))
		}
		rr.Problems = append(rr.Problems, p)
	}
	fail := func() (ImportRowResult, error) {
		rr.Action = ImportError
		return rr, nil
	}
	if v, ok := r.cell(row, "name"); ok {
		rr.Name = v
	}
	no, _ := r.cell(row, "inventory_no")
	rr.InventoryNo = no
	var existing *Asset
	if no != "" {
		if n, code := cleanLine(no, 64); code != "" {
			problem("inventory_no", code)
		} else {
			no = n
		}
		key := strings.ToLower(no)
		if r.seen[key] {
			problem("inventory_no", "dup_in_file")
		}
		r.seen[key] = true
		if len(rr.Problems) > 0 {
			return fail()
		}
		a, err := r.assetByNumber(no)
		switch {
		case err == nil:
			existing = a
		case !errors.Is(err, ErrNotFound):
			return rr, err
		}
	}
	if existing != nil && !r.opts.UpdateExisting {
		rr.Action, rr.Note, rr.AssetID = ImportSkip, "exists", existing.ID
		rr.Category = existing.CategoryName
		return rr, nil
	}

	// Category.
	var cat *Category
	catCell, _ := r.cell(row, "category")
	if catCell != "" {
		cat = r.catsBy["n:"+strings.ToLower(catCell)]
		if cat == nil {
			cat = r.catsBy["p:"+strings.ToLower(catCell)]
		}
		if cat == nil {
			problem("category", "invalid_category")
		} else if existing != nil && existing.CategoryID != cat.ID {
			problem("category", "category_mismatch")
		}
	} else if existing != nil {
		cat = r.catsBy["n:"+strings.ToLower(existing.CategoryName)]
	} else if r.opts.DefaultCategoryID != 0 {
		for _, c := range r.catsBy {
			if c.ID == r.opts.DefaultCategoryID {
				cat = c
				break
			}
		}
		if cat == nil {
			problem("category", "invalid_category")
		}
	} else {
		problem("category", "required")
	}
	if cat == nil {
		return fail()
	}
	rr.Category = cat.Name
	fields, err := r.catFields(cat.ID)
	if err != nil {
		return rr, err
	}

	// Build the asset.
	a := &Asset{CategoryID: cat.ID, Values: map[int64]string{}}
	if existing != nil {
		cp := *existing
		a = &cp
		a.Values = copyValues(existing.Values)
	} else {
		a.InventoryNo = no
		a.LocationID = r.opts.DefaultLocationID
	}
	setStr := func(target string, dst *string) {
		if v, ok := r.cell(row, target); ok {
			*dst = v
		}
	}
	setStr("name", &a.Name)
	setStr("serial_no", &a.SerialNo)
	setStr("manufacturer", &a.Manufacturer)
	setStr("model", &a.Model)
	setStr("note", &a.Note)
	if v, ok := r.cell(row, "status"); ok && v != "" {
		if st, ok := parseImportStatus(v); ok {
			a.Status = st
		} else {
			problem("status", "invalid_status")
		}
	}
	if v, ok := r.cell(row, "purchase_date"); ok {
		if v == "" {
			a.PurchaseDate = ""
		} else if d, ok := parseImportDate(v); ok {
			a.PurchaseDate = d
		} else {
			problem("purchase_date", "invalid_date")
		}
	}
	if v, ok := r.cell(row, "dguv_interval_months"); ok {
		if v == "" {
			a.DGUVIntervalMonths = 0
		} else if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 120 {
			a.DGUVIntervalMonths = n
		} else {
			problem("dguv_interval_months", "invalid_interval")
		}
	}
	locName := ""
	if v, ok := r.cell(row, "location"); ok {
		switch v {
		case "":
			a.LocationID = r.opts.DefaultLocationID // an empty cell clears the location unless a default is set
		default:
			if n, code := cleanLine(v, 100); code != "" {
				problem("location", code)
			} else if id, ok := r.locs[strings.ToLower(n)]; ok {
				a.LocationID = id
			} else if r.opts.CreateLocations {
				locName = n
				a.LocationID = 0
			} else {
				problem("location", "invalid_location")
			}
		}
	}
	byKey := map[string]*Field{}
	for i := range fields {
		byKey[fields[i].Key] = &fields[i]
	}
	var keys []string
	for t := range r.col {
		if strings.HasPrefix(t, FieldPrefix) {
			keys = append(keys, strings.TrimPrefix(t, FieldPrefix))
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		target := FieldPrefix + key
		v, _ := r.cell(row, target)
		f := byKey[key]
		if f == nil {
			if v != "" {
				problem(target, "field_not_in_category")
			}
			continue
		}
		if v == "" {
			delete(a.Values, f.ID)
			continue
		}
		switch f.Type {
		case FieldBool:
			if b, ok := parseImportBool(v); ok {
				v = b
			} else {
				problem(target, "invalid_bool")
				continue
			}
		case FieldDate:
			if d, ok := parseImportDate(v); ok {
				v = d
			} else {
				problem(target, "invalid_date")
				continue
			}
		case FieldSelect:
			for _, o := range f.Options {
				if strings.EqualFold(o, v) {
					v = o
					break
				}
			}
		}
		a.Values[f.ID] = v
	}
	insp, ierr := r.parseInspection(row, a, cat, problem)
	if ierr != nil {
		return rr, ierr
	}
	// Validate with the store rules before anything is written.
	probe := *a
	probe.Values = copyValues(a.Values)
	ve := validateAsset(r.ctx, r.q, &probe, fields)
	for field, code := range ve.Fields {
		problem(veTarget(field), code)
	}
	if len(rr.Problems) > 0 {
		sort.Slice(rr.Problems, func(i, j int) bool { return rr.Problems[i].Column < rr.Problems[j].Column })
		return fail()
	}
	if rr.Name == "" {
		rr.Name = a.Name
	}

	// Write.
	if locName != "" {
		id, err := r.createLocation(locName)
		if err != nil {
			return rr, err
		}
		a.LocationID = id
	}
	var changes map[string][2]string
	if existing == nil {
		changes, err = createAsset(r.ctx, r.q, a, r.opts.NumberFormat)
	} else {
		changes, err = updateAsset(r.ctx, r.q, a)
	}
	var cve *ValidationError
	if errors.As(err, &cve) {
		for field, code := range cve.Fields {
			problem(veTarget(field), code)
		}
		return fail()
	}
	if err != nil {
		return rr, err
	}
	rr.AssetID, rr.InventoryNo, rr.Changes = a.ID, a.InventoryNo, changes
	if insp != nil {
		if rr.Inspection, err = r.recordInspection(a.ID, insp); err != nil {
			return rr, err
		}
	}
	switch {
	case existing == nil:
		rr.Action = ImportCreate
	case len(changes) == 0:
		rr.Action, rr.Note = ImportSkip, "unchanged"
	default:
		rr.Action = ImportUpdate
	}
	return rr, nil
}

func (r *importRun) createLocation(name string) (int64, error) {
	if id, ok := r.locs[strings.ToLower(name)]; ok {
		return id, nil
	}
	now := unix(Now())
	res, err := r.q.ExecContext(r.ctx, `INSERT INTO locations (name, description, created_at, updated_at) VALUES (?, '', ?, ?)`, name, now, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	r.locs[strings.ToLower(name)] = id
	return id, nil
}

// resultWords maps normalized words (letters and digits only) to the
// result of an inspection.
var resultWords = map[string]string{
	"pass": ResultPass, "passed": ResultPass, "bestanden": ResultPass, "io": ResultPass, "ok": ResultPass, "ja": ResultPass,
	"yes": ResultPass, "true": ResultPass, "1": ResultPass,
	"fail": ResultFail, "failed": ResultFail, "nichtbestanden": ResultFail, "nichtio": ResultFail, "nio": ResultFail,
	"nein": ResultFail, "no": ResultFail, "false": ResultFail, "0": ResultFail, "durchgefallen": ResultFail,
}

// foldWord lowercases, folds umlauts and drops everything but letters and digits.
func foldWord(s string) string { return normHeader(s) }

func parseImportResult(s string) (string, bool) {
	v, ok := resultWords[foldWord(s)]
	return v, ok
}

func parseImportReason(s string) (string, bool) {
	w := foldWord(s)
	switch {
	case w == "":
		return "", true
	case strings.HasPrefix(w, "initial") || strings.Contains(w, "erst") || strings.Contains(w, "neu"):
		return ReasonInitial, true
	case strings.Contains(w, "wiederhol") || strings.HasPrefix(w, "repeat") || strings.Contains(w, "regel") || strings.Contains(w, "period"):
		return ReasonRepeat, true
	case strings.Contains(w, "instandsetz") || strings.Contains(w, "repar") || strings.HasPrefix(w, "repair"):
		return ReasonRepair, true
	}
	return "", false
}

func parseImportMethod(s string) (string, bool) {
	w := foldWord(s)
	switch {
	case w == "":
		return "", true
	case strings.HasPrefix(w, "dir"):
		return MethodDirect, true
	case strings.HasPrefix(w, "diff"):
		return MethodDifferential, true
	case w == "am" || strings.HasPrefix(w, "ersatz") || strings.HasPrefix(w, "alt"):
		return MethodAlternative, true
	}
	return "", false
}

func parseImportClass(s string) (string, bool) {
	w := strings.ToUpper(foldWord(s))
	for _, p := range []string{"SCHUTZKLASSE", "KLASSE", "CLASS", "SK", "PC"} {
		w = strings.TrimPrefix(w, p)
	}
	switch w {
	case "":
		return "", true
	case "I", "1":
		return "I", true
	case "II", "2":
		return "II", true
	case "III", "3":
		return "III", true
	}
	return "", false
}

// parseImportOK reads a pass/fail style cell (yes/no and the result words);
// an empty cell is false.
func parseImportOK(s string) (bool, bool) {
	if foldWord(s) == "" {
		return false, true
	}
	if v, ok := parseImportBool(s); ok {
		return v == "1", true
	}
	if v, ok := parseImportResult(s); ok {
		return v == ResultPass, true
	}
	return false, false
}

// measureCell returns the raw measurement of a cell: "-" and the dashes of
// spreadsheets mean not measured.
func measureCell(s string) string {
	switch s {
	case "-", "–", "—", "n/a", "N/A":
		return ""
	}
	return s
}

// importInspection is a validated inspection of a row, waiting for its asset.
type importInspection struct {
	in       Inspection
	interval int
}

// parseInspection reads the inspection columns of a row. It returns nil when
// the row carries none (no inspection column mapped, or date and result both
// empty). Problems are reported through problem.
func (r *importRun) parseInspection(row []string, a *Asset, cat *Category, problem func(target, code string)) (*importInspection, error) {
	cell := func(t string) string { v, _ := r.cell(row, t); return v }
	date, result := cell("inspected_on"), cell("result")
	mapped := false
	for _, t := range ImportInspectionTargets {
		if _, isMapped := r.col[t]; isMapped {
			mapped = true
		}
	}
	if !mapped || (date == "" && result == "") {
		return nil, nil
	}
	ok := true
	flagged := map[string]bool{}
	bad := func(target, code string) {
		if !flagged[target] {
			problem(target, code)
		}
		flagged[target] = true
		ok = false
	}
	if !cat.DGUVRequired {
		bad("inspected_on", "not_inspectable")
		return nil, nil
	}
	in := Inspection{InspectorName: cell("inspector_name"), TestDevice: cell("test_device"), TestDeviceID: cell("test_device_id"),
		Remark: cell("remark"), PEInput: measureCell(cell("pe_resistance_ohm")), InsulationInput: measureCell(cell("insulation_mohm")),
		LeakageInput: measureCell(cell("leakage_ma")), TouchInput: measureCell(cell("touch_ma"))}
	switch d, isDate := parseImportDate(date); {
	case date == "":
		bad("inspected_on", "required")
	case !isDate:
		bad("inspected_on", "invalid_date")
	default:
		in.InspectedOn = d
	}
	switch v, isRes := parseImportResult(result); {
	case result == "":
		bad("result", "required")
	case !isRes:
		bad("result", "invalid_result")
	default:
		in.Result = v
	}
	var good bool
	if in.Reason, good = parseImportReason(cell("reason")); !good {
		bad("reason", "invalid_reason")
	}
	if in.LeakageMethod, good = parseImportMethod(cell("leakage_method")); !good {
		bad("leakage_method", "invalid_method")
	}
	if in.ProtectionClass, good = parseImportClass(cell("protection_class")); !good {
		bad("protection_class", "invalid_class")
	}
	if in.VisualOK, good = parseImportOK(cell("visual_ok")); !good {
		bad("visual_ok", "invalid_bool")
	}
	if in.FunctionOK, good = parseImportOK(cell("function_ok")); !good {
		bad("function_ok", "invalid_bool")
	}
	if v := measureCell(cell("test_device_calibrated_until")); v != "" {
		if d, isDate := parseImportDate(v); isDate {
			in.TestDeviceCalibratedUntil = d
		} else {
			bad("test_device_calibrated_until", "invalid_date")
		}
	}
	probe := in
	if ve := probe.validate(Now()); ve.err() != nil {
		fields := make([]string, 0, len(ve.Fields))
		for field := range ve.Fields {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			bad(field, ve.Fields[field])
		}
	}
	if !ok {
		return nil, nil
	}
	return &importInspection{in: probe, interval: EffectiveInterval(a.DGUVIntervalMonths, cat.DGUVIntervalMonths)}, nil
}

// recordInspection writes the inspection of a row for the asset, unless an
// identical one (asset, date, result) exists. The asset status stays as it is.
func (r *importRun) recordInspection(assetID int64, p *importInspection) (*ImportInspection, error) {
	in := p.in
	out := &ImportInspection{InspectedOn: in.InspectedOn, Result: in.Result}
	var id int64
	err := r.q.QueryRowContext(r.ctx, `SELECT id, next_due FROM dguv_inspections WHERE asset_id = ? AND inspected_on = ? AND result = ?
		ORDER BY id LIMIT 1`, assetID, in.InspectedOn, in.Result).Scan(&id, &out.NextDue)
	switch {
	case err == nil:
		out.ID = id
		return out, nil
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	var nerr error
	in.IntervalMonths = p.interval
	if in.NextDue, nerr = NextDue(in.InspectedOn, in.IntervalMonths); nerr != nil {
		return nil, nerr
	}
	if out.ID, err = insertInspection(r.ctx, r.q, assetID, &in, r.opts.RecordedBy, Now()); err != nil {
		return nil, err
	}
	out.Recorded, out.NextDue = true, in.NextDue
	return out, nil
}
