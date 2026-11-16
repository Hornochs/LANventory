package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type bulkFixture struct {
	db         *DB
	cableCat   *Category // key length_m (number, required), color (text)
	deviceCat  *Category // key color (text), watts (number)
	loc1, loc2 *Location
	assets     []*Asset // three cables
	device     *Asset
}

func newBulkFixture(t *testing.T) *bulkFixture {
	t.Helper()
	ctx := context.Background()
	db := openTest(t)
	f := &bulkFixture{db: db}
	f.cableCat = &Category{Name: "BulkCable", Prefix: "BC"}
	f.deviceCat = &Category{Name: "BulkDevice", Prefix: "BD"}
	for _, c := range []*Category{f.cableCat, f.deviceCat} {
		if err := db.CreateCategory(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	for _, fd := range []*Field{
		{CategoryID: f.cableCat.ID, Key: "length_m", Label: "Length", Type: FieldNumber, Required: true},
		{CategoryID: f.cableCat.ID, Key: "color", Label: "Color", Type: FieldText},
		{CategoryID: f.deviceCat.ID, Key: "color", Label: "Colour", Type: FieldText},
		{CategoryID: f.deviceCat.ID, Key: "watts", Label: "Watts", Type: FieldNumber},
	} {
		if err := db.CreateField(ctx, fd); err != nil {
			t.Fatal(err)
		}
	}
	f.loc1, f.loc2 = &Location{Name: "Bulk A"}, &Location{Name: "Bulk B"}
	for _, l := range []*Location{f.loc1, f.loc2} {
		if err := db.CreateLocation(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	lenF := fieldByKey(t, db, f.cableCat.ID, "length_m")
	colF := fieldByKey(t, db, f.cableCat.ID, "color")
	for i, n := range []string{"c1", "c2", "c3"} {
		a := &Asset{CategoryID: f.cableCat.ID, Name: n, Values: map[int64]string{lenF.ID: "5", colF.ID: "red"}}
		if i == 0 {
			a.Note = "first"
		}
		if _, err := db.CreateAsset(ctx, a, testFormat); err != nil {
			t.Fatal(err)
		}
		f.assets = append(f.assets, a)
	}
	f.device = &Asset{CategoryID: f.deviceCat.ID, Name: "d1"}
	if _, err := db.CreateAsset(ctx, f.device, testFormat); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *bulkFixture) ids(extra ...*Asset) []int64 {
	var out []int64
	for _, a := range append(append([]*Asset{}, f.assets...), extra...) {
		out = append(out, a.ID)
	}
	return out
}

func sp(s string) *string { return &s }
func ip(n int64) *int64   { return &n }

func TestBulkLocationAndStatus(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	res, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{LocationID: ip(f.loc1.ID), Status: sp(StatusDefective)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 3 || len(res.Items) != 3 {
		t.Fatalf("result: %+v", res)
	}
	if ch := res.Items[0].Changes; ch["status"] != [2]string{"active", "defective"} || ch["location"][1] != "Bulk A" || len(ch) != 2 {
		t.Fatalf("changes: %v", ch)
	}
	for _, a := range f.assets {
		got, _ := f.db.AssetByID(ctx, a.ID)
		if got.LocationID != f.loc1.ID || got.Status != StatusDefective || got.Name != a.Name {
			t.Fatalf("asset: %+v", got)
		}
	}
	// Clearing the location; second run with the same status changes only the location.
	res, err = f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{LocationID: ip(0), Status: sp(StatusDefective)}, false)
	if err != nil || res.Changed != 3 || len(res.Items[0].Changes) != 1 {
		t.Fatalf("clear: %v %+v", err, res)
	}
	got, _ := f.db.AssetByID(ctx, f.assets[0].ID)
	if got.LocationID != 0 {
		t.Fatalf("location not cleared: %+v", got)
	}
	// No-op change.
	res, err = f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Status: sp(StatusDefective)}, false)
	if err != nil || res.Changed != 0 {
		t.Fatalf("noop: %v %+v", err, res)
	}
}

func TestBulkSimpleFields(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	zero := 0
	ch := BulkChange{Manufacturer: sp(" ACME "), Model: sp("X1"), PurchaseDate: sp("2024-02-29"), DGUVIntervalMonths: new(int)}
	*ch.DGUVIntervalMonths = 6
	if _, err := f.db.BulkUpdateAssets(ctx, f.ids(), ch, false); err != nil {
		t.Fatal(err)
	}
	got, _ := f.db.AssetByID(ctx, f.assets[1].ID)
	if got.Manufacturer != "ACME" || got.Model != "X1" || got.PurchaseDate != "2024-02-29" || got.DGUVIntervalMonths != 6 {
		t.Fatalf("got %+v", got)
	}
	ch = BulkChange{PurchaseDate: sp(""), DGUVIntervalMonths: &zero}
	if _, err := f.db.BulkUpdateAssets(ctx, f.ids(), ch, false); err != nil {
		t.Fatal(err)
	}
	got, _ = f.db.AssetByID(ctx, f.assets[1].ID)
	if got.PurchaseDate != "" || got.DGUVIntervalMonths != 0 {
		t.Fatalf("got %+v", got)
	}
	res, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{PurchaseDate: sp("nope")}, false)
	var be *BulkValidationError
	if !errors.As(err, &be) || len(res.Errors) != 3 || res.Errors[0].Field != "purchase_date" || res.Errors[0].Code != "invalid_date" {
		t.Fatalf("want 3 date errors, got %v %+v", err, res)
	}
}

func TestBulkNote(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	if _, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Note: sp("more"), NoteAppend: true}, false); err != nil {
		t.Fatal(err)
	}
	a0, _ := f.db.AssetByID(ctx, f.assets[0].ID)
	a1, _ := f.db.AssetByID(ctx, f.assets[1].ID)
	if a0.Note != "first\nmore" || a1.Note != "more" {
		t.Fatalf("append: %q %q", a0.Note, a1.Note)
	}
	if _, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Note: sp("only")}, false); err != nil {
		t.Fatal(err)
	}
	a0, _ = f.db.AssetByID(ctx, f.assets[0].ID)
	if a0.Note != "only" {
		t.Fatalf("replace: %q", a0.Note)
	}
	// Appending nothing changes nothing.
	res, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Note: sp(" "), NoteAppend: true}, false)
	if err != nil || res.Changed != 0 {
		t.Fatalf("empty append: %v %+v", err, res)
	}
	// Too long after appending.
	if _, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Note: sp(strings.Repeat("x", 1990))}, false); err != nil {
		t.Fatal(err)
	}
	_, err = f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Note: sp(strings.Repeat("y", 20)), NoteAppend: true}, false)
	var be *BulkValidationError
	if !errors.As(err, &be) || be.Errors[0].Field != "note" {
		t.Fatalf("want note error, got %v", err)
	}
}

