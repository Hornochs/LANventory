package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/config"
	"github.com/lan-dot-party/lanventory/internal/store"
)

func TestAuthenticationRequired(t *testing.T) {
	f := newFixture(t)
	for _, h := range []string{"", "Bearer", "Bearer nope", "Basic abc", "Bearer " + strings.Repeat("a", 44)} {
		rec := f.do(req{method: "GET", path: "/api/v1/assets", headers: map[string]string{"Authorization": h}})
		if h == "" {
			delete(rec.Header(), "Authorization")
		}
		m := expect(t, rec, 401, "")
		if m["code"] != "invalid_token" && m["code"] != "unauthorized" {
			t.Errorf("%q: code %v", h, m["code"])
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%q: no WWW-Authenticate header", h)
		}
	}
	expect(t, f.call("GET", "/api/v1/assets", "", ""), 401, "unauthorized")
	// Unknown paths answer in the problem format, without needing a login.
	expect(t, f.call("GET", "/api/v1/nope", "", ""), 404, "not_found")
	expect(t, f.call("GET", "/api/nope", "", ""), 404, "not_found")
	expect(t, f.call("PUT", "/api/v1/assets", f.full, ""), 405, "method_not_allowed")
}

func TestScopeEnforcement(t *testing.T) {
	f := newFixture(t)
	id := idStr(f.drum1.ID)
	cases := []struct {
		name   string
		user   *store.User
		scopes []string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{"read needs read:assets", f.admin, []string{"read:inspections"}, "GET", "/api/v1/assets", "", 403, "insufficient_scope"},
		{"categories need read:assets", f.admin, []string{"write:assets"}, "GET", "/api/v1/categories", "", 403, "insufficient_scope"},
		{"write:assets does not imply read", f.admin, []string{"write:assets"}, "GET", "/api/v1/locations", "", 403, "insufficient_scope"},
		{"write needs write:assets", f.admin, []string{"read:assets"}, "POST", "/api/v1/locations", `{"name":"Z"}`, 403, "insufficient_scope"},
		{"write asset", f.admin, []string{"read:assets"}, "POST", "/api/v1/assets", `{}`, 403, "insufficient_scope"},
		{"inspections need read:inspections", f.admin, []string{"read:assets", "write:assets", "write:inspections"}, "GET", "/api/v1/inspections", "", 403, "insufficient_scope"},
		{"due needs read:inspections", f.admin, []string{"read:assets"}, "GET", "/api/v1/inspections/due", "", 403, "insufficient_scope"},
		{"asset inspections need read:inspections", f.admin, []string{"read:assets"}, "GET", "/api/v1/assets/" + id + "/inspections", "", 403, "insufficient_scope"},
		{"record needs write:inspections", f.admin, []string{"read:inspections"}, "POST", "/api/v1/assets/" + id + "/inspections", `{"result":"pass"}`, 403, "insufficient_scope"},
		{"delete inspection needs write:inspections", f.admin, []string{"read:inspections", "write:assets"}, "DELETE", fmt.Sprintf("/api/v1/inspections/%d", f.insp.ID), "", 403, "insufficient_scope"},
		{"delete asset needs write:assets", f.admin, []string{"read:assets", "write:inspections"}, "DELETE", "/api/v1/assets/" + id, "", 403, "insufficient_scope"},
		{"me needs no scope", f.viewer, []string{"read:inspections"}, "GET", "/api/v1/me", "", 200, ""},
		{"scope present", f.viewer, []string{"read:assets"}, "GET", "/api/v1/assets", "", 200, ""},
		// Scope AND role: the scope alone is not enough.
		{"editor scope, viewer owner", f.viewer, []string{"read:assets", "write:assets"}, "POST", "/api/v1/locations", `{"name":"Z"}`, 403, "forbidden"},
		{"delete asset, editor owner", f.editor, []string{"write:assets"}, "DELETE", "/api/v1/assets/" + id, "", 403, "forbidden"},
		{"delete inspection, inspector owner", f.inspector, []string{"write:inspections"}, "DELETE", fmt.Sprintf("/api/v1/inspections/%d", f.insp.ID), "", 403, "forbidden"},
		{"record without can_inspect", f.editor, []string{"write:inspections"}, "POST", "/api/v1/assets/" + id + "/inspections", `{"result":"pass"}`, 403, "forbidden"},
		{"record with can_inspect, viewer role", f.inspector, []string{"write:inspections"}, "POST", "/api/v1/assets/" + id + "/inspections", `{"result":"pass"}`, 201, ""},
	}
	for _, c := range cases {
		rec := f.call(c.method, c.path, f.token(c.user, c.scopes...), c.body)
		expect(t, rec, c.status, c.code)
	}
	// Nothing was changed by the refused calls.
	if _, err := f.db.AssetByID(context.Background(), f.drum1.ID); err != nil {
		t.Errorf("asset deleted by a refused call: %v", err)
	}
}

func TestOwnerRoleChangeTakesEffect(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tok := f.token(f.editor, "read:assets", "write:assets")
	expect(t, f.call("POST", "/api/v1/locations", tok, `{"name":"Garage"}`), 201, "")
	expect(t, f.call("DELETE", fmt.Sprintf("/api/v1/assets/%d", f.mug1.ID), tok, ""), 403, "forbidden")

	// Downgrade: the same token loses its write access at once.
	must(t, f.db.UpdateUser(ctx, f.editor.ID, store.RoleViewer, false, false))
	expect(t, f.call("POST", "/api/v1/locations", tok, `{"name":"Attic"}`), 403, "forbidden")
	expect(t, f.call("GET", "/api/v1/locations", tok, ""), 200, "")
	me := expect(t, f.call("GET", "/api/v1/me", tok, ""), 200, "")
	if fmt.Sprint(me["effective_scopes"]) != "[read:assets]" {
		t.Errorf("effective scopes after downgrade: %v", me["effective_scopes"])
	}

	// Upgrade to admin: deleting assets now works with the same token.
	must(t, f.db.UpdateUser(ctx, f.editor.ID, store.RoleAdmin, false, false))
	expect(t, f.call("DELETE", fmt.Sprintf("/api/v1/assets/%d", f.mug1.ID), tok, ""), 204, "")

	// can_inspect is read from the owner at request time, too.
	itok := f.token(f.inspector, "write:inspections")
	path := fmt.Sprintf("/api/v1/assets/%d/inspections", f.drum1.ID)
	expect(t, f.call("POST", path, itok, `{"result":"pass"}`), 201, "")
	must(t, f.db.UpdateUser(ctx, f.inspector.ID, store.RoleViewer, false, false))
	expect(t, f.call("POST", path, itok, `{"result":"pass"}`), 403, "forbidden")
}

func TestTokenLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ok := func(tok string) { t.Helper(); expect(t, f.call("GET", "/api/v1/me", tok, ""), 200, "") }
	bad := func(tok string) { t.Helper(); expect(t, f.call("GET", "/api/v1/me", tok, ""), 401, "invalid_token") }

	// Revoked.
	tok, row := f.tokenRow(f.editor, nil, "read:assets")
	ok(tok)
	must(t, f.db.RevokeAPIToken(ctx, row.ID))
	bad(tok)

	// Expiry in the past, and in the future.
	past := time.Now().Add(-time.Minute)
	tok, _ = f.tokenRow(f.editor, &past, "read:assets")
	bad(tok)
	future := time.Now().Add(time.Hour)
	tok, _ = f.tokenRow(f.editor, &future, "read:assets")
	ok(tok)

	// Disabled owner.
	tok = f.token(f.viewer, "read:assets")
	ok(tok)
	must(t, f.db.UpdateUser(ctx, f.viewer.ID, store.RoleViewer, false, true))
	bad(tok)
	must(t, f.db.UpdateUser(ctx, f.viewer.ID, store.RoleViewer, false, false))
	ok(tok)

	// Deleting the owner removes the token (cascade).
	tok = f.token(f.editor, "read:assets")
	must(t, f.db.DeleteUser(ctx, f.editor.ID))
	bad(tok)

	// A token must not be accepted from a prefix alone or with changed case.
	tok = f.token(f.admin, "read:assets")
	bad(tok[:12])
	bad(strings.ToUpper(tok))
}

func TestTokenStoredHashedOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	tok, row := f.tokenRow(f.admin, nil, "read:assets")
	if !strings.HasPrefix(tok, auth.APITokenPrefix) || len(tok) != len(auth.APITokenPrefix)+40 {
		t.Fatalf("token format: %q", tok)
	}
	got, err := f.db.APITokenByHash(ctx, auth.HashToken(tok))
	if err != nil || got.ID != row.ID {
		t.Fatalf("lookup by hash: %v", err)
	}
	if got.TokenHash == tok || got.TokenHash != auth.HashToken(tok) || got.Prefix != tok[:8] {
		t.Errorf("stored row: %+v", got)
	}
	// The plaintext appears nowhere in a copy of the database file.
	f.call("GET", "/api/v1/me", tok, "")
	backup := filepath.Join(t.TempDir(), "copy.db")
	must(t, f.db.Backup(ctx, backup))
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), tok) || strings.Contains(string(data), tok[len(auth.APITokenPrefix):]) {
		t.Error("the plaintext token is in the database")
	}
	if !strings.Contains(string(data), auth.HashToken(tok)) {
		t.Error("the token hash is not in the database")
	}
	// Use is recorded.
	got, _ = f.db.APITokenByHash(ctx, auth.HashToken(tok))
	if got.LastUsedAt == nil {
		t.Error("last use not recorded")
	}
}

func TestRateLimitPerToken(t *testing.T) {
	f := newFixtureCfg(t, func(c *config.Config) { c.API.RateLimit, c.API.RateBurst = 0.001, 3 })
	a, b := f.token(f.admin, "read:assets"), f.token(f.admin, "read:assets")
	for i := 0; i < 3; i++ {
		expect(t, f.call("GET", "/api/v1/locations", a, ""), 200, "")
	}
	rec := f.call("GET", "/api/v1/locations", a, "")
	expect(t, rec, 429, "rate_limited")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	// Another token has its own bucket.
	expect(t, f.call("GET", "/api/v1/locations", b, ""), 200, "")
}

func TestSessionAccess(t *testing.T) {
	f := newFixture(t)
	cookie, csrf := f.session(f.editor, false)
	hdr := func(h map[string]string) req {
		return req{method: "POST", path: "/api/v1/locations", cookie: cookie, body: `{"name":"From browser"}`, headers: h}
	}
	// Reading works with the cookie alone and follows the role.
	expect(t, f.do(req{method: "GET", path: "/api/v1/assets", cookie: cookie}), 200, "")
	me := expect(t, f.do(req{method: "GET", path: "/api/v1/me", cookie: cookie}), 200, "")
	if me["auth"] != "session" || me["token"] != nil || fmt.Sprint(me["effective_scopes"]) != "[read:assets write:assets read:inspections]" {
		t.Errorf("me: %v", me)
	}
	// Unsafe methods need the CSRF header and a same-origin request.
	expect(t, f.do(hdr(nil)), 403, "csrf_failed")
	expect(t, f.do(hdr(map[string]string{"X-CSRF-Token": "wrong"})), 403, "csrf_failed")
	expect(t, f.do(hdr(map[string]string{"X-CSRF-Token": csrf, "Origin": "https://evil.example"})), 403, "csrf_failed")
	expect(t, f.do(hdr(map[string]string{"X-CSRF-Token": csrf, "Sec-Fetch-Site": "cross-site"})), 403, "csrf_failed")
	expect(t, f.do(hdr(map[string]string{"X-CSRF-Token": csrf, "Origin": testOrigin})), 201, "")
	// Role based like the UI: an editor may not delete assets.
	r := req{method: "DELETE", path: fmt.Sprintf("/api/v1/assets/%d", f.mug1.ID), cookie: cookie, headers: map[string]string{"X-CSRF-Token": csrf}}
	expect(t, f.do(r), 403, "forbidden")
	vc, vcsrf := f.session(f.viewer, false)
	expect(t, f.do(req{method: "POST", path: "/api/v1/locations", cookie: vc, body: `{"name":"V"}`, headers: map[string]string{"X-CSRF-Token": vcsrf}}), 403, "forbidden")
	// Inspections need can_inspect for a session as well.
	pr := req{method: "POST", path: fmt.Sprintf("/api/v1/assets/%d/inspections", f.drum1.ID), cookie: cookie, body: `{"result":"pass"}`,
		headers: map[string]string{"X-CSRF-Token": csrf}}
	expect(t, f.do(pr), 403, "forbidden")

	// A bearer header wins over the cookie and needs no CSRF token.
	expect(t, f.do(req{method: "POST", path: "/api/v1/locations", cookie: cookie, token: f.full, body: `{"name":"Bearer"}`}), 201, "")

	// Pending MFA sessions (password only) are rejected.
	pc, pcsrf := f.session(f.admin, true)
	expect(t, f.do(req{method: "GET", path: "/api/v1/assets", cookie: pc}), 401, "unauthorized")
	expect(t, f.do(req{method: "POST", path: "/api/v1/locations", cookie: pc, body: `{"name":"P"}`, headers: map[string]string{"X-CSRF-Token": pcsrf}}), 401, "unauthorized")
	// Unknown cookie.
	expect(t, f.do(req{method: "GET", path: "/api/v1/assets", cookie: "deadbeef"}), 401, "unauthorized")
}

