package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/store"
)

// inspUser creates a user with the can_inspect flag.
func (h *harness) inspUser(name, role string) {
	h.t.Helper()
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		h.t.Fatal(err)
	}
	u := &store.User{Username: name, PasswordHash: hash, Role: role, CanInspect: true}
	if err := h.db.CreateUser(context.Background(), u); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) langDE(name string) {
	h.t.Helper()
	u, err := h.db.UserByName(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.db.SetUserPrefs(context.Background(), u.ID, "de", "auto"); err != nil {
		h.t.Fatal(err)
	}
}

// addInsp records an inspection directly in the database.
func (h *harness) addInsp(assetID int64, on, result string, mod func(*store.Inspection)) *store.Inspection {
	h.t.Helper()
	in := &store.Inspection{AssetID: assetID, InspectedOn: on, InspectorName: "Eli", Result: result}
	if mod != nil {
		mod(in)
	}
	if _, _, err := h.db.CreateInspection(context.Background(), in, false, false); err != nil {
		h.t.Fatal(err)
	}
	return in
}

func validInspForm() url.Values {
	return url.Values{"inspected_on": {"2025-05-05"}, "inspector_name": {"Eli Ectric"}, "result": {"pass"},
		"protection_class": {"I"}, "visual_ok": {"1"}, "function_ok": {"1"}, "pe_resistance_ohm": {"0,25"},
		"insulation_mohm": {"5"}, "leakage_ma": {"1,5"}, "test_device": {"Tester"}, "remark": {"fine"}}
}

func inspCount(t *testing.T, h *harness, assetID int64) int {
	t.Helper()
	l, err := h.db.InspectionsByAsset(context.Background(), assetID)
	if err != nil {
		t.Fatal(err)
	}
	return len(l)
}

func TestInspectionPermissions(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	h.user("admin", store.RoleAdmin)
	h.inspUser("vinsp", store.RoleViewer)
	a := h.newAsset("SL", "Strip", nil)
	id := idStr(a.ID)
	in := h.addInsp(a.ID, "2025-01-01", "pass", nil)
	iid := idStr(in.ID)

	resp, _ := h.get("/inspections/due")
	status(t, resp.StatusCode, 303, "anonymous due page")

	// Without can_inspect: 403 for every role, even admins; nothing is stored.
	for _, name := range []string{"viewer", "admin"} {
		c := h.other()
		c.login(name)
		resp, _ := c.get("/assets/" + id + "/inspections/new")
		status(t, resp.StatusCode, 403, name+" new form")
		status(t, c.postOK("/assets/"+id+"/inspections", validInspForm()).StatusCode, 403, name+" POST")
		for _, p := range []string{"/inspections", "/inspections/due", "/inspections/" + iid, "/inspections/" + iid + "/print", "/inspections.csv"} {
			resp, _ := c.get(p)
			status(t, resp.StatusCode, 200, name+" "+p)
		}
	}
	if n := inspCount(t, h, a.ID); n != 1 {
		t.Fatalf("inspections stored by forbidden users: %d", n)
	}

	// A viewer with the flag may record, but not delete.
	c := h.other()
	c.login("vinsp")
	resp, body := c.get("/assets/" + id + "/inspections/new")
	status(t, resp.StatusCode, 200, "vinsp form")
	if !strings.Contains(body, `value="vinsp"`) {
		t.Error("inspector name is not prefilled with the user name")
	}
	resp = c.postOK("/assets/"+id+"/inspections", validInspForm())
	status(t, resp.StatusCode, 303, "vinsp POST")
	if n := inspCount(t, h, a.ID); n != 2 {
		t.Fatalf("inspection not stored: %d", n)
	}
	status(t, c.postOK("/inspections/"+iid+"/delete", url.Values{}).StatusCode, 403, "vinsp delete")
	// No CSRF token: rejected before anything happens.
	f := validInspForm()
	resp = c.post("/assets/"+id+"/inspections", f, map[string]string{"Origin": h.srv.URL})
	status(t, resp.StatusCode, 403, "POST without CSRF token")

	// Admin deletes (and it is audited).
	ad := h.other()
	ad.login("admin")
	status(t, ad.postOK("/inspections/"+iid+"/delete", url.Values{}).StatusCode, 303, "admin delete")
	if n := inspCount(t, h, a.ID); n != 1 {
		t.Fatalf("after delete: %d", n)
	}
	found := false
	entries, _ := h.db.ListAudit(context.Background(), 50)
	for _, e := range entries {
		if e.Action == "inspection.delete" && e.ObjectID == iid && e.Details["inventory_no"] == a.InventoryNo {
			found = true
		}
	}
	if !found {
		t.Error("inspection.delete not audited")
	}
	resp, _ = ad.get("/inspections/" + iid)
	status(t, resp.StatusCode, 404, "deleted inspection")
}

func TestInspectionRefusals(t *testing.T) {
	h := newHarness(t)
	h.inspUser("insp", store.RoleEditor)
	h.login("insp")
	nk := h.newAsset("NK", "Network cable", nil)
	ret := h.newAsset("SL", "Retired strip", func(a *store.Asset, _ map[string]store.Field) { a.Status = store.StatusRetired })
	lost := h.newAsset("SL", "Lost strip", func(a *store.Asset, _ map[string]store.Field) { a.Status = store.StatusLost })
	for name, a := range map[string]*store.Asset{"non-DGUV": nk, "retired": ret, "lost": lost} {
		id := idStr(a.ID)
		resp, _ := h.get("/assets/" + id + "/inspections/new")
		status(t, resp.StatusCode, 422, name+" form")
		status(t, h.postOK("/assets/"+id+"/inspections", validInspForm()).StatusCode, 422, name+" POST")
		if n := inspCount(t, h, a.ID); n != 0 {
			t.Errorf("%s: inspection stored", name)
		}
	}
	resp, body := h.get("/assets/" + idStr(nk.ID))
	if strings.Contains(body, "/inspections/new") || resp.StatusCode != 200 {
		t.Error("non-DGUV asset offers the record button")
	}
	_, body = h.get("/assets/" + idStr(ret.ID))
	if strings.Contains(body, "/inspections/new") {
		t.Error("retired asset offers the record button")
	}
	resp, _ = h.get("/assets/99999/inspections/new")
	status(t, resp.StatusCode, 404, "unknown asset")
}

func TestInspectionCreateStatusChangeAndAudit(t *testing.T) {
	h := newHarness(t)
	h.inspUser("insp", store.RoleViewer)
	h.login("insp")
	a := h.newAsset("SL", "Strip", nil)
	id := idStr(a.ID)

	_, body := h.get("/assets/" + id + "/inspections/new")
	if !strings.Contains(body, `name="set_defective"`) || regexp.MustCompile(`name="set_defective" value="1" checked`).MatchString(body) {
		t.Error("set defective missing or checked by default")
	}
	if strings.Contains(body, `name="set_active"`) {
		t.Error("set active offered for an active asset")
	}
	for _, want := range []string{"at most 0.3", "at least 1", "Simplified guidance"} {
		if !strings.Contains(body, want) {
			t.Errorf("form lacks guidance text %q", want)
		}
	}

	// A failed inspection with the default checkbox sets the asset to defective.
	f := validInspForm()
	f.Set("result", "fail")
	f.Set("pe_resistance_ohm", "0,5")
	f.Set("set_defective", "1")
	resp := h.postOK("/assets/"+id+"/inspections", f)
	status(t, resp.StatusCode, 303, "create")
	loc := resp.Header.Get("Location")
	if !strings.HasPrefix(loc, "/inspections/") {
		t.Fatalf("redirect %q", loc)
	}
	cur, _ := h.db.AssetByID(context.Background(), a.ID)
	if cur.Status != store.StatusDefective {
		t.Fatalf("status %s", cur.Status)
	}
	actions := map[string]store.AuditEntry{}
	entries, _ := h.db.ListAudit(context.Background(), 50)
	for _, e := range entries {
		actions[e.Action] = e
	}
	ic, ok := actions["inspection.create"]
	if !ok || ic.Details["result"] != "fail" || ic.Details["next_due"] != "2026-05-05" || ic.Details["inventory_no"] != a.InventoryNo {
		t.Fatalf("inspection.create audit: %+v", ic)
	}
	if au, ok := actions["asset.update"]; !ok || !strings.Contains(au.RawDetails, `"status":["active","defective"]`) {
		t.Fatalf("asset.update audit: %+v", au)
	}
	// Detail page: guidance hint for 0.5 ohm; asset page: card and status history.
	_, body = h.get(loc)
	for _, want := range []string{"Protective conductor resistance above 0.3", "0.5 Ω", "Eli Ectric", "failed"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail lacks %q", want)
		}
	}
	_, body = h.get("/assets/" + id)
	for _, want := range []string{"DGUV V3", "due on 2026-05-05", "badge-due-overdue", "badge-defective", "Record inspection"} {
		if !strings.Contains(body, want) {
			t.Errorf("asset page lacks %q", want)
		}
	}
	// Pass with "set back to active".
	_, body = h.get("/assets/" + id + "/inspections/new")
	if !strings.Contains(body, `name="set_active"`) || strings.Contains(body, `name="set_defective"`) {
		t.Error("defective asset must offer set_active only")
	}
	if regexp.MustCompile(`name="set_active" value="1" checked`).MatchString(body) {
		t.Error("set active must be unchecked by default")
	}
	// Unchecked: stays defective. Checked: back to active.
	f = validInspForm()
	status(t, h.postOK("/assets/"+id+"/inspections", f).StatusCode, 303, "pass without option")
	if cur, _ = h.db.AssetByID(context.Background(), a.ID); cur.Status != store.StatusDefective {
		t.Fatalf("status %s", cur.Status)
	}
	f.Set("set_active", "1")
	status(t, h.postOK("/assets/"+id+"/inspections", f).StatusCode, 303, "pass with option")
	if cur, _ = h.db.AssetByID(context.Background(), a.ID); cur.Status != store.StatusActive {
		t.Fatalf("status %s", cur.Status)
	}
}

