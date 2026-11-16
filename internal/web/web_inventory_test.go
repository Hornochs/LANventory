package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/lan-dot-party/lanventory/internal/store"
)

func (h *harness) getWith(path string, hdr map[string]string) (*http.Response, string) {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

// postBody is like postOK but returns the response body.
func (h *harness) postBody(path string, form url.Values) (*http.Response, string) {
	h.t.Helper()
	form.Set("_csrf", h.csrf)
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.srv.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

func (h *harness) cat(prefix string) (*store.Category, map[string]store.Field) {
	h.t.Helper()
	cats, err := h.db.ListCategories(context.Background())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range cats {
		if c.Prefix == prefix {
			fs, _ := h.db.CategoryFields(context.Background(), c.ID)
			m := map[string]store.Field{}
			for _, f := range fs {
				m[f.Key] = f
			}
			return c, m
		}
	}
	h.t.Fatalf("category %s missing", prefix)
	return nil, nil
}

// newAsset creates an asset directly in the database.
func (h *harness) newAsset(prefix, name string, extra func(*store.Asset, map[string]store.Field)) *store.Asset {
	h.t.Helper()
	c, fs := h.cat(prefix)
	a := &store.Asset{CategoryID: c.ID, Name: name, Values: map[int64]string{}}
	if f, ok := fs["length_m"]; ok {
		a.Values[f.ID] = "2"
	}
	if extra != nil {
		extra(a, fs)
	}
	if _, err := h.db.CreateAsset(context.Background(), a, "{prefix}-{seq:05}"); err != nil {
		h.t.Fatal(err)
	}
	return a
}

func status(t *testing.T, got, want int, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s: status %d, want %d", what, got, want)
	}
}

func TestInventoryRoleChecks(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	h.user("editor", store.RoleEditor)
	h.user("admin", store.RoleAdmin)
	a := h.newAsset("KG", "Cable", nil)
	cat, fields := h.cat("KG")
	loc := &store.Location{Name: "Shelf"}
	_ = h.db.CreateLocation(context.Background(), loc)
	id := idStr(a.ID)
	cid := idStr(cat.ID)
	fid := idStr(fields["length_m"].ID)

	// Anonymous users are sent to the login page.
	resp, _ := h.get("/assets")
	status(t, resp.StatusCode, http.StatusSeeOther, "anonymous /assets")

	type req struct {
		method, path string
		form         url.Values
	}
	readRoutes := []req{{"GET", "/assets", nil}, {"GET", "/assets/" + id, nil}, {"GET", "/locations", nil},
		{"GET", "/categories", nil}, {"GET", "/categories/" + cid, nil}}
	editorRoutes := []req{{"GET", "/assets/new?category=" + cid, nil}, {"GET", "/assets/" + id + "/edit", nil},
		{"POST", "/assets", url.Values{"category": {cid}, "name": {"x"}, "f_length_m": {"1"}}},
		{"POST", "/assets/" + id, url.Values{"name": {"Cable"}, "f_length_m": {"2"}}},
		{"POST", "/locations", url.Values{"name": {"New"}}},
		{"POST", "/locations/" + idStr(loc.ID), url.Values{"name": {"Renamed"}}},
		{"POST", "/locations/" + idStr(loc.ID) + "/delete", nil}}
	adminRoutes := []req{{"POST", "/categories", url.Values{"name": {"Z"}, "prefix": {"ZZ"}}},
		{"POST", "/categories/" + cid, url.Values{"name": {"KG2"}, "prefix": {"KG"}}},
		{"POST", "/categories/" + cid + "/fields", url.Values{"key": {"k2"}, "label": {"K2"}, "type": {"text"}}},
		{"POST", "/categories/" + cid + "/fields/" + fid, url.Values{"label": {"Len"}, "type": {"number"}}},
		{"POST", "/categories/" + cid + "/fields/" + fid + "/move", url.Values{"dir": {"up"}}},
		{"POST", "/categories/" + cid + "/fields/" + fid + "/delete", nil},
		{"POST", "/categories/" + cid + "/delete", nil},
		{"POST", "/assets/" + id + "/delete", nil}}

	do := func(h *harness, r req) int {
		if r.method == "GET" {
			resp, _ := h.get(r.path)
			return resp.StatusCode
		}
		f := r.form
		if f == nil {
			f = url.Values{}
		}
		return h.postOK(r.path, f).StatusCode
	}
	// Forbidden requests must not change anything, so check them first for
	// each role; permitted writes follow in the allowed pass.
	for _, role := range []string{store.RoleViewer, store.RoleEditor} {
		c := h.other()
		c.login(role)
		for _, r := range readRoutes {
			status(t, do(c, r), 200, role+" "+r.method+" "+r.path)
		}
		forbidden := adminRoutes
		if role == store.RoleViewer {
			forbidden = append(append([]req{}, editorRoutes...), adminRoutes...)
		} else {
			for _, r := range editorRoutes[:2] {
				status(t, do(c, r), 200, role+" "+r.method+" "+r.path)
			}
		}
		for _, r := range forbidden {
			status(t, do(c, r), http.StatusForbidden, role+" "+r.method+" "+r.path)
		}
	}
	// Nothing was changed by the refused requests.
	if got, _ := h.db.AssetByID(context.Background(), a.ID); got == nil || got.Name != "Cable" {
		t.Fatal("asset changed by a forbidden request")
	}
	if _, fs := h.cat("KG"); len(fs) != 1 {
		t.Fatal("fields changed by a forbidden request")
	}

	// Editor writes succeed.
	ed := h.other()
	ed.login("editor")
	for _, r := range editorRoutes[2:] {
		status(t, do(ed, r), http.StatusSeeOther, "editor "+r.method+" "+r.path)
	}
	// Admin can do everything (delete last since it removes things).
	ad := h.other()
	ad.login("admin")
	for _, r := range adminRoutes {
		if code := do(ad, r); code != http.StatusSeeOther {
			t.Errorf("admin %s %s: %d", r.method, r.path, code)
		}
	}
	// Writes need the CSRF token.
	resp = ad.post("/locations", url.Values{"name": {"nocsrf"}}, map[string]string{"Origin": ad.srv.URL})
	status(t, resp.StatusCode, http.StatusForbidden, "location without CSRF")
}

func TestLocationsPage(t *testing.T) {
	h := newHarness(t)
	h.user("editor", store.RoleEditor)
	h.login("editor")
	resp := h.postOK("/locations", url.Values{"name": {"Storage <1>"}, "description": {"top"}})
	status(t, resp.StatusCode, 303, "create")
	_, body := h.get("/locations")
	if !strings.Contains(body, "Storage &lt;1&gt;") || strings.Contains(body, "Storage <1>") {
		t.Fatal("location name not escaped")
	}
	resp = h.postOK("/locations", url.Values{"name": {"storage <1>"}})
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "err=verr_taken") {
		t.Fatalf("duplicate: %s", loc)
	}
	_, body = h.get("/locations?err=verr_taken&vf=name")
	if !strings.Contains(body, "Name: Already taken.") {
		t.Fatal("validation message missing")
	}
	list, _ := h.db.ListLocations(context.Background())
	id := idStr(list[0].ID)
	if r := h.postOK("/locations/"+id, url.Values{"name": {"Hall"}}); r.StatusCode != 303 {
		t.Fatal("rename")
	}
	h.newAsset("KG", "Cable", func(a *store.Asset, _ map[string]store.Field) { a.LocationID = list[0].ID })
	resp = h.postOK("/locations/"+id+"/delete", url.Values{})
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "err=in_use") {
		t.Fatalf("delete in use: %s", loc)
	}
	_, body = h.get("/locations?err=in_use")
	if !strings.Contains(body, "Still in use") {
		t.Fatal("in-use message missing")
	}
	h.postOK("/locations", url.Values{"name": {"Empty"}})
	list, _ = h.db.ListLocations(context.Background())
	for _, l := range list {
		if l.Name == "Empty" {
			if r := h.postOK("/locations/"+idStr(l.ID)+"/delete", url.Values{}); r.StatusCode != 303 || strings.Contains(r.Header.Get("Location"), "err") {
				t.Fatal("delete empty location")
			}
		}
	}
	audits, _ := h.db.ListAudit(context.Background(), 50)
	seen := map[string]bool{}
	for _, e := range audits {
		seen[e.Action] = true
	}
	for _, want := range []string{"location.create", "location.update", "location.delete"} {
		if !seen[want] {
			t.Errorf("no audit entry %s", want)
		}
	}
}