func TestSessionRequiresSecondFactorByPolicy(t *testing.T) {
	f := newFixture(t)
	cookie, _ := f.session(f.admin, false)
	expect(t, f.do(req{method: "GET", path: "/api/v1/assets", cookie: cookie}), 200, "")
	must(t, f.db.SetSetting(context.Background(), store.SettingMFAPolicy, store.MFAPolicyAll))
	expect(t, f.do(req{method: "GET", path: "/api/v1/assets", cookie: cookie}), 403, "mfa_required")
	// The policy is about interactive sessions; tokens are unaffected.
	expect(t, f.call("GET", "/api/v1/assets", f.full, ""), 200, "")
}

func TestValidationFailedShape(t *testing.T) {
	f := newFixture(t)
	rec := f.call("POST", "/api/v1/assets", f.full, fmt.Sprintf(`{"category_id":%d,"name":"","location_id":4711,"purchase_date":"31.12.2025","status":"broken","dguv_interval_months":500}`, f.mug.ID))
	m := expect(t, rec, 422, "validation_failed")
	errs, _ := m["errors"].(map[string]any)
	want := map[string]string{"name": "required", "location_id": "invalid_location", "purchase_date": "invalid_date",
		"status": "invalid_status", "dguv_interval_months": "invalid_interval"}
	for k, v := range want {
		if errs[k] != v {
			t.Errorf("errors[%s] = %v, want %s (all: %v)", k, errs[k], v, errs)
		}
	}
	if m["status"] != float64(422) || m["type"] != "urn:lanventory:problem:validation_failed" || m["title"] == "" {
		t.Errorf("problem fields: %v", m)
	}
	// Custom fields are reported as fields.<key>.
	rec = f.call("POST", "/api/v1/assets", f.full, fmt.Sprintf(`{"category_id":%d,"name":"x","fields":{"length":"abc","color":"green","label":"","bought":"2026-13-01"}}`, f.drum.ID))
	errs, _ = expect(t, rec, 422, "validation_failed")["errors"].(map[string]any)
	for k, v := range map[string]string{"fields.length": "invalid_number", "fields.color": "invalid_option", "fields.label": "required", "fields.bought": "invalid_date"} {
		if errs[k] != v {
			t.Errorf("errors[%s] = %v, want %s (all: %v)", k, errs[k], v, errs)
		}
	}
	// Wrong JSON types.
	rec = f.call("POST", "/api/v1/assets", f.full, fmt.Sprintf(`{"category_id":%d,"name":5,"fields":{"sealed":"yes","length":true}}`, f.drum.ID))
	errs, _ = expect(t, rec, 422, "validation_failed")["errors"].(map[string]any)
	if errs["name"] != "invalid_type" || errs["fields.sealed"] != "invalid_type" || errs["fields.length"] != "invalid_type" {
		t.Errorf("type errors: %v", errs)
	}
	// Not an object, wrong content type, too large.
	expect(t, f.call("POST", "/api/v1/locations", f.full, `[1]`), 400, "invalid_json")
	expect(t, f.call("POST", "/api/v1/locations", f.full, `{"name":"a"} {"name":"b"}`), 400, "invalid_json")
	expect(t, f.do(req{method: "POST", path: "/api/v1/locations", token: f.full, body: `name=x`, headers: map[string]string{"Content-Type": "text/plain"}}), 415, "unsupported_media_type")
	expect(t, f.call("POST", "/api/v1/locations", f.full, `{"name":"`+strings.Repeat("a", maxBody)+`"}`), 413, "payload_too_large")
}

func TestPagingAndFilters(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		f.asset(f.mug, fmt.Sprintf("Mug %d", i), nil)
	}
	page := func(q string) map[string]any {
		return expect(t, f.call("GET", "/api/v1/assets"+q, f.full, ""), 200, "")
	}
	m := page("?per_page=3")
	if m["total"] != float64(8) || m["page"] != float64(1) || m["per_page"] != float64(3) || len(m["items"].([]any)) != 3 {
		t.Errorf("page 1: %v", m)
	}
	if n := len(page("?per_page=3&page=3")["items"].([]any)); n != 2 {
		t.Errorf("last page has %d items", n)
	}
	// A page past the end is empty (and does not repeat the last page).
	m = page("?per_page=3&page=4")
	if len(m["items"].([]any)) != 0 || m["page"] != float64(4) || m["total"] != float64(8) {
		t.Errorf("page past the end: %v", m)
	}
	expect(t, f.call("GET", "/api/v1/assets?per_page=201", f.full, ""), 400, "invalid_parameter")
	expect(t, f.call("GET", "/api/v1/assets?page=0", f.full, ""), 400, "invalid_parameter")
	if n := page("?q=drum+25")["total"]; n != float64(1) {
		t.Errorf("search: %v", n)
	}
	if n := page(fmt.Sprintf("?category=%d&f_color=red", f.drum.ID))["total"]; n != float64(1) {
		t.Errorf("field filter: %v", n)
	}
	if n := page(fmt.Sprintf("?category=%d&f_sealed=true", f.drum.ID))["total"]; n != float64(1) {
		t.Errorf("bool field filter: %v", n)
	}
	items := page("?sort=name&dir=desc&per_page=2")["items"].([]any)
	if items[0].(map[string]any)["name"] != "Mug 4" {
		t.Errorf("sort: %v", items[0])
	}
}