func TestInspectionValidationRerender(t *testing.T) {
	h := newHarness(t)
	h.inspUser("insp", store.RoleViewer)
	h.login("insp")
	a := h.newAsset("SL", "Strip", nil)
	f := validInspForm()
	f.Set("pe_resistance_ohm", "abc")
	f.Set("insulation_mohm", "-1")
	f.Set("leakage_ma", "5000")
	f.Set("remark", `<script>alert(1)</script>`)
	f.Set("result", "")
	resp, body := h.postBody("/assets/"+idStr(a.ID)+"/inspections", f)
	status(t, resp.StatusCode, 422, "invalid input")
	if n := inspCount(t, h, a.ID); n != 0 {
		t.Fatal("invalid inspection stored")
	}
	if strings.Count(body, "field-error") < 4 {
		t.Errorf("want 4 field errors, body has %d", strings.Count(body, "field-error"))
	}
	if !strings.Contains(body, `value="abc"`) || !strings.Contains(body, `value="Eli Ectric"`) {
		t.Error("input not kept")
	}
	if strings.Contains(body, "<script>alert(1)") || !strings.Contains(body, "&lt;script&gt;alert(1)") {
		t.Error("remark not escaped")
	}
}

func TestInspectionProtocolPage(t *testing.T) {
	h := newHarness(t)
	h.ui.Cfg.Organization.Name = "Test Club <e.V.>"
	h.user("viewer", store.RoleViewer)
	h.langDE("viewer")
	a := h.newAsset("SL", "Strip <b>1</b>", func(a *store.Asset, fs map[string]store.Field) { a.Values[fs["length_m"].ID] = "2.5" })
	in := h.addInsp(a.ID, "2025-05-05", "fail", func(i *store.Inspection) {
		i.ProtectionClass = "I"
		i.PEInput = "0,45"
		i.InsulationInput = "0,8"
		i.Remark = "<img src=x>"
	})
	h.login("viewer")
	resp, body := h.get("/inspections/" + idStr(in.ID) + "/print")
	status(t, resp.StatusCode, 200, "protocol")
	if strings.Contains(strings.ToLower(body), "<script") {
		t.Error("protocol page contains a script")
	}
	for _, bad := range []string{"topbar", "app.js", "htmx", "data-csrf", `class="nav"`} {
		if strings.Contains(body, bad) {
			t.Errorf("protocol page contains app layout part %q", bad)
		}
	}
	for _, want := range []string{
		"/static/print.css", "Test Club &lt;e.V.&gt;", "Strip &lt;b&gt;1&lt;/b&gt;", SLNo(a), "05.05.2025", "05.05.2026",
		"0,45 Ω", "0,8 MΩ", "2,5 m", "nicht bestanden", "Schutzleiterwiderstand über 0,3 Ω", "Isolationswiderstand unter 1 MΩ",
		"Elektrofachkraft", "Unterschrift Prüfer", "Ort, Datum", "&lt;img src=x&gt;", "Strg&#43;P",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("protocol lacks %q", want)
		}
	}
	// English variant.
	e := h.other()
	h.user("viewer2", store.RoleViewer)
	e.login("viewer2")
	_, body = e.get("/inspections/" + idStr(in.ID) + "/print")
	for _, want := range []string{"qualified electrician", "Inspector&#39;s signature", "0.45 Ω", "2025-05-05"} {
		if !strings.Contains(body, want) {
			t.Errorf("English protocol lacks %q", want)
		}
	}
	// The print stylesheet is served and has an A4 page rule.
	resp, css := h.get("/static/print.css")
	if resp.StatusCode != 200 || !strings.Contains(css, "A4") {
		t.Errorf("print.css: %d", resp.StatusCode)
	}
	// Unknown inspection.
	resp, _ = h.get("/inspections/99999/print")
	status(t, resp.StatusCode, 404, "unknown protocol")
}

