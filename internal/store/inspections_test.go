package store

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestAddMonthsAndNextDue(t *testing.T) {
	cases := []struct {
		on     string
		months int
		want   string
	}{
		{"2025-01-31", 1, "2025-02-28"},
		{"2024-01-31", 1, "2024-02-29"}, // leap year
		{"2024-02-29", 12, "2025-02-28"},
		{"2024-02-29", 48, "2028-02-29"},
		{"2025-03-31", 1, "2025-04-30"},
		{"2025-08-31", 6, "2026-02-28"},
		{"2025-12-15", 1, "2026-01-15"},
		{"2025-11-30", 3, "2026-02-28"},
		{"2025-06-15", 12, "2026-06-15"},
		{"2025-06-15", 120, "2035-06-15"},
		{"2025-01-01", 1, "2025-02-01"},
	}
	for _, c := range cases {
		got, err := NextDue(c.on, c.months)
		if err != nil || got != c.want {
			t.Errorf("NextDue(%s, %d) = %q, %v; want %s", c.on, c.months, got, err, c.want)
		}
	}
	for _, bad := range []struct {
		on string
		m  int
	}{{"2025-13-01", 12}, {"", 12}, {"2025-01-01", 0}, {"2025-01-01", 121}} {
		if _, err := NextDue(bad.on, bad.m); err == nil {
			t.Errorf("NextDue(%q, %d): want error", bad.on, bad.m)
		}
	}
	if got := AddMonths(time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC), -1); got.Format(DateLayout) != "2025-02-28" {
		t.Errorf("AddMonths -1: %s", got)
	}
	if EffectiveInterval(0, 12) != 12 || EffectiveInterval(6, 12) != 6 {
		t.Error("EffectiveInterval")
	}
}

func TestDueState(t *testing.T) {
	today := time.Date(2026, 3, 15, 13, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"":           DueNever,
		"2026-03-14": DueOverdue,
		"2026-03-15": DueSoon, // today is not yet overdue
		"2026-04-14": DueSoon, // today + 30 days
		"2026-04-15": DueOK,
	}
	for next, want := range cases {
		if got := DueState(next, today); got != want {
			t.Errorf("DueState(%q) = %s, want %s", next, got, want)
		}
	}
}

