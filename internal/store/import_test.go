package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSniffDelimiter(t *testing.T) {
	for _, c := range []struct {
		in   string
		want rune
	}{
		{"a;b;c\n1,2,3", ';'},
		{"a,b,c\n1;2;3", ','},
		{"a\tb\tc", '\t'},
		{"\"a;b;c\",d,e\n", ','}, // separators inside quotes do not count
		{"single", ','},
		{"a;b,c;d", ';'},
		{"a,b;c", ';'}, // tie prefers ;
	} {
		if got := SniffDelimiter(c.in); got != c.want {
			t.Errorf("SniffDelimiter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDecodeEncodings(t *testing.T) {
	// "Länge;Größe;Preis €;Ü" in Windows-1252 (ä F6 ö ß DF € 80 Ü DC).
	cp := []byte("L\xe4nge;Gr\xf6\xdfe;Preis \x80;\xdc\n1;2;3;4\n")
	tb, err := ParseCSV(cp, "auto", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if tb.Encoding != EncCP1252 || tb.Delimiter != ';' {
		t.Fatalf("detected %q %q", tb.Encoding, tb.Delimiter)
	}
	if got := strings.Join(tb.Header, "|"); got != "Länge|Größe|Preis €|Ü" {
		t.Fatalf("header %q", got)
	}
	// ü (FC) and the quotes at 0x93/0x94.
	tb, err = ParseCSV([]byte("a\n\xfcber \x93x\x94\n"), "auto", "auto")
	if err != nil || tb.Rows[0][0] != "über “x”" {
		t.Fatalf("%v %v", tb, err)
	}
	// Valid UTF-8 is UTF-8, a BOM is stripped.
	tb, err = ParseCSV([]byte("\xef\xbb\xbfName,Länge\nSchuko,2\n"), "auto", "auto")
	if err != nil || tb.Encoding != EncUTF8 || tb.Header[0] != "Name" || tb.Header[1] != "Länge" {
		t.Fatalf("%+v %v", tb, err)
	}
	// Explicit UTF-8 rejects invalid bytes; explicit Windows-1252 decodes anything.
	if _, err := ParseCSV(cp, "auto", EncUTF8); err == nil {
		t.Fatal("invalid UTF-8 accepted as UTF-8")
	}
	var fe *ImportFileError
	if _, err := ParseCSV(cp, "auto", EncUTF8); !errors.As(err, &fe) || fe.Code != "encoding" {
		t.Fatalf("error %v", err)
	}
	tb, err = ParseCSV([]byte("a\nä\n"), "auto", EncCP1252) // UTF-8 bytes read as cp1252: mojibake, as requested
	if err != nil || tb.Rows[0][0] != "Ã¤" {
		t.Fatalf("%+v %v", tb, err)
	}
	// UTF-16 with BOM (Excel "Unicode text").
	u16 := []byte{0xFF, 0xFE, 'a', 0, '\t', 0, 'b', 0, '\n', 0, 0xE4, 0, '\t', 0, '1', 0, '\n', 0}
	tb, err = ParseCSV(u16, "auto", "auto")
	if err != nil || tb.Delimiter != '\t' || tb.Rows[0][0] != "ä" {
		t.Fatalf("%+v %v", tb, err)
	}
}

func TestParseCSVShape(t *testing.T) {
	tb, err := ParseCSV([]byte("A;B;C;\r\n1;2\r\n\r\n;;;\r\n\"x;y\";\"q\"\"r\";3;4\r\n"), "auto", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if len(tb.Header) != 3 || len(tb.Rows) != 2 {
		t.Fatalf("header %v rows %v", tb.Header, tb.Rows)
	}
	if tb.Rows[0][2] != "" || tb.Rows[1][0] != "x;y" || tb.Rows[1][1] != `q"r` {
		t.Fatalf("rows %q", tb.Rows)
	}
	if _, err := ParseCSV([]byte("  \n"), "auto", "auto"); err == nil {
		t.Fatal("empty file accepted")
	}
	// Explicit delimiter overrides sniffing.
	tb, _ = ParseCSV([]byte("a,b;c\n1,2;3\n"), DelimSemicolon, "auto")
	if len(tb.Header) != 2 || tb.Header[0] != "a,b" {
		t.Fatalf("header %q", tb.Header)
	}
}

func TestSuggestMapping(t *testing.T) {
	fields := []FieldTarget{{Key: "length_m", Label: "Länge"}, {Key: "managed", Label: "Managed"}, {Key: "cable_category", Label: "Kategorie"}}
	got := SuggestMapping([]string{"inventory_no", "category", "category_prefix", "name", "dguv_interval_months", "field:length_m",
		"last_inspected_on", "next_due", "field:unknown"}, fields)
	want := []string{"inventory_no", "category", "", "name", "dguv_interval_months", "field:length_m", "", "", ""}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("export headers: %q", got)
	}
	got = SuggestMapping([]string{"Inventarnummer", "Kategorie", "Bezeichnung", "Seriennummer", "Hersteller", "Modell", "Lagerort",
		"Status", "Kaufdatum", "Prüfintervall", "Bemerkung", "Länge", "Managed", "Foo", "Notiz"}, fields)
	want = []string{"inventory_no", "category", "name", "serial_no", "manufacturer", "model", "location", "status", "purchase_date",
		"dguv_interval_months", "note", "field:length_m", "field:managed", "", ""} // "Notiz" repeats note: first column wins
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("german headers: %q", got)
	}
	got = SuggestMapping([]string{"Standort", "Name", "NAME", "length_m"}, fields)
	if strings.Join(got, ",") != "location,name,,field:length_m" {
		t.Fatalf("%q", got)
	}
}

func TestImportValueParsers(t *testing.T) {
	for in, want := range map[string]string{"1": "1", "Ja": "1", "yes": "1", "TRUE": "1", "x": "1", "X": "1", "0": "0", "nein": "0", "No": "0", "false": "0"} {
		if got, ok := parseImportBool(in); !ok || got != want {
			t.Errorf("bool %q = %q %v", in, got, ok)
		}
	}
	if _, ok := parseImportBool("vielleicht"); ok {
		t.Error("bool accepted")
	}
	for in, want := range map[string]string{"2024-03-05": "2024-03-05", "05.03.2024": "2024-03-05", "5.3.2024": "2024-03-05"} {
		if got, ok := parseImportDate(in); !ok || got != want {
			t.Errorf("date %q = %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"31.02.2024", "2024/03/05", "03/05/2024", "x"} {
		if _, ok := parseImportDate(in); ok {
			t.Errorf("date %q accepted", in)
		}
	}
	for in, want := range map[string]string{"active": "active", "Aktiv": "active", "defekt": "defective", "Ausgemustert": "retired",
		"verloren": "lost", "verschollen": "lost", "lost": "lost"} {
		if got, ok := parseImportStatus(in); !ok || got != want {
			t.Errorf("status %q = %q %v", in, got, ok)
		}
	}
	if _, ok := parseImportStatus("kaputt"); ok {
		t.Error("status accepted")
	}
	for in, want := range map[string]string{"'=1+1": "=1+1", "'-5": "-5", "'abc": "'abc", "  x ": "x", "'@a": "@a", "'": "'"} {
		if got := unguardCell(in); got != want {
			t.Errorf("unguard %q = %q, want %q", in, got, want)
		}
	}
}

type importFixture struct {
	t    *testing.T
	db   *DB
	ctx  context.Context
	opts ImportOptions
}

func newImportFixture(t *testing.T) *importFixture {
	return &importFixture{t: t, db: openTest(t), ctx: context.Background(),
		opts: ImportOptions{CreateLocations: true, NumberFormat: "{prefix}-{seq:05}"}}
}

// run parses csvText, maps it automatically and runs the import.
func (f *importFixture) run(csvText string, commit bool) *ImportResult {
	f.t.Helper()
	tb, err := ParseCSV([]byte(csvText), "auto", "auto")
	if err != nil {
		f.t.Fatal(err)
	}
	targets, err := f.db.ImportFieldTargets(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	f.opts.Mapping = SuggestMapping(tb.Header, targets)
	res, err := f.db.RunImport(f.ctx, tb, f.opts, commit)
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *importFixture) count() int {
	var n int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM assets`).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func hasProblem(r ImportRowResult, target, code string) bool {
	for _, p := range r.Problems {
		if p.Target == target && p.Code == code {
			return true
		}
	}
	return false
}

func TestImportCreateAndValueParsing(t *testing.T) {
	f := newImportFixture(t)
	res := f.run("Kategorie;Bezeichnung;Standort;Status;Kaufdatum;Länge;Schalter;Anschluss\n"+
		"Steckerleiste;Leiste 1;Lager A;aktiv;05.03.2024;\"2,5\";ja;\n"+
		"sl;Leiste 2;lager a;DEFEKT;2024-03-06;3.5;nein;\n"+
		"Kabeltrommel;Trommel;;;;25;;cee 16 a blau\n", true)
	if res.Create != 3 || res.Errors != 0 || !res.Committed {
		t.Fatalf("%+v", res)
	}
	a, err := f.db.AssetByNumber(f.ctx, "SL-00001")
	if err != nil || a.Name != "Leiste 1" || a.LocationName != "Lager A" || a.Status != StatusActive || a.PurchaseDate != "2024-03-05" {
		t.Fatalf("%+v %v", a, err)
	}
	sl := catByPrefix(t, f.db, "SL")
	if a.Values[fieldByKey(t, f.db, sl.ID, "length_m").ID] != "2.5" || a.Values[fieldByKey(t, f.db, sl.ID, "switch").ID] != "1" {
		t.Fatalf("values %v", a.Values)
	}
	b, _ := f.db.AssetByNumber(f.ctx, "SL-00002")
	if b.Status != StatusDefective || b.LocationID != a.LocationID || b.Values[fieldByKey(t, f.db, sl.ID, "switch").ID] != "0" {
		t.Fatalf("%+v", b)
	}
	kt := catByPrefix(t, f.db, "KT")
	c, _ := f.db.AssetByNumber(f.ctx, "KT-00001")
	if c.LocationID != 0 || c.Values[fieldByKey(t, f.db, kt.ID, "connector").ID] != "CEE 16 A blau" {
		t.Fatalf("%+v", c)
	}
	locs, _ := f.db.ListLocations(f.ctx)
	if len(locs) != 1 {
		t.Fatalf("locations %d", len(locs))
	}
}

func TestImportRowErrors(t *testing.T) {
	f := newImportFixture(t)
	res := f.run("Kategorie;Name;Status;Kaufdatum;Länge;Schalter\n"+
		"Steckerleiste;Ok;aktiv;;1;\n"+
		"Unbekannt;Cat;;;1;\n"+
		"Steckerleiste;;;;1;\n"+
		"Steckerleiste;Status;kaputt;;1;\n"+
		"Steckerleiste;Datum;;31.02.2024;1;\n"+
		"Steckerleiste;Zahl;;;abc;\n"+
		"Steckerleiste;Bool;;;1;vielleicht\n"+
		"Steckerleiste;Pflicht;;;;\n"+
		"Netzwerkkabel;Feld;;;1;ja\n", false)
	if res.Committed || res.Create != 1 || res.Errors != 8 {
		t.Fatalf("create %d errors %d", res.Create, res.Errors)
	}
	checks := []struct {
		row          int
		target, code string
	}{{1, "category", "invalid_category"}, {2, "name", "required"}, {3, "status", "invalid_status"}, {4, "purchase_date", "invalid_date"},
		{5, "field:length_m", "invalid_number"}, {6, "field:switch", "invalid_bool"}, {7, "field:length_m", "required"},
		{8, "field:switch", "field_not_in_category"}}
	for _, c := range checks {
		if !hasProblem(res.Rows[c.row], c.target, c.code) || res.Rows[c.row].Action != ImportError {
			t.Errorf("row %d: %+v", c.row, res.Rows[c.row])
		}
	}
	if p := res.Rows[1].Problems[0]; p.Column != 0 || p.Value != "Unbekannt" {
		t.Errorf("problem cell %+v", p)
	}
	if f.count() != 0 {
		t.Fatal("preview wrote assets")
	}
}

func TestImportPreviewMatchesCommit(t *testing.T) {
	f := newImportFixture(t)
	text := "Kategorie;Name;Länge\nSteckerleiste;A;1\nSteckerleiste;B;2\n"
	prev := f.run(text, false)
	if f.count() != 0 {
		t.Fatal("preview wrote")
	}
	done := f.run(text, true)
	if prev.Rows[0].InventoryNo != "SL-00001" || done.Rows[1].InventoryNo != "SL-00002" || prev.Rows[1].InventoryNo != "SL-00002" || f.count() != 2 {
		t.Fatalf("%+v %+v", prev.Rows, done.Rows)
	}
}

func TestImportAutoNumbersAndDuplicates(t *testing.T) {
	f := newImportFixture(t)
	res := f.run("Inventarnummer;Kategorie;Name;Länge\n"+
		"X-1;Steckerleiste;Manual;1\n"+
		";Steckerleiste;Auto 1;1\n"+
		"x-1;Steckerleiste;Dup;1\n"+
		";SL;Auto 2;1\n", true)
	// Row 3 repeats X-1 (case-insensitive) and is an error, so nothing is written (all or nothing).
	if res.Committed || res.Errors != 1 || !hasProblem(res.Rows[2], "inventory_no", "dup_in_file") {
		t.Fatalf("%+v", res)
	}
	if res.Rows[1].InventoryNo != "SL-00001" || res.Rows[3].InventoryNo != "SL-00002" || res.Rows[0].InventoryNo != "X-1" {
		t.Fatalf("numbers %q %q %q", res.Rows[0].InventoryNo, res.Rows[1].InventoryNo, res.Rows[3].InventoryNo)
	}
	if f.count() != 0 {
		t.Fatal("all-or-nothing wrote rows")
	}
	// The sequence was rolled back with everything else.
	f.opts.SkipInvalid = true
	res = f.run("Inventarnummer;Kategorie;Name;Länge\nX-1;SL;Manual;1\n;SL;Auto;1\nX-1;SL;Dup;1\n", true)
	if !res.Committed || res.Create != 2 || res.Errors != 1 || f.count() != 2 {
		t.Fatalf("%+v count %d", res, f.count())
	}
	if _, err := f.db.AssetByNumber(f.ctx, "SL-00001"); err != nil {
		t.Fatalf("auto number: %v", err)
	}
	// A manual number that looks like a generated one is skipped by the generator.
	res = f.run("Inventarnummer;Kategorie;Name;Länge\nSL-00002;SL;M;1\n;SL;A;1\n", true)
	if res.Rows[1].InventoryNo != "SL-00003" {
		t.Fatalf("%+v", res.Rows)
	}
}

func TestImportExistingSkipOrUpdate(t *testing.T) {
	f := newImportFixture(t)
	f.opts.SkipInvalid = true
	f.run("Inventarnummer;Kategorie;Name;Seriennummer;Länge;Schalter\nA-1;SL;Alt;S1;1;ja\nB-1;KT;Trommel;;10;\n", true)
	text := "Inventarnummer;Kategorie;Name;Länge\nA-1;Steckerleiste;Neu;5\nB-1;Steckerleiste;Falsch;1\nA-1;;Dup;1\nC-1;;Ohne;1\n"
	// Default: skip existing numbers. The row without a category fails without default category.
	res := f.run(text, true)
	if res.Skip != 2 || res.Rows[0].Note != "exists" || res.Errors != 2 || res.Create != 0 {
		t.Fatalf("%+v", res)
	}
	a, _ := f.db.AssetByNumber(f.ctx, "A-1")
	if a.Name != "Alt" {
		t.Fatal("skip changed the asset")
	}
	// Update: core and field values change; unmapped columns (serial number, switch) stay.
	f.opts.UpdateExisting = true
	f.opts.DefaultCategoryID = catByPrefix(t, f.db, "SL").ID
	res = f.run(text, true)
	if res.Update != 1 || !hasProblem(res.Rows[1], "category", "category_mismatch") || !hasProblem(res.Rows[2], "inventory_no", "dup_in_file") ||
		res.Create != 1 || res.Errors != 2 {
		t.Fatalf("%+v", res)
	}
	a, _ = f.db.AssetByNumber(f.ctx, "A-1")
	sl := catByPrefix(t, f.db, "SL")
	if a.Name != "Neu" || a.SerialNo != "S1" || a.Values[fieldByKey(t, f.db, sl.ID, "length_m").ID] != "5" ||
		a.Values[fieldByKey(t, f.db, sl.ID, "switch").ID] != "1" {
		t.Fatalf("%+v", a)
	}
	if res.Rows[0].Changes["name"][1] != "Neu" || res.Rows[0].AssetID != a.ID {
		t.Fatalf("changes %+v", res.Rows[0])
	}
	b, _ := f.db.AssetByNumber(f.ctx, "B-1")
	if b.Name != "Trommel" {
		t.Fatal("mismatching category row changed the asset")
	}
	// Same data again: nothing changes.
	res = f.run("Inventarnummer;Kategorie;Name;Länge\nA-1;Steckerleiste;Neu;5\n", true)
	if res.Update != 0 || res.Skip != 1 || res.Rows[0].Note != "unchanged" {
		t.Fatalf("%+v", res)
	}
	// An empty mapped cell clears an optional value on update.
	res = f.run("Inventarnummer;Seriennummer\nA-1;\n", true)
	a, _ = f.db.AssetByNumber(f.ctx, "A-1")
	if res.Update != 1 || a.SerialNo != "" || a.Name != "Neu" {
		t.Fatalf("%+v %+v", res, a)
	}
}

func TestImportAllOrNothingVersusSkipInvalid(t *testing.T) {
	f := newImportFixture(t)
	text := "Kategorie;Name;Länge;Standort\nSL;Gut 1;1;Neu\nSL;;1;\nSL;Gut 2;2;\n"
	res := f.run(text, true)
	if res.Committed || res.Create != 2 || res.Errors != 1 || f.count() != 0 {
		t.Fatalf("%+v", res)
	}
	if locs, _ := f.db.ListLocations(f.ctx); len(locs) != 0 {
		t.Fatal("location created although rolled back")
	}
	f.opts.SkipInvalid = true
	res = f.run(text, true)
	if !res.Committed || res.Create != 2 || res.Errors != 1 || f.count() != 2 {
		t.Fatalf("%+v count %d", res, f.count())
	}
}

func TestImportLocations(t *testing.T) {
	f := newImportFixture(t)
	f.opts.SkipInvalid = true
	l := &Location{Name: "Lager A"}
	if err := f.db.CreateLocation(f.ctx, l); err != nil {
		t.Fatal(err)
	}
	f.opts.CreateLocations = false
	f.opts.DefaultLocationID = l.ID
	text := "Kategorie;Name;Länge;Standort\nSL;A;1;lager a\nSL;B;1;Unbekannt\nSL;C;1;\n"
	res := f.run(text, true)
	if res.Create != 2 || res.Errors != 1 || !hasProblem(res.Rows[1], "location", "invalid_location") {
		t.Fatalf("%+v", res)
	}
	if locs, _ := f.db.ListLocations(f.ctx); len(locs) != 1 {
		t.Fatalf("locations created: %d", len(locs))
	}
	c, _ := f.db.AssetByNumber(f.ctx, res.Rows[2].InventoryNo)
	if c.LocationID != l.ID {
		t.Fatalf("default location not applied: %+v", c)
	}
	f.opts.CreateLocations = true
	res = f.run(text, true)
	if res.Create != 3 || res.Errors != 0 {
		t.Fatalf("%+v", res)
	}
	locs, _ := f.db.ListLocations(f.ctx)
	if len(locs) != 2 {
		t.Fatalf("locations: %d", len(locs))
	}
}

func TestImportMappingErrors(t *testing.T) {
	f := newImportFixture(t)
	tb, _ := ParseCSV([]byte("a;b\n1;2\n"), "auto", "auto")
	for _, m := range [][]string{{"name", "name"}, {"nonsense", ""}, {"name"}, {"field:nope", ""}} {
		f.opts.Mapping = m
		f.opts.DefaultCategoryID = 1
		var fe *ImportFileError
		if _, err := f.db.RunImport(f.ctx, tb, f.opts, false); !errors.As(err, &fe) || fe.Code != "bad_mapping" {
			t.Errorf("mapping %v: %v", m, err)
		}
	}
	f.opts.Mapping = []string{"name", ""}
	f.opts.DefaultCategoryID = 0
	var fe *ImportFileError
	if _, err := f.db.RunImport(f.ctx, tb, f.opts, false); !errors.As(err, &fe) || fe.Code != "no_category" {
		t.Errorf("no category: %v", err)
	}
}

func TestImportGuardedCellsAndDefaultCategory(t *testing.T) {
	f := newImportFixture(t)
	f.opts.DefaultCategoryID = catByPrefix(t, f.db, "SL").ID
	res := f.run("Name;Länge;Notiz\n'=SUM(A1);'-5;'@x\n", true)
	// -5 is a valid number for the store; the apostrophes of the export guard are removed.
	if res.Create != 1 {
		t.Fatalf("%+v", res)
	}
	a, _ := f.db.AssetByNumber(f.ctx, res.Rows[0].InventoryNo)
	if a.Name != "=SUM(A1)" || a.Note != "@x" {
		t.Fatalf("%+v", a)
	}
}

const inspectionCSV = "Inventarnummer;Kategorie;Bezeichnung;Länge;Prüfdatum;Prüfer;Ergebnis;Prüfanlass;Schutzklasse;Sichtprüfung;Funktionsprüfung;" +
	"RPE;Riso;IPE;Messverfahren;IB;Prüfgerät;Prüfmittel-ID;Kalibriert bis;Bemerkung\n"

func TestImportSuggestInspectionHeaders(t *testing.T) {
	header := []string{"Prüfdatum", "Prüfer", "Ergebnis", "Prüfanlass", "Schutzklasse", "Sichtprüfung", "Funktionsprüfung", "RPE", "Riso",
		"ISL", "IB", "Prüfgerät", "Prüfmittel-ID", "Kalibriert bis", "Bemerkung", "Messverfahren", "inspected_on"}
	got := SuggestMapping(header, nil)
	want := []string{"inspected_on", "inspector_name", "result", "reason", "protection_class", "visual_ok", "function_ok", "pe_resistance_ohm",
		"insulation_mohm", "leakage_ma", "touch_ma", "test_device", "test_device_id", "test_device_calibrated_until", "remark", "leakage_method", ""}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%q", got)
	}
	// ISL is the protective conductor current (I_SL), not the insulation
	// resistance (Riso); "Verfahren" is its measuring method.
	if got := SuggestMapping([]string{"Riso", "ISL", "Verfahren"}, nil); strings.Join(got, ",") != "insulation_mohm,leakage_ma,leakage_method" {
		t.Fatalf("%q", got)
	}
	// Without inspection columns, "Bemerkung" stays the asset's note.
	if got := SuggestMapping([]string{"Name", "Bemerkung"}, nil); got[1] != "note" {
		t.Fatalf("%q", got)
	}
}

func TestImportInspectionValueParsers(t *testing.T) {
	for in, want := range map[string]string{"bestanden": "pass", "i.O.": "pass", "I.O.": "pass", "ja": "pass", "pass": "pass",
		"nicht bestanden": "fail", "nicht i.O.": "fail", "n.i.O.": "fail", "nein": "fail", "FAIL": "fail"} {
		if got, ok := parseImportResult(in); !ok || got != want {
			t.Errorf("result %q = %q %v", in, got, ok)
		}
	}
	if _, ok := parseImportResult("vielleicht"); ok {
		t.Error("result accepted")
	}
	for in, want := range map[string]string{"SK I": "I", "SK II": "II", "Schutzklasse 3": "III", "I": "I", "2": "II", "SKIII": "III", "": ""} {
		if got, ok := parseImportClass(in); !ok || got != want {
			t.Errorf("class %q = %q %v", in, got, ok)
		}
	}
	if _, ok := parseImportClass("SK IV"); ok {
		t.Error("class accepted")
	}
	for in, want := range map[string]string{"Dir": MethodDirect, "Diff": MethodDifferential, "AM": MethodAlternative, "Ersatzableitstrom": MethodAlternative,
		"direkt": MethodDirect, "Differenzstrom": MethodDifferential} {
		if got, ok := parseImportMethod(in); !ok || got != want {
			t.Errorf("method %q = %q %v", in, got, ok)
		}
	}
	for in, want := range map[string]string{"Prüfung vor Erstinbetriebnahme": ReasonInitial, "Wiederholungsprüfung": ReasonRepeat,
		"nach Instandsetzung": ReasonRepair, "repeat": ReasonRepeat, "": ""} {
		if got, ok := parseImportReason(in); !ok || got != want {
			t.Errorf("reason %q = %q %v", in, got, ok)
		}
	}
}

func TestImportInspections(t *testing.T) {
	f := newImportFixture(t)
	f.opts.AllowInspections = true
	f.opts.RecordedBy = 0
	csvText := inspectionCSV +
		"SL-1;Steckerleiste;Leiste 1;2;15.03.2025;Eli Ectric;bestanden;Wiederholungsprüfung;SK I;ja;ja;0,12;>299;<0,01;Diff;0,2;Tester 9;PM-7;31.12.2026;ok\n" +
		"SL-2;Steckerleiste;Leiste 2;2;2025-03-16;Eli Ectric;nicht i.O.;;SK II;ja;nein;-;;;;;;;;defekt\n" +
		"NK-1;Netzwerkkabel;Kabel;;2025-03-16;Eli Ectric;bestanden;;;;;;;;;;;;;\n" +
		"SL-3;Steckerleiste;Leiste 3;2;;;;;;;;;;;;;;;;\n"
	res := f.run(csvText, false)
	if res.Errors != 1 || res.Inspections != 2 || res.Create != 3 || !hasProblem(res.Rows[2], "inspected_on", "not_inspectable") {
		t.Fatalf("preview: %+v", res)
	}
	if f.count() != 0 {
		t.Fatal("preview wrote")
	}
	f.opts.SkipInvalid = true
	res = f.run(csvText, true)
	if !res.Committed || res.Inspections != 2 || res.Create != 3 {
		t.Fatalf("commit: %+v", res)
	}
	a, err := f.db.AssetByNumber(f.ctx, "SL-1")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := f.db.InspectionsByAsset(f.ctx, a.ID)
	if len(list) != 1 {
		t.Fatalf("inspections %d", len(list))
	}
	in := list[0]
	if in.InspectedOn != "2025-03-15" || in.NextDue != "2026-03-15" || in.Result != ResultPass || in.Reason != ReasonRepeat ||
		in.ProtectionClass != "I" || !in.VisualOK || !in.FunctionOK || *in.PEResistanceOhm != 0.12 || *in.InsulationMOhm != 299 ||
		in.InsulationCmp != ">" || *in.LeakageMA != 0.01 || in.LeakageCmp != "<" || in.LeakageMethod != MethodDifferential ||
		*in.TouchMA != 0.2 || in.TestDevice != "Tester 9" || in.TestDeviceID != "PM-7" || in.TestDeviceCalibratedUntil != "2026-12-31" ||
		in.Remark != "ok" || in.InspectorName != "Eli Ectric" {
		t.Fatalf("inspection %+v", in)
	}
	// A failed inspection does not change the status; the status column does.
	b, _ := f.db.AssetByNumber(f.ctx, "SL-2")
	bl, _ := f.db.InspectionsByAsset(f.ctx, b.ID)
	if len(bl) != 1 || bl[0].Result != ResultFail || bl[0].PEResistanceOhm != nil || b.Status != StatusActive {
		t.Fatalf("row 2: %+v %+v", b, bl)
	}
	if _, err := f.db.AssetByNumber(f.ctx, "NK-1"); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid row imported")
	}

	// Re-import is idempotent: the identical inspection is not duplicated.
	f.opts.UpdateExisting = true
	res = f.run(csvText, true)
	if res.Inspections != 0 || res.InspectionsExisting != 2 {
		t.Fatalf("re-import: %+v", res)
	}
	list, _ = f.db.InspectionsByAsset(f.ctx, a.ID)
	if len(list) != 1 {
		t.Fatalf("duplicated: %d", len(list))
	}
	// A different date is a new inspection of the existing asset.
	res = f.run(inspectionCSV+"SL-1;Steckerleiste;Leiste 1;2;2025-09-01;Eli;bestanden;;;;;;;;;;;;;\n", true)
	if res.Inspections != 1 {
		t.Fatalf("second inspection: %+v", res)
	}
	list, _ = f.db.InspectionsByAsset(f.ctx, a.ID)
	if len(list) != 2 {
		t.Fatalf("inspections %d", len(list))
	}
}

func TestImportInspectionProblemsAndRights(t *testing.T) {
	f := newImportFixture(t)
	csvText := inspectionCSV + "SL-1;Steckerleiste;Leiste;2;2025-03-15;Eli;bestanden;;;;;;;;;;;;;\n"
	// Without the inspection right the columns are refused.
	tb, err := ParseCSV([]byte(csvText), "auto", "auto")
	if err != nil {
		t.Fatal(err)
	}
	f.opts.Mapping = SuggestMapping(tb.Header, nil)
	var fe *ImportFileError
	if _, err := f.db.RunImport(f.ctx, tb, f.opts, false); !errors.As(err, &fe) || fe.Code != "inspection_forbidden" {
		t.Fatalf("error %v", err)
	}
	f.opts.AllowInspections = true
	res := f.run(inspectionCSV+
		"SL-1;Steckerleiste;A;2;2025-03-15;;bestanden;;;;;;;;;;;;;\n"+
		"SL-2;Steckerleiste;B;2;2025-03-15;E;vielleicht;;SK 9;;;;;;;;;;;\n"+
		"SL-3;Steckerleiste;C;2;2025-03-15;E;bestanden;;;;;abc;>20000;;;;;;31.02.2026;\n"+
		"SL-4;Steckerleiste;D;2;;E;bestanden;;;;;;;;;;;;;\n"+
		"SL-5;Steckerleiste;E;2;2199-01-01;E;bestanden;;;;;;;;;;;;;\n", false)
	r := res.Rows
	if !hasProblem(r[0], "inspector_name", "required") || !hasProblem(r[1], "result", "invalid_result") ||
		!hasProblem(r[1], "protection_class", "invalid_class") || !hasProblem(r[2], "pe_resistance_ohm", "invalid_number") ||
		!hasProblem(r[2], "insulation_mohm", "out_of_range") || !hasProblem(r[2], "test_device_calibrated_until", "invalid_date") ||
		!hasProblem(r[3], "inspected_on", "required") || !hasProblem(r[4], "inspected_on", "future_date") || res.Errors != 5 {
		t.Fatalf("%+v", res.Rows)
	}
}