func TestAutoNumbersAndByNumber(t *testing.T) {
	f := newFixture(t)
	mk := func(body string) map[string]any {
		rec := f.call("POST", "/api/v1/assets", f.full, body)
		m := expect(t, rec, 201, "")
		if loc := rec.Header().Get("Location"); loc != fmt.Sprintf("/api/v1/assets/%v", m["id"]) {
			t.Errorf("Location header %q", loc)
		}
		return m
	}
	cat := idStr(f.mug.ID)
	a1 := mk(`{"category_id":` + cat + `,"name":"One"}`)
	a2 := mk(`{"category_id":` + cat + `,"name":"Two"}`)
	if a1["inventory_no"] != "XM-00002" || a2["inventory_no"] != "XM-00003" { // XM-00001 is the fixture's mug
		t.Errorf("generated numbers: %v %v", a1["inventory_no"], a2["inventory_no"])
	}
	man := mk(`{"category_id":` + cat + `,"name":"Manual","inventory_no":"OLD/17 b"}`)
	if man["inventory_no"] != "OLD/17 b" {
		t.Errorf("manual number: %v", man["inventory_no"])
	}
	// Lookup by number, case-insensitive; special characters percent-encoded.
	for _, p := range []string{"XM-00002", "xm-00002"} {
		m := expect(t, f.call("GET", "/api/v1/assets/by-number/"+p, f.full, ""), 200, "")
		if m["id"] != a1["id"] {
			t.Errorf("by-number %s: %v", p, m["id"])
		}
	}
	m := expect(t, f.call("GET", "/api/v1/assets/by-number/OLD%2F17%20b", f.full, ""), 200, "")
	if m["id"] != man["id"] {
		t.Errorf("by-number with special characters: %v", m["id"])
	}
	expect(t, f.call("GET", "/api/v1/assets/by-number/XM-99999", f.full, ""), 404, "not_found")
	// Taken numbers are refused (case-insensitive), the sequence goes on.
	expect(t, f.call("POST", "/api/v1/assets", f.full, `{"category_id":`+cat+`,"name":"Dup","inventory_no":"old/17 B"}`), 422, "validation_failed")
	if a3 := mk(`{"category_id":` + cat + `,"name":"Three"}`); a3["inventory_no"] != "XM-00004" {
		t.Errorf("number after a refused create: %v", a3["inventory_no"])
	}
}

func TestPatchIsPartial(t *testing.T) {
	f := newFixture(t)
	id := fmt.Sprintf("/api/v1/assets/%d", f.drum1.ID)
	before := expect(t, f.call("GET", id, f.full, ""), 200, "")
	m := expect(t, f.call("PATCH", id, f.full, `{"note":"scratched","fields":{"length":30}}`), 200, "")
	if m["note"] != "scratched" || m["name"] != before["name"] || m["location_id"] != before["location_id"] || m["inventory_no"] != before["inventory_no"] {
		t.Errorf("patched asset: %v", m)
	}
	fields := m["fields"].(map[string]any)
	if fields["length"] != float64(30) || fields["color"] != "red" || fields["sealed"] != true || fields["label"] != "A" {
		t.Errorf("fields after partial update: %v", fields)
	}
	// null clears optional values; the same category and number may be sent back.
	m = expect(t, f.call("PATCH", id, f.full, fmt.Sprintf(`{"location_id":null,"fields":{"color":null,"sealed":null},"category_id":%d,"inventory_no":%q}`,
		f.drum.ID, strings.ToLower(f.drum1.InventoryNo))), 200, "")
	if m["location_id"] != nil || m["location_name"] != nil {
		t.Errorf("location not cleared: %v", m)
	}
	if _, ok := m["fields"].(map[string]any)["color"]; ok {
		t.Errorf("color not cleared: %v", m["fields"])
	}
	// The inventory number is immutable; an unknown category is refused.
	errs := expect(t, f.call("PATCH", id, f.full, `{"inventory_no":"NEW-1"}`), 422, "validation_failed")["errors"].(map[string]any)
	if errs["inventory_no"] != "immutable" {
		t.Errorf("immutable errors: %v", errs)
	}
	errs = expect(t, f.call("PATCH", id, f.full, `{"category_id":99999}`), 422, "validation_failed")["errors"].(map[string]any)
	if errs["category_id"] != "invalid_category" {
		t.Errorf("unknown category: %v", errs)
	}
	// Required custom fields stay required.
	expect(t, f.call("PATCH", id, f.full, `{"fields":{"label":null}}`), 422, "validation_failed")
	// Unknown properties are refused, not ignored.
	errs = expect(t, f.call("PATCH", id, f.full, `{"colour":"x"}`), 422, "validation_failed")["errors"].(map[string]any)
	if errs["colour"] != "unknown_field" {
		t.Errorf("unknown field: %v", errs)
	}
	// An update without changes writes no audit entry.
	before2 := f.lastAudit("asset.update")
	expect(t, f.call("PATCH", id, f.full, `{}`), 200, "")
	if after := f.lastAudit("asset.update"); (before2 == nil) != (after == nil) || (after != nil && after.ID != before2.ID) {
		t.Error("an empty update was audited")
	}
}

func TestLocationsViaAPI(t *testing.T) {
	f := newFixture(t)
	loc := expect(t, f.call("POST", "/api/v1/locations", f.full, `{"name":"Garage"}`), 201, "")
	if loc["asset_count"] != float64(0) || loc["description"] != "" {
		t.Errorf("new location: %v", loc)
	}
	path := fmt.Sprintf("/api/v1/locations/%v", loc["id"])
	m := expect(t, f.call("PATCH", path, f.full, `{"name":"Garage 2"}`), 200, "")
	if m["name"] != "Garage 2" {
		t.Errorf("rename: %v", m)
	}
	// In use: 409; unused: 204.
	expect(t, f.call("DELETE", fmt.Sprintf("/api/v1/locations/%d", f.loc.ID), f.full, ""), 409, "conflict")
	expect(t, f.call("DELETE", path, f.full, ""), 204, "")
	expect(t, f.call("GET", path, f.full, ""), 404, "not_found")
	got := expect(t, f.call("GET", fmt.Sprintf("/api/v1/locations/%d", f.loc.ID), f.full, ""), 200, "")
	if got["asset_count"] != float64(3) {
		t.Errorf("asset count: %v", got["asset_count"])
	}
}

