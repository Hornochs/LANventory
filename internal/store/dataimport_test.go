package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fieldID(t *testing.T, db *DB, catID int64, key string) int64 {
	t.Helper()
	fs, err := db.CategoryFields(context.Background(), catID)
	mustDo(t, err)
	for _, f := range fs {
		if f.Key == key {
			return f.ID
		}
	}
	t.Fatalf("no field %s", key)
	return 0
}

// populate fills db with locations, categories (with and without prefix),
// assets in all statuses and inspections.
func populate(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	mustDo(t, db.CreateUser(ctx, &User{Username: "alice", PasswordHash: "x", Role: RoleEditor, CanInspect: true}))
	u, err := db.UserByName(ctx, "alice")
	mustDo(t, err)
	loc := &Location{Name: "Storage 1", Description: "Basement"}
	mustDo(t, db.CreateLocation(ctx, loc))
	mustDo(t, db.CreateLocation(ctx, &Location{Name: "Storage 2"}))
	pre := &Category{Name: "Gadgets", Prefix: "GD", DGUVRequired: true, DGUVIntervalMonths: 24}
	mustDo(t, db.CreateCategory(ctx, pre))
	bare := &Category{Name: "Misc", DGUVRequired: false}
	mustDo(t, db.CreateCategory(ctx, bare))
	for _, f := range []Field{
		{CategoryID: pre.ID, Key: "length_m", Label: "Länge", Type: FieldNumber, Unit: "m", Required: true},
		{CategoryID: pre.ID, Key: "switch", Label: "Schalter", Type: FieldBool},
		{CategoryID: pre.ID, Key: "color", Label: "Farbe", Type: FieldSelect, Options: []string{"red", "blue"}},
		{CategoryID: pre.ID, Key: "bought", Label: "Gekauft", Type: FieldDate},
		{CategoryID: bare.ID, Key: "memo", Label: "Memo", Type: FieldText},
	} {
		f := f
		mustDo(t, db.CreateField(ctx, &f))
	}
	mk := func(a *Asset) *Asset {
		_, err := db.CreateAsset(ctx, a, "{seq:04}")
		mustDo(t, err)
		return a
	}
	a1 := mk(&Asset{CategoryID: pre.ID, Name: "Strip", SerialNo: "S1", Manufacturer: "M", Model: "X", LocationID: loc.ID, Note: "n",
		PurchaseDate: "2025-01-02", DGUVIntervalMonths: 6, Values: map[int64]string{
			fieldID(t, db, pre.ID, "length_m"): "1,5", fieldID(t, db, pre.ID, "switch"): "yes",
			fieldID(t, db, pre.ID, "color"): "red", fieldID(t, db, pre.ID, "bought"): "2024-05-06"}})
	a2 := mk(&Asset{CategoryID: pre.ID, Name: "Old", Values: map[int64]string{fieldID(t, db, pre.ID, "length_m"): "3"}})
	mk(&Asset{CategoryID: bare.ID, Name: "Thing", InventoryNo: "x-9", Status: StatusLost, Values: map[int64]string{fieldID(t, db, bare.ID, "memo"): "hi"}})
	mk(&Asset{CategoryID: bare.ID, Name: "Gone", Status: StatusRetired})
	pe, ins, leak := "0,12", "299", "0.02"
	for _, in := range []*Inspection{
		{AssetID: a1.ID, InspectedOn: "2026-11-14", InspectorName: "Bob", RecordedBy: u.ID, Result: ResultPass, ProtectionClass: "I",
			VisualOK: true, FunctionOK: true, PEInput: pe, InsulationInput: ">" + ins, LeakageInput: leak, TestDevice: "Dev", Remark: "ok",
			TouchInput: "<0,01", Reason: ReasonRepeat, LeakageMethod: MethodDifferential, TestDeviceID: "T-1",
			TestDeviceCalibratedUntil: "2027-01-31"},
		{AssetID: a1.ID, InspectedOn: "2025-05-01", InspectorName: "Carol", Result: ResultFail},
		{AssetID: a2.ID, InspectedOn: "2026-01-01", InspectorName: "Bob", RecordedBy: u.ID, Result: ResultPass},
	} {
		_, _, err := db.CreateInspection(ctx, in, false, false)
		mustDo(t, err)
	}
	// Retire an inspected asset: status is exported as stored.
	a2.Status = StatusRetired
	_, err = db.UpdateAsset(ctx, a2)
	mustDo(t, err)
}

