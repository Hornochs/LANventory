package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lan-dot-party/lanventory/internal/store"
)

var reToken = regexp.MustCompile(`name="token" value="([0-9a-f]+)"`)

// upload posts a multipart form with a file to path.
func (h *harness) upload(path string, fields map[string]string, content []byte) (*http.Response, string) {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("_csrf", h.csrf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if content != nil {
		fw, _ := mw.CreateFormFile("file", "assets.csv")
		_, _ = fw.Write(content)
	}
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", h.srv.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

func (h *harness) editor(name string) {
	h.t.Helper()
	h.user(name, store.RoleEditor)
	h.login(name)
}

// importMapped uploads content, maps the columns as SuggestMapping does and
// returns the token and the mapping form.
func (h *harness) importUploaded(content []byte) (string, url.Values) {
	h.t.Helper()
	resp, body := h.upload("/assets/import", map[string]string{"delimiter": "auto", "encoding": "auto"}, content)
	if resp.StatusCode != 200 {
		h.t.Fatalf("upload: %d %s", resp.StatusCode, body)
	}
	m := reToken.FindStringSubmatch(body)
	if m == nil {
		h.t.Fatalf("no token in %s", body)
	}
	tbl, err := store.ParseCSV(content, "auto", "auto")
	if err != nil {
		h.t.Fatal(err)
	}
	targets, _ := h.db.ImportFieldTargets(context.Background())
	form := url.Values{"token": {m[1]}, "create_locations": {"1"}, "existing": {"skip"}, "on_error": {"all"}}
	for i, t := range store.SuggestMapping(tbl.Header, targets) {
		form.Set(fmt.Sprintf("map_%d", i), t)
	}
	return m[1], form
}

func TestImportRoleChecks(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	h.login("viewer")
	for _, r := range []struct{ method, path string }{{"GET", "/assets/import"}, {"POST", "/assets/import"}, {"POST", "/assets/import/preview"},
		{"POST", "/assets/import/map"}, {"POST", "/assets/import/confirm"}} {
		var resp *http.Response
		if r.method == "GET" {
			resp, _ = h.get(r.path)
		} else if r.path == "/assets/import" {
			resp, _ = h.upload(r.path, nil, []byte("name\nx\n"))
		} else {
			resp = h.postOK(r.path, url.Values{"token": {"x"}})
		}
		status(t, resp.StatusCode, http.StatusForbidden, "viewer "+r.method+" "+r.path)
	}
	// Viewers may export and do not see the import link.
	resp, body := h.get("/assets")
	status(t, resp.StatusCode, 200, "viewer /assets")
	if strings.Contains(body, `href="/assets/import"`) || !strings.Contains(body, `href="/assets.csv"`) {
		t.Error("links for viewer wrong")
	}
	resp, _ = h.get("/assets.csv")
	status(t, resp.StatusCode, 200, "viewer export")

	e := h.other()
	e.user("ed", store.RoleEditor)
	e.login("ed")
	resp, body = e.get("/assets/import")
	status(t, resp.StatusCode, 200, "editor import page")
	if !strings.Contains(body, `enctype="multipart/form-data"`) {
		t.Error("upload form")
	}
	_, body = e.get("/assets")
	if !strings.Contains(body, `href="/assets/import"`) {
		t.Error("editor lacks import link")
	}
	// Anonymous users go to the login page.
	anon := h.other()
	resp, _ = anon.get("/assets/import")
	status(t, resp.StatusCode, http.StatusSeeOther, "anonymous")
	resp, _ = anon.get("/assets.csv")
	status(t, resp.StatusCode, http.StatusSeeOther, "anonymous export")
}

func TestImportUploadLimitsAndCSRF(t *testing.T) {
	h := newHarness(t)
	h.editor("ed")
	big := bytes.Repeat([]byte("a;b\n"), importMaxFile/4+10) // just over 5 MB
	resp, body := h.upload("/assets/import", nil, big)
	status(t, resp.StatusCode, http.StatusRequestEntityTooLarge, "5 MB limit")
	if !strings.Contains(body, "too large") && !strings.Contains(body, "zu groß") {
		t.Errorf("message missing: %.200s", body)
	}
	// A file just under the limit is accepted.
	ok := append([]byte("Name;Notiz\nx;"), bytes.Repeat([]byte("n"), importMaxFile-1000)...)
	resp, _ = h.upload("/assets/import", nil, ok)
	status(t, resp.StatusCode, 200, "file under the limit")
	// No file, empty file, invalid encoding.
	resp, _ = h.upload("/assets/import", nil, nil)
	status(t, resp.StatusCode, http.StatusUnprocessableEntity, "no file")
	resp, _ = h.upload("/assets/import", nil, []byte(" \n"))
	status(t, resp.StatusCode, http.StatusUnprocessableEntity, "empty file")
	resp, _ = h.upload("/assets/import", map[string]string{"encoding": "utf-8"}, []byte("a\n\xe4\n"))
	status(t, resp.StatusCode, http.StatusUnprocessableEntity, "invalid utf-8")
	// CSRF is checked on the multipart upload too.
	saved := h.csrf
	h.csrf = "wrong"
	resp, _ = h.upload("/assets/import", nil, []byte("name\nx\n"))
	status(t, resp.StatusCode, http.StatusForbidden, "bad csrf")
	h.csrf = saved
}

func TestImportFlow(t *testing.T) {
	h := newHarness(t)
	h.editor("ed")
	// Excel on German Windows: Windows-1252, semicolons.
	content := []byte("Inventarnummer;Kategorie;Bezeichnung;Standort;Status;L\xe4nge;Schalter\n" +
		";Steckerleiste;Leiste \xe4\xf6\xfc\xdf;Lager \x80;aktiv;\"2,5\";ja\n" +
		";Steckerleiste;<script>alert(1)</script>;;defekt;1;nein\n" +
		";Unbekannt;Fehler;;;1;\n")
	token, form := h.importUploaded(content)
	// Mapping page (re-rendered from state) shows the detected values and German header labels.
	resp, body := h.postBody("/assets/import/map", url.Values{"token": {token}})
	status(t, resp.StatusCode, 200, "map back")
	for _, want := range []string{"windows-1252", "Länge", `name="map_5"`} {
		if !strings.Contains(body, want) {
			t.Errorf("mapping page lacks %q", want)
		}
	}
	if !regexp.MustCompile(`name="map_2"[^>]*>(?s:.*?)value="name" selected`).MatchString(body) {
		t.Error("name column not preselected")
	}

	// All or nothing: the invalid row blocks the import.
	resp, body = h.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, 200, "preview")
	if strings.Contains(body, "<script>alert(1)") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("cell not escaped in preview")
	}
	if !strings.Contains(body, "Unknown category") || !strings.Contains(body, "Leiste äöüß") {
		t.Errorf("preview lacks the German messages or decoded cells: %.600s", body)
	}
	if strings.Contains(body, `action="/assets/import/confirm"`) {
		t.Error("confirm offered although rows are invalid")
	}
	resp, _ = h.postBody("/assets/import/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, http.StatusUnprocessableEntity, "confirm with errors")
	if n, _ := h.db.ListAssets(context.Background(), store.AssetFilter{}); n.Total != 0 {
		t.Fatal("assets written")
	}

	// Skip invalid rows.
	form.Set("on_error", "skip")
	resp, body = h.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, 200, "preview skip")
	if !strings.Contains(body, `action="/assets/import/confirm"`) {
		t.Error("confirm not offered in skip mode")
	}
	resp, body = h.postBody("/assets/import/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, 200, "confirm")
	if !strings.Contains(body, "2 angelegt") && !strings.Contains(body, "2 created") {
		t.Errorf("result page: %.400s", body)
	}
	// Double submit: the token is consumed.
	resp, _ = h.postBody("/assets/import/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, http.StatusGone, "second confirm")

	ctx := context.Background()
	a, err := h.db.AssetByNumber(ctx, "SL-00001")
	if err != nil || a.Name != "Leiste äöüß" || a.LocationName != "Lager €" || a.Status != "active" {
		t.Fatalf("asset: %+v %v", a, err)
	}
	b, _ := h.db.AssetByNumber(ctx, "SL-00002")
	if b.Name != "<script>alert(1)</script>" || b.Status != "defective" {
		t.Fatalf("asset b: %+v", b)
	}
	// The XSS name is escaped on the list too.
	_, body = h.get("/assets")
	if strings.Contains(body, "<script>alert(1)") {
		t.Error("unescaped name on the list")
	}
	// Audit: one import entry plus one per asset with via=import; the asset history shows it.
	audits, _ := h.db.ListAudit(ctx, 50)
	var imp, creates int
	for _, e := range audits {
		switch e.Action {
		case "asset.import":
			imp++
			if e.Details["created"] != float64(2) || e.Details["invalid"] != float64(1) {
				t.Errorf("import details %v", e.Details)
			}
		case "asset.create":
			creates++
			if e.Details["via"] != "import" {
				t.Errorf("create details %v", e.Details)
			}
		}
	}
	if imp != 1 || creates != 2 {
		t.Fatalf("audit: %d import, %d create", imp, creates)
	}
	_, body = h.get("/assets/" + idStr(a.ID))
	if !strings.Contains(body, "Leiste äöüß") {
		t.Error("history lacks the imported values")
	}
}

func TestImportTokenBoundToUserAndSession(t *testing.T) {
	h := newHarness(t)
	h.editor("ed")
	token, form := h.importUploaded([]byte("Kategorie;Name;Länge\nSL;A;1\n"))

	// Another editor cannot use the token.
	o := h.other()
	o.editor("ed2")
	for _, path := range []string{"/assets/import/preview", "/assets/import/map", "/assets/import/confirm"} {
		resp, _ := o.postBody(path, form)
		status(t, resp.StatusCode, http.StatusGone, "other user "+path)
	}
	// A new session of the same user cannot either.
	s2 := h.other()
	s2.login("ed")
	resp, _ := s2.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, http.StatusGone, "other session")
	// The owner still can (the failed attempts did not consume it).
	resp, _ = h.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, 200, "owner")
	// Confirm without a preview of the current options is refused after a new upload.
	h.importUploaded([]byte("Kategorie;Name;Länge\nSL;B;1\n"))
	resp, _ = h.postBody("/assets/import/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, http.StatusGone, "old token after new upload")
	// Unknown token.
	resp, _ = h.postBody("/assets/import/preview", url.Values{"token": {"00"}})
	status(t, resp.StatusCode, http.StatusGone, "unknown token")
	if n, _ := h.db.ListAssets(context.Background(), store.AssetFilter{}); n.Total != 0 {
		t.Fatal("assets written")
	}
}

func TestImportStatesExpiry(t *testing.T) {
	s := newImportStates()
	now := time.Now()
	s.now = func() time.Time { return now }
	tok, ok := s.Put(1, "sess", &store.ImportTable{})
	if !ok {
		t.Fatal("put")
	}
	if _, ok := s.Get(tok, 1, "sess"); !ok {
		t.Fatal("get")
	}
	if _, ok := s.Get(tok, 2, "sess"); ok {
		t.Fatal("other user")
	}
	if _, ok := s.Get(tok, 1, "other"); ok {
		t.Fatal("other session")
	}
	now = now.Add(importTTL - time.Minute)
	if _, ok := s.Get(tok, 1, "sess"); !ok { // extends
		t.Fatal("not yet expired")
	}
	now = now.Add(importTTL + time.Second)
	if _, ok := s.Get(tok, 1, "sess"); ok {
		t.Fatal("expired state returned")
	}
	// One state per user; the number of users is bounded.
	t1, _ := s.Put(1, "s", &store.ImportTable{})
	t2, _ := s.Put(1, "s", &store.ImportTable{})
	if _, ok := s.Get(t1, 1, "s"); ok || t1 == t2 {
		t.Fatal("old state of the user kept")
	}
	for i := int64(10); i < 10+importMaxStates; i++ {
		s.Put(i, "s", &store.ImportTable{})
	}
	if _, ok := s.Put(99, "s", &store.ImportTable{}); ok {
		t.Fatal("limit not enforced")
	}
}

// --- export ---

func parseCSVBody(t *testing.T, body string) [][]string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(body))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatalf("export is not valid CSV: %v\n%s", err, body)
	}
	return recs
}