func TestCategoryAndFieldEditor(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	h.login("admin")
	// Create with an invalid prefix, then a valid one.
	resp := h.postOK("/categories", url.Values{"name": {"Beamer"}, "prefix": {"b m"}})
	if !strings.Contains(resp.Header.Get("Location"), "err=verr_invalid_prefix") {
		t.Fatalf("invalid prefix: %s", resp.Header.Get("Location"))
	}
	resp = h.postOK("/categories", url.Values{"name": {"Beamer"}, "prefix": {"bm"}, "dguv_required": {"1"}, "interval": {"24"}})
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 303 || !strings.HasPrefix(loc, "/categories/") {
		t.Fatalf("create: %d %s", resp.StatusCode, loc)
	}
	base := strings.SplitN(loc, "?", 2)[0]
	_, body := h.get(base)
	for _, want := range []string{"Beamer", "BM", `value="24"`, "Add field"} {
		if !strings.Contains(body, want) {
			t.Errorf("category page lacks %q", want)
		}
	}
	// A category without prefix is accepted and shown as such.
	resp = h.postOK("/categories", url.Values{"name": {"Misc"}, "prefix": {""}})
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/categories/") {
		t.Fatalf("no prefix: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, page := h.get(strings.SplitN(resp.Header.Get("Location"), "?", 2)[0]); !strings.Contains(page, "no prefix") {
		t.Error("category without prefix not marked")
	}
	// Fields: select without options is refused, with options accepted.
	resp = h.postOK(base+"/fields", url.Values{"key": {"conn"}, "label": {"Anschluss"}, "type": {"select"}})
	if !strings.Contains(resp.Header.Get("Location"), "verr_invalid_options") {
		t.Fatalf("select without options: %s", resp.Header.Get("Location"))
	}
	h.postOK(base+"/fields", url.Values{"key": {"conn"}, "label": {"Anschluss"}, "type": {"select"}, "options": {"HDMI\r\nVGA\r\n"}})
	h.postOK(base+"/fields", url.Values{"key": {"lumen"}, "label": {"Helligkeit"}, "type": {"number"}, "unit": {"lm"}})
	c, fs := h.cat("BM")
	if len(fs) != 2 || len(fs["conn"].Options) != 2 || fs["lumen"].Unit != "lm" || fs["lumen"].Position != 1 {
		t.Fatalf("fields: %+v", fs)
	}
	// Reorder with the buttons.
	h.postOK(base+"/fields/"+idStr(fs["lumen"].ID)+"/move", url.Values{"dir": {"up"}})
	_, fs2 := h.cat("BM")
	if fs2["lumen"].Position != 0 || fs2["conn"].Position != 1 {
		t.Fatalf("move: %+v", fs2)
	}
	// Type change is refused while values exist, with a visible message.
	a := h.newAsset("BM", "Projector", func(a *store.Asset, fs map[string]store.Field) { a.Values[fs["lumen"].ID] = "3000" })
	resp = h.postOK(base+"/fields/"+idStr(fs["lumen"].ID), url.Values{"label": {"Helligkeit"}, "type": {"text"}})
	loc = resp.Header.Get("Location")
	if !strings.Contains(loc, "verr_type_locked") {
		t.Fatalf("type change: %s", loc)
	}
	_, body = h.get(loc)
	if !strings.Contains(body, "cannot be changed while values exist") {
		t.Fatal("type-locked message missing")
	}
	// Category with assets cannot be deleted; fields (and values) can.
	resp = h.postOK(base+"/delete", url.Values{})
	if !strings.Contains(resp.Header.Get("Location"), "err=in_use") {
		t.Fatalf("delete in use: %s", resp.Header.Get("Location"))
	}
	h.postOK(base+"/fields/"+idStr(fs["lumen"].ID)+"/delete", url.Values{})
	if got, _ := h.db.AssetByID(context.Background(), a.ID); len(got.Values) != 0 {
		t.Fatal("values survived field delete")
	}
	// A field of another category cannot be addressed through this one.
	_, other := h.cat("KG")
	resp = h.postOK(base+"/fields/"+idStr(other["length_m"].ID)+"/delete", url.Values{})
	status(t, resp.StatusCode, 404, "foreign field")
	_ = h.db.DeleteAsset(context.Background(), a.ID)
	resp = h.postOK(base+"/delete", url.Values{})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/categories?ok=deleted" {
		t.Fatalf("delete: %s", resp.Header.Get("Location"))
	}
	if _, err := h.db.CategoryByID(context.Background(), c.ID); err == nil {
		t.Fatal("category not deleted")
	}
	audits, _ := h.db.ListAudit(context.Background(), 100)
	seen := map[string]bool{}
	for _, e := range audits {
		seen[e.Action] = true
	}
	for _, want := range []string{"category.create", "category.delete", "category.field_create", "category.field_update", "category.field_delete", "category.field_move"} {
		if want == "category.field_update" {
			continue // the refused update must not be audited
		}
		if !seen[want] {
			t.Errorf("no audit entry %s", want)
		}
	}
	if seen["category.field_update"] {
		t.Error("refused field update was audited")
	}
}

