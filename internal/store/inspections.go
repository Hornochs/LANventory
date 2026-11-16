package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Errors of CreateInspection besides ValidationError.
var (
	// ErrNotInspectable: the asset's category does not require DGUV V3 inspections.
	ErrNotInspectable = errors.New("category does not require inspections")
	// ErrAssetInactive: retired and lost assets are not inspected.
	ErrAssetInactive = errors.New("asset is retired or lost")
)

// Inspection is one DGUV V3 inspection record. Inspections are never edited.
type Inspection struct {
	ID      int64
	AssetID int64
	// InspectedOn and NextDue are YYYY-MM-DD.
	InspectedOn string
	// InspectorName is the person who tested; may be an external electrician.
	InspectorName string
	// RecordedBy is the user who entered the record (0 = user deleted).
	RecordedBy      int64
	RecordedByName  string // read only
	Result          string
	ProtectionClass string // "", I, II or III
	VisualOK        bool
	FunctionOK      bool
	// Measurements; nil = not measured.
	// A comparison qualifier ("", "<" or ">") says that the reading lies
	// beyond the instrument's range (">299" MOhm).
	PEResistanceOhm *float64
	PECmp           string
	InsulationMOhm  *float64
	InsulationCmp   string
	// LeakageMA is the protective conductor / leakage current, TouchMA the
	// touch current.
	LeakageMA     *float64
	LeakageCmp    string
	LeakageMethod string // "", direct, differential or alternative
	TouchMA       *float64
	TouchCmp      string
	// Reason is the inspection reason: "", initial, repeat or repair.
	Reason     string
	TestDevice string
	// TestDeviceID is the test device's ID or serial number;
	// TestDeviceCalibratedUntil its calibration date (YYYY-MM-DD, "" = not recorded).
	TestDeviceID              string
	TestDeviceCalibratedUntil string
	Remark                    string
	// IntervalMonths is the interval that was applied to compute NextDue.
	IntervalMonths int
	NextDue        string
	CreatedAt      time.Time

	// Read only: the asset at query time.
	InventoryNo  string
	AssetName    string
	CategoryName string
	LocationName string

	// Raw measurement input for CreateInspection (comma or dot decimals, an
	// optional leading < or >). A qualifier in the input and one preset in
	// the Cmp field must not contradict each other.
	PEInput, InsulationInput, LeakageInput, TouchInput string
}

// Flags returns the guidance flags for the measurements.
func (in *Inspection) Flags() []GuidanceFlag {
	return Guidance(in.ProtectionClass, in.PEResistanceOhm, in.InsulationMOhm, in.LeakageMA, in.TouchMA)
}

const inspCols = `SELECT i.id, i.asset_id, i.inspected_on, i.inspector_name, COALESCE(i.recorded_by, 0), COALESCE(u.username, ''),
	i.result, COALESCE(i.protection_class, ''), i.visual_ok, i.function_ok, i.pe_resistance_ohm, i.insulation_mohm, i.leakage_ma,
	i.test_device, i.remark, i.interval_months, i.next_due, i.created_at, a.inventory_no, a.name, c.name, COALESCE(l.name, ''),
	i.pe_resistance_cmp, i.insulation_cmp, i.leakage_cmp, i.touch_ma, i.touch_cmp, i.leakage_method, i.reason, i.test_device_id,
	COALESCE(i.test_device_calibrated_until, '')`

const inspFrom = ` FROM dguv_inspections i JOIN assets a ON a.id = i.asset_id JOIN categories c ON c.id = a.category_id
	LEFT JOIN locations l ON l.id = a.location_id LEFT JOIN users u ON u.id = i.recorded_by`

