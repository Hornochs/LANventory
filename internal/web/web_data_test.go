package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lan-dot-party/lanventory/internal/store"
)

// seedData creates a small inventory with an inspection in the harness DB.
func (h *harness) seedData() {
	h.t.Helper()
	ctx := context.Background()
	must := func(err error) {
		h.t.Helper()
		if err != nil {
			h.t.Fatal(err)
		}
	}
	loc := &store.Location{Name: "Depot", Description: "Main"}
	must(h.db.CreateLocation(ctx, loc))
	cat := &store.Category{Name: "Lamps", Prefix: "LP", DGUVRequired: true, DGUVIntervalMonths: 12}
	must(h.db.CreateCategory(ctx, cat))
	f := &store.Field{CategoryID: cat.ID, Key: "watt", Label: "Watt", Type: store.FieldNumber, Unit: "W"}
	must(h.db.CreateField(ctx, f))
	a := &store.Asset{CategoryID: cat.ID, Name: "Lamp", LocationID: loc.ID, Values: map[int64]string{f.ID: "40"}}
	_, err := h.db.CreateAsset(ctx, a, "{prefix}-{seq:03}")
	must(err)
	_, _, err = h.db.CreateInspection(ctx, &store.Inspection{AssetID: a.ID, InspectedOn: "2026-11-14", InspectorName: "Bob",
		Result: store.ResultPass, VisualOK: true, FunctionOK: true, InsulationInput: "299"}, false, false)
	must(err)
}

func TestDataRoutesAreAdminOnly(t *testing.T) {
	h := newHarness(t)
	h.user("ed", store.RoleEditor)
	h.login("ed")
	for _, path := range []string{"/admin/data", "/admin/data/export.json"} {
		resp, _ := h.get(path)
		status(t, resp.StatusCode, http.StatusForbidden, "editor GET "+path)
	}
	resp, _ := h.upload("/admin/data/import", nil, []byte("{}"))
	status(t, resp.StatusCode, http.StatusForbidden, "editor upload")
	for _, p := range []string{"/admin/data/preview", "/admin/data/confirm"} {
		status(t, h.postOK(p, url.Values{"token": {"x"}}).StatusCode, http.StatusForbidden, "editor POST "+p)
	}
	anon := h.other()
	resp, _ = anon.get("/admin/data/export.json")
	status(t, resp.StatusCode, http.StatusSeeOther, "anonymous export")
}

func TestDataExportDownload(t *testing.T) {
	h := newHarness(t)
	h.user("root", store.RoleAdmin)
	h.login("root")
	h.seedData()
	resp, body := h.get("/admin/data")
	status(t, resp.StatusCode, 200, "page")
	if !strings.Contains(body, `href="/admin/data/export.json"`) || !strings.Contains(body, `enctype="multipart/form-data"`) ||
		!strings.Contains(body, `href="/admin/data"`) {
		t.Error("page lacks export link, upload form or nav entry")
	}
	resp, body = h.get("/admin/data/export.json")
	status(t, resp.StatusCode, 200, "export")
	wantName := `attachment; filename="lanventory-export-` + time.Now().UTC().Format("2006-01-02") + `.json"`
	if cd := resp.Header.Get("Content-Disposition"); cd != wantName {
		t.Errorf("Content-Disposition %q, want %q", cd, wantName)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %q %q", ct, resp.Header.Get("Cache-Control"))
	}
	var exp store.DataExport
	if err := json.Unmarshal([]byte(body), &exp); err != nil {
		t.Fatal(err)
	}
	if exp.Format != "lanventory-export" || exp.Version != 1 || len(exp.Assets) != 1 || len(exp.Assets[0].Inspections) != 1 ||
		exp.Assets[0].Fields["watt"] != "40" {
		t.Fatalf("export: %+v", exp)
	}
	if strings.Contains(body, "password") || strings.Contains(body, "root") {
		t.Error("export leaks user data")
	}
	audit, _ := h.db.ListAudit(context.Background(), 20)
	found := false
	for _, e := range audit {
		found = found || e.Action == "data.export"
	}
	if !found {
		t.Error("no data.export audit entry")
	}
	// Recent authentication is required.
	h.ageAuth(time.Hour)
	for _, p := range []string{"/admin/data", "/admin/data/export.json"} {
		resp, _ = h.get(p)
		if !strings.HasPrefix(resp.Header.Get("Location"), "/reauth?next=") {
			t.Errorf("%s without recent auth: %d", p, resp.StatusCode)
		}
	}
}