func TestAssetFormValidationAndXSS(t *testing.T) {
	h := newHarness(t)
	h.user("editor", store.RoleEditor)
	h.login("editor")
	kt, fs := h.cat("KT")
	cid := idStr(kt.ID)

	// Category picker, then the form with unit suffix and options.
	_, body := h.get("/assets/new")
	if !strings.Contains(body, "/assets/new?category="+cid) {
		t.Fatal("category picker lacks links")
	}
	_, body = h.get("/assets/new?category=" + cid)
	for _, want := range []string{`name="f_length_m"`, `<span class="unit">m</span>`, `type="checkbox"`[:0] + `<option value="CEE 16 A blau">`, `name="category" value="` + cid + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks %q", want)
		}
	}
	// Invalid input re-renders the form with values kept and messages per field.
	xss := `<script>alert(1)</script>`
	resp, body := h.postBody("/assets", url.Values{"category": {cid}, "name": {xss}, "serial_no": {"SN-9"},
		"f_length_m": {"abc"}, "f_connector": {"Nope"}, "f_sockets": {"4"}, "purchase_date": {"2020-99-99"}})
	status(t, resp.StatusCode, http.StatusUnprocessableEntity, "invalid create")
	for _, want := range []string{"Not a valid number", "Value is not one of the options", "Not a valid date", "Please correct the marked input",
		`value="SN-9"`, `value="abc"`, `value="4"`, "&lt;script&gt;alert(1)&lt;/script&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("re-rendered form lacks %q", want)
		}
	}
	if strings.Contains(body, xss) {
		t.Fatal("name not escaped in form")
	}
	if n, _ := h.db.ListAssets(context.Background(), store.AssetFilter{}); n.Total != 0 {
		t.Fatal("invalid asset was stored")
	}
	// Required field missing.
	resp, body = h.postBody("/assets", url.Values{"category": {cid}, "name": {"Drum"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "Required.") {
		t.Fatalf("required: %d", resp.StatusCode)
	}

	// Valid create with an automatic number; comma accepted as decimal separator.
	form := url.Values{"category": {cid}, "name": {xss}, "f_length_m": {"12,5"}, "f_connector": {"Schuko"}, "location": {""}}
	resp = h.postOK("/assets", form)
	loc := resp.Header.Get("Location")
	if resp.StatusCode != 303 || !strings.HasPrefix(loc, "/assets/") {
		t.Fatalf("create: %d %s", resp.StatusCode, loc)
	}
	_, body = h.get(strings.SplitN(loc, "?", 2)[0])
	for _, want := range []string{"KT-00001", "12.5 m", "Schuko", "&lt;script&gt;alert(1)&lt;/script&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	if strings.Contains(body, xss) {
		t.Fatal("name not escaped on detail page")
	}
	_, body = h.get("/assets")
	if strings.Contains(body, xss) || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("name not escaped in list")
	}
	// Manual number; a collision shows "taken" next to the field and keeps input.
	form.Set("name", "Second")
	form.Set("inventory_no", "kt-00001")
	resp, body = h.postBody("/assets", form)
	if resp.StatusCode != 422 || !strings.Contains(body, "Already taken.") || !strings.Contains(body, `value="kt-00001"`) {
		t.Fatalf("collision: %d", resp.StatusCode)
	}
	form.Set("inventory_no", "LEGACY-1")
	if r := h.postOK("/assets", form); r.StatusCode != 303 {
		t.Fatal("manual number")
	}
	form.Del("inventory_no")
	if r := h.postOK("/assets", form); r.StatusCode != 303 {
		t.Fatal("next automatic number")
	}
	page, _ := h.db.ListAssets(context.Background(), store.AssetFilter{Q: "KT-00002"})
	if page.Total != 1 {
		t.Fatal("manual number consumed the sequence")
	}
	_ = fs
	// Unknown category on create goes back to the picker.
	resp = h.postOK("/assets", url.Values{"category": {"9999"}, "name": {"x"}})
	if resp.Header.Get("Location") != "/assets/new" {
		t.Fatalf("unknown category: %s", resp.Header.Get("Location"))
	}
	if resp, _ := h.get("/assets/9999"); resp.StatusCode != 404 {
		t.Fatalf("unknown asset: %d", resp.StatusCode)
	}
}