func scanInspection(row interface{ Scan(...any) error }) (*Inspection, error) {
	var in Inspection
	var vis, fun int
	var pe, ins, leak, touch sql.NullFloat64
	var cr int64
	err := row.Scan(&in.ID, &in.AssetID, &in.InspectedOn, &in.InspectorName, &in.RecordedBy, &in.RecordedByName,
		&in.Result, &in.ProtectionClass, &vis, &fun, &pe, &ins, &leak, &in.TestDevice, &in.Remark,
		&in.IntervalMonths, &in.NextDue, &cr, &in.InventoryNo, &in.AssetName, &in.CategoryName, &in.LocationName,
		&in.PECmp, &in.InsulationCmp, &in.LeakageCmp, &touch, &in.TouchCmp, &in.LeakageMethod, &in.Reason, &in.TestDeviceID,
		&in.TestDeviceCalibratedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	in.VisualOK, in.FunctionOK = vis != 0, fun != 0
	for _, p := range []struct {
		n sql.NullFloat64
		v **float64
	}{{pe, &in.PEResistanceOhm}, {ins, &in.InsulationMOhm}, {leak, &in.LeakageMA}, {touch, &in.TouchMA}} {
		if p.n.Valid {
			f := p.n.Float64
			*p.v = &f
		}
	}
	in.CreatedAt = fromUnix(cr)
	return &in, nil
}

// parseMeasure parses an optional non-negative measurement (comma or dot) up
// to max. A leading < or > (optionally followed by a space) is returned as the
// comparison qualifier.
func parseMeasure(raw string, max float64) (*float64, string, string) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, "", ""
	}
	cmp := ""
	if v[0] == '<' || v[0] == '>' {
		cmp, v = v[:1], strings.TrimSpace(v[1:])
	}
	norm, code := normalizeNumber(v)
	if code != "" {
		return nil, "", code
	}
	f, err := strconv.ParseFloat(norm, 64)
	if err != nil {
		return nil, "", "invalid_number"
	}
	if f < 0 || f > max {
		return nil, "", "out_of_range"
	}
	return &f, cmp, ""
}

// measure parses one measurement from its raw input and merges the qualifier
// of the input with a preset one. Problems are added to ve under field (the
// value) or field+"_cmp" (the qualifier).
func measure(ve *ValidationError, field, raw, preset string, max float64) (*float64, string) {
	val, cmp, code := parseMeasure(raw, max)
	if code != "" {
		ve.add(field, code)
		return nil, ""
	}
	switch {
	case preset != "" && preset != CmpLess && preset != CmpGreater:
		ve.add(cmpField(field), "invalid_cmp")
	case cmp != "" && preset != "" && cmp != preset:
		ve.add(cmpField(field), "invalid_cmp")
	case cmp == "":
		cmp = preset
	}
	if val == nil && cmp != "" {
		ve.add(cmpField(field), "invalid_cmp")
	}
	return val, cmp
}

// cmpField returns the name of the qualifier property of a measurement.
func cmpField(field string) string {
	switch field {
	case "pe_resistance_ohm":
		return "pe_resistance_cmp"
	case "insulation_mohm":
		return "insulation_cmp"
	case "leakage_ma":
		return "leakage_cmp"
	}
	return "touch_cmp"
}