func TestCategoriesReadOnly(t *testing.T) {
	f := newFixture(t)
	for _, m := range []string{"POST", "PATCH", "DELETE", "PUT"} {
		p := "/api/v1/categories"
		if m != "POST" {
			p += "/" + idStr(f.drum.ID)
		}
		expect(t, f.call(m, p, f.full, `{"name":"x"}`), 405, "method_not_allowed")
	}
	c := expect(t, f.call("GET", "/api/v1/categories/"+idStr(f.drum.ID), f.full, ""), 200, "")
	if c["prefix"] != "XD" || c["dguv_required"] != true || c["dguv_interval_months"] != float64(12) {
		t.Errorf("category: %v", c)
	}
	fields := c["fields"].([]any)
	if len(fields) != 5 {
		t.Fatalf("fields: %v", fields)
	}
	first := fields[0].(map[string]any)
	if first["key"] != "length" || first["type"] != "number" || first["unit"] != "m" || first["required"] != false {
		t.Errorf("first field: %v", first)
	}
	if opts := fields[1].(map[string]any)["options"]; fmt.Sprint(opts) != "[red blue]" {
		t.Errorf("select options: %v", opts)
	}
}

func TestInspectionRulesViaAPI(t *testing.T) {
	f := newFixture(t)
	insp := f.token(f.inspector, "read:assets", "read:inspections", "write:inspections")
	post := func(assetID int64, body string) *struct {
		code int
		m    map[string]any
	} {
		rec := f.call("POST", fmt.Sprintf("/api/v1/assets/%d/inspections", assetID), insp, body)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return &struct {
			code int
			m    map[string]any
		}{rec.Code, m}
	}
	status := func(id int64) string {
		a, err := f.db.AssetByID(context.Background(), id)
		must(t, err)
		return a.Status
	}

	// Defaults: today, the token owner as inspector; a failure alone keeps the status.
	if r := post(f.drum2.ID, `{"result":"fail"}`); r.code != 201 || status(f.drum2.ID) != store.StatusActive {
		t.Fatalf("fail without set_defective changed the status: %d %v", r.code, r.m)
	}
	r := post(f.drum2.ID, `{"result":"fail","visual_ok":true,"set_defective":true}`)
	if r.code != 201 || r.m["inspector_name"] != "ines" || r.m["inspected_on"] != today().Format(store.DateLayout) ||
		r.m["recorded_by"] != float64(f.inspector.ID) || r.m["interval_months"] != float64(12) {
		t.Fatalf("defaults: %d %v", r.code, r.m)
	}
	if st := r.m["asset_status"].(map[string]any); st["previous"] != "active" || st["current"] != "defective" {
		t.Errorf("asset_status: %v", st)
	}
	if status(f.drum2.ID) != store.StatusDefective {
		t.Error("failed inspection did not mark the asset defective")
	}
	// The status change is in the asset history like in the UI.
	if e := f.lastAudit("asset.update"); e == nil || e.ObjectID != idStr(f.drum2.ID) || e.Details["via"] != "api" {
		t.Errorf("status change audit: %+v", e)
	}
	// A pass without set_active leaves it defective; with set_active it is active again.
	post(f.drum2.ID, `{"result":"pass"}`)
	if status(f.drum2.ID) != store.StatusDefective {
		t.Error("pass without set_active changed the status")
	}
	if r := post(f.drum2.ID, `{"result":"pass","set_active":true}`); r.m["asset_status"].(map[string]any)["current"] != "active" {
		t.Errorf("set_active: %v", r.m)
	}
	// set_defective:false keeps the asset active.
	post(f.drum2.ID, `{"result":"fail","set_defective":false}`)
	if status(f.drum2.ID) != store.StatusActive {
		t.Error("set_defective:false still changed the status")
	}

	// The interval comes from the asset override, the due date is clamped to the month end.
	expect(t, f.call("PATCH", fmt.Sprintf("/api/v1/assets/%d", f.drum1.ID), f.full, `{"dguv_interval_months":1}`), 200, "")
	r = post(f.drum1.ID, `{"result":"pass","inspected_on":"2026-01-31"}`)
	if r.code != 201 || r.m["next_due"] != "2026-02-28" || r.m["interval_months"] != float64(1) {
		t.Errorf("next due: %d %v", r.code, r.m)
	}
	a := expect(t, f.call("GET", fmt.Sprintf("/api/v1/assets/%d", f.drum1.ID), f.full, ""), 200, "")
	// The latest inspection by date wins (the fixture's one is from 2025-12).
	if d := a["dguv"].(map[string]any); d["last_inspected_on"] != "2026-01-31" || d["next_due"] != "2026-02-28" || d["state"] != "overdue" && d["state"] != "soon" && d["state"] != "ok" {
		t.Errorf("dguv state: %v", d)
	}

	// Guidance hints, never a derived result.
	r = post(f.drum1.ID, `{"result":"pass","protection_class":"I","pe_resistance_ohm":0.9,"insulation_mohm":0.5,"leakage_ma":5}`)
	var keys []string
	for _, g := range r.m["guidance"].([]any) {
		keys = append(keys, g.(map[string]any)["key"].(string))
	}
	if r.code != 201 || r.m["result"] != "pass" || fmt.Sprint(keys) != "[pe_high insulation_low leakage_high]" {
		t.Errorf("guidance: %d %v", r.code, r.m)
	}

	// Qualifiers, touch current, method, reason and test device.
	r = post(f.drum1.ID, `{"result":"pass","protection_class":"II","insulation_mohm":">299","leakage_ma":"< 0,01","pe_resistance_ohm":0.1,`+
		`"touch_ma":0.7,"leakage_method":"differential","reason":"repair","test_device":"T","test_device_id":"SN-1","test_device_calibrated_until":"2027-02-01"}`)
	keys = nil
	for _, g := range r.m["guidance"].([]any) {
		keys = append(keys, g.(map[string]any)["key"].(string))
	}
	if r.code != 201 || r.m["insulation_mohm"] != float64(299) || r.m["insulation_cmp"] != ">" || r.m["leakage_ma"] != 0.01 || r.m["leakage_cmp"] != "<" ||
		r.m["pe_resistance_cmp"] != "" || r.m["touch_ma"] != 0.7 || r.m["touch_cmp"] != "" || r.m["leakage_method"] != "differential" ||
		r.m["reason"] != "repair" || r.m["test_device_id"] != "SN-1" || r.m["test_device_calibrated_until"] != "2027-02-01" ||
		fmt.Sprint(keys) != "[touch_high]" {
		t.Errorf("new fields: %d %v", r.code, r.m)
	}
	got := expect(t, f.call("GET", fmt.Sprintf("/api/v1/inspections/%v", r.m["id"]), f.full, ""), 200, "")
	if got["insulation_cmp"] != ">" || got["touch_ma"] != 0.7 || got["test_device_calibrated_until"] != "2027-02-01" {
		t.Errorf("get: %v", got)
	}
	// Explicit qualifiers with numbers; a contradiction or a lone qualifier is refused.
	if r := post(f.drum1.ID, `{"result":"pass","insulation_mohm":299,"insulation_cmp":">"}`); r.code != 201 || r.m["insulation_cmp"] != ">" {
		t.Errorf("explicit cmp: %d %v", r.code, r.m)
	}
	r = post(f.drum1.ID, `{"result":"pass","insulation_mohm":">299","insulation_cmp":"<","touch_cmp":">","reason":"x","leakage_method":"y","test_device_calibrated_until":"nope"}`)
	errs, _ := r.m["errors"].(map[string]any)
	if r.code != 422 || errs["insulation_cmp"] != "invalid_cmp" || errs["touch_cmp"] != "invalid_cmp" || errs["reason"] != "invalid_reason" ||
		errs["leakage_method"] != "invalid_method" || errs["test_device_calibrated_until"] != "invalid_date" {
		t.Errorf("invalid new fields: %d %v", r.code, r.m)
	}

	// Refusals: not inspectable, inactive, invalid values.
	if r := post(f.mug1.ID, `{"result":"pass"}`); r.code != 422 || r.m["code"] != "not_inspectable" {
		t.Errorf("category without DGUV: %d %v", r.code, r.m)
	}
	must(t, f.db.UpdateUser(context.Background(), f.admin.ID, store.RoleAdmin, true, false))
	expect(t, f.call("PATCH", fmt.Sprintf("/api/v1/assets/%d", f.drum2.ID), f.full, `{"status":"retired"}`), 200, "")
	if r := post(f.drum2.ID, `{"result":"pass"}`); r.code != 422 || r.m["code"] != "asset_inactive" {
		t.Errorf("retired asset: %d %v", r.code, r.m)
	}
	r = post(f.drum1.ID, `{"result":"pass","inspected_on":"2100-01-01","pe_resistance_ohm":-1,"leakage_ma":5000,"protection_class":"IV"}`)
	errs, _ = r.m["errors"].(map[string]any)
	if r.code != 422 || errs["inspected_on"] != "future_date" || errs["pe_resistance_ohm"] != "out_of_range" || errs["leakage_ma"] != "out_of_range" || errs["protection_class"] != "invalid_class" {
		t.Errorf("invalid values: %d %v", r.code, r.m)
	}
	r = post(f.drum1.ID, `{"inspector_name":"  ","result":""}`)
	errs, _ = r.m["errors"].(map[string]any)
	if r.code != 422 || errs["inspector_name"] != "required" || errs["result"] != "required" {
		t.Errorf("required values: %d %v", r.code, r.m)
	}

	// Inspections cannot be edited; an admin deletes one and the state falls back.
	expect(t, f.call("PATCH", fmt.Sprintf("/api/v1/inspections/%d", f.insp.ID), f.full, `{}`), 405, "method_not_allowed")
	expect(t, f.call("DELETE", fmt.Sprintf("/api/v1/inspections/%d", f.insp.ID), f.full, ""), 204, "")
	if e := f.lastAudit("inspection.delete"); e == nil || e.Details["via"] != "api" {
		t.Errorf("delete audit: %+v", e)
	}
	// Listing and filters.
	list := expect(t, f.call("GET", fmt.Sprintf("/api/v1/assets/%d/inspections?result=fail", f.drum2.ID), insp, ""), 200, "")
	if list["total"] != float64(3) {
		t.Errorf("fail inspections of the asset: %v", list["total"])
	}
	all := expect(t, f.call("GET", "/api/v1/inspections", insp, ""), 200, "")
	if first := all["items"].([]any)[0].(map[string]any); first["inspected_on"].(string) < "2026-01-31" {
		t.Errorf("newest first: %v", first["inspected_on"])
	}
}