func TestAssetEditAndHistory(t *testing.T) {
	h := newHarness(t)
	h.user("editor", store.RoleEditor)
	h.login("editor")
	_, fs := h.cat("SL")
	a := h.newAsset("SL", "Strip", func(a *store.Asset, fs map[string]store.Field) {
		a.Values[fs["switch"].ID] = "0"
	})
	_ = fs
	// The create entry exists (the helper bypasses the handler, so create through the UI).
	cat, _ := h.cat("SL")
	resp := h.postOK("/assets", url.Values{"category": {idStr(cat.ID)}, "name": {"Via UI"}, "f_length_m": {"3"}, "f_switch": {"0", "1"}})
	loc := strings.SplitN(resp.Header.Get("Location"), "?", 2)[0]
	id := strings.TrimPrefix(loc, "/assets/")

	_, body := h.get("/assets/" + id + "/edit")
	for _, want := range []string{`value="Via UI"`, `value="3"`, "checked", "disabled"} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form lacks %q", want)
		}
	}
	// Invalid edit keeps input.
	resp, body = h.postBody("/assets/"+id, url.Values{"name": {""}, "f_length_m": {"x"}, "note": {"kept note"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "kept note") || !strings.Contains(body, "Required.") {
		t.Fatalf("invalid edit: %d", resp.StatusCode)
	}
	// Edit: rename, change a value, set status.
	resp = h.postOK("/assets/"+id, url.Values{"name": {"Renamed"}, "status": {"defective"}, "f_length_m": {"4,5"}, "f_switch": {"0", "1"}, "category": {"9999"}})
	if resp.StatusCode != 303 {
		t.Fatalf("edit: %d", resp.StatusCode)
	}
	// No change: no audit entry.
	before, _ := h.db.AuditByObject(context.Background(), "asset", id, 50)
	h.postOK("/assets/"+id, url.Values{"name": {"Renamed"}, "status": {"defective"}, "f_length_m": {"4.5"}, "f_switch": {"0", "1"}})
	after, _ := h.db.AuditByObject(context.Background(), "asset", id, 50)
	if len(before) != len(after) || len(after) != 2 {
		t.Fatalf("audit entries: %d then %d", len(before), len(after))
	}
	got, _ := h.db.AssetByID(context.Background(), parseID(id))
	if got.CategoryID != cat.ID {
		t.Fatal("category changed through the form")
	}
	// Retire it.
	h.postOK("/assets/"+id, url.Values{"name": {"Renamed"}, "status": {"retired"}, "f_length_m": {"4.5"}, "f_switch": {"0", "1"}})

	_, body = h.get("/assets/" + id)
	for _, want := range []string{"History", "created", "changed", "Via UI", "Renamed", "badge badge-defective", "badge badge-retired",
		"badge badge-active", "Länge", "3 → 4.5", "Schalter"} {
		if !strings.Contains(body, want) {
			t.Errorf("history lacks %q", want)
		}
	}
	// Newest first: the retire entry precedes the creation.
	if strings.Index(body, "badge-retired") > strings.Index(body, "Via UI") {
		t.Error("history is not newest first")
	}
	if strings.Contains(body, "editor</td>") == false {
		t.Error("history lacks the user")
	}
	// Audit details carry the changes map.
	es, _ := h.db.AuditByObject(context.Background(), "asset", id, 50)
	ch, _ := es[len(es)-1].Details["changes"].(map[string]any)
	if es[len(es)-1].Action != "asset.create" || ch["name"] == nil || ch["field:length_m"] == nil {
		t.Fatalf("create audit: %+v", es[len(es)-1].Details)
	}
	_ = a
}

func TestAssetDeleteAudit(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	h.login("admin")
	a := h.newAsset("KG", "Gone", nil)
	resp := h.postOK("/assets/"+idStr(a.ID)+"/delete", url.Values{})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/assets?ok=deleted" {
		t.Fatalf("delete: %s", resp.Header.Get("Location"))
	}
	es, _ := h.db.AuditByObject(context.Background(), "asset", idStr(a.ID), 5)
	if len(es) != 1 || es[0].Action != "asset.delete" {
		t.Fatalf("audit: %+v", es)
	}
}