func colIndex(t *testing.T, header []string, name string) int {
	t.Helper()
	for i, h := range header {
		if h == name {
			return i
		}
	}
	t.Fatalf("column %q missing in %v", name, header)
	return -1
}

func TestAssetExport(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	ctx := context.Background()
	loc := &store.Location{Name: "Lager, \"Nord\""}
	_ = h.db.CreateLocation(ctx, loc)
	a := h.newAsset("SL", "=HYPERLINK(\"x\")", func(a *store.Asset, fs map[string]store.Field) {
		a.Values[fs["length_m"].ID] = "2,50"
		a.Values[fs["switch"].ID] = "1"
		a.LocationID = loc.ID
		a.Note = "line1\nline2, \"quoted\""
		a.SerialNo = "+49 123"
		a.DGUVIntervalMonths = 6
		a.PurchaseDate = "2024-03-05"
		a.Manufacturer = "Müller & Söhne"
	})
	h.newAsset("SL", "Leiste 2", func(a *store.Asset, fs map[string]store.Field) { a.Status = store.StatusDefective })
	h.newAsset("KT", "Trommel", func(a *store.Asset, fs map[string]store.Field) {
		a.Values[fs["length_m"].ID] = "25"
		a.Values[fs["connector"].ID] = "Schuko"
	})
	h.newAsset("MO", "Monitor", func(a *store.Asset, fs map[string]store.Field) { a.Values[fs["size_in"].ID] = "-27" })
	h.newAsset("NK", "Kabel", nil)
	h.addInsp(a.ID, "2024-01-31", store.ResultPass, nil)
	h.login("viewer")

	resp, body := h.get("/assets.csv")
	status(t, resp.StatusCode, 200, "export")
	if ct := resp.Header.Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !regexp.MustCompile(`^attachment; filename="lanventory-assets-\d{4}-\d{2}-\d{2}\.csv"$`).MatchString(cd) {
		t.Errorf("content disposition %q", cd)
	}
	if strings.HasPrefix(body, "\xef\xbb\xbf") || !strings.Contains(body, "\r\n") || strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\r") {
		t.Error("expected UTF-8 without BOM and CRLF")
	}
	recs := parseCSVBody(t, body)
	hd := recs[0]
	wantFixed := "inventory_no,category,category_prefix,name,serial_no,manufacturer,model,location,status,purchase_date,dguv_interval_months,note,last_inspected_on,next_due"
	if got := strings.Join(hd[:14], ","); got != wantFixed {
		t.Fatalf("fixed columns: %s", got)
	}
	// Union of the fields, categories sorted by name (Kabeltrommel, Monitor, Netzwerkkabel, Steckerleiste), position order.
	if got := strings.Join(hd[14:], ","); got != "field:length_m,field:cross_section,field:connector,field:sockets,field:size_in,field:resolution,"+
		"field:cable_category,field:color,field:switch,field:surge_protection" {
		t.Fatalf("field columns: %s", got)
	}
	if len(recs) != 6 {
		t.Fatalf("%d records", len(recs))
	}
	var row []string
	for _, r := range recs[1:] {
		if r[colIndex(t, hd, "inventory_no")] == a.InventoryNo {
			row = r
		}
	}
	get := func(col string) string { return row[colIndex(t, hd, col)] }
	checks := map[string]string{
		"name":                 "'=HYPERLINK(\"x\")", // formula guard
		"serial_no":            "'+49 123",
		"category":             "Steckerleiste",
		"category_prefix":      "SL",
		"location":             "Lager, \"Nord\"",
		"status":               "active",
		"purchase_date":        "2024-03-05",
		"dguv_interval_months": "6",
		"note":                 "line1\nline2, \"quoted\"",
		"manufacturer":         "Müller & Söhne",
		"last_inspected_on":    "2024-01-31",
		"next_due":             "2024-07-31",
		"field:length_m":       "2.5",
		"field:switch":         "1",
		"field:connector":      "",
	}
	for k, want := range checks {
		if got := get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// A negative number gets the guard apostrophe too (the import removes it); NK is not a DGUV category.
	for _, r := range recs[1:] {
		switch r[colIndex(t, hd, "name")] {
		case "Monitor":
			if r[colIndex(t, hd, "field:size_in")] != "'-27" {
				t.Errorf("negative number: %q", r[colIndex(t, hd, "field:size_in")])
			}
		case "Kabel":
			if r[colIndex(t, hd, "next_due")] != "" || r[colIndex(t, hd, "dguv_interval_months")] != "" {
				t.Errorf("non-DGUV row: %v", r)
			}
		}
	}

	// Filters and sort of the list apply; a single category exports only its fields.
	cat, _ := h.cat("KT")
	_, body = h.get("/assets.csv?category=" + idStr(cat.ID) + "&sort=name&dir=desc")
	recs = parseCSVBody(t, body)
	if len(recs) != 2 || recs[1][colIndex(t, recs[0], "name")] != "Trommel" || len(recs[0]) != 14+4 {
		t.Fatalf("category filter: %v", recs)
	}
	_, body = h.get("/assets.csv?status=defective")
	if recs = parseCSVBody(t, body); len(recs) != 2 || recs[1][colIndex(t, recs[0], "name")] != "Leiste 2" {
		t.Fatalf("status filter: %v", recs)
	}
	_, body = h.get("/assets.csv?q=trommel")
	if recs = parseCSVBody(t, body); len(recs) != 2 {
		t.Fatalf("search filter: %v", recs)
	}
	_, body = h.get("/assets.csv?location=" + idStr(loc.ID))
	if recs = parseCSVBody(t, body); len(recs) != 2 {
		t.Fatalf("location filter: %v", recs)
	}
	sl, fs := h.cat("SL")
	_, body = h.get("/assets.csv?category=" + idStr(sl.ID) + "&f_switch=1")
	_ = fs
	if recs = parseCSVBody(t, body); len(recs) != 2 {
		t.Fatalf("field filter: %v", recs)
	}
	_, body = h.get("/assets.csv?sort=name&dir=desc")
	recs = parseCSVBody(t, body)
	if recs[1][colIndex(t, recs[0], "name")] != "Trommel" {
		t.Fatalf("sort: %v", recs[1])
	}
	// The assets page links the export with the current filters.
	_, body = h.get("/assets?status=defective&sort=name&dir=desc")
	if !strings.Contains(body, `href="/assets.csv?dir=desc&amp;sort=name&amp;status=defective"`) {
		t.Errorf("csv link missing: %s", regexp.MustCompile(`/assets\.csv[^"]*`).FindString(body))
	}
	// Empty result: header only.
	_, body = h.get("/assets.csv?q=doesnotexist")
	if recs = parseCSVBody(t, body); len(recs) != 1 {
		t.Fatalf("empty export: %v", recs)
	}
}