func clearCategories(t *testing.T, db *DB) {
	t.Helper()
	cs, err := db.ListCategories(context.Background())
	mustDo(t, err)
	for _, c := range cs {
		mustDo(t, db.DeleteCategory(context.Background(), c.ID))
	}
}

func exportJSON(t *testing.T, db *DB) string {
	t.Helper()
	d, err := db.ExportData(context.Background())
	mustDo(t, err)
	d.ExportedAt = ""
	b, err := json.MarshalIndent(d, "", " ")
	mustDo(t, err)
	return string(b)
}

func TestDataRoundTrip(t *testing.T) {
	ctx := context.Background()
	src := openTest(t)
	populate(t, src)
	want := exportJSON(t, src)
	d, err := src.ExportData(ctx)
	mustDo(t, err)
	if d.Format != DataFormat || d.Version != 1 || d.ExportedAt == "" || d.AppVersion == "" {
		t.Fatalf("header: %+v", d)
	}
	nl, nc, na, ni := d.Summary()
	if nl != 2 || na != 4 || ni != 3 || nc < 2 {
		t.Fatalf("summary %d %d %d %d", nl, nc, na, ni)
	}
	// Through JSON, as a file.
	raw, err := json.Marshal(d)
	mustDo(t, err)
	parsed, err := ParseDataExport(raw)
	mustDo(t, err)

	for _, tc := range []struct {
		name  string
		setup func(*DB)
	}{
		{"empty target", func(db *DB) { clearCategories(t, db) }},
		{"target with the seeded categories", func(db *DB) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := openTest(t)
			tc.setup(dst)
			mustDo(t, dst.CreateUser(ctx, &User{Username: "alice", PasswordHash: "x", Role: RoleViewer}))
			res, err := dst.ImportData(ctx, parsed, DataImportOptions{})
			mustDo(t, err)
			if !res.Committed || res.AssetsCreated != 4 || res.InspectionsCreated != 3 || len(res.CreatedAssets) != 4 || len(res.Errors) != 0 {
				t.Fatalf("result %+v", res)
			}
			if got := exportJSON(t, dst); got != want {
				t.Fatalf("round trip differs:\n%s\n---\n%s", got, want)
			}
			// Importing again skips everything and writes nothing.
			res, err = dst.ImportData(ctx, parsed, DataImportOptions{})
			mustDo(t, err)
			if res.AssetsCreated != 0 || len(res.SkippedAssets) != 4 || res.InspectionsCreated != 0 || res.LocationsCreated != 0 {
				t.Fatalf("second import %+v", res)
			}
			if got := exportJSON(t, dst); got != want {
				t.Fatal("second import changed data")
			}
		})
	}
}

func TestDataImportRecordedByFallsBackToActor(t *testing.T) {
	ctx := context.Background()
	src := openTest(t)
	populate(t, src)
	d, err := src.ExportData(ctx)
	mustDo(t, err)
	dst := openTest(t)
	mustDo(t, dst.CreateUser(ctx, &User{Username: "imp", PasswordHash: "x", Role: RoleAdmin}))
	imp, _ := dst.UserByName(ctx, "imp")
	_, err = dst.ImportData(ctx, d, DataImportOptions{ActorID: imp.ID})
	mustDo(t, err)
	for _, a := range func() []DataAsset { e, _ := dst.ExportData(ctx); return e.Assets }() {
		for _, in := range a.Inspections {
			if in.RecordedBy != "imp" {
				t.Errorf("recorded_by = %q", in.RecordedBy)
			}
		}
	}
	// CLI: no actor, no user.
	dst2 := openTest(t)
	_, err = dst2.ImportData(ctx, d, DataImportOptions{})
	mustDo(t, err)
	e, _ := dst2.ExportData(ctx)
	for _, a := range e.Assets {
		for _, in := range a.Inspections {
			if in.RecordedBy != "" {
				t.Errorf("recorded_by = %q", in.RecordedBy)
			}
		}
	}
}