func TestAssetListFiltersAndHTMX(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	h.login("viewer")
	kt, _ := h.cat("KT")
	loc := &store.Location{Name: "Shelf"}
	_ = h.db.CreateLocation(context.Background(), loc)
	h.newAsset("KT", "Drum blue", func(a *store.Asset, fs map[string]store.Field) {
		a.Values[fs["connector"].ID] = "CEE 16 A blau"
		a.LocationID = loc.ID
	})
	h.newAsset("KT", "Drum plain", func(a *store.Asset, fs map[string]store.Field) { a.Values[fs["connector"].ID] = "Schuko" })
	h.newAsset("SL", "Strip", func(a *store.Asset, fs map[string]store.Field) { a.Status = store.StatusLost })
	for i := 0; i < 55; i++ {
		h.newAsset("KG", "Bulk", nil)
	}

	// The full page loads htmx and its config; other pages do not.
	_, body := h.get("/assets")
	for _, want := range []string{`<script src="/static/htmx.min.js" defer>`, `name="htmx-config"`, `"allowEval":false`, `hx-get="/assets"`,
		`hx-push-url="true"`, "<!doctype html>", `id="results"`, "58 results"} {
		if !strings.Contains(body, want) {
			t.Errorf("list page lacks %q", want)
		}
	}
	for _, p := range []string{"/", "/locations", "/categories", "/account"} {
		if _, b := h.get(p); strings.Contains(b, "htmx.min.js") {
			t.Errorf("%s loads htmx", p)
		}
	}
	// Paging: 50 per page, links keep state.
	if !strings.Contains(body, "Page 1 of 2") || !strings.Contains(body, `href="/assets?page=2"`) {
		t.Error("paging links missing")
	}
	_, body = h.get("/assets?page=2")
	if !strings.Contains(body, "Page 2 of 2") || !strings.Contains(body, `href="/assets"`) {
		t.Error("page 2 / previous link")
	}

	// Filters in the URL: category with a field filter and search.
	q := "/assets?category=" + idStr(kt.ID) + "&f_connector=" + url.QueryEscape("CEE 16 A blau")
	_, body = h.get(q)
	if !strings.Contains(body, "Drum blue") || strings.Contains(body, "Drum plain") || !strings.Contains(body, "1 results") {
		t.Fatal("field filter")
	}
	// The form shows the state again (round trip) and the sort links keep it.
	for _, want := range []string{`<option value="` + idStr(kt.ID) + `" selected>`, `<option value="CEE 16 A blau" selected>`, `name="f_connector"`,
		"category=" + idStr(kt.ID), "f_connector=CEE&#43;16&#43;A&#43;blau"} {
		if !strings.Contains(body, want) {
			t.Errorf("round trip lacks %q", want)
		}
	}
	for _, c := range []struct{ q, in, out string }{
		{"?q=DRUM", "Drum plain", "Strip"},
		{"?q=schuko", "Drum plain", "Drum blue"}, // search in field values
		{"?location=" + idStr(loc.ID), "Drum blue", "Drum plain"},
		{"?status=lost", "Strip", "Drum"},
		{"?status=bogus&q=strip", "Strip", "Drum"}, // invalid status ignored
		{"?category=" + idStr(kt.ID) + "&q=plain&status=active", "Drum plain", "Drum blue"},
		{"?q=%25", "No assets found", "Strip"},
		{"?sort=name&dir=desc&q=drum", "Drum plain", "Strip"},
	} {
		_, b := h.get("/assets" + c.q)
		if !strings.Contains(b, c.in) || strings.Contains(b, c.out) {
			t.Errorf("%s: want %q, not %q", c.q, c.in, c.out)
		}
	}
	_, body = h.get("/assets?sort=name&dir=desc&q=drum")
	if strings.Index(body, "Drum plain") > strings.Index(body, "Drum blue") {
		t.Error("descending sort")
	}
	if !strings.Contains(body, `name="sort" value="name"`) || !strings.Contains(body, `name="dir" value="desc"`) {
		t.Error("sort lost in the form")
	}

	// htmx requests get the fragment only; a history restore gets the full page.
	resp, frag := h.getWith(q, map[string]string{"HX-Request": "true"})
	if resp.StatusCode != 200 || strings.Contains(frag, "<html") || strings.Contains(frag, "<!doctype") || strings.Contains(frag, "<nav class=\"nav\"") {
		t.Fatalf("fragment is a full page: %d", resp.StatusCode)
	}
	for _, want := range []string{"Drum blue", `<table class="assets">`, `id="field-filters"`, `hx-swap-oob="true"`, `<option value="CEE 16 A blau" selected>`} {
		if !strings.Contains(frag, want) {
			t.Errorf("fragment lacks %q", want)
		}
	}
	if strings.Contains(frag, "Drum plain") {
		t.Error("fragment ignores the filter")
	}
	if !strings.Contains(resp.Header.Get("Vary"), "HX-Request") {
		t.Error("no Vary: HX-Request")
	}
	_, full := h.getWith(q, map[string]string{"HX-Request": "true", "HX-History-Restore-Request": "true"})
	if !strings.Contains(full, "<!doctype html>") {
		t.Error("history restore must return the full page")
	}
	// Changing the category via htmx swaps the extra filters.
	sl, _ := h.cat("SL")
	_, frag = h.getWith("/assets?category="+idStr(sl.ID), map[string]string{"HX-Request": "true"})
	if !strings.Contains(frag, `name="f_switch"`) || strings.Contains(frag, `name="f_connector"`) {
		t.Error("field filters not swapped")
	}
}