func TestDueViaAPI(t *testing.T) {
	f := newFixture(t)
	m := expect(t, f.call("GET", "/api/v1/inspections/due", f.full, ""), 200, "")
	// drum2 was never inspected; drum1 is due within 30 days or later depending on the date.
	items := m["items"].([]any)
	if len(items) == 0 || items[0].(map[string]any)["state"] != "never" || items[0].(map[string]any)["asset_id"] != float64(f.drum2.ID) {
		t.Errorf("due items: %v", items)
	}
	m = expect(t, f.call("GET", "/api/v1/inspections/due?state=overdue", f.full, ""), 200, "")
	for _, it := range m["items"].([]any) {
		if it.(map[string]any)["state"] != "overdue" {
			t.Errorf("state filter: %v", it)
		}
	}
	// Equal to the dashboard counts of the UI.
	c, err := f.db.CountDue(context.Background(), today())
	must(t, err)
	if all := expect(t, f.call("GET", "/api/v1/inspections/due", f.full, ""), 200, ""); int(all["total"].(float64)) != c.Overdue+c.Soon+c.Never {
		t.Errorf("total %v, counts %+v", all["total"], c)
	}
}

func TestAuditViaAPI(t *testing.T) {
	f := newFixture(t)
	tok, row := f.tokenRow(f.editor, nil, "read:assets", "write:assets")
	m := expect(t, f.call("POST", "/api/v1/assets", tok, fmt.Sprintf(`{"category_id":%d,"name":"Audited"}`, f.mug.ID)), 201, "")
	e := f.lastAudit("asset.create")
	if e == nil || e.UserID != f.editor.ID || e.Username != "eli" || e.ObjectType != "asset" || e.ObjectID != idStr(int64(m["id"].(float64))) {
		t.Fatalf("asset.create entry: %+v", e)
	}
	if e.Details["via"] != "api" || e.Details["token_id"] != float64(row.ID) || e.Details["ip"] == nil || e.Details["inventory_no"] == nil || e.Details["changes"] == nil {
		t.Errorf("details: %v", e.Details)
	}
	if strings.Contains(e.RawDetails, tok) || strings.Contains(e.RawDetails, auth.HashToken(tok)) {
		t.Error("the token is in the audit details")
	}
	aid := fmt.Sprintf("/api/v1/assets/%d", int64(m["id"].(float64)))
	expect(t, f.call("PATCH", aid, tok, `{"note":"n"}`), 200, "")
	if e := f.lastAudit("asset.update"); e == nil || e.Details["via"] != "api" || e.Details["token_id"] != float64(row.ID) {
		t.Errorf("asset.update entry: %+v", e)
	}
	expect(t, f.call("POST", "/api/v1/locations", tok, `{"name":"Audit loc"}`), 201, "")
	if e := f.lastAudit("location.create"); e == nil || e.Details["via"] != "api" {
		t.Errorf("location.create entry: %+v", e)
	}
	expect(t, f.call("DELETE", aid, f.full, ""), 204, "")
	if e := f.lastAudit("asset.delete"); e == nil || e.Details["via"] != "api" || e.Username != "ada" {
		t.Errorf("asset.delete entry: %+v", e)
	}
	expect(t, f.call("POST", fmt.Sprintf("/api/v1/assets/%d/inspections", f.drum2.ID), f.full, `{"result":"pass"}`), 201, "")
	if e := f.lastAudit("inspection.create"); e == nil || e.Details["via"] != "api" || e.Details["result"] != "pass" || e.Details["asset_id"] != float64(f.drum2.ID) {
		t.Errorf("inspection.create entry: %+v", e)
	}
	// A session user is audited as well, without a token ID.
	cookie, csrf := f.session(f.editor, false)
	expect(t, f.do(req{method: "POST", path: "/api/v1/locations", cookie: cookie, body: `{"name":"By session"}`, headers: map[string]string{"X-CSRF-Token": csrf}}), 201, "")
	if e := f.lastAudit("location.create"); e == nil || e.Details["via"] != "api" || e.Details["token_id"] != nil || e.UserID != f.editor.ID {
		t.Errorf("session audit: %+v", e)
	}
	// Reads are not audited.
	n := len(mustAudit(t, f))
	f.call("GET", "/api/v1/assets", tok, "")
	if len(mustAudit(t, f)) != n {
		t.Error("a read was audited")
	}
}