func counts(t *testing.T, db *DB) [4]int {
	t.Helper()
	var out [4]int
	for i, q := range []string{"locations", "categories", "assets", "dguv_inspections"} {
		mustDo(t, db.sql.QueryRow(`SELECT COUNT(*) FROM `+q).Scan(&out[i]))
	}
	return out
}

func TestDataImportDryRunAndConflicts(t *testing.T) {
	ctx := context.Background()
	src := openTest(t)
	populate(t, src)
	d, err := src.ExportData(ctx)
	mustDo(t, err)
	dst := openTest(t)
	clearCategories(t, dst)
	before := counts(t, dst)
	res, err := dst.ImportData(ctx, d, DataImportOptions{DryRun: true})
	mustDo(t, err)
	if res.Committed || !res.DryRun || res.AssetsCreated != 4 || res.LocationsCreated != 2 || res.CategoriesCreated == 0 || res.FieldsCreated == 0 {
		t.Fatalf("dry run result %+v", res)
	}
	if counts(t, dst) != before {
		t.Fatal("dry run changed the database")
	}
	_, err = dst.ImportData(ctx, d, DataImportOptions{})
	mustDo(t, err)

	// The same inventory number twice in the file (case-insensitive) is an error.
	d2 := *d
	dup := d.Assets[0]
	dup.InventoryNo = strings.ToLower(dup.InventoryNo)
	d2.Assets = append(append([]DataAsset(nil), d.Assets...), dup)
	res, err = dst.ImportData(ctx, &d2, DataImportOptions{})
	if err == nil || res.Errors[0].Code != "duplicate" || res.Errors[0].Path != "assets[4].inventory_no" {
		t.Fatalf("duplicate: %v %+v", err, res)
	}
}