// SLNo returns the inventory number of an asset.
func SLNo(a *store.Asset) string { return a.InventoryNo }

func TestInspectionCSV(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	h.login("viewer")
	a := h.newAsset("SL", "=HYPERLINK(\"x\")", nil)
	b := h.newAsset("KT", "Drum, \"big\"", nil)
	h.addInsp(a.ID, "2025-05-05", "pass", func(i *store.Inspection) {
		i.InspectorName = "+49 Eli"
		i.Remark = "line1\nline2, \"quoted\""
		i.ProtectionClass = "II"
		i.PEInput = "0,25"
		i.VisualOK = true
		i.TestDevice = "@dev"
	})
	h.addInsp(b.ID, "2025-06-06", "fail", func(i *store.Inspection) { i.InspectorName = "-x" })

	resp, body := h.get("/inspections.csv")
	status(t, resp.StatusCode, 200, "csv")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content type %q", ct)
	}
	wantName := `attachment; filename="lanventory-inspections-` + time.Now().UTC().Format("2006-01-02") + `.csv"`
	if cd := resp.Header.Get("Content-Disposition"); cd != wantName {
		t.Errorf("content disposition %q, want %q", cd, wantName)
	}
	if strings.HasPrefix(body, "\ufeff") {
		t.Error("CSV starts with a BOM")
	}
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, body)
	}
	if len(rows) != 3 {
		t.Fatalf("rows: %d", len(rows))
	}
	wantHeader := "inspection_id,inventory_no,asset_name,category,location,inspected_on,result,protection_class,visual_ok,function_ok," +
		"pe_resistance_ohm,pe_resistance_cmp,insulation_mohm,insulation_cmp,leakage_ma,leakage_cmp,leakage_method,touch_ma,touch_cmp,reason," +
		"test_device,test_device_id,test_device_calibrated_until,inspector_name,next_due,remark,created_at"
	if strings.Join(rows[0], ",") != wantHeader {
		t.Errorf("header: %v", rows[0])
	}
	// Newest first: the drum, then the strip.
	if rows[1][2] != `Drum, "big"` || rows[1][23] != "'-x" || rows[1][6] != "fail" {
		t.Errorf("row 1: %q", rows[1])
	}
	r := rows[2]
	if r[2] != `'=HYPERLINK("x")` || r[20] != "'@dev" || r[23] != "'+49 Eli" || r[25] != "line1\nline2, \"quoted\"" {
		t.Errorf("escaping / formula protection: %q", r)
	}
	if r[0] == "" || r[5] != "2025-05-05" || r[7] != "II" || r[8] != "1" || r[9] != "0" || r[10] != "0.25" || r[11] != "" ||
		r[24] != "2026-05-05" {
		t.Errorf("values: %q", r)
	}
	if _, err := time.Parse(time.RFC3339, r[26]); err != nil || !strings.HasSuffix(r[26], "Z") {
		t.Errorf("created_at %q", r[26])
	}
	// Same filters as the list.
	_, body = h.get("/inspections.csv?result=fail")
	rows, _ = csv.NewReader(strings.NewReader(body)).ReadAll()
	if len(rows) != 2 || rows[1][6] != "fail" {
		t.Errorf("filtered rows: %q", rows)
	}
	_, body = h.get("/inspections.csv?from=2025-06-01&to=2025-12-31&inspector=x&category=" + idStr(b.CategoryID))
	rows, _ = csv.NewReader(strings.NewReader(body)).ReadAll()
	if len(rows) != 2 {
		t.Errorf("combined filter rows: %d", len(rows))
	}
	// The list links to the export with the same filters.
	_, body = h.get("/inspections?result=pass&inspector=Eli")
	if !strings.Contains(body, `href="/inspections.csv?inspector=Eli&amp;result=pass"`) {
		t.Errorf("CSV link lacks the filters: %s", regexp.MustCompile(`inspections\.csv[^"]*`).FindString(body))
	}
	// Garbage filter values are ignored.
	resp, _ = h.get("/inspections.csv?from=nope&result=x&category=abc")
	status(t, resp.StatusCode, 200, "garbage filters")
}