func mustAudit(t *testing.T, f *fixture) []store.AuditEntry {
	t.Helper()
	l, err := f.db.ListAudit(context.Background(), 1000)
	must(t, err)
	return l
}

func TestDocsAndSpecServed(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/api/", "/api"} {
		rec := f.do(req{method: "GET", path: p})
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Header().Get("Content-Type"))
		}
		body := rec.Body.String()
		for _, want := range []string{"/api/v1/assets/by-number/{inventory_no}", "Bearer lvk_", "/api/openapi.json"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: docs lack %q", p, want)
			}
		}
		if strings.Contains(body, "<script") || strings.Contains(body, "style=") || strings.Contains(body, "<style") {
			t.Errorf("%s: inline code in the docs page (CSP)", p)
		}
		if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
			t.Errorf("%s: CSP header %q", p, csp)
		}
	}
	rec := f.do(req{method: "GET", path: "/api/openapi.json"})
	var doc map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &doc) != nil || doc["openapi"] != "3.1.0" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("openapi.json: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = f.do(req{method: "GET", path: "/api/openapi.yaml"})
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "openapi: 3.1.0") {
		t.Fatalf("openapi.yaml: %d", rec.Code)
	}
}

func TestPatchMovesCategory(t *testing.T) {
	f := newFixture(t)
	id := fmt.Sprintf("/api/v1/assets/%d", f.drum1.ID)
	// Moving to a category without fields drops the values; number and inspections stay.
	m := expect(t, f.call("PATCH", id, f.full, fmt.Sprintf(`{"category_id":%d}`, f.mug.ID)), 200, "")
	if m["category_id"] != float64(f.mug.ID) || m["inventory_no"] != f.drum1.InventoryNo || len(m["fields"].(map[string]any)) != 0 {
		t.Fatalf("moved: %v", m)
	}
	if l := expect(t, f.call("GET", id+"/inspections", f.full, ""), 200, ""); l["total"] != float64(1) {
		t.Errorf("inspections after move: %v", l["total"])
	}
	if e := f.lastAudit("asset.update"); e == nil || e.Details["changes"] == nil {
		t.Errorf("move audit: %+v", e)
	}
	// Back to the drum category: its required field must be sent.
	errs := expect(t, f.call("PATCH", id, f.full, fmt.Sprintf(`{"category_id":%d}`, f.drum.ID)), 422, "validation_failed")["errors"].(map[string]any)
	if errs["fields.label"] != "required" {
		t.Errorf("required after move: %v", errs)
	}
	m = expect(t, f.call("PATCH", id, f.full, fmt.Sprintf(`{"category_id":%d,"fields":{"label":"Z","length":10}}`, f.drum.ID)), 200, "")
	if fl := m["fields"].(map[string]any); fl["label"] != "Z" || fl["length"] != float64(10) {
		t.Errorf("fields after move back: %v", fl)
	}
}