func TestDataImportOnConflict(t *testing.T) {
	ctx := context.Background()
	src := openTest(t)
	populate(t, src)
	d, err := src.ExportData(ctx)
	mustDo(t, err)
	dst := openTest(t)
	_, err = dst.ImportData(ctx, d, DataImportOptions{})
	mustDo(t, err)
	before := counts(t, dst)

	// A new asset next to existing ones.
	d.Assets = append(d.Assets, DataAsset{InventoryNo: "NEW-1", Category: "Misc", Name: "N", Status: StatusActive})
	res, err := dst.ImportData(ctx, d, DataImportOptions{OnConflict: OnConflictSkip})
	mustDo(t, err)
	if res.AssetsCreated != 1 || len(res.SkippedAssets) != 4 || res.SkippedAssets[0] != d.Assets[0].InventoryNo {
		t.Fatalf("skip: %+v", res)
	}
	before[2]++
	if counts(t, dst) != before {
		t.Fatalf("counts %v want %v", counts(t, dst), before)
	}
	// Abort: existing numbers (any case) fail everything.
	d.Assets = append(d.Assets, DataAsset{InventoryNo: "NEW-2", Category: "Misc", Name: "N2"})
	d.Assets[0].InventoryNo = strings.ToLower(d.Assets[0].InventoryNo)
	res, err = dst.ImportData(ctx, d, DataImportOptions{OnConflict: OnConflictAbort})
	var de *DataImportError
	if !errors.As(err, &de) || res == nil || res.Committed || res.Errors[0].Code != "exists" {
		t.Fatalf("abort: %v %+v", err, res)
	}
	if counts(t, dst) != before {
		t.Fatal("abort wrote data")
	}
	if _, err := dst.ImportData(ctx, d, DataImportOptions{OnConflict: "merge"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
}

func TestDataImportCategoryRules(t *testing.T) {
	ctx := context.Background()
	dst := openTest(t)
	clearCategories(t, dst)
	ex := &Category{Name: "Cable", Prefix: "CB", DGUVRequired: true, DGUVIntervalMonths: 12}
	mustDo(t, dst.CreateCategory(ctx, ex))
	mustDo(t, dst.CreateField(ctx, &Field{CategoryID: ex.ID, Key: "len", Label: "Len", Type: FieldNumber}))
	mustDo(t, dst.CreateField(ctx, &Field{CategoryID: ex.ID, Key: "kind", Label: "Kind", Type: FieldSelect, Options: []string{"a"}}))
	_, err := dst.CreateAsset(ctx, &Asset{CategoryID: ex.ID, Name: "old", InventoryNo: "OLD"}, "{seq}")
	mustDo(t, err)
	base := func() *DataExport {
		return &DataExport{Format: DataFormat, Version: 1, Locations: []DataLocation{{Name: "storage"}}}
	}

	// Merge: differing settings warn, fields are added, options extended; required dropped for a populated category.
	d := base()
	d.Categories = []DataCategory{{Name: "CABLE", Prefix: "XX", DGUVRequired: false, DGUVIntervalMonths: 6, Fields: []DataField{
		{Key: "len", Label: "Länge", Type: FieldNumber},
		{Key: "kind", Label: "Kind", Type: FieldSelect, Options: []string{"a", "b", "c"}},
		{Key: "extra", Label: "Extra", Type: FieldText, Required: true}}}}
	d.Assets = []DataAsset{{InventoryNo: "N1", Category: "cable", Name: "n", Location: "STORAGE", Fields: map[string]string{"kind": "c", "extra": "e"}}}
	res, err := dst.ImportData(ctx, d, DataImportOptions{})
	mustDo(t, err)
	if res.CategoriesCreated != 0 || res.FieldsCreated != 1 || res.OptionsAdded != 2 || res.AssetsCreated != 1 || res.LocationsCreated != 1 {
		t.Fatalf("merge result %+v", res)
	}
	if len(res.Warnings) != 4 {
		t.Fatalf("warnings %v", res.Warnings)
	}
	c, _ := dst.CategoryByID(ctx, ex.ID)
	if c.Prefix != "CB" || !c.DGUVRequired || c.DGUVIntervalMonths != 12 {
		t.Fatalf("existing category changed: %+v", c)
	}
	fs, _ := dst.CategoryFields(ctx, ex.ID)
	if len(fs) != 3 || fs[2].Key != "extra" || fs[2].Required || len(fs[1].Options) != 3 {
		t.Fatalf("fields %+v", fs)
	}

	// Prefix clash for a new category names both categories.
	d = base()
	d.Categories = []DataCategory{{Name: "Other", Prefix: "CB", DGUVIntervalMonths: 12}}
	res, err = dst.ImportData(ctx, d, DataImportOptions{})
	if err == nil || len(res.Errors) != 1 || res.Errors[0].Code != "prefix_conflict" || res.Errors[0].Path != "categories[0].prefix" ||
		!strings.Contains(res.Errors[0].Detail, "Other") || !strings.Contains(res.Errors[0].Detail, "Cable") {
		t.Fatalf("prefix clash: %v %+v", err, res)
	}
	// Field type mismatch.
	d = base()
	d.Categories = []DataCategory{{Name: "Cable", DGUVIntervalMonths: 12, Fields: []DataField{{Key: "len", Label: "Len", Type: FieldText}}}}
	res, err = dst.ImportData(ctx, d, DataImportOptions{})
	if err == nil || res.Errors[0].Code != "field_type_conflict" || res.Errors[0].Path != "categories[0].fields[0].type" {
		t.Fatalf("type mismatch: %v %+v", err, res)
	}
	// Nothing of the failed imports was written.
	if n := counts(t, dst); n[0] != 1 || n[1] != 1 || n[2] != 2 {
		t.Fatalf("counts %v", n)
	}
}

func TestDataImportValidationPaths(t *testing.T) {
	ctx := context.Background()
	dst := openTest(t)
	clearCategories(t, dst)
	d := &DataExport{Format: DataFormat, Version: 1,
		Categories: []DataCategory{{Name: "C", DGUVRequired: true, DGUVIntervalMonths: 12, Fields: []DataField{
			{Key: "length_m", Label: "L", Type: FieldNumber}}}}}
	good := DataAsset{Category: "C", Name: "ok", Status: StatusActive}
	for i := 0; i < 12; i++ {
		a := good
		a.InventoryNo = "A" + string(rune('a'+i))
		d.Assets = append(d.Assets, a)
	}
	d.Assets[12-1].InventoryNo = "BAD"
	d.Assets[11].Fields = map[string]string{"length_m": "abc", "nope": "1"}
	d.Assets[10].Status = "broken"
	d.Assets[9].Location = "nowhere"
	d.Assets[8].Category = "nothing"
	d.Assets[7].Inspections = []DataInspection{{InspectedOn: "2026-13-01", InspectorName: "x", Result: "pass", IntervalMonths: 12, NextDue: "2027-01-01",
		LeakageMA: ptr(-1)}}
	res, err := dst.ImportData(ctx, d, DataImportOptions{})
	if err == nil {
		t.Fatal("want error")
	}
	got := map[string]string{}
	for _, e := range res.Errors {
		got[e.Path] = e.Code
	}
	want := map[string]string{
		"assets[11].fields.length_m": "invalid_number", "assets[11].fields.nope": "unknown_field", "assets[10].status": "invalid_status",
		"assets[9].location": "unknown_location", "assets[8].category": "unknown_category",
		"assets[7].inspections[0].inspected_on": "invalid_date", "assets[7].inspections[0].leakage_ma": "out_of_range",
	}
	for p, c := range want {
		if got[p] != c {
			t.Errorf("%s = %q, want %q (all: %v)", p, got[p], c, got)
		}
	}
	if !strings.Contains(err.Error(), "assets[") || counts(t, dst)[2] != 0 {
		t.Fatalf("error %q / data written", err)
	}
}

func ptr(f float64) *float64 { return &f }

func TestDataImportFormatAndVersion(t *testing.T) {
	ctx := context.Background()
	dst := openTest(t)
	for _, d := range []*DataExport{{Format: "other", Version: 1}, {Format: DataFormat, Version: 2}, {Format: DataFormat}, nil} {
		res, err := dst.ImportData(ctx, d, DataImportOptions{})
		if err == nil || res == nil || res.Committed || len(res.Errors) != 1 {
			t.Fatalf("%+v: %v %+v", d, err, res)
		}
	}
	if res, _ := dst.ImportData(ctx, &DataExport{Format: DataFormat, Version: 2}, DataImportOptions{}); res.Errors[0].Code != "unsupported_version" {
		t.Fatalf("%+v", res.Errors)
	}
	for _, bad := range []string{"", "{", `{"format":"lanventory-export","version":1,"bogus":1}`, `{"format":"x"} {}`} {
		if _, err := ParseDataExport([]byte(bad)); err == nil {
			t.Errorf("ParseDataExport(%q): want error", bad)
		}
	}
}

func TestDataExportOrder(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	populate(t, db)
	d, err := db.ExportData(ctx)
	mustDo(t, err)
	for i := 1; i < len(d.Assets); i++ {
		if strings.ToLower(d.Assets[i-1].InventoryNo) > strings.ToLower(d.Assets[i].InventoryNo) {
			t.Errorf("assets not sorted: %s > %s", d.Assets[i-1].InventoryNo, d.Assets[i].InventoryNo)
		}
	}
	a := d.Assets[0]
	if len(a.Inspections) != 2 || a.Inspections[0].InspectedOn > a.Inspections[1].InspectedOn {
		t.Fatalf("inspections not sorted: %+v", a.Inspections)
	}
	if a.Fields["length_m"] != "1.5" || a.Fields["switch"] != "1" || a.Inspections[1].InsulationMOhm == nil || *a.Inspections[1].LeakageMA != 0.02 {
		t.Fatalf("values: %+v", a)
	}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"purchase_date":null`) || strings.Contains(string(b), "password") {
		t.Fatal("json shape")
	}
}

func TestDataImportUpdateCategories(t *testing.T) {
	ctx := context.Background()
	dst := openTest(t) // seeded templates: Steckerleiste (SL, 12 months, length required)
	sl := catByPrefix(t, dst, "SL")
	d := &DataExport{Format: DataFormat, Version: 1, Categories: []DataCategory{{Name: "Steckerleiste", Prefix: "", DGUVRequired: true,
		DGUVIntervalMonths: 24, Fields: []DataField{{Key: "length_m", Label: "Länge", Type: FieldNumber, Unit: "m", Required: false}}}},
		Assets: []DataAsset{{InventoryNo: "0001", Category: "Steckerleiste", Name: "Strip"}}}
	// Without the option the target keeps its settings.
	res, err := dst.ImportData(ctx, d, DataImportOptions{DryRun: true})
	mustDo(t, err)
	if res.CategoriesUpdated != 0 || len(res.Warnings) != 2 {
		t.Fatalf("keep: %+v", res)
	}
	res, err = dst.ImportData(ctx, d, DataImportOptions{UpdateCategories: true})
	mustDo(t, err)
	if res.CategoriesUpdated != 1 || res.FieldsUpdated != 1 || res.AssetsCreated != 1 || len(res.Warnings) != 0 {
		t.Fatalf("update: %+v", res)
	}
	c, _ := dst.CategoryByID(ctx, sl.ID)
	if c.Prefix != "" || c.DGUVIntervalMonths != 24 {
		t.Fatalf("category not adopted: %+v", c)
	}
	if f := fieldByKey(t, dst, sl.ID, "length_m"); f.Required {
		t.Fatal("required flag not adopted")
	}
	// Required is not switched on for a category with assets; a prefix of another category clashes.
	d.Assets = nil
	d.Categories[0].Fields[0].Required = true
	res, err = dst.ImportData(ctx, d, DataImportOptions{UpdateCategories: true})
	mustDo(t, err)
	if len(res.Warnings) != 1 || res.Warnings[0].Code != "required_dropped" || fieldByKey(t, dst, sl.ID, "length_m").Required {
		t.Fatalf("required on populated category: %+v", res)
	}
	d.Categories[0].Prefix = "KT"
	res, err = dst.ImportData(ctx, d, DataImportOptions{UpdateCategories: true})
	if err == nil || res.Errors[0].Code != "prefix_conflict" {
		t.Fatalf("prefix clash: %v %+v", err, res)
	}
}

func TestDataImportNewInspectionFields(t *testing.T) {
	ctx := context.Background()
	src := openTest(t)
	populate(t, src)
	d, err := src.ExportData(ctx)
	mustDo(t, err)
	var found *DataInspection
	for i := range d.Assets {
		for j := range d.Assets[i].Inspections {
			if in := &d.Assets[i].Inspections[j]; in.TestDeviceID == "T-1" {
				found = in
			}
		}
	}
	if found == nil || found.InsulationCmp != ">" || found.TouchCmp != "<" || found.TouchMA == nil || *found.TouchMA != 0.01 ||
		found.Reason != ReasonRepeat || found.LeakageMethod != MethodDifferential || found.TestDeviceCalibratedUntil != "2027-01-31" {
		t.Fatalf("export: %+v", found)
	}
	// A file without the new properties (older format 1 file) still imports.
	raw, err := json.Marshal(d)
	mustDo(t, err)
	var generic map[string]any
	mustDo(t, json.Unmarshal(raw, &generic))
	for _, a := range generic["assets"].([]any) {
		for _, in := range a.(map[string]any)["inspections"].([]any) {
			m := in.(map[string]any)
			for _, k := range []string{"pe_resistance_cmp", "insulation_cmp", "leakage_cmp", "leakage_method", "touch_ma", "touch_cmp",
				"reason", "test_device_id", "test_device_calibrated_until"} {
				delete(m, k)
			}
		}
	}
	old, err := json.Marshal(generic)
	mustDo(t, err)
	parsed, err := ParseDataExport(old)
	mustDo(t, err)
	dst := openTest(t)
	clearCategories(t, dst)
	res, err := dst.ImportData(ctx, parsed, DataImportOptions{})
	mustDo(t, err)
	if !res.Committed || res.InspectionsCreated != 3 {
		t.Fatalf("old file: %+v", res)
	}
	// An invalid qualifier is reported with its path.
	d.Assets[0].Inspections[0].InsulationCmp = "="
	for i := range d.Assets {
		for j := range d.Assets[i].Inspections {
			d.Assets[i].Inspections[j].Reason = "bogus"
		}
	}
	dst2 := openTest(t)
	clearCategories(t, dst2)
	if _, err = dst2.ImportData(ctx, d, DataImportOptions{}); err == nil || !strings.Contains(err.Error(), "insulation_cmp: invalid_cmp") {
		t.Fatalf("invalid file: %v", err)
	}
}