func TestDashboardCounts(t *testing.T) {
	h := newHarness(t)
	h.user("editor", store.RoleEditor)
	h.user("viewer", store.RoleViewer)
	h.login("editor")
	h.newAsset("KG", "A", nil)
	h.newAsset("KG", "B", func(a *store.Asset, _ map[string]store.Field) { a.Status = store.StatusDefective })
	h.newAsset("SL", "C", nil)
	kg, _ := h.cat("KG")
	_, body := h.get("/")
	for _, want := range []string{`href="/assets?status=defective"`, `href="/assets?category=` + idStr(kg.ID) + `"`, `href="/assets/new"`, "Assets by category"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	// Viewers read but get no "new asset" button.
	v := h.other()
	v.login("viewer")
	_, body = v.get("/")
	if strings.Contains(body, `href="/assets/new"`) || !strings.Contains(body, `href="/assets"`) {
		t.Error("viewer dashboard")
	}
	// The inventory entry is in the navigation for everybody.
	if !strings.Contains(body, ">Inventory<") {
		t.Error("nav entry missing")
	}
}

func TestAssetCategoryChange(t *testing.T) {
	h := newHarness(t)
	h.user("ed", store.RoleEditor)
	h.login("ed")
	a := h.newAsset("SL", "Strip", func(a *store.Asset, fs map[string]store.Field) { a.Values[fs["sockets"].ID] = "5" })
	kg, _ := h.cat("KG")
	base := "/assets/" + idStr(a.ID)
	// Preview: the length is carried over, the sockets are listed as lost.
	_, body := h.get(base + "/edit?category=" + idStr(kg.ID))
	for _, want := range []string{"Kaltgerätekabel", `name="category" value="` + idStr(kg.ID) + `"`, `name="f_length_m" value="2"`, "Anzahl Steckplätze: 5"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	// Saving moves the asset; the number stays.
	f := url.Values{"category": {idStr(kg.ID)}, "name": {"Strip"}, "status": {"active"}, "f_length_m": {"2"}}
	resp := h.postOK(base, f)
	if resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Location"), "ok=saved") {
		t.Fatalf("move: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	got, err := h.db.AssetByID(context.Background(), a.ID)
	if err != nil || got.CategoryID != kg.ID || got.InventoryNo != a.InventoryNo || len(got.Values) != 1 {
		t.Fatalf("after move: %+v %v", got, err)
	}
	// Back to the power strips without the required length: 422 with the new fields.
	sl, _ := h.cat("SL")
	f = url.Values{"category": {idStr(sl.ID)}, "name": {"Strip"}, "status": {"active"}}
	if resp = h.postOK(base, f); resp.StatusCode != 422 {
		t.Fatalf("required after move: %d", resp.StatusCode)
	}
	if got, _ := h.db.AssetByID(context.Background(), a.ID); got.CategoryID != kg.ID {
		t.Error("refused move changed the category")
	}
}