func TestBulkUpdateViaAPI(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ids := fmt.Sprintf("[%d,%d]", f.drum1.ID, f.drum2.ID)
	body := func(changes string, extra string) string {
		return `{"ids":` + ids + `,"changes":` + changes + extra + `}`
	}

	// Dry run: nothing written, nothing audited.
	m := expect(t, f.call("PATCH", "/api/v1/assets", f.full, body(`{"status":"defective","fields":{"color":"red"}}`, `,"dry_run":true`)), 200, "")
	if m["dry_run"] != true || m["updated"] != float64(2) || len(m["items"].([]any)) != 2 {
		t.Fatalf("dry run: %v", m)
	}
	if got, _ := f.db.AssetByID(ctx, f.drum1.ID); got.Status != store.StatusActive {
		t.Fatal("dry run wrote")
	}
	if f.lastAudit("asset.update") != nil {
		t.Fatal("dry run audited")
	}

	// Success: status for both, color only changes on the second (already red on the first).
	m = expect(t, f.call("PATCH", "/api/v1/assets", f.full, body(`{"status":"defective","fields":{"color":"red"}}`, "")), 200, "")
	if m["updated"] != float64(2) || m["dry_run"] != false {
		t.Fatalf("result: %v", m)
	}
	items := m["items"].([]any)
	if first := items[0].(map[string]any); first["status"] != "defective" || first["fields"].(map[string]any)["color"] != "red" {
		t.Fatalf("item: %v", first)
	}
	got, _ := f.db.AssetByID(ctx, f.drum2.ID)
	if got.Status != store.StatusDefective || got.Values[f.fields["color"].ID] != "red" {
		t.Fatalf("drum2: %+v", got)
	}
	es, _ := f.db.AuditByObject(ctx, "asset", idStr(f.drum1.ID), 5)
	if len(es) < 1 || es[0].Details["via"] != "api_bulk" || es[0].Details["token_id"] == nil {
		t.Fatalf("audit: %+v", es)
	}

	// Note append and clearing the location.
	expect(t, f.call("PATCH", "/api/v1/assets", f.full, body(`{"note":"one","location_id":null}`, "")), 200, "")
	expect(t, f.call("PATCH", "/api/v1/assets", f.full, body(`{"note":"two","note_append":true}`, "")), 200, "")
	if got, _ := f.db.AssetByID(ctx, f.drum1.ID); got.Note != "one\ntwo" || got.LocationID != 0 {
		t.Fatalf("note/location: %+v", got)
	}

	// Per-asset errors keyed by asset ID and API field name; nothing is written.
	mugs := fmt.Sprintf(`{"ids":[%d,%d],"changes":{"status":"lost","fields":{"length":3}}}`, f.drum1.ID, f.mug1.ID)
	errs := expect(t, f.call("PATCH", "/api/v1/assets", f.full, mugs), 422, "validation_failed")["errors"].(map[string]any)
	if errs[idStr(f.mug1.ID)+".fields.length"] != "unknown_field" || len(errs) != 1 {
		t.Fatalf("errors: %v", errs)
	}
	if got, _ := f.db.AssetByID(ctx, f.drum1.ID); got.Status == store.StatusLost {
		t.Fatal("written despite errors")
	}
	// Required field of the new category (mug -> drum lacks "label").
	move := fmt.Sprintf(`{"ids":[%d],"changes":{"category_id":%d}}`, f.mug1.ID, f.drum.ID)
	errs = expect(t, f.call("PATCH", "/api/v1/assets", f.full, move), 422, "validation_failed")["errors"].(map[string]any)
	if errs[idStr(f.mug1.ID)+".fields.label"] != "required" {
		t.Fatalf("required: %v", errs)
	}
	// ...fixed in the same request.
	move = fmt.Sprintf(`{"ids":[%d],"changes":{"category_id":%d,"fields":{"label":"M"}}}`, f.mug1.ID, f.drum.ID)
	m = expect(t, f.call("PATCH", "/api/v1/assets", f.full, move), 200, "")
	if it := m["items"].([]any)[0].(map[string]any); it["category_id"] != float64(f.drum.ID) {
		t.Fatalf("moved: %v", it)
	}

	// Request-level problems.
	for _, c := range []struct{ body, key, code string }{
		{`{"ids":[1],"changes":{"name":"x"}}`, "changes.name", "not_allowed"},
		{`{"ids":[1],"changes":{"serial_no":"x"}}`, "changes.serial_no", "not_allowed"},
		{`{"ids":[1],"changes":{"inventory_no":"x"}}`, "changes.inventory_no", "not_allowed"},
		{`{"ids":[1],"changes":{"colour":"x"}}`, "changes.colour", "unknown_field"},
		{`{"ids":[1],"changes":{"fields":{"nope":1}}}`, "changes.fields.nope", "unknown_field"},
		{`{"ids":[1],"changes":{}}`, "changes", "required"},
		{`{"changes":{"note":"x"}}`, "ids", "required"},
		{`{"ids":[],"changes":{"note":"x"}}`, "ids", "required"},
		{`{"ids":[999999],"changes":{"note":"x"}}`, "ids", "unknown_id"},
		{`{"ids":[1],"changes":{"note":"x"},"extra":1}`, "extra", "unknown_field"},
	} {
		errs := expect(t, f.call("PATCH", "/api/v1/assets", f.full, c.body), 422, "validation_failed")["errors"].(map[string]any)
		if errs[c.key] != c.code {
			t.Errorf("%s: errors %v, want %s=%s", c.body, errs, c.key, c.code)
		}
	}
	// More than 1000 ids.
	many := make([]string, 1001)
	for i := range many {
		many[i] = strconv.Itoa(i + 1)
	}
	errs = expect(t, f.call("PATCH", "/api/v1/assets", f.full, `{"ids":[`+strings.Join(many, ",")+`],"changes":{"note":"x"}}`), 422, "validation_failed")["errors"].(map[string]any)
	if errs["ids"] != "too_many" {
		t.Errorf("too many: %v", errs)
	}
}

func TestBulkUpdateScopeAndRole(t *testing.T) {
	f := newFixture(t)
	body := fmt.Sprintf(`{"ids":[%d],"changes":{"status":"lost"}}`, f.mug1.ID)
	expect(t, f.call("PATCH", "/api/v1/assets", f.token(f.admin, "read:assets"), body), 403, "insufficient_scope")
	expect(t, f.call("PATCH", "/api/v1/assets", f.token(f.viewer, "read:assets", "write:assets"), body), 403, "forbidden")
	expect(t, f.call("PATCH", "/api/v1/assets", "", body), 401, "")
	if got, _ := f.db.AssetByID(context.Background(), f.mug1.ID); got.Status != store.StatusActive {
		t.Fatal("refused call changed an asset")
	}
	expect(t, f.call("PATCH", "/api/v1/assets", f.token(f.editor, "write:assets"), body), 200, "")
}