func TestInspectionExportUsesSharedGuard(t *testing.T) {
	if csvCell("=1") != "'=1" || csvCell("-1") != "'-1" || csvCell("@a") != "'@a" || csvCell("+1") != "'+1" ||
		csvCell("\tx") != "'\tx" || csvCell("\rx") != "'\rx" || csvCell("ok") != "ok" || csvCell("") != "" {
		t.Fatal("guard")
	}
}

// --- round trip ---

func TestExportImportRoundTrip(t *testing.T) {
	src := newHarness(t)
	ctx := context.Background()
	src.user("viewer", store.RoleViewer)
	loc1, loc2 := &store.Location{Name: "Lager A"}, &store.Location{Name: "Bühne; \"2\""}
	_ = src.db.CreateLocation(ctx, loc1)
	_ = src.db.CreateLocation(ctx, loc2)
	src.newAsset("SL", "Leiste =1", func(a *store.Asset, fs map[string]store.Field) {
		a.Values[fs["length_m"].ID] = "2.5"
		a.Values[fs["switch"].ID] = "1"
		a.Values[fs["surge_protection"].ID] = "0"
		a.Values[fs["sockets"].ID] = "6"
		a.LocationID = loc1.ID
		a.Note = "mehrzeilig\nNotiz, mit \"Zitat\""
		a.SerialNo = "-123"
		a.DGUVIntervalMonths = 24
		a.PurchaseDate = "2023-12-31"
		a.Manufacturer = "Müller & Söhne"
		a.Model = "ÄÖÜ ß"
	})
	src.newAsset("SL", "Leiste 2", func(a *store.Asset, fs map[string]store.Field) {
		a.Status = store.StatusRetired
		a.InventoryNo = "OLD-7"
	})
	src.newAsset("KT", "Trommel", func(a *store.Asset, fs map[string]store.Field) {
		a.Values[fs["length_m"].ID] = "25"
		a.Values[fs["cross_section"].ID] = "2,5 mm²"
		a.Values[fs["connector"].ID] = "CEE 16 A rot"
		a.LocationID = loc2.ID
		a.Status = store.StatusLost
	})
	src.newAsset("MO", "Monitor", func(a *store.Asset, fs map[string]store.Field) {
		a.Values[fs["size_in"].ID] = "-27.5"
		a.Values[fs["resolution"].ID] = "=1920x1080"
	})
	src.newAsset("NK", "Kabel 5 m", func(a *store.Asset, fs map[string]store.Field) {
		a.Values[fs["length_m"].ID] = "5"
		a.Values[fs["color"].ID] = "@rot"
	})
	src.login("viewer")
	resp, exported := src.get("/assets.csv")
	status(t, resp.StatusCode, 200, "export")

	// Fresh database with the same categories (seeded identically).
	dst := newHarness(t)
	dst.editor("ed")
	token, form := dst.importUploaded([]byte(exported))
	resp, body := dst.postBody("/assets/import/preview", form)
	status(t, resp.StatusCode, 200, "preview")
	if strings.Contains(body, "field-error") {
		t.Fatalf("preview has errors: %s", body)
	}
	resp, _ = dst.postBody("/assets/import/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, 200, "confirm")

	// Identical assets including custom fields: compare the exports byte for byte,
	// and the stored values of every asset.
	dst.user("viewer", store.RoleViewer)
	v := dst.other()
	v.login("viewer")
	_, again := v.get("/assets.csv")
	if again != exported {
		t.Fatalf("round trip differs\n--- exported\n%s\n--- re-exported\n%s", exported, again)
	}
	sa, _ := src.db.ExportAssets(ctx, store.AssetFilter{})
	da, _ := dst.db.ExportAssets(ctx, store.AssetFilter{})
	if len(sa.Rows) != 5 || len(da.Rows) != 5 {
		t.Fatalf("rows %d %d", len(sa.Rows), len(da.Rows))
	}
	for i := range sa.Rows {
		x, y := sa.Rows[i], da.Rows[i]
		if x.Asset.InventoryNo != y.Asset.InventoryNo || x.Asset.Name != y.Asset.Name || x.Asset.Note != y.Asset.Note ||
			x.Asset.Status != y.Asset.Status || x.Asset.LocationName != y.Asset.LocationName || x.Asset.SerialNo != y.Asset.SerialNo ||
			x.Asset.DGUVIntervalMonths != y.Asset.DGUVIntervalMonths || x.Asset.PurchaseDate != y.Asset.PurchaseDate {
			t.Errorf("asset %d differs:\n%+v\n%+v", i, x.Asset, y.Asset)
		}
		if fmt.Sprint(x.Values) != fmt.Sprint(y.Values) {
			t.Errorf("values of %s differ: %v vs %v", x.Asset.InventoryNo, x.Values, y.Values)
		}
	}
}

// Keys the import templates build with print, and the codes of import problems, are translated.
func TestImportKeysExist(t *testing.T) {
	var keys []string
	for _, tg := range append(append([]string{}, store.ImportCoreTargets...), store.ImportInspectionTargets...) {
		keys = append(keys, "imp.t."+tg)
	}
	for _, r := range store.InspectionReasons {
		keys = append(keys, "reason."+r)
	}
	for _, m := range store.LeakageMethods {
		keys = append(keys, "method."+m)
	}
	for _, d := range []string{store.DelimAuto, store.DelimComma, store.DelimSemicolon, store.DelimTab} {
		keys = append(keys, "imp.delim."+d)
	}
	for _, e := range []string{store.EncAuto, store.EncUTF8, store.EncCP1252} {
		keys = append(keys, "imp.enc."+e)
	}
	for _, a := range []string{store.ImportCreate, store.ImportUpdate, store.ImportSkip, store.ImportError} {
		keys = append(keys, "imp.a."+a)
	}
	for _, c := range []string{"empty", "no_header", "encoding", "too_many_rows", "too_many_columns", "parse", "bad_mapping", "no_category", "inspection_forbidden"} {
		keys = append(keys, "imperr."+c)
	}
	for _, c := range []string{"dup_in_file", "category_mismatch", "field_not_in_category", "invalid_category", "invalid_status", "invalid_location",
		"invalid_date", "invalid_bool", "invalid_number", "invalid_interval", "required", "too_long", "invalid", "invalid_option", "taken", "invalid_cmp", "invalid_method", "invalid_reason",
		"invalid_result", "invalid_class", "not_inspectable", "future_date", "out_of_range"} {
		keys = append(keys, "verr."+c)
	}
	keys = append(keys, "imp.note.exists", "imp.note.unchanged", "imp.note.inspection", "imp.note.inspection_exists")
	for _, k := range keys {
		if !hasKey(k) {
			t.Errorf("missing translation key %q", k)
		}
	}
}
