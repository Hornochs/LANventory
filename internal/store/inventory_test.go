package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

const testFormat = "{prefix}-{seq:05}"

func wantVE(t *testing.T, err error, field, code string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want validation error %s/%s, got %v", field, code, err)
	}
	if ve.Fields[field] != code {
		t.Fatalf("want %s=%s, got %v", field, code, ve.Fields)
	}
}

func catByPrefix(t *testing.T, db *DB, prefix string) *Category {
	t.Helper()
	cats, err := db.ListCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cats {
		if c.Prefix == prefix {
			return c
		}
	}
	t.Fatalf("category %s not found", prefix)
	return nil
}

func fieldByKey(t *testing.T, db *DB, catID int64, key string) Field {
	t.Helper()
	fs, err := db.CategoryFields(context.Background(), catID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("field %s not found", key)
	return Field{}
}

func TestLocations(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	l := &Location{Name: "  Storage 1 ", Description: "shelf"}
	if err := db.CreateLocation(ctx, l); err != nil || l.Name != "Storage 1" {
		t.Fatalf("create: %v %+v", err, l)
	}
	wantVE(t, db.CreateLocation(ctx, &Location{Name: "storage 1"}), "name", "taken")
	wantVE(t, db.CreateLocation(ctx, &Location{Name: " "}), "name", "required")
	wantVE(t, db.CreateLocation(ctx, &Location{Name: strings.Repeat("x", 101)}), "name", "too_long")
	l2 := &Location{Name: "Storage 2"}
	if err := db.CreateLocation(ctx, l2); err != nil {
		t.Fatal(err)
	}
	l.Name = "Hall"
	if err := db.UpdateLocation(ctx, l); err != nil {
		t.Fatal(err)
	}
	l2.Name = "hall"
	wantVE(t, db.UpdateLocation(ctx, l2), "name", "taken")
	if err := db.UpdateLocation(ctx, &Location{ID: 999, Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	list, _ := db.ListLocations(ctx)
	if len(list) != 2 || list[0].Name != "Hall" {
		t.Fatalf("list: %+v", list)
	}

	// Delete protection.
	cat := catByPrefix(t, db, "KG")
	a := &Asset{CategoryID: cat.ID, Name: "Cable", LocationID: l.ID, Values: map[int64]string{fieldByKey(t, db, cat.ID, "length_m").ID: "3"}}
	if _, err := db.CreateAsset(ctx, a, testFormat); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteLocation(ctx, l.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete in use: %v", err)
	}
	if err := db.DeleteLocation(ctx, l2.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteLocation(ctx, l2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if got, _ := db.LocationByID(ctx, l.ID); got.AssetCount != 1 {
		t.Fatalf("asset count: %+v", got)
	}
}

func TestSeedingRunsOnce(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	cats, _ := db.ListCategories(ctx)
	if len(cats) != 7 {
		t.Fatalf("seeded %d categories", len(cats))
	}
	sl := catByPrefix(t, db, "SL")
	if sl.Name != "Steckerleiste" || !sl.DGUVRequired || sl.DGUVIntervalMonths != 12 {
		t.Fatalf("SL: %+v", sl)
	}
	if nk := catByPrefix(t, db, "NK"); nk.DGUVRequired {
		t.Fatal("network cable must not require DGUV")
	}
	fs, _ := db.CategoryFields(ctx, sl.ID)
	if len(fs) != 4 || fs[0].Key != "length_m" || fs[0].Unit != "m" || !fs[0].Required || fs[0].Type != FieldNumber {
		t.Fatalf("SL fields: %+v", fs)
	}
	kt := catByPrefix(t, db, "KT")
	if f := fieldByKey(t, db, kt.ID, "connector"); f.Type != FieldSelect || len(f.Options) != 4 {
		t.Fatalf("connector: %+v", f)
	}
	// An admin deletes one; it does not come back.
	if err := db.DeleteCategory(ctx, sl.ID); err != nil {
		t.Fatal(err)
	}
	if seeded, err := db.SeedCategories(ctx); err != nil || seeded {
		t.Fatalf("second seeding: %v %v", seeded, err)
	}
	if again, _ := db.ListCategories(ctx); len(again) != 6 {
		t.Fatalf("%d categories after reseed", len(again))
	}
}

func TestCategoryValidationAndDelete(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	c := &Category{Name: "Beamer", Prefix: " bm ", DGUVRequired: true}
	if err := db.CreateCategory(ctx, c); err != nil || c.Prefix != "BM" || c.DGUVIntervalMonths != 12 {
		t.Fatalf("create: %v %+v", err, c)
	}
	for _, p := range []string{"A B", "ABCDEFGHI", "Ä", "A-1"} {
		wantVE(t, db.CreateCategory(ctx, &Category{Name: "X" + p, Prefix: p}), "prefix", "invalid_prefix")
	}
	// The prefix is optional, and several categories may have none.
	for _, name := range []string{"Without A", "Without B"} {
		if err := db.CreateCategory(ctx, &Category{Name: name, Prefix: "  "}); err != nil {
			t.Fatalf("no prefix %s: %v", name, err)
		}
	}
	wantVE(t, db.CreateCategory(ctx, &Category{Name: "beamer", Prefix: "BX"}), "name", "taken")
	wantVE(t, db.CreateCategory(ctx, &Category{Name: "Other", Prefix: "bm"}), "prefix", "taken")
	wantVE(t, db.CreateCategory(ctx, &Category{Name: "Other", Prefix: "OT", DGUVIntervalMonths: 121}), "interval", "invalid_interval")
	wantVE(t, db.CreateCategory(ctx, &Category{Name: "", Prefix: "OT"}), "name", "required")
	c.Name = "Projektor"
	c.DGUVIntervalMonths = 24
	if err := db.UpdateCategory(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.CategoryByID(ctx, c.ID); got.Name != "Projektor" || got.DGUVIntervalMonths != 24 {
		t.Fatalf("update: %+v", got)
	}
	if _, err := db.CategoryByID(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing category")
	}

	if _, err := db.CreateAsset(ctx, &Asset{CategoryID: c.ID, Name: "B1"}, testFormat); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteCategory(ctx, c.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete in use: %v", err)
	}
	empty := &Category{Name: "Leer", Prefix: "LE"}
	_ = db.CreateCategory(ctx, empty)
	_ = db.CreateField(ctx, &Field{CategoryID: empty.ID, Key: "a", Label: "A", Type: FieldText})
	if err := db.DeleteCategory(ctx, empty.ID); err != nil {
		t.Fatal(err)
	}
	if fs, _ := db.CategoryFields(ctx, empty.ID); len(fs) != 0 {
		t.Fatal("fields survived category delete")
	}
}

func TestFieldValidationAndEditor(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	c := &Category{Name: "Test", Prefix: "TT"}
	_ = db.CreateCategory(ctx, c)
	mk := func(key, typ string, opts ...string) *Field {
		return &Field{CategoryID: c.ID, Key: key, Label: "L " + key, Type: typ, Options: opts}
	}
	for _, k := range []string{"", "Upper", "1abc", "a-b", "a b", strings.Repeat("a", 33)} {
		wantVE(t, db.CreateField(ctx, mk(k, FieldText)), "key", "invalid_key")
	}
	wantVE(t, db.CreateField(ctx, mk("t", "blob")), "type", "invalid_type")
	wantVE(t, db.CreateField(ctx, mk("s", FieldSelect)), "options", "invalid_options")
	wantVE(t, db.CreateField(ctx, mk("s", FieldSelect, "a", "a")), "options", "invalid_options")
	wantVE(t, db.CreateField(ctx, mk("s", FieldSelect, strings.Repeat("x", 65))), "options", "invalid_options")
	wantVE(t, db.CreateField(ctx, &Field{CategoryID: c.ID, Key: "x", Type: FieldText}), "label", "required")

	a, b, d := mk("a", FieldText), mk("b", FieldNumber), mk("d", FieldSelect, " x ", "y", "")
	b.Unit = "m"
	for _, f := range []*Field{a, b, d} {
		if err := db.CreateField(ctx, f); err != nil {
			t.Fatal(err)
		}
	}
	wantVE(t, db.CreateField(ctx, mk("a", FieldText)), "key", "taken")
	if got, _ := db.FieldByID(ctx, d.ID); len(got.Options) != 2 || got.Options[0] != "x" {
		t.Fatalf("options: %+v", got)
	}
	keys := func() string {
		fs, _ := db.CategoryFields(ctx, c.ID)
		var out []string
		for _, f := range fs {
			out = append(out, f.Key)
		}
		return strings.Join(out, "")
	}
	if keys() != "abd" {
		t.Fatalf("order %s", keys())
	}
	_ = db.MoveField(ctx, d.ID, -1)
	if keys() != "adb" {
		t.Fatalf("after up: %s", keys())
	}
	_ = db.MoveField(ctx, d.ID, -1)
	_ = db.MoveField(ctx, d.ID, -1) // already first: no-op
	if keys() != "dab" {
		t.Fatalf("after up twice: %s", keys())
	}
	_ = db.MoveField(ctx, b.ID, 1) // last: no-op
	_ = db.MoveField(ctx, d.ID, 1)
	if keys() != "adb" {
		t.Fatalf("after down: %s", keys())
	}

	// Type change refused while values exist; allowed otherwise. A non-number drops the unit.
	as := &Asset{CategoryID: c.ID, Name: "T", Values: map[int64]string{b.ID: "1,5", d.ID: "x"}}
	if _, err := db.CreateAsset(ctx, as, testFormat); err != nil {
		t.Fatal(err)
	}
	upd := *b
	upd.Type = FieldText
	wantVE(t, db.UpdateField(ctx, &upd), "type", "type_locked")
	upd = *b
	upd.Label = "Renamed"
	upd.Required = true
	if err := db.UpdateField(ctx, &upd); err != nil {
		t.Fatalf("label change: %v", err)
	}
	// Removing an option in use is refused; unused option may go; adding is fine.
	upd = *d
	upd.Options = []string{"y"}
	wantVE(t, db.UpdateField(ctx, &upd), "options", "option_in_use")
	upd.Options = []string{"x", "z"}
	if err := db.UpdateField(ctx, &upd); err != nil {
		t.Fatalf("drop unused option: %v", err)
	}
	// A field without values can change type.
	upd = *a
	upd.Type = FieldDate
	if err := db.UpdateField(ctx, &upd); err != nil {
		t.Fatalf("type change without values: %v", err)
	}
	// Deleting a field deletes its values.
	if err := db.DeleteField(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := db.AssetByID(ctx, as.ID)
	if _, ok := got.Values[b.ID]; ok || got.Values[d.ID] != "x" {
		t.Fatalf("values after field delete: %v", got.Values)
	}
	if err := db.DeleteField(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete twice")
	}
}

func TestNormalizeValue(t *testing.T) {
	sel := &Field{Type: FieldSelect, Options: []string{"Cat 6", "LWL"}}
	cases := []struct {
		f    *Field
		in   string
		want string
		code string
	}{
		{&Field{Type: FieldNumber}, "1,5", "1.5", ""},
		{&Field{Type: FieldNumber}, " 007.50 ", "7.5", ""},
		{&Field{Type: FieldNumber}, "-0", "0", ""},
		{&Field{Type: FieldNumber}, "+3", "3", ""},
		{&Field{Type: FieldNumber}, ".5", "0.5", ""},
		{&Field{Type: FieldNumber}, "10", "10", ""},
		{&Field{Type: FieldNumber}, "1.2.3", "", "invalid_number"},
		{&Field{Type: FieldNumber}, "abc", "", "invalid_number"},
		{&Field{Type: FieldNumber}, "1e5", "", "invalid_number"},
		{&Field{Type: FieldNumber}, "NaN", "", "invalid_number"},
		{&Field{Type: FieldNumber}, "1,000.5", "", "invalid_number"},
		{&Field{Type: FieldBool}, "on", "1", ""},
		{&Field{Type: FieldBool}, "0", "0", ""},
		{&Field{Type: FieldBool}, "maybe", "", "invalid_bool"},
		{&Field{Type: FieldDate}, "2024-02-29", "2024-02-29", ""},
		{&Field{Type: FieldDate}, "2023-02-29", "", "invalid_date"},
		{&Field{Type: FieldDate}, "29.02.2024", "", "invalid_date"},
		{sel, "LWL", "LWL", ""},
		{sel, "lwl", "", "invalid_option"},
		{&Field{Type: FieldText}, "  hi  ", "hi", ""},
		{&Field{Type: FieldText}, strings.Repeat("ä", 501), "", "too_long"},
		{&Field{Type: FieldText}, strings.Repeat("ä", 500), strings.Repeat("ä", 500), ""},
		{&Field{Type: FieldNumber}, "   ", "", ""},
	}
	for _, c := range cases {
		got, code := NormalizeValue(c.f, c.in)
		if got != c.want || code != c.code {
			t.Errorf("%s %q: got %q/%q want %q/%q", c.f.Type, c.in, got, code, c.want, c.code)
		}
	}
}

func TestAssetCRUDAndValidation(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	sl := catByPrefix(t, db, "SL")
	length := fieldByKey(t, db, sl.ID, "length_m")
	sw := fieldByKey(t, db, sl.ID, "switch")
	loc := &Location{Name: "Shelf"}
	_ = db.CreateLocation(ctx, loc)

	// Required field, name, status, date, interval, location.
	_, err := db.CreateAsset(ctx, &Asset{CategoryID: sl.ID, Name: "", Status: "bogus", PurchaseDate: "2020-13-01",
		DGUVIntervalMonths: 500, LocationID: 777, Values: map[int64]string{sw.ID: "x"}}, testFormat)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want validation error: %v", err)
	}
	want := map[string]string{"name": "required", "status": "invalid_status", "purchase_date": "invalid_date",
		"dguv_interval": "invalid_interval", "location": "invalid_location", "f_length_m": "required", "f_switch": "invalid_bool"}
	for k, v := range want {
		if ve.Fields[k] != v {
			t.Errorf("%s: got %q want %q (%v)", k, ve.Fields[k], v, ve.Fields)
		}
	}
	// A failed create must not burn a number.
	a := &Asset{CategoryID: sl.ID, Name: "Strip", SerialNo: "SN1", Manufacturer: "Acme", Model: "X", LocationID: loc.ID,
		Note: "n", PurchaseDate: "2022-05-01", DGUVIntervalMonths: 6,
		Values: map[int64]string{length.ID: "1,50", sw.ID: "1"}}
	changes, err := db.CreateAsset(ctx, a, testFormat)
	if err != nil {
		t.Fatal(err)
	}
	if a.InventoryNo != "SL-00001" {
		t.Fatalf("number: %s", a.InventoryNo)
	}
	if changes["field:length_m"] != [2]string{"", "1.5"} || changes["name"][1] != "Strip" || changes["location"][1] != "Shelf" {
		t.Fatalf("changes: %v", changes)
	}
	got, err := db.AssetByID(ctx, a.ID)
	if err != nil || got.Values[length.ID] != "1.5" || got.LocationName != "Shelf" || got.CategoryName != "Steckerleiste" ||
		got.DGUVIntervalMonths != 6 || got.PurchaseDate != "2022-05-01" || got.Status != StatusActive {
		t.Fatalf("get: %+v %v", got, err)
	}

	// An unknown category is refused.
	_, err = db.UpdateAsset(ctx, &Asset{ID: a.ID, CategoryID: 999, Name: "x"})
	wantVE(t, err, "category", "invalid_category")
	// Update: the number stays; status change and value removal are tracked.
	up := &Asset{ID: a.ID, InventoryNo: "HACK", Name: "Strip 2", Status: StatusDefective, LocationID: 0,
		Values: map[int64]string{length.ID: "2"}}
	changes, err = db.UpdateAsset(ctx, up)
	if err != nil {
		t.Fatal(err)
	}
	if changes["status"] != [2]string{"active", "defective"} || changes["field:switch"] != [2]string{"1", ""} ||
		changes["location"] != [2]string{"Shelf", ""} || changes["field:length_m"] != [2]string{"1.5", "2"} {
		t.Fatalf("update changes: %v", changes)
	}
	if _, ok := changes["inventory_no"]; ok {
		t.Fatal("inventory number changed")
	}
	got, _ = db.AssetByID(ctx, a.ID)
	if got.InventoryNo != "SL-00001" || got.CategoryID != sl.ID || got.Name != "Strip 2" || len(got.Values) != 1 || got.LocationID != 0 {
		t.Fatalf("after update: %+v", got)
	}
	// No change: empty result.
	same := &Asset{ID: a.ID, Name: "Strip 2", Status: StatusDefective, Values: map[int64]string{length.ID: "2"}}
	if changes, err = db.UpdateAsset(ctx, same); err != nil || len(changes) != 0 {
		t.Fatalf("no-op update: %v %v", changes, err)
	}
	if _, err := db.UpdateAsset(ctx, &Asset{ID: 4242, Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := db.DeleteAsset(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AssetByID(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("asset not deleted")
	}
	if err := db.DeleteAsset(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete twice")
	}
	if _, err := db.CreateAsset(ctx, &Asset{CategoryID: 12345, Name: "x"}, testFormat); err == nil {
		t.Fatal("unknown category accepted")
	} else {
		wantVE(t, err, "category", "invalid_category")
	}
}

func TestFormatNumber(t *testing.T) {
	for _, c := range []struct {
		format, prefix string
		seq            int64
		want           string
	}{
		{"{prefix}-{seq:05}", "SL", 42, "SL-00042"},
		{"{prefix}-{seq}", "SL", 42, "SL-42"},
		{"{seq:03}", "X", 7, "007"},
		{"{seq:03}", "X", 12345, "12345"},
		{"LAN/{prefix}/{seq:04}", "KT", 1, "LAN/KT/0001"},
		{"{prefix}{prefix}-{seq}", "A", 3, "AA-3"},
		{"{prefix}-{seq:05}", "", 42, "00042"},
		{"LAN/{prefix}/{seq:04}", "", 1, "LAN/0001"},
		{"{seq:04}", "", 3, "0003"},
	} {
		if got := FormatNumber(c.format, c.prefix, c.seq); got != c.want {
			t.Errorf("%s: got %s want %s", c.format, got, c.want)
		}
	}
}

func TestInventoryNumbers(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	sl, kt := catByPrefix(t, db, "SL"), catByPrefix(t, db, "KT")
	mk := func(cat *Category, manual, format string) (*Asset, error) {
		a := &Asset{CategoryID: cat.ID, Name: "x", InventoryNo: manual,
			Values: map[int64]string{fieldByKey(t, db, cat.ID, "length_m").ID: "1"}}
		_, err := db.CreateAsset(ctx, a, format)
		return a, err
	}
	// Sequence per prefix.
	for i, want := range []string{"SL-00001", "SL-00002"} {
		a, err := mk(sl, "", testFormat)
		if err != nil || a.InventoryNo != want {
			t.Fatalf("%d: %v %v", i, a, err)
		}
	}
	if a, _ := mk(kt, "", testFormat); a.InventoryNo != "KT-00001" {
		t.Fatalf("KT: %s", a.InventoryNo)
	}
	// A manual number does not consume the sequence; a collision is reported (case-insensitive).
	if a, err := mk(sl, "OLD-7", testFormat); err != nil || a.InventoryNo != "OLD-7" {
		t.Fatalf("manual: %v %v", a, err)
	}
	_, err := mk(sl, "old-7", testFormat)
	wantVE(t, err, "inventory_no", "taken")
	_, err = mk(sl, "SL-00001", testFormat)
	wantVE(t, err, "inventory_no", "taken")
	// A manual number ahead of the sequence is skipped.
	if _, err := mk(sl, "SL-00003", testFormat); err != nil {
		t.Fatal(err)
	}
	if a, _ := mk(sl, "", testFormat); a.InventoryNo != "SL-00004" {
		t.Fatalf("skip taken: %s", a.InventoryNo)
	}
	// Without {prefix} in the format all categories share one sequence, and
	// numbers that are already taken (imported 0001, 0002) are skipped.
	for _, no := range []string{"0001", "0002"} {
		if _, err := mk(kt, no, testFormat); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []struct {
		cat *Category
		no  string
	}{{sl, "0003"}, {kt, "0004"}, {sl, "0005"}} {
		if a, _ := mk(want.cat, "", "{seq:04}"); a.InventoryNo != want.no {
			t.Fatalf("shared %d: %s want %s", i, a.InventoryNo, want.no)
		}
	}
	// A changed prefix does not renumber assets and starts its own sequence.
	sl.Prefix = "ST"
	if err := db.UpdateCategory(ctx, sl); err != nil {
		t.Fatal(err)
	}
	if a, _ := mk(sl, "", testFormat); a.InventoryNo != "ST-00001" {
		t.Fatalf("new prefix: %s", a.InventoryNo)
	}
	page, _ := db.ListAssets(ctx, AssetFilter{Q: "SL-00001"})
	if page.Total != 1 {
		t.Fatal("old number lost")
	}
	// Overlong manual numbers are refused.
	_, err = mk(kt, strings.Repeat("9", 65), testFormat)
	wantVE(t, err, "inventory_no", "too_long")
}

func TestInventoryNumbersConcurrent(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	cat := catByPrefix(t, db, "KG")
	fid := fieldByKey(t, db, cat.ID, "length_m").ID
	const n = 20
	var wg sync.WaitGroup
	nums := make(chan string, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := &Asset{CategoryID: cat.ID, Name: fmt.Sprintf("c%d", i), Values: map[int64]string{fid: "2"}}
			if _, err := db.CreateAsset(ctx, a, testFormat); err != nil {
				errs <- err
				return
			}
			nums <- a.InventoryNo
		}(i)
	}
	wg.Wait()
	close(nums)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for no := range nums {
		if seen[no] {
			t.Fatalf("duplicate number %s", no)
		}
		seen[no] = true
	}
	if len(seen) != n {
		t.Fatalf("%d numbers", len(seen))
	}
	for i := 1; i <= n; i++ {
		if !seen[fmt.Sprintf("KG-%05d", i)] {
			t.Fatalf("gap: KG-%05d missing", i)
		}
	}
}

func TestListAssetsSearchFilterSortPaging(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	sl, kt, nk := catByPrefix(t, db, "SL"), catByPrefix(t, db, "KT"), catByPrefix(t, db, "NK")
	l1, l2 := &Location{Name: "A"}, &Location{Name: "B"}
	_ = db.CreateLocation(ctx, l1)
	_ = db.CreateLocation(ctx, l2)
	lenSL := fieldByKey(t, db, sl.ID, "length_m").ID
	swSL := fieldByKey(t, db, sl.ID, "switch").ID
	lenKT := fieldByKey(t, db, kt.ID, "length_m").ID
	conKT := fieldByKey(t, db, kt.ID, "connector").ID
	lenNK := fieldByKey(t, db, nk.ID, "length_m").ID
	mk := func(a *Asset) *Asset {
		t.Helper()
		if _, err := db.CreateAsset(ctx, a, testFormat); err != nil {
			t.Fatal(err)
		}
		return a
	}
	mk(&Asset{CategoryID: sl.ID, Name: "Power strip", SerialNo: "ABC-100", Manufacturer: "Brennenstuhl", LocationID: l1.ID,
		Values: map[int64]string{lenSL: "3", swSL: "1"}})
	mk(&Asset{CategoryID: sl.ID, Name: "Plain strip", Model: "50%_off", LocationID: l2.ID, Status: StatusDefective,
		Values: map[int64]string{lenSL: "1.5", swSL: "0"}})
	mk(&Asset{CategoryID: kt.ID, Name: "Drum", LocationID: l1.ID, Status: StatusRetired,
		Values: map[int64]string{lenKT: "25", conKT: "CEE 16 A blau"}})
	mk(&Asset{CategoryID: kt.ID, Name: "Drum two", Status: StatusLost, Values: map[int64]string{lenKT: "10", conKT: "Schuko"}})
	mk(&Asset{CategoryID: nk.ID, Name: "Patch", Values: map[int64]string{lenNK: "2"}})

	count := func(f AssetFilter) int {
		t.Helper()
		p, err := db.ListAssets(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if p.Total != len(p.Assets) {
			t.Fatalf("total %d vs %d rows", p.Total, len(p.Assets))
		}
		return p.Total
	}
	for _, c := range []struct {
		name string
		f    AssetFilter
		want int
	}{
		{"all", AssetFilter{}, 5},
		{"search number", AssetFilter{Q: "sl-0000"}, 2},
		{"search name ci", AssetFilter{Q: "STRIP"}, 2},
		{"search serial", AssetFilter{Q: "abc-1"}, 1},
		{"search manufacturer", AssetFilter{Q: "brennen"}, 1},
		{"search model literal percent", AssetFilter{Q: "50%_"}, 1},
		{"percent is not a wildcard", AssetFilter{Q: "%"}, 1},
		{"underscore is not a wildcard", AssetFilter{Q: "p_wer"}, 0},
		{"search field value", AssetFilter{Q: "CEE 16"}, 1},
		{"two words", AssetFilter{Q: "strip power"}, 1},
		{"category", AssetFilter{CategoryID: kt.ID}, 2},
		{"location", AssetFilter{LocationID: l1.ID}, 2},
		{"status", AssetFilter{Status: StatusLost}, 1},
		{"field select", AssetFilter{CategoryID: kt.ID, Fields: map[string]string{"connector": "Schuko"}}, 1},
		{"field bool", AssetFilter{CategoryID: sl.ID, Fields: map[string]string{"switch": "1"}}, 1},
		{"field without category ignored", AssetFilter{Fields: map[string]string{"switch": "1"}}, 5},
		{"field of other category", AssetFilter{CategoryID: kt.ID, Fields: map[string]string{"switch": "1"}}, 0},
		{"combination", AssetFilter{CategoryID: sl.ID, LocationID: l2.ID, Status: StatusDefective, Q: "plain"}, 1},
		{"combination no match", AssetFilter{CategoryID: sl.ID, LocationID: l2.ID, Status: StatusActive}, 0},
	} {
		if got := count(c.f); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}

	// Sorting.
	first := func(sort string, desc bool) string {
		p, err := db.ListAssets(ctx, AssetFilter{Sort: sort, Desc: desc})
		if err != nil {
			t.Fatal(err)
		}
		return p.Assets[0].Name
	}
	if first("inventory_no", false) != "Drum" || first("name", false) != "Drum" || first("name", true) != "Power strip" ||
		first("category", false) != "Drum" || first("status", false) != "Power strip" || first("location", true) != "Plain strip" ||
		first("bogus", false) != "Drum" {
		t.Fatalf("sorting: %s %s %s %s %s %s", first("inventory_no", false), first("name", false), first("name", true),
			first("category", false), first("status", false), first("location", true))
	}

	// Paging.
	for i := 0; i < 60; i++ {
		mk(&Asset{CategoryID: nk.ID, Name: fmt.Sprintf("bulk %02d", i), Values: map[int64]string{lenNK: "1"}})
	}
	p, _ := db.ListAssets(ctx, AssetFilter{Page: 1})
	if p.Total != 65 || len(p.Assets) != 50 || p.Pages != 2 || p.Page != 1 {
		t.Fatalf("page 1: %d %d %d", p.Total, len(p.Assets), p.Pages)
	}
	p, _ = db.ListAssets(ctx, AssetFilter{Page: 2})
	if len(p.Assets) != 15 || p.Page != 2 {
		t.Fatalf("page 2: %d", len(p.Assets))
	}
	p, _ = db.ListAssets(ctx, AssetFilter{Page: 99, PerPage: 10})
	if p.Page != 7 || len(p.Assets) != 5 {
		t.Fatalf("clamped page: %d %d", p.Page, len(p.Assets))
	}
	p, _ = db.ListAssets(ctx, AssetFilter{Q: "nothing matches"})
	if p.Total != 0 || p.Pages != 1 || p.Page != 1 {
		t.Fatalf("empty: %+v", p)
	}

	st, _ := db.CountAssetsByStatus(ctx)
	if st[StatusActive] != 62 || st[StatusDefective] != 1 || st[StatusLost] != 1 || st[StatusRetired] != 1 {
		t.Fatalf("status counts: %v", st)
	}
	cats, _ := db.ListCategories(ctx)
	for _, c := range cats {
		if c.Prefix == "NK" && c.AssetCount != 61 {
			t.Fatalf("NK count %d", c.AssetCount)
		}
	}
}

func TestAuditByObject(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	_ = db.Audit(ctx, AuditEntry{Action: "asset.create", ObjectType: "asset", ObjectID: "1", Details: map[string]any{"x": 1}})
	_ = db.Audit(ctx, AuditEntry{Action: "asset.update", ObjectType: "asset", ObjectID: "1"})
	_ = db.Audit(ctx, AuditEntry{Action: "asset.update", ObjectType: "asset", ObjectID: "11"})
	_ = db.Audit(ctx, AuditEntry{Action: "user.create", ObjectType: "user", ObjectID: "1"})
	es, err := db.AuditByObject(ctx, "asset", "1", 10)
	if err != nil || len(es) != 2 || es[0].Action != "asset.update" || es[1].Details["x"] == nil {
		t.Fatalf("%+v %v", es, err)
	}
}

func TestInventoryNumbersWithoutPrefix(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	a, b := &Category{Name: "Misc A"}, &Category{Name: "Misc B"}
	for _, c := range []*Category{a, b} {
		if err := db.CreateCategory(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// Categories without prefix share a sequence; the separator is dropped.
	for i, c := range []*Category{a, b, a} {
		as := &Asset{CategoryID: c.ID, Name: "x"}
		if _, err := db.CreateAsset(ctx, as, testFormat); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("%05d", i+1); as.InventoryNo != want {
			t.Fatalf("%d: %s want %s", i, as.InventoryNo, want)
		}
	}
}

func TestChangeAssetCategory(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	sl, kg := catByPrefix(t, db, "SL"), catByPrefix(t, db, "KG")
	slFields, _ := db.CategoryFields(ctx, sl.ID)
	kgFields, _ := db.CategoryFields(ctx, kg.ID)
	a := &Asset{CategoryID: sl.ID, Name: "Kabel", Values: map[int64]string{
		fieldByKey(t, db, sl.ID, "length_m").ID: "3", fieldByKey(t, db, sl.ID, "sockets").ID: "5"}}
	if _, err := db.CreateAsset(ctx, a, "{seq:04}"); err != nil {
		t.Fatal(err)
	}
	insp := &Inspection{AssetID: a.ID, InspectedOn: "2026-01-10", InspectorName: "x", RecordedBy: 0, Result: ResultPass}
	if _, _, err := db.CreateInspection(ctx, insp, false, false); err != nil {
		t.Fatal(err)
	}
	// The length moves by key, the number of sockets is lost.
	vals, lost := CarryValues(a, slFields, kgFields)
	if vals[fieldByKey(t, db, kg.ID, "length_m").ID] != "3" || len(lost) != 1 || lost[0].Key != "sockets" {
		t.Fatalf("carry: %v %v", vals, lost)
	}
	up := *a
	up.CategoryID, up.Values = kg.ID, vals
	changes, err := db.UpdateAsset(ctx, &up)
	if err != nil {
		t.Fatal(err)
	}
	if changes["category"] != [2]string{"Steckerleiste", "Kaltgerätekabel"} || changes["field:sockets"] != [2]string{"5", ""} {
		t.Fatalf("changes: %v", changes)
	}
	if _, ok := changes["field:length_m"]; ok {
		t.Errorf("unchanged length reported: %v", changes)
	}
	got, err := db.AssetByID(ctx, a.ID)
	if err != nil || got.CategoryID != kg.ID || got.InventoryNo != a.InventoryNo || len(got.Values) != 1 {
		t.Fatalf("after move: %+v %v", got, err)
	}
	// The inspection history stays with the asset.
	if list, err := db.ListInspections(ctx, InspectionFilter{AssetID: a.ID}); err != nil || list.Total != 1 {
		t.Fatalf("inspections: %+v %v", list, err)
	}
	// A required field of the new category must be filled.
	up2 := *got
	up2.CategoryID, up2.Values = sl.ID, nil
	_, err = db.UpdateAsset(ctx, &up2)
	wantVE(t, err, "f_length_m", "required")
}