func oneOf(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// validate normalizes the input in place (measurements from the raw fields).
func (in *Inspection) validate(today time.Time) *ValidationError {
	ve := newVE()
	var code string
	in.InspectedOn = strings.TrimSpace(in.InspectedOn)
	switch t, err := time.Parse(DateLayout, in.InspectedOn); {
	case in.InspectedOn == "":
		ve.add("inspected_on", "required")
	case err != nil || t.Year() < 1900 || t.Year() > 2200:
		ve.add("inspected_on", "invalid_date")
	case in.InspectedOn > today.UTC().AddDate(0, 0, 1).Format(DateLayout):
		// One day of slack for time zones.
		ve.add("inspected_on", "future_date")
	}
	if in.InspectorName, code = cleanLine(in.InspectorName, 100); code != "" {
		ve.add("inspector_name", code)
	} else if in.InspectorName == "" {
		ve.add("inspector_name", "required")
	}
	switch in.Result {
	case ResultPass, ResultFail:
	case "":
		ve.add("result", "required")
	default:
		ve.add("result", "invalid_result")
	}
	if in.ProtectionClass != "" {
		ok := false
		for _, c := range ProtectionClasses {
			ok = ok || c == in.ProtectionClass
		}
		if !ok {
			ve.add("protection_class", "invalid_class")
		}
	}
	in.PEResistanceOhm, in.PECmp = measure(ve, "pe_resistance_ohm", in.PEInput, in.PECmp, MaxPEResistanceOhm)
	in.InsulationMOhm, in.InsulationCmp = measure(ve, "insulation_mohm", in.InsulationInput, in.InsulationCmp, MaxInsulationMOhm)
	in.LeakageMA, in.LeakageCmp = measure(ve, "leakage_ma", in.LeakageInput, in.LeakageCmp, MaxLeakageMA)
	in.TouchMA, in.TouchCmp = measure(ve, "touch_ma", in.TouchInput, in.TouchCmp, MaxTouchMA)
	if in.LeakageMethod != "" && !oneOf(in.LeakageMethod, LeakageMethods) {
		ve.add("leakage_method", "invalid_method")
	}
	if in.Reason != "" && !oneOf(in.Reason, InspectionReasons) {
		ve.add("reason", "invalid_reason")
	}
	if in.TestDevice, code = cleanLine(in.TestDevice, 100); code != "" {
		ve.add("test_device", code)
	}
	if in.TestDeviceID, code = cleanLine(in.TestDeviceID, 100); code != "" {
		ve.add("test_device_id", code)
	}
	if in.TestDeviceCalibratedUntil = strings.TrimSpace(in.TestDeviceCalibratedUntil); in.TestDeviceCalibratedUntil != "" {
		if t, err := time.Parse(DateLayout, in.TestDeviceCalibratedUntil); err != nil || t.Year() < 1900 || t.Year() > 2300 {
			ve.add("test_device_calibrated_until", "invalid_date")
		}
	}
	if in.Remark, code = cleanText(in.Remark, 2000); code != "" {
		ve.add("remark", code)
	}
	return ve
}

// CreateInspection validates and stores an inspection of in.AssetID. The
// applied interval (asset override, else category) and the next due date are
// computed. Assets of categories without DGUV V3 yield ErrNotInspectable,
// retired and lost assets ErrAssetInactive. In the same transaction a failed
// result sets an active asset to defective when setDefective is true, and a
// passed result sets a defective asset back to active when setActive is true.
// It returns the asset status before and after (equal when unchanged).
func (db *DB) CreateInspection(ctx context.Context, in *Inspection, setDefective, setActive bool) (oldStatus, newStatus string, err error) {
	err = db.InTx(ctx, func(tx *Tx) error {
		var err error
		oldStatus, newStatus, err = createInspection(ctx, tx.tx, in, setDefective, setActive)
		return err
	})
	if err != nil {
		return "", "", err
	}
	return oldStatus, newStatus, nil
}

// createInspection is CreateInspection inside a transaction (also used by the
// CSV import).
func createInspection(ctx context.Context, q *sql.Tx, in *Inspection, setDefective, setActive bool) (oldStatus, newStatus string, err error) {
	a, err := getAsset(ctx, q, in.AssetID)
	if err != nil {
		return "", "", err
	}
	cat, err := scanCategory(q.QueryRowContext(ctx, `SELECT `+categoryCols+` FROM categories c WHERE c.id = ?`, a.CategoryID))
	if err != nil {
		return "", "", err
	}
	if !cat.DGUVRequired {
		return "", "", ErrNotInspectable
	}
	if a.Status == StatusRetired || a.Status == StatusLost {
		return "", "", ErrAssetInactive
	}
	if e := in.validate(Now()); e.err() != nil {
		return "", "", e
	}
	in.IntervalMonths = EffectiveInterval(a.DGUVIntervalMonths, cat.DGUVIntervalMonths)
	if in.NextDue, err = NextDue(in.InspectedOn, in.IntervalMonths); err != nil {
		return "", "", err
	}
	now := Now()
	if in.ID, err = insertInspection(ctx, q, a.ID, in, in.RecordedBy, now); err != nil {
		return "", "", err
	}
	in.CreatedAt = now
	in.InventoryNo, in.AssetName, in.CategoryName, in.LocationName = a.InventoryNo, a.Name, a.CategoryName, a.LocationName
	oldStatus, newStatus = a.Status, a.Status
	switch {
	case in.Result == ResultFail && setDefective && a.Status == StatusActive:
		newStatus = StatusDefective
	case in.Result == ResultPass && setActive && a.Status == StatusDefective:
		newStatus = StatusActive
	}
	if newStatus != oldStatus {
		if _, err := q.ExecContext(ctx, `UPDATE assets SET status = ?, updated_at = ? WHERE id = ?`,
			newStatus, unix(now), a.ID); err != nil {
			return "", "", err
		}
	}
	return oldStatus, newStatus, nil
}

// insertInspection writes an already validated inspection (interval and next
// due date set) and returns its ID. recorder 0 stores no recording user.
func insertInspection(ctx context.Context, q *sql.Tx, assetID int64, in *Inspection, recorder int64, created time.Time) (int64, error) {
	res, err := q.ExecContext(ctx, `INSERT INTO dguv_inspections (asset_id, inspected_on, inspector_name, recorded_by, result,
		protection_class, visual_ok, function_ok, pe_resistance_ohm, pe_resistance_cmp, insulation_mohm, insulation_cmp,
		leakage_ma, leakage_cmp, leakage_method, touch_ma, touch_cmp, reason, test_device, test_device_id,
		test_device_calibrated_until, remark, interval_months, next_due, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		assetID, in.InspectedOn, in.InspectorName, nullInt(recorder), in.Result, nullStr(in.ProtectionClass),
		boolInt(in.VisualOK), boolInt(in.FunctionOK), in.PEResistanceOhm, in.PECmp, in.InsulationMOhm, in.InsulationCmp,
		in.LeakageMA, in.LeakageCmp, in.LeakageMethod, in.TouchMA, in.TouchCmp, in.Reason, in.TestDevice, in.TestDeviceID,
		nullStr(in.TestDeviceCalibratedUntil), in.Remark, in.IntervalMonths, in.NextDue, unix(created))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InspectionByID returns one inspection.
func (db *DB) InspectionByID(ctx context.Context, id int64) (*Inspection, error) {
	return scanInspection(db.sql.QueryRowContext(ctx, inspCols+inspFrom+` WHERE i.id = ?`, id))
}

// LastInspectionByUser returns the inspection that the user recorded last
// (the web form prefills the test device from it), or ErrNotFound.
func (db *DB) LastInspectionByUser(ctx context.Context, userID int64) (*Inspection, error) {
	return scanInspection(db.sql.QueryRowContext(ctx, inspCols+inspFrom+` WHERE i.recorded_by = ? ORDER BY i.created_at DESC, i.id DESC LIMIT 1`, userID))
}

// InspectionsByAsset returns the inspections of an asset, newest first.
func (db *DB) InspectionsByAsset(ctx context.Context, assetID int64) ([]*Inspection, error) {
	return db.queryInspections(ctx, inspCols+inspFrom+` WHERE i.asset_id = ? ORDER BY i.inspected_on DESC, i.id DESC`, assetID)
}

func (db *DB) queryInspections(ctx context.Context, q string, args ...any) ([]*Inspection, error) {
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Inspection
	for rows.Next() {
		in, err := scanInspection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// DeleteInspection removes an inspection and returns what was deleted (for
// the audit log).
func (db *DB) DeleteInspection(ctx context.Context, id int64) (*Inspection, error) {
	in, err := db.InspectionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM dguv_inspections WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return in, nil
}

// InspectionFilter selects inspections for the list and the CSV export.
type InspectionFilter struct {
	// From and To bound inspected_on (YYYY-MM-DD, inclusive); empty = open.
	From, To string
	// Result is pass or fail; empty = both.
	Result     string
	CategoryID int64
	LocationID int64
	// AssetID restricts the list to one asset; 0 = all.
	AssetID int64
	// Inspector matches a part of the inspector's name (case-insensitive for ASCII).
	Inspector string
	Page      int
	PerPage   int
}

func (f InspectionFilter) where() (string, []any) {
	var where []string
	var args []any
	if f.From != "" {
		where = append(where, `i.inspected_on >= ?`)
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, `i.inspected_on <= ?`)
		args = append(args, f.To)
	}
	if f.Result == ResultPass || f.Result == ResultFail {
		where = append(where, `i.result = ?`)
		args = append(args, f.Result)
	}
	if f.CategoryID != 0 {
		where = append(where, `a.category_id = ?`)
		args = append(args, f.CategoryID)
	}
	if f.LocationID != 0 {
		where = append(where, `a.location_id = ?`)
		args = append(args, f.LocationID)
	}
	if f.AssetID != 0 {
		where = append(where, `i.asset_id = ?`)
		args = append(args, f.AssetID)
	}
	if f.Inspector != "" {
		where = append(where, `i.inspector_name LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(f.Inspector)+"%")
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

const inspOrder = ` ORDER BY i.inspected_on DESC, i.id DESC`

// InspectionPage is one page of the inspection list.
type InspectionPage struct {
	Inspections []*Inspection
	Total       int
	Page        int
	Pages       int
}

// ListInspections returns one page of inspections matching the filter, newest first.
func (db *DB) ListInspections(ctx context.Context, f InspectionFilter) (*InspectionPage, error) {
	cond, args := f.where()
	var total int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*)`+inspFrom+cond, args...).Scan(&total); err != nil {
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
	list, err := db.queryInspections(ctx, inspCols+inspFrom+cond+inspOrder+` LIMIT ? OFFSET ?`, append(args, per, (page-1)*per)...)
	if err != nil {
		return nil, err
	}
	return &InspectionPage{Inspections: list, Total: total, Page: page, Pages: pages}, nil
}

// ExportInspections calls fn for every inspection matching the filter (paging
// is ignored), newest first.
func (db *DB) ExportInspections(ctx context.Context, f InspectionFilter, fn func(*Inspection) error) error {
	cond, args := f.where()
	rows, err := db.sql.QueryContext(ctx, inspCols+inspFrom+cond+inspOrder, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		in, err := scanInspection(rows)
		if err != nil {
			return err
		}
		if err := fn(in); err != nil {
			return err
		}
	}
	return rows.Err()
}

// DueFilter selects assets for the due overview.
type DueFilter struct {
	CategoryID int64
	LocationID int64
	// State is overdue, soon or never; empty = all three.
	State string
	// Today is the reference date (UTC date is used).
	Today time.Time
}

// DueRow is an asset in the due overview.
type DueRow struct {
	AssetID       int64
	InventoryNo   string
	Name          string
	CategoryName  string
	LocationName  string
	Status        string
	LastInspected string // empty = never
	LastResult    string
	NextDue       string // empty = never inspected
	State         string // DueOverdue, DueSoon or DueNever
}

// dueInner selects every asset that needs inspections (category requires
// DGUV V3, status active or defective) with its state. The overview and the
// dashboard counts both build on it.
func dueInner(f DueFilter) (string, []any) {
	today := f.Today.UTC()
	args := []any{today.Format(DateLayout), today.AddDate(0, 0, DueSoonDays).Format(DateLayout)}
	q := `SELECT a.id AS id, a.inventory_no AS inv, a.name AS name, c.name AS cat, COALESCE(l.name, '') AS loc, a.status AS status,
		COALESCE(i.inspected_on, '') AS last, COALESCE(i.result, '') AS res, COALESCE(i.next_due, '') AS next,
		CASE WHEN i.id IS NULL THEN 'never' WHEN i.next_due < ? THEN 'overdue' WHEN i.next_due <= ? THEN 'soon' ELSE 'ok' END AS state
		FROM assets a JOIN categories c ON c.id = a.category_id LEFT JOIN locations l ON l.id = a.location_id
		LEFT JOIN dguv_inspections i ON i.id = (SELECT x.id FROM dguv_inspections x WHERE x.asset_id = a.id
			ORDER BY x.inspected_on DESC, x.id DESC LIMIT 1)
		WHERE c.dguv_required = 1 AND a.status IN ('active', 'defective')`
	if f.CategoryID != 0 {
		q += ` AND a.category_id = ?`
		args = append(args, f.CategoryID)
	}
	if f.LocationID != 0 {
		q += ` AND a.location_id = ?`
		args = append(args, f.LocationID)
	}
	return q, args
}

// ListDue returns the assets that are overdue, due within DueSoonDays or never
// inspected, never inspected first, then by due date and inventory number.
func (db *DB) ListDue(ctx context.Context, f DueFilter) ([]*DueRow, error) {
	inner, args := dueInner(f)
	q := `SELECT id, inv, name, cat, loc, status, last, res, next, state FROM (` + inner + `) WHERE state != 'ok'`
	switch f.State {
	case DueOverdue, DueSoon, DueNever:
		q += ` AND state = ?`
		args = append(args, f.State)
	}
	q += ` ORDER BY CASE WHEN state = 'never' THEN 0 ELSE 1 END, next, inv COLLATE NOCASE, id`
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DueRow
	for rows.Next() {
		var r DueRow
		if err := rows.Scan(&r.AssetID, &r.InventoryNo, &r.Name, &r.CategoryName, &r.LocationName, &r.Status,
			&r.LastInspected, &r.LastResult, &r.NextDue, &r.State); err != nil {
			return nil, err
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// DueCounts are the numbers of the dashboard widget; they equal the lengths of
// ListDue for the respective state without other filters.
type DueCounts struct {
	Overdue, Soon, Never int
}

// CountDue counts the assets per due state.
func (db *DB) CountDue(ctx context.Context, today time.Time) (DueCounts, error) {
	inner, args := dueInner(DueFilter{Today: today})
	rows, err := db.sql.QueryContext(ctx, `SELECT state, COUNT(*) FROM (`+inner+`) GROUP BY state`, args...)
	if err != nil {
		return DueCounts{}, err
	}
	defer rows.Close()
	var c DueCounts
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return c, err
		}
		switch s {
		case DueOverdue:
			c.Overdue = n
		case DueSoon:
			c.Soon = n
		case DueNever:
			c.Never = n
		}
	}
	return c, rows.Err()
}