func TestBulkValuesByKey(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	lenF := fieldByKey(t, f.db, f.cableCat.ID, "length_m")
	colF := fieldByKey(t, f.db, f.cableCat.ID, "color")
	res, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Values: map[string]*string{"length_m": sp("7,5"), "color": nil}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Changes["field:color"] != [2]string{"red", ""} {
		t.Fatalf("changes: %v", res.Items[0].Changes)
	}
	got, _ := f.db.AssetByID(ctx, f.assets[0].ID)
	if got.Values[lenF.ID] != "7.5" || got.Values[colF.ID] != "" {
		t.Fatalf("values: %v", got.Values)
	}
	// Required field cleared → error for every asset, nothing written.
	res, err = f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Status: sp(StatusLost), Values: map[string]*string{"length_m": nil}}, false)
	var be *BulkValidationError
	if !errors.As(err, &be) || len(res.Errors) != 3 || res.Errors[0].Field != "f_length_m" || res.Errors[0].Code != "required" {
		t.Fatalf("required: %v %+v", err, res)
	}
	got, _ = f.db.AssetByID(ctx, f.assets[0].ID)
	if got.Status != StatusActive {
		t.Fatal("written despite errors")
	}
	// A key the category lacks is an error for that asset only.
	res, err = f.db.BulkUpdateAssets(ctx, f.ids(f.device), BulkChange{Values: map[string]*string{"watts": sp("10")}}, false)
	if !errors.As(err, &be) || len(res.Errors) != 3 || res.Errors[0].Code != "unknown_field" || res.Errors[0].Field != "f_watts" {
		t.Fatalf("unknown key: %v %+v", err, res)
	}
	for _, e := range res.Errors {
		if e.AssetID == f.device.ID {
			t.Fatalf("device should be fine: %+v", e)
		}
	}
}

func TestBulkCategoryMove(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	// color carries over by key; length_m is lost. Target has no required fields.
	res, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{CategoryID: ip(f.deviceCat.ID), Values: map[string]*string{"watts": sp("60")}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 3 {
		t.Fatalf("changed: %d", res.Changed)
	}
	it := res.Items[0]
	if len(it.Lost) != 1 || it.Lost[0].Key != "length_m" || it.Lost[0].Value != "5" {
		t.Fatalf("lost: %+v", it.Lost)
	}
	if it.Changes["category"][1] != "BulkDevice" || it.Changes["field:watts"][1] != "60" || it.Changes["field:length_m"][1] != "" {
		t.Fatalf("changes: %v", it.Changes)
	}
	got, _ := f.db.AssetByID(ctx, f.assets[0].ID)
	colD := fieldByKey(t, f.db, f.deviceCat.ID, "color")
	if got.CategoryID != f.deviceCat.ID || got.Values[colD.ID] != "red" || got.InventoryNo != f.assets[0].InventoryNo {
		t.Fatalf("moved: %+v", got)
	}
	if res, err = f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{CategoryID: ip(99999)}, false); err == nil || len(res.Errors) != 3 || res.Errors[0].Code != "invalid_category" {
		t.Fatalf("invalid category: %v %+v", err, res)
	}
}