func TestGuidance(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	keys := func(fl []GuidanceFlag) string {
		var k []string
		for _, x := range fl {
			k = append(k, x.Key)
		}
		return strings.Join(k, ",")
	}
	cases := []struct {
		class                string
		pe, ins, leak, touch *float64
		want                 string
	}{
		{"I", f(0.3), f(1), f(3.5), f(0.5), ""}, // exactly at the limits: no flag
		{"I", f(0.31), f(0.99), f(3.6), f(0.6), "pe_high,insulation_low,leakage_high,touch_high"},
		{"II", nil, f(1.5), nil, f(0.6), "insulation_low,touch_high"},
		{"II", nil, f(2), f(9), f(0.5), ""},    // class II has no protective conductor current limit
		{"", f(0.1), f(0.1), f(100), f(5), ""}, // unknown class: only the PE limit applies
		{"III", nil, f(0.1), f(9), f(9), ""},
		{"", f(0.5), nil, nil, nil, "pe_high"},
		// The numbers are compared as they are: ">299" MOhm is not low, "<0.01" mA is not high.
		{"I", nil, f(299), f(0.01), f(0.01), ""},
	}
	for i, c := range cases {
		if got := keys(Guidance(c.class, c.pe, c.ins, c.leak, c.touch)); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
}

// inspTestAsset creates an asset in the seeded category with the given prefix.
func inspTestAsset(t *testing.T, db *DB, prefix, name string, mod func(*Asset)) *Asset {
	t.Helper()
	ctx := context.Background()
	c := catByPrefix(t, db, prefix)
	a := &Asset{CategoryID: c.ID, Name: name, Values: map[int64]string{}}
	if f, err := db.CategoryFields(ctx, c.ID); err == nil {
		for _, fd := range f {
			if fd.Required {
				a.Values[fd.ID] = "2"
			}
		}
	}
	if mod != nil {
		mod(a)
	}
	if _, err := db.CreateAsset(ctx, a, testFormat); err != nil {
		t.Fatal(err)
	}
	return a
}

func inspect(t *testing.T, db *DB, assetID int64, on, result string) *Inspection {
	t.Helper()
	in := &Inspection{AssetID: assetID, InspectedOn: on, InspectorName: "Tester", Result: result}
	if _, _, err := db.CreateInspection(context.Background(), in, false, false); err != nil {
		t.Fatal(err)
	}
	return in
}

func TestCreateInspection(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := inspTestAsset(t, db, "SL", "Strip", nil) // interval 12 from the category
	b := inspTestAsset(t, db, "SL", "Strip 2", func(a *Asset) { a.DGUVIntervalMonths = 6 })

	in := &Inspection{AssetID: a.ID, InspectedOn: "2025-01-31", InspectorName: " Eli Ectric ", Result: ResultPass,
		ProtectionClass: "I", VisualOK: true, FunctionOK: true, PEInput: "0,25", InsulationInput: "12.5", LeakageInput: "1",
		TestDevice: "Tester 3000", Remark: "ok"}
	if _, _, err := db.CreateInspection(ctx, in, false, false); err != nil {
		t.Fatal(err)
	}
	got, err := db.InspectionByID(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextDue != "2026-01-31" || got.IntervalMonths != 12 || got.InspectorName != "Eli Ectric" ||
		got.PEResistanceOhm == nil || *got.PEResistanceOhm != 0.25 || *got.InsulationMOhm != 12.5 || *got.LeakageMA != 1 ||
		!got.VisualOK || !got.FunctionOK || got.InventoryNo != a.InventoryNo || got.CategoryName == "" {
		t.Fatalf("inspection: %+v", got)
	}
	// The asset override wins over the category interval; month ends clamp.
	in2 := &Inspection{AssetID: b.ID, InspectedOn: "2025-08-31", InspectorName: "x", Result: ResultPass}
	if _, _, err := db.CreateInspection(ctx, in2, false, false); err != nil {
		t.Fatal(err)
	}
	got2, _ := db.InspectionByID(ctx, in2.ID)
	if got2.IntervalMonths != 6 || got2.NextDue != "2026-02-28" {
		t.Fatalf("override: %+v", got2)
	}
	if got2.PEResistanceOhm != nil {
		t.Fatal("unset measurement must stay NULL")
	}
	list, _ := db.InspectionsByAsset(ctx, a.ID)
	if len(list) != 1 {
		t.Fatalf("by asset: %d", len(list))
	}

	// Validation.
	bad := []struct {
		mod         func(*Inspection)
		field, code string
	}{
		{func(i *Inspection) { i.InspectedOn = "" }, "inspected_on", "required"},
		{func(i *Inspection) { i.InspectedOn = "2025-02-30" }, "inspected_on", "invalid_date"},
		{func(i *Inspection) { i.InspectedOn = time.Now().UTC().AddDate(0, 0, 5).Format(DateLayout) }, "inspected_on", "future_date"},
		{func(i *Inspection) { i.InspectorName = "  " }, "inspector_name", "required"},
		{func(i *Inspection) { i.Result = "" }, "result", "required"},
		{func(i *Inspection) { i.Result = "maybe" }, "result", "invalid_result"},
		{func(i *Inspection) { i.ProtectionClass = "IV" }, "protection_class", "invalid_class"},
		{func(i *Inspection) { i.PEInput = "abc" }, "pe_resistance_ohm", "invalid_number"},
		{func(i *Inspection) { i.PEInput = "-0,1" }, "pe_resistance_ohm", "out_of_range"},
		{func(i *Inspection) { i.PEInput = "1000,1" }, "pe_resistance_ohm", "out_of_range"},
		{func(i *Inspection) { i.InsulationInput = "10001" }, "insulation_mohm", "out_of_range"},
		{func(i *Inspection) { i.LeakageInput = "1001" }, "leakage_ma", "out_of_range"},
		{func(i *Inspection) { i.Remark = strings.Repeat("x", 2001) }, "remark", "too_long"},
	}
	for _, c := range bad {
		x := &Inspection{AssetID: a.ID, InspectedOn: "2025-05-05", InspectorName: "x", Result: ResultPass}
		c.mod(x)
		_, _, err := db.CreateInspection(ctx, x, false, false)
		wantVE(t, err, c.field, c.code)
	}
	for _, ok := range []string{"1000", "0", "0,0", "12"} {
		x := &Inspection{AssetID: a.ID, InspectedOn: "2025-05-05", InspectorName: "x", Result: ResultPass, PEInput: ok}
		if _, _, err := db.CreateInspection(ctx, x, false, false); err != nil {
			t.Errorf("PE %q: %v", ok, err)
		}
	}
	if _, _, err := db.CreateInspection(ctx, &Inspection{AssetID: 9999, InspectedOn: "2025-05-05", InspectorName: "x", Result: ResultPass}, false, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing asset: %v", err)
	}
}

func TestInspectionRefusals(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	nk := inspTestAsset(t, db, "NK", "Network cable", nil) // category without DGUV
	_, _, err := db.CreateInspection(ctx, &Inspection{AssetID: nk.ID, InspectedOn: "2025-05-05", InspectorName: "x", Result: ResultPass}, false, false)
	if !errors.Is(err, ErrNotInspectable) {
		t.Fatalf("non-DGUV category: %v", err)
	}
	for _, st := range []string{StatusRetired, StatusLost} {
		a := inspTestAsset(t, db, "SL", "S "+st, func(a *Asset) { a.Status = st })
		_, _, err := db.CreateInspection(ctx, &Inspection{AssetID: a.ID, InspectedOn: "2025-05-05", InspectorName: "x", Result: ResultPass}, true, false)
		if !errors.Is(err, ErrAssetInactive) {
			t.Fatalf("%s: %v", st, err)
		}
		if l, _ := db.InspectionsByAsset(ctx, a.ID); len(l) != 0 {
			t.Fatalf("%s: inspection stored", st)
		}
	}
}

func TestInspectionStatusChange(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := inspTestAsset(t, db, "SL", "Strip", nil)
	status := func() string {
		x, _ := db.AssetByID(ctx, a.ID)
		return x.Status
	}
	rec := func(result string, def, act bool) (string, string) {
		in := &Inspection{AssetID: a.ID, InspectedOn: "2025-05-05", InspectorName: "x", Result: result}
		o, n, err := db.CreateInspection(ctx, in, def, act)
		if err != nil {
			t.Fatal(err)
		}
		return o, n
	}
	// Fail without the option leaves the status alone.
	if o, n := rec(ResultFail, false, false); o != StatusActive || n != StatusActive || status() != StatusActive {
		t.Fatalf("fail, no option: %s %s", o, n)
	}
	// Fail with the option: defective, in the same transaction as the record.
	if o, n := rec(ResultFail, true, false); o != StatusActive || n != StatusDefective || status() != StatusDefective {
		t.Fatalf("fail: %s %s", o, n)
	}
	// Pass without the option keeps defective, with it sets back to active.
	if _, n := rec(ResultPass, false, false); n != StatusDefective {
		t.Fatalf("pass, no option: %s", n)
	}
	if o, n := rec(ResultPass, false, true); o != StatusDefective || n != StatusActive || status() != StatusActive {
		t.Fatalf("pass: %s %s", o, n)
	}
	// A failing validation must not touch the status (rollback).
	if _, _, err := db.CreateInspection(ctx, &Inspection{AssetID: a.ID, InspectedOn: "bad", InspectorName: "x", Result: ResultFail}, true, false); err == nil {
		t.Fatal("want validation error")
	}
	if status() != StatusActive {
		t.Fatal("status changed despite error")
	}
	// A defective asset stays inspectable and is not changed by a fail.
	_ = db.InTx(ctx, func(tx *Tx) error {
		_, err := tx.tx.ExecContext(ctx, `UPDATE assets SET status = 'defective' WHERE id = ?`, a.ID)
		return err
	})
	if o, n := rec(ResultFail, true, false); o != StatusDefective || n != StatusDefective {
		t.Fatalf("defective + fail: %s %s", o, n)
	}
}

func TestDeleteInspectionAndCascade(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := inspTestAsset(t, db, "SL", "Strip", nil)
	i1 := inspect(t, db, a.ID, "2025-01-01", ResultPass)
	inspect(t, db, a.ID, "2025-06-01", ResultPass)
	if got, err := db.DeleteInspection(ctx, i1.ID); err != nil || got.InspectedOn != "2025-01-01" {
		t.Fatalf("delete: %v %+v", err, got)
	}
	if _, err := db.DeleteInspection(ctx, i1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := db.DeleteAsset(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM dguv_inspections`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade: %d %v", n, err)
	}
}

func TestDueOverview(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	today := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	loc := &Location{Name: "Hall"}
	if err := db.CreateLocation(ctx, loc); err != nil {
		t.Fatal(err)
	}
	// Interval 1 month: inspected_on + 1 month = next_due.
	mk := func(name, prefix, st string, locID int64) *Asset {
		return inspTestAsset(t, db, prefix, name, func(a *Asset) {
			a.Status, a.LocationID, a.DGUVIntervalMonths = st, locID, 1
		})
	}
	overdue := mk("overdue", "SL", StatusActive, 0)     // next 2026-03-14
	dueToday := mk("today", "SL", StatusActive, loc.ID) // next 2026-03-15
	soonEdge := mk("edge", "SL", StatusActive, 0)       // next 2026-04-14
	okLater := mk("later", "SL", StatusActive, 0)       // next 2026-04-15
	never := mk("never", "SL", StatusDefective, loc.ID) // defective, never inspected: included
	retired := mk("retired", "SL", StatusRetired, 0)    // excluded
	lost := mk("lost", "SL", StatusLost, 0)             // excluded
	nodguv := inspTestAsset(t, db, "NK", "net", nil)    // category without DGUV: excluded
	_, _, _, _ = retired, lost, nodguv, never
	inspect(t, db, overdue.ID, "2026-02-14", ResultPass)
	inspect(t, db, dueToday.ID, "2026-02-15", ResultPass)
	inspect(t, db, soonEdge.ID, "2026-03-14", ResultPass)
	inspect(t, db, okLater.ID, "2026-03-15", ResultPass)
	// An older, longer-ago inspection must not hide the latest one.
	inspect(t, db, overdue.ID, "2026-02-14", ResultFail)
	inspect(t, db, dueToday.ID, "2024-01-01", ResultPass)

	names := func(f DueFilter) string {
		f.Today = today
		rows, err := db.ListDue(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var n []string
		for _, r := range rows {
			n = append(n, r.Name+":"+r.State)
		}
		return strings.Join(n, ",")
	}
	if got, want := names(DueFilter{}), "never:never,overdue:overdue,today:soon,edge:soon"; got != want {
		t.Fatalf("all: %s, want %s", got, want)
	}
	if got := names(DueFilter{State: DueOverdue}); got != "overdue:overdue" {
		t.Fatalf("overdue: %s", got)
	}
	if got := names(DueFilter{State: DueSoon}); got != "today:soon,edge:soon" {
		t.Fatalf("soon: %s", got)
	}
	if got := names(DueFilter{State: DueNever}); got != "never:never" {
		t.Fatalf("never: %s", got)
	}
	if got := names(DueFilter{LocationID: loc.ID}); got != "never:never,today:soon" {
		t.Fatalf("location: %s", got)
	}
	if got := names(DueFilter{CategoryID: catByPrefix(t, db, "NK").ID}); got != "" {
		t.Fatalf("non-DGUV category: %s", got)
	}
	c, err := db.CountDue(ctx, today)
	if err != nil || c != (DueCounts{Overdue: 1, Soon: 2, Never: 1}) {
		t.Fatalf("counts: %+v %v", c, err)
	}
	// The Go classification and the SQL one agree.
	for _, r := range []struct{ next string }{{"2026-03-14"}, {"2026-03-15"}, {"2026-04-14"}, {"2026-04-15"}} {
		rows, _ := db.ListDue(ctx, DueFilter{Today: today})
		for _, row := range rows {
			if row.NextDue == r.next && row.State != DueState(row.NextDue, today) {
				t.Errorf("state mismatch for %s", r.next)
			}
		}
	}
	// Deleting the latest inspection falls back to the previous one.
	last, _ := db.InspectionsByAsset(ctx, dueToday.ID)
	if _, err := db.DeleteInspection(ctx, last[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := names(DueFilter{State: DueOverdue}); got != "today:overdue,overdue:overdue" && got != "overdue:overdue,today:overdue" {
		t.Fatalf("after delete: %s", got)
	}
}

func TestInspectionFilters(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	loc := &Location{Name: "Hall"}
	_ = db.CreateLocation(ctx, loc)
	a := inspTestAsset(t, db, "SL", "Strip", func(a *Asset) { a.LocationID = loc.ID })
	b := inspTestAsset(t, db, "KT", "Drum", nil)
	add := func(id int64, on, result, who string) {
		in := &Inspection{AssetID: id, InspectedOn: on, InspectorName: who, Result: result}
		if _, _, err := db.CreateInspection(ctx, in, false, false); err != nil {
			t.Fatal(err)
		}
	}
	add(a.ID, "2025-01-10", ResultPass, "Anna Elektro")
	add(a.ID, "2025-03-10", ResultFail, "Bob 100%")
	add(b.ID, "2025-03-20", ResultPass, "anna Elektro")
	add(b.ID, "2025-05-01", ResultPass, "Cy_x")
	count := func(f InspectionFilter) int {
		p, err := db.ListInspections(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		_ = db.ExportInspections(ctx, f, func(*Inspection) error { n++; return nil })
		if n != p.Total {
			t.Errorf("export %d != total %d", n, p.Total)
		}
		return p.Total
	}
	sl, kt := catByPrefix(t, db, "SL").ID, catByPrefix(t, db, "KT").ID
	for name, c := range map[string]struct {
		f    InspectionFilter
		want int
	}{
		"all":       {InspectionFilter{}, 4},
		"from":      {InspectionFilter{From: "2025-03-10"}, 3},
		"to":        {InspectionFilter{To: "2025-03-10"}, 2},
		"range":     {InspectionFilter{From: "2025-03-10", To: "2025-03-20"}, 2},
		"fail":      {InspectionFilter{Result: ResultFail}, 1},
		"pass":      {InspectionFilter{Result: ResultPass}, 3},
		"category":  {InspectionFilter{CategoryID: sl}, 2},
		"category2": {InspectionFilter{CategoryID: kt}, 2},
		"location":  {InspectionFilter{LocationID: loc.ID}, 2},
		"inspector": {InspectionFilter{Inspector: "anna"}, 2},
		"wildcard":  {InspectionFilter{Inspector: "%"}, 1}, // literal percent sign only
		"underline": {InspectionFilter{Inspector: "_"}, 1},
		"combined":  {InspectionFilter{Result: ResultPass, CategoryID: kt, Inspector: "elektro"}, 1},
		"none":      {InspectionFilter{From: "2030-01-01"}, 0},
	} {
		if got := count(c.f); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
	// Newest first, paging.
	p, _ := db.ListInspections(ctx, InspectionFilter{PerPage: 3})
	if p.Pages != 2 || len(p.Inspections) != 3 || p.Inspections[0].InspectedOn != "2025-05-01" {
		t.Fatalf("paging: %+v", p)
	}
	p, _ = db.ListInspections(ctx, InspectionFilter{PerPage: 3, Page: 9})
	if p.Page != 2 || len(p.Inspections) != 1 {
		t.Fatalf("last page: %d %d", p.Page, len(p.Inspections))
	}
}

func TestParseMeasureQualifiers(t *testing.T) {
	cases := []struct {
		in      string
		val     float64
		cmp     string
		code    string
		nilWant bool
	}{
		{in: ">299", val: 299, cmp: ">"},
		{in: "< 0,01", val: 0.01, cmp: "<"},
		{in: ">  0.5", val: 0.5, cmp: ">"},
		{in: "0,25", val: 0.25},
		{in: "", nilWant: true},
		{in: ">", code: "invalid_number", nilWant: true},
		{in: "<>3", code: "invalid_number", nilWant: true},
		{in: ">20000", code: "out_of_range", nilWant: true},
		{in: "=3", code: "invalid_number", nilWant: true},
	}
	for _, c := range cases {
		v, cmp, code := parseMeasure(c.in, MaxInsulationMOhm)
		if code != c.code || cmp != c.cmp || (v == nil) != c.nilWant || (v != nil && *v != c.val) {
			t.Errorf("%q: %v %q %q", c.in, v, cmp, code)
		}
	}
}

func TestInspectionNewFields(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a := inspTestAsset(t, db, "SL", "Strip", nil)
	in := &Inspection{AssetID: a.ID, InspectedOn: "2025-03-01", InspectorName: "E", Result: ResultPass, ProtectionClass: "I",
		PEInput: "0,1", InsulationInput: ">299", LeakageInput: "< 0,01", TouchInput: "0,2", LeakageMethod: MethodAlternative,
		Reason: ReasonInitial, TestDevice: "Dev", TestDeviceID: " SN-7 ", TestDeviceCalibratedUntil: "2026-12-31"}
	if _, _, err := db.CreateInspection(ctx, in, false, false); err != nil {
		t.Fatal(err)
	}
	got, err := db.InspectionByID(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InsulationCmp != ">" || *got.InsulationMOhm != 299 || got.LeakageCmp != "<" || *got.LeakageMA != 0.01 || got.PECmp != "" ||
		got.TouchMA == nil || *got.TouchMA != 0.2 || got.TouchCmp != "" || got.LeakageMethod != MethodAlternative ||
		got.Reason != ReasonInitial || got.TestDeviceID != "SN-7" || got.TestDeviceCalibratedUntil != "2026-12-31" {
		t.Fatalf("stored: %+v", got)
	}
	if len(got.Flags()) != 0 {
		t.Fatalf("flags for >299 / <0,01 / 0,2: %+v", got.Flags())
	}
	// Preset qualifier (API): applies, may repeat the input's, must not contradict it or stand alone.
	x := &Inspection{AssetID: a.ID, InspectedOn: "2025-03-02", InspectorName: "E", Result: ResultPass, InsulationInput: "299", InsulationCmp: ">"}
	if _, _, err := db.CreateInspection(ctx, x, false, false); err != nil || x.InsulationCmp != ">" {
		t.Fatalf("preset: %v %+v", err, x)
	}
	for _, c := range []struct {
		mod         func(*Inspection)
		field, code string
	}{
		{func(i *Inspection) { i.InsulationInput, i.InsulationCmp = ">5", "<" }, "insulation_cmp", "invalid_cmp"},
		{func(i *Inspection) { i.LeakageCmp = ">" }, "leakage_cmp", "invalid_cmp"},
		{func(i *Inspection) { i.TouchInput, i.TouchCmp = "1", "=" }, "touch_cmp", "invalid_cmp"},
		{func(i *Inspection) { i.TouchInput = "1001" }, "touch_ma", "out_of_range"},
		{func(i *Inspection) { i.LeakageMethod = "magic" }, "leakage_method", "invalid_method"},
		{func(i *Inspection) { i.Reason = "boredom" }, "reason", "invalid_reason"},
		{func(i *Inspection) { i.TestDeviceID = strings.Repeat("x", 101) }, "test_device_id", "too_long"},
		{func(i *Inspection) { i.TestDeviceCalibratedUntil = "2026-13-01" }, "test_device_calibrated_until", "invalid_date"},
	} {
		y := &Inspection{AssetID: a.ID, InspectedOn: "2025-03-03", InspectorName: "E", Result: ResultPass}
		c.mod(y)
		_, _, err := db.CreateInspection(ctx, y, false, false)
		wantVE(t, err, c.field, c.code)
	}
	// Touch current flags for classes I and II.
	two := &Inspection{ProtectionClass: "II", TouchMA: ptr(0.6)}
	if f := two.Flags(); len(f) != 1 || f[0].Key != FlagTouchHigh || f[0].Field != "touch_ma" {
		t.Fatalf("flags %+v", f)
	}
}

// Migration 6 applies on a database at version 5 and keeps its inspections.
func TestMigration6OnVersion5(t *testing.T) {
	ctx := context.Background()
	db, err := Open(Options{Path: filepath.Join(t.TempDir(), "v5.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sub, err := fs.Sub(sqliteMigrations, "migrations/sqlite")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db.sql, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO categories (name, prefix, dguv_required, dguv_interval_months, created_at, updated_at) VALUES ('C', 'C', 1, 12, 1, 1)`,
		`INSERT INTO assets (inventory_no, category_id, name, status, created_at, updated_at) VALUES ('C-1', 1, 'A', 'active', 1, 1)`,
		`INSERT INTO dguv_inspections (asset_id, inspected_on, inspector_name, result, leakage_ma, interval_months, next_due, created_at)
			VALUES (1, '2025-01-01', 'X', 'pass', 1.5, 12, '2026-01-01', 1)`,
	} {
		if _, err := db.sql.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if v, err := db.SchemaVersion(ctx); err != nil || v < 6 {
		t.Fatalf("version %d %v", v, err)
	}
	in, err := db.InspectionByID(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if *in.LeakageMA != 1.5 || in.LeakageCmp != "" || in.Reason != "" || in.TouchMA != nil || in.TestDeviceCalibratedUntil != "" {
		t.Fatalf("migrated row: %+v", in)
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE dguv_inspections SET touch_cmp = '=' WHERE id = 1`); err == nil {
		t.Fatal("CHECK on touch_cmp missing")
	}
}