func TestDataImportFlow(t *testing.T) {
	src := newHarness(t)
	src.user("root", store.RoleAdmin)
	src.login("root")
	src.seedData()
	_, file := src.get("/admin/data/export.json")

	h := newHarness(t)
	h.user("root", store.RoleAdmin)
	h.login("root")
	// CSRF: the upload needs the token.
	h2 := h.other()
	h2.user("other", store.RoleAdmin)
	h2.login("other")
	goodCSRF := h2.csrf
	h2.csrf = "wrong"
	resp, _ := h2.upload("/admin/data/import", nil, []byte(file))
	status(t, resp.StatusCode, http.StatusForbidden, "bad CSRF")
	h2.csrf = goodCSRF

	// Not a LANventory file.
	resp, body := h.upload("/admin/data/import", nil, []byte("not json"))
	status(t, resp.StatusCode, http.StatusUnprocessableEntity, "garbage")
	if !strings.Contains(body, "alert-error") {
		t.Error("no error shown")
	}
	resp, _ = h.upload("/admin/data/import", nil, nil)
	status(t, resp.StatusCode, http.StatusUnprocessableEntity, "no file")
	// Wrong version: preview with an error and no import button enabled.
	resp, body = h.upload("/admin/data/import", nil, []byte(`{"format":"lanventory-export","version":9}`))
	status(t, resp.StatusCode, 200, "unsupported version preview")
	if !strings.Contains(body, "Unsupported export file version") && !strings.Contains(body, "Nicht unterstützte Version") {
		t.Error("version problem not shown")
	}
	if !strings.Contains(body, "disabled") {
		t.Error("confirm button not disabled")
	}

	resp, body = h.upload("/admin/data/import", nil, []byte(file))
	status(t, resp.StatusCode, 200, "upload")
	m := reToken.FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, `action="/admin/data/confirm"`) {
		t.Fatalf("preview page: %s", body)
	}
	if !strings.Contains(body, `name="update_categories" value="1" checked`) {
		t.Error("adopting category settings is not the default")
	}
	token := m[1]
	if before, _ := h.db.ExportData(context.Background()); len(before.Assets) != 0 {
		t.Fatal("upload wrote data")
	}
	// A token of the data upload is not valid for the CSV wizard.
	resp = h.postOK("/assets/import/preview", url.Values{"token": {token}})
	status(t, resp.StatusCode, http.StatusGone, "data token in CSV wizard")
	// Re-check with abort.
	resp = h.postOK("/admin/data/preview", url.Values{"token": {token}, "on_conflict": {"abort"}})
	status(t, resp.StatusCode, 200, "recheck")
	// Another user cannot use the token.
	resp = h2.postOK("/admin/data/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, http.StatusGone, "foreign token")
	if resp := h.post("/admin/data/confirm", url.Values{"token": {token}}, map[string]string{"Origin": h.srv.URL}); resp.StatusCode != http.StatusForbidden {
		t.Errorf("confirm without CSRF: %d", resp.StatusCode)
	}

	resp = h.postOK("/admin/data/confirm", url.Values{"token": {token}, "on_conflict": {"skip"}})
	status(t, resp.StatusCode, 200, "confirm")
	got, err := h.db.ExportData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Assets) != 1 || len(got.Assets[0].Inspections) != 1 || got.Assets[0].InventoryNo != "LP-001" || got.Locations[0].Name != "Depot" {
		t.Fatalf("imported: %+v", got)
	}
	if got.Assets[0].Inspections[0].RecordedBy != "" && got.Assets[0].Inspections[0].RecordedBy != "root" {
		t.Errorf("recorded_by %q", got.Assets[0].Inspections[0].RecordedBy)
	}
	var actions []string
	audit, _ := h.db.ListAudit(context.Background(), 50)
	for _, e := range audit {
		actions = append(actions, e.Action)
		if e.Action == "asset.create" && e.Details["via"] != "data_import" {
			t.Errorf("asset.create details %v", e.Details)
		}
	}
	if !strings.Contains(strings.Join(actions, " "), "data.import") || !strings.Contains(strings.Join(actions, " "), "asset.create") {
		t.Errorf("audit actions %v", actions)
	}
	// The upload is gone; importing the token again fails, a second upload skips the asset.
	resp = h.postOK("/admin/data/confirm", url.Values{"token": {token}})
	status(t, resp.StatusCode, http.StatusGone, "second confirm")
	_, body = h.upload("/admin/data/import", nil, []byte(file))
	if m = reToken.FindStringSubmatch(body); m == nil || !strings.Contains(body, "LP-001") {
		t.Fatalf("second preview should list the skipped number: %s", body)
	}
	// Abort mode: preview shows an error, confirm answers 422 and writes nothing.
	resp = h.postOK("/admin/data/confirm", url.Values{"token": {m[1]}, "on_conflict": {"abort"}})
	status(t, resp.StatusCode, http.StatusUnprocessableEntity, "abort confirm")
}

func TestCSVImportPageLinksToDataExport(t *testing.T) {
	h := newHarness(t)
	h.user("root", store.RoleAdmin)
	h.login("root")
	_, body := h.get("/assets/import")
	if !strings.Contains(body, `href="/admin/data"`) {
		t.Error("admin lacks the hint")
	}
	e := h.other()
	e.user("ed", store.RoleEditor)
	e.login("ed")
	_, body = e.get("/assets/import")
	if strings.Contains(body, `href="/admin/data"`) {
		t.Error("editor sees the data link")
	}
}