func TestBulkCategoryMoveRequiredMissing(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	// device d1 and one cable move to the cable category (length_m required): the device lacks it.
	res, err := f.db.BulkUpdateAssets(ctx, []int64{f.assets[0].ID, f.device.ID}, BulkChange{CategoryID: ip(f.cableCat.ID), Status: sp(StatusRetired)}, false)
	var be *BulkValidationError
	if !errors.As(err, &be) || len(res.Errors) != 1 {
		t.Fatalf("want one error, got %v %+v", err, res)
	}
	e := res.Errors[0]
	if e.AssetID != f.device.ID || e.InventoryNo != f.device.InventoryNo || e.Field != "f_length_m" || e.Code != "required" {
		t.Fatalf("error: %+v", e)
	}
	// Nothing written, not even for the valid asset.
	got, _ := f.db.AssetByID(ctx, f.assets[0].ID)
	if got.Status != StatusActive {
		t.Fatal("valid asset was written")
	}
	// Setting the field in the same change fixes it.
	res, err = f.db.BulkUpdateAssets(ctx, []int64{f.assets[0].ID, f.device.ID}, BulkChange{CategoryID: ip(f.cableCat.ID), Values: map[string]*string{"length_m": sp("3")}}, false)
	if err != nil || res.Changed != 2 {
		t.Fatalf("fixed: %v %+v", err, res)
	}
}

func TestBulkDryRun(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	res, err := f.db.BulkUpdateAssets(ctx, f.ids(), BulkChange{Status: sp(StatusLost), CategoryID: ip(f.deviceCat.ID)}, true)
	if err != nil || !res.DryRun || res.Changed != 3 || len(res.Items[0].Lost) != 1 {
		t.Fatalf("dry run: %v %+v", err, res)
	}
	got, _ := f.db.AssetByID(ctx, f.assets[0].ID)
	if got.Status != StatusActive || got.CategoryID != f.cableCat.ID || len(got.Values) != 2 {
		t.Fatalf("dry run wrote: %+v", got)
	}
}

func TestBulkLimitsAndUnknownIDs(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	ch := BulkChange{Status: sp(StatusLost)}
	if _, err := f.db.BulkUpdateAssets(ctx, nil, ch, false); !errors.Is(err, ErrBulkEmpty) {
		t.Fatalf("empty: %v", err)
	}
	many := make([]int64, MaxBulkAssets+1)
	for i := range many {
		many[i] = int64(i + 1)
	}
	if _, err := f.db.BulkUpdateAssets(ctx, many, ch, false); !errors.Is(err, ErrBulkTooMany) {
		t.Fatalf("too many: %v", err)
	}
	_, err := f.db.BulkUpdateAssets(ctx, append(f.ids(), 424242), ch, false)
	var nf *BulkNotFoundError
	if !errors.As(err, &nf) || len(nf.IDs) != 1 || nf.IDs[0] != 424242 {
		t.Fatalf("unknown: %v", err)
	}
	got, _ := f.db.AssetByID(ctx, f.assets[0].ID)
	if got.Status != StatusActive {
		t.Fatal("written despite unknown id")
	}
	// Duplicates are collapsed.
	res, err := f.db.BulkUpdateAssets(ctx, append(f.ids(), f.assets[0].ID, f.assets[0].ID), ch, false)
	if err != nil || len(res.Items) != 3 {
		t.Fatalf("dedupe: %v %+v", err, res)
	}
}

func TestAssetIDsByFilter(t *testing.T) {
	ctx := context.Background()
	f := newBulkFixture(t)
	ids, err := f.db.AssetIDsByFilter(ctx, AssetFilter{CategoryID: f.cableCat.ID}, 10)
	if err != nil || len(ids) != 3 {
		t.Fatalf("ids: %v %v", ids, err)
	}
	if _, err := f.db.AssetIDsByFilter(ctx, AssetFilter{CategoryID: f.cableCat.ID}, 2); !errors.Is(err, ErrBulkTooMany) {
		t.Fatalf("limit: %v", err)
	}
}