func TestInspectionListAndDetail(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	h.login("viewer")
	a := h.newAsset("SL", "Strip", nil)
	for i := 1; i <= 55; i++ {
		h.addInsp(a.ID, fmt.Sprintf("2025-01-%02d", i%28+1), "pass", nil)
	}
	_, body := h.get("/inspections")
	if !strings.Contains(body, "55 results") || !strings.Contains(body, "Page 1 of 2") || !strings.Contains(body, "page=2") {
		t.Error("list lacks count or paging")
	}
	_, body = h.get("/inspections?page=2")
	if !strings.Contains(body, "Page 2 of 2") {
		t.Error("page 2")
	}
	_, body = h.get("/inspections?result=fail")
	if !strings.Contains(body, "No inspections found") {
		t.Error("empty filter result")
	}
	_, body = h.get("/assets")
	for _, want := range []string{`href="/inspections/due"`, `href="/inspections"`} {
		if !strings.Contains(body, want) {
			t.Errorf("subnav lacks %s", want)
		}
	}
}

// dueCountText extracts "N results" from a due page body.
func dueCountText(t *testing.T, body string) int {
	t.Helper()
	m := regexp.MustCompile(`(\d+) results`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no result count in %q", body)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func TestDuePageAndDashboard(t *testing.T) {
	h := newHarness(t)
	h.user("viewer", store.RoleViewer)
	h.login("viewer")
	now := time.Now().UTC()
	mk := func(name string, st string) *store.Asset {
		return h.newAsset("SL", name, func(a *store.Asset, _ map[string]store.Field) { a.Status = st; a.DGUVIntervalMonths = 1 })
	}
	overdue1 := mk("overdue1", store.StatusActive)
	overdue2 := mk("overdue2", store.StatusDefective)
	soon := mk("soon", store.StatusActive)
	ok := h.newAsset("SL", "ok", func(a *store.Asset, _ map[string]store.Field) { a.DGUVIntervalMonths = 12 })
	mk("never1", store.StatusActive)
	mk("never2", store.StatusActive)
	mk("retired", store.StatusRetired)
	h.newAsset("NK", "netcable", nil)
	d := func(t time.Time) string { return t.Format(store.DateLayout) }
	h.addInsp(overdue1.ID, d(now.AddDate(0, -3, 0)), "pass", nil)
	h.addInsp(overdue2.ID, d(now.AddDate(0, -2, 0)), "pass", nil)
	h.addInsp(soon.ID, d(now.AddDate(0, -1, 5)), "pass", nil)
	h.addInsp(ok.ID, d(now), "pass", nil)

	counts, err := h.db.CountDue(context.Background(), now)
	if err != nil || counts != (store.DueCounts{Overdue: 2, Soon: 1, Never: 2}) {
		t.Fatalf("counts %+v %v", counts, err)
	}
	// Dashboard widget equals the numbers of the overview page per state.
	_, dash := h.get("/")
	for state, want := range map[string]int{"overdue": counts.Overdue, "soon": counts.Soon, "never": counts.Never} {
		m := regexp.MustCompile(`data-count="` + state + `" href="([^"]+)">(\d+)<`).FindStringSubmatch(dash)
		if m == nil {
			t.Fatalf("dashboard lacks the %s count", state)
		}
		if n, _ := strconv.Atoi(m[2]); n != want {
			t.Errorf("dashboard %s = %d, want %d", state, n, want)
		}
		if m[1] != "/inspections/due?state="+state {
			t.Errorf("dashboard link %q", m[1])
		}
		_, page := h.get(m[1])
		if got := dueCountText(t, page); got != want {
			t.Errorf("overview for %s lists %d, dashboard says %d", state, got, want)
		}
	}
	// The existing widgets stay.
	if !strings.Contains(dash, "Assets by status") || !strings.Contains(dash, "Assets by category") {
		t.Error("dashboard lost its status/category widgets")
	}
	// All states together; order: never first, then by due date.
	_, page := h.get("/inspections/due")
	if got := dueCountText(t, page); got != 5 {
		t.Errorf("all: %d", got)
	}
	idx := func(s string) int { return strings.Index(page, "<td>"+s+"</td>") }
	if idx("never1") >= idx("overdue1") || idx("never2") >= idx("overdue1") || idx("overdue1") >= idx("overdue2") || idx("overdue2") >= idx("soon") {
		t.Errorf("wrong sort order: %d %d %d %d %d", idx("never1"), idx("never2"), idx("overdue1"), idx("overdue2"), idx("soon"))
	}
	for _, absent := range []string{"<td>ok</td>", "<td>retired</td>", "<td>netcable</td>"} {
		if strings.Contains(page, absent) {
			t.Errorf("due page lists %q: %s", absent, page[strings.Index(page, absent)-60:strings.Index(page, absent)+60])
		}
	}
	// htmx: fragment only; plain and history restore: full page.
	resp, frag := h.getWith("/inspections/due?state=never", map[string]string{"HX-Request": "true"})
	status(t, resp.StatusCode, 200, "fragment")
	if strings.Contains(frag, "<html") || strings.Contains(frag, "topbar") || !strings.Contains(frag, "never1") || strings.Contains(frag, "overdue1") {
		t.Errorf("fragment: %q", frag)
	}
	if !strings.Contains(resp.Header.Get("Vary"), "HX-Request") {
		t.Error("Vary: HX-Request missing")
	}
	_, full := h.getWith("/inspections/due?state=never", map[string]string{"HX-Request": "true", "HX-History-Restore-Request": "true"})
	if !strings.Contains(full, "<html") {
		t.Error("history restore must get the full page")
	}
	_, full = h.get("/inspections/due?state=never")
	if !strings.Contains(full, "<html") || !strings.Contains(full, `hx-get="/inspections/due"`) {
		t.Error("full page")
	}
	// Category and location filters; garbage ignored.
	_, page = h.get("/inspections/due?category=" + idStr(overdue1.CategoryID))
	if got := dueCountText(t, page); got != 5 {
		t.Errorf("category filter: %d", got)
	}
	_, page = h.get("/inspections/due?location=99999&state=bogus")
	if got := dueCountText(t, page); got != 0 {
		t.Errorf("unknown location: %d", got)
	}
}

func TestGermanNumberAndDateFormatting(t *testing.T) {
	cases := []struct {
		lang string
		v    any
		want string
	}{
		{"de", "2.5", "2,5"}, {"en", "2.5", "2.5"}, {"de", "12", "12"}, {"de", 0.25, "0,25"}, {"en", 0.25, "0.25"},
		{"de", (*float64)(nil), ""}, {"de", "-1.5", "-1,5"}, {"de", 3, "3"}, {"de", "1000.5", "1000,5"},
	}
	for _, c := range cases {
		if got := fmtNum(c.lang, c.v); got != c.want {
			t.Errorf("fmtNum(%s, %v) = %q, want %q", c.lang, c.v, got, c.want)
		}
	}
	if fmtDate("de", "2025-05-05") != "05.05.2025" || fmtDate("en", "2025-05-05") != "2025-05-05" || fmtDate("de", "") != "" || fmtDate("de", "x") != "x" {
		t.Error("fmtDate")
	}

	h := newHarness(t)
	h.user("de", store.RoleViewer)
	h.langDE("de")
	a := h.newAsset("SL", "Strip", func(a *store.Asset, fs map[string]store.Field) { a.Values[fs["length_m"].ID] = "2.5" })
	h.addInsp(a.ID, "2025-05-05", "pass", func(i *store.Inspection) { i.PEInput = "0.25"; i.LeakageInput = "1,5" })
	h.login("de")
	_, body := h.get("/assets/" + idStr(a.ID))
	if !strings.Contains(body, "2,5 m") || strings.Contains(body, "2.5 m") {
		t.Error("asset detail: number not shown with a decimal comma")
	}
	if !strings.Contains(body, "05.05.2025") {
		t.Error("asset detail: German date missing")
	}
	list, _ := h.db.InspectionsByAsset(context.Background(), a.ID)
	_, body = h.get("/inspections/" + idStr(list[0].ID))
	if !strings.Contains(body, "0,25 Ω") || !strings.Contains(body, "1,5 mA") {
		t.Error("inspection detail: decimal comma missing")
	}
}
