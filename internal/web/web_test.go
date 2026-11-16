package web

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/config"
	"github.com/lan-dot-party/lanventory/internal/secret"
	"github.com/lan-dot-party/lanventory/internal/store"
)

// Every key used in a template exists, with German and English text.
func TestTemplateKeysExist(t *testing.T) {
	re := regexp.MustCompile(`\.T "([a-z0-9_.-]+)"`)
	err := fs.WalkDir(templateFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			if !hasKey(m[1]) {
				t.Errorf("%s: unknown translation key %q", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range messages {
		if v[0] == "" || v[1] == "" {
			t.Errorf("key %q lacks a translation", k)
		}
	}
	// Keys built with print in templates.
	for _, prefix := range []string{"role.", "theme."} {
		found := false
		for k := range messages {
			if strings.HasPrefix(k, prefix) {
				found = true
			}
		}
		if !found {
			t.Errorf("no keys with prefix %q", prefix)
		}
	}
	for _, r := range store.Roles {
		if !hasKey("role." + r) {
			t.Errorf("no translation for role %s", r)
		}
	}
	for _, st := range store.Statuses {
		if !hasKey("status." + st) {
			t.Errorf("no translation for status %s", st)
		}
	}
	for _, ft := range store.FieldTypes {
		if !hasKey("ftype." + ft) {
			t.Errorf("no translation for field type %s", ft)
		}
	}
	for _, st := range []string{"ok", "soon", "overdue", "never", "not_required"} {
		if !hasKey("dstate." + st) {
			t.Errorf("no translation for due state %s", st)
		}
	}
	for _, k := range []string{store.FlagPEHigh, store.FlagInsulationLow, store.FlagLeakageHigh, store.FlagTouchHigh} {
		if !hasKey("guide." + k) {
			t.Errorf("no translation for guidance flag %s", k)
		}
	}
	for _, k := range []string{store.ResultPass, store.ResultFail} {
		if !hasKey("result." + k) {
			t.Errorf("no translation for result %s", k)
		}
	}
	for _, k := range store.AssetSorts {
		if !hasKey("acol." + k) {
			t.Errorf("no column title for sort %s", k)
		}
	}
}

// No template carries inline scripts, styles or event handlers (CSP).
func TestTemplatesHaveNoInlineCode(t *testing.T) {
	bad := regexp.MustCompile(`(?i)<script[^>]*>[^<]|<style|\sstyle=|\son[a-z]+=`)
	_ = fs.WalkDir(templateFS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := fs.ReadFile(templateFS, path)
		if m := bad.FindString(string(data)); m != "" {
			t.Errorf("%s: inline code %q", path, m)
		}
		return nil
	})
}

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	db     *store.DB
	ui     *Server
	client *http.Client
	csrf   string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Migrate(context.Background(), log); err != nil {
		t.Fatal(err)
	}
	key, err := secret.LoadOrCreate(filepath.Join(dir, "k"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, db: db}
	h.srv = httptest.NewUnstartedServer(nil)
	cfg := config.Default()
	cfg.PublicURL = "http://" + h.srv.Listener.Addr().String()
	a := app.New(cfg, db, key, log)
	ui, err := New(a)
	if err != nil {
		t.Fatal(err)
	}
	h.ui = ui
	h.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			ui.Health(w, r)
			return
		}
		a.SecurityHeaders(a.SessionMiddleware(ui.Routes())).ServeHTTP(w, r)
	})
	h.srv.Start()
	t.Cleanup(h.srv.Close)
	h.client = newClient()
	return h
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// other returns a second client (separate cookies) against the same server.
func (h *harness) other() *harness {
	return &harness{t: h.t, srv: h.srv, db: h.db, ui: h.ui, client: newClient()}
}

func (h *harness) user(name, role string) {
	h.t.Helper()
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.db.CreateUser(context.Background(), &store.User{Username: name, PasswordHash: hash, Role: role}); err != nil {
		h.t.Fatal(err)
	}
}

var reLoginCSRF = regexp.MustCompile(`name="_login_csrf" value="([0-9a-f]+)"`)
var reCSRF = regexp.MustCompile(`data-csrf="([0-9a-f]+)"`)

func (h *harness) get(path string) (*http.Response, string) {
	h.t.Helper()
	resp, err := h.client.Get(h.srv.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(body)
}

func (h *harness) post(path string, form url.Values, hdr map[string]string) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// postOK posts with the CSRF token and a same-origin header.
func (h *harness) postOK(path string, form url.Values) *http.Response {
	form.Set("_csrf", h.csrf)
	return h.post(path, form, map[string]string{"Origin": h.srv.URL})
}

func (h *harness) tryLogin(name, password string) *http.Response {
	h.t.Helper()
	_, body := h.get("/login")
	m := reLoginCSRF.FindStringSubmatch(body)
	if m == nil {
		h.t.Fatal("no login CSRF token")
	}
	return h.post("/login", url.Values{"username": {name}, "password": {password}, "_login_csrf": {m[1]}, "next": {"/"}},
		map[string]string{"Origin": h.srv.URL})
}

func (h *harness) login(name string) {
	h.t.Helper()
	resp := h.tryLogin(name, "correct horse battery")
	if resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("login status %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == app.SessionCookie && (c.SameSite != http.SameSiteStrictMode || !c.HttpOnly) {
			h.t.Fatal("session cookie is not HttpOnly and SameSite=Strict")
		}
	}
	_, body := h.get("/")
	m := reCSRF.FindStringSubmatch(body)
	if m == nil {
		h.t.Fatal("no CSRF token on the dashboard")
	}
	h.csrf = m[1]
}

func TestHealth(t *testing.T) {
	h := newHarness(t)
	resp, body := h.get("/health")
	var out map[string]string
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &out) != nil || out["status"] != "ok" || out["version"] == "" {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}
}

func TestSecurityHeadersAndLoginPage(t *testing.T) {
	h := newHarness(t)
	resp, body := h.get("/login")
	if resp.StatusCode != 200 || !strings.Contains(body, "LANventory") {
		t.Fatalf("login page: %d", resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP too loose: %s", csp)
	}
	for k, v := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "same-origin"} {
		if resp.Header.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, resp.Header.Get(k), v)
		}
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("login page is cacheable")
	}
}

func TestLoginLogoutAndDashboard(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	resp, _ := h.get("/")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("anonymous dashboard: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Wrong password and unknown user look the same.
	for _, name := range []string{"admin", "nobody"} {
		if r := h.tryLogin(name, "wrong password!!"); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("login %s with wrong password: %d", name, r.StatusCode)
		}
	}
	h.login("admin")
	resp, body := h.get("/")
	if resp.StatusCode != 200 || !strings.Contains(body, "Assets by status") {
		t.Fatalf("dashboard: %d", resp.StatusCode)
	}
	if !strings.Contains(body, "LANventory dev") || !strings.Contains(body, "https://github.com/lan-dot-party/lanventory") {
		t.Fatal("footer missing")
	}
	// Logout needs the CSRF token and ends the session.
	if r := h.post("/logout", url.Values{}, map[string]string{"Origin": h.srv.URL}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", r.StatusCode)
	}
	if r := h.postOK("/logout", url.Values{}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if resp, _ = h.get("/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("dashboard after logout: %d", resp.StatusCode)
	}
}

func TestLoginLockout(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	var last int
	for i := 0; i < 7; i++ {
		last = h.tryLogin("admin", "wrong password!!").StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("no lockout after repeated failures: %d", last)
	}
	// Even the correct password is refused while locked.
	if r := h.tryLogin("admin", "correct horse battery"); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked login accepted: %d", r.StatusCode)
	}
}

func TestLoginRejectsForeignOriginAndBadCSRF(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	_, body := h.get("/login")
	m := reLoginCSRF.FindStringSubmatch(body)
	form := url.Values{"username": {"admin"}, "password": {"correct horse battery"}, "_login_csrf": {m[1]}}
	if r := h.post("/login", form, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", r.StatusCode)
	}
	form.Set("_login_csrf", "deadbeef")
	if r := h.post("/login", form, map[string]string{"Origin": h.srv.URL}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("bad login CSRF token: %d", r.StatusCode)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{"/admin/users": "/admin/users", "//evil.example": "/", "https://evil.example": "/",
		"/\\evil": "/", "": "/", "x": "/"} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateChangesNeedCSRFAndOrigin(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	h.login("admin")
	form := url.Values{"theme": {"dark"}}
	if r := h.post("/account/theme", form, nil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF token: %d", r.StatusCode)
	}
	form.Set("_csrf", h.csrf)
	if r := h.post("/account/theme", form, map[string]string{"Origin": "null"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("Origin null accepted: %d", r.StatusCode)
	}
	if r := h.post("/account/theme", form, map[string]string{"Origin": "https://evil.example"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin accepted: %d", r.StatusCode)
	}
	if r := h.post("/account/theme", form, map[string]string{"Sec-Fetch-Site": "cross-site"}); r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site fetch accepted: %d", r.StatusCode)
	}
	if r := h.post("/account/theme", form, map[string]string{"Origin": h.srv.URL}); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("legit request rejected: %d", r.StatusCode)
	}
	u, _ := h.db.UserByName(context.Background(), "admin")
	if u.Theme != "dark" {
		t.Fatalf("theme %q", u.Theme)
	}
}

func TestRoleEnforcement(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	for _, role := range []string{store.RoleViewer, store.RoleEditor} {
		c := h.other()
		c.user("u-"+role, role)
		c.login("u-" + role)
		for _, p := range []string{"/admin/users", "/admin/audit"} {
			if resp, _ := c.get(p); resp.StatusCode != http.StatusForbidden {
				t.Errorf("%s reached %s: %d", role, p, resp.StatusCode)
			}
		}
		if r := c.postOK("/admin/users", url.Values{"username": {"x"}, "password": {"long enough pw"}, "role": {"admin"},
			"confirm_password": {"correct horse battery"}}); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s created a user: %d", role, r.StatusCode)
		}
		if resp, _ := c.get("/"); resp.StatusCode != 200 {
			t.Errorf("%s cannot see the dashboard: %d", role, resp.StatusCode)
		}
	}
	h.login("admin")
	for _, p := range []string{"/admin/users", "/admin/audit", "/account"} {
		if resp, _ := h.get(p); resp.StatusCode != 200 {
			t.Errorf("admin %s: %d", p, resp.StatusCode)
		}
	}
}

func TestUserManagement(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	h.login("admin")
	ctx := context.Background()

	// Creating a user needs the admin's own password.
	form := url.Values{"username": {"Bob <b>"}, "password": {"another long password"}, "role": {"editor"},
		"can_inspect": {"1"}, "language": {"de"}, "confirm_password": {"wrong"}}
	if r := h.postOK("/admin/users", form); !strings.Contains(r.Header.Get("Location"), "err=wrong_password") {
		t.Fatalf("create with wrong confirmation: %s", r.Header.Get("Location"))
	}
	form.Set("confirm_password", "correct horse battery")
	if r := h.postOK("/admin/users", form); !strings.Contains(r.Header.Get("Location"), "ok=saved") {
		t.Fatalf("create: %s", r.Header.Get("Location"))
	}
	bob, err := h.db.UserByName(ctx, "Bob <b>")
	if err != nil || bob.Role != store.RoleEditor || !bob.CanInspect || bob.Lang != "de" {
		t.Fatalf("created user: %+v %v", bob, err)
	}
	_, body := h.get("/admin/users")
	if strings.Contains(body, "Bob <b>") || !strings.Contains(body, "Bob &lt;b&gt;") {
		t.Fatal("user name not escaped")
	}
	// Short passwords and invalid roles are refused.
	form.Set("username", "carol")
	form.Set("password", "short")
	if r := h.postOK("/admin/users", form); !strings.Contains(r.Header.Get("Location"), "err=password_short") {
		t.Fatalf("short password: %s", r.Header.Get("Location"))
	}
	form.Set("password", "another long password")
	form.Set("role", "root")
	if r := h.postOK("/admin/users", form); !strings.Contains(r.Header.Get("Location"), "err=bad_request") {
		t.Fatalf("invalid role: %s", r.Header.Get("Location"))
	}

	// Update: demote, remove inspection right.
	id := idStr(bob.ID)
	if r := h.postOK("/admin/users/"+id, url.Values{"role": {"viewer"}}); !strings.Contains(r.Header.Get("Location"), "ok=saved") {
		t.Fatalf("update: %s", r.Header.Get("Location"))
	}
	if bob, _ = h.db.UserByID(ctx, bob.ID); bob.Role != store.RoleViewer || bob.CanInspect {
		t.Fatalf("after update: %+v", bob)
	}
	// Reset password ends sessions and the new password works.
	if r := h.postOK("/admin/users/"+id+"/password", url.Values{"password": {"brand new password"}, "confirm_password": {"correct horse battery"}}); !strings.Contains(r.Header.Get("Location"), "ok=saved") {
		t.Fatalf("reset password: %s", r.Header.Get("Location"))
	}
	bobC := h.other()
	if r := bobC.tryLogin("Bob <b>", "brand new password"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("login with the new password: %d", r.StatusCode)
	}
	// Delete, but never yourself.
	admin, _ := h.db.UserByName(ctx, "admin")
	if r := h.postOK("/admin/users/"+idStr(admin.ID)+"/delete", url.Values{}); !strings.Contains(r.Header.Get("Location"), "err=self") {
		t.Fatalf("self delete: %s", r.Header.Get("Location"))
	}
	if r := h.postOK("/admin/users/"+id+"/delete", url.Values{}); !strings.Contains(r.Header.Get("Location"), "ok=deleted") {
		t.Fatalf("delete: %s", r.Header.Get("Location"))
	}
	// The audit log recorded it, without secrets.
	_, body = h.get("/admin/audit")
	for _, want := range []string{"user.create", "user.update", "user.delete", "login"} {
		if !strings.Contains(body, want) {
			t.Errorf("audit page lacks %q", want)
		}
	}
	if strings.Contains(body, "brand new password") || strings.Contains(body, "argon2id") {
		t.Fatal("audit page leaks secrets")
	}
}

func TestLastAdminProtection(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	h.user("second", store.RoleAdmin)
	h.login("admin")
	ctx := context.Background()
	admin, _ := h.db.UserByName(ctx, "admin")
	second, _ := h.db.UserByName(ctx, "second")

	// Two admins: the second may be demoted ...
	if r := h.postOK("/admin/users/"+idStr(second.ID), url.Values{"role": {"editor"}}); !strings.Contains(r.Header.Get("Location"), "ok=saved") {
		t.Fatalf("demote second admin: %s", r.Header.Get("Location"))
	}
	// ... but not the last one: no demotion, no disabling, no deletion.
	if r := h.postOK("/admin/users/"+idStr(admin.ID), url.Values{"role": {"viewer"}}); !strings.Contains(r.Header.Get("Location"), "err=last_admin") {
		t.Fatalf("demote last admin: %s", r.Header.Get("Location"))
	}
	if r := h.postOK("/admin/users/"+idStr(admin.ID), url.Values{"role": {"admin"}, "disabled": {"1"}}); !strings.Contains(r.Header.Get("Location"), "err=last_admin") {
		t.Fatalf("disable last admin: %s", r.Header.Get("Location"))
	}
	// Promote second again, then it can delete the first only while another admin remains.
	_ = h.postOK("/admin/users/"+idStr(second.ID), url.Values{"role": {"admin"}})
	// A disabled admin does not count: disable second, then the first is the last.
	_ = h.postOK("/admin/users/"+idStr(second.ID), url.Values{"role": {"admin"}, "disabled": {"1"}})
	if r := h.postOK("/admin/users/"+idStr(admin.ID), url.Values{"role": {"editor"}}); !strings.Contains(r.Header.Get("Location"), "err=last_admin") {
		t.Fatalf("demote last enabled admin: %s", r.Header.Get("Location"))
	}
	if n, _ := h.db.CountAdmins(ctx); n != 1 {
		t.Fatalf("admins: %d", n)
	}
}

func TestAccountPasswordAndPrefs(t *testing.T) {
	h := newHarness(t)
	h.user("eve", store.RoleViewer)
	h.login("eve")
	if r := h.postOK("/account/password", url.Values{"current": {"nope"}, "password": {"a new long password"}}); !strings.Contains(r.Header.Get("Location"), "err=wrong_password") {
		t.Fatalf("wrong current password: %s", r.Header.Get("Location"))
	}
	if r := h.postOK("/account/prefs", url.Values{"language": {"de"}, "theme": {"light"}}); !strings.Contains(r.Header.Get("Location"), "ok=saved") {
		t.Fatalf("prefs: %s", r.Header.Get("Location"))
	}
	_, body := h.get("/")
	if !strings.Contains(body, `lang="de"`) || !strings.Contains(body, `data-theme="light"`) || !strings.Contains(body, "Übersicht") {
		t.Fatal("language/theme not applied")
	}
	if r := h.postOK("/account/theme", url.Values{"theme": {"bogus"}}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid theme: %d", r.StatusCode)
	}
	if r := h.postOK("/account/password", url.Values{"current": {"correct horse battery"}, "password": {"a new long password"}}); r.Header.Get("Location") != "/login" {
		t.Fatalf("password change: %s", r.Header.Get("Location"))
	}
	if resp, _ := h.get("/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatal("session survived the password change")
	}
	if r := h.tryLogin("eve", "a new long password"); r.StatusCode != http.StatusSeeOther {
		t.Fatalf("login with changed password: %d", r.StatusCode)
	}
}

func TestDisabledUserCannotLogin(t *testing.T) {
	h := newHarness(t)
	h.user("admin", store.RoleAdmin)
	h.user("dis", store.RoleViewer)
	u, _ := h.db.UserByName(context.Background(), "dis")
	if err := h.db.UpdateUser(context.Background(), u.ID, store.RoleViewer, false, true); err != nil {
		t.Fatal(err)
	}
	if r := h.tryLogin("dis", "correct horse battery"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled user login: %d", r.StatusCode)
	}
}

func TestNotFoundAndStatic(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.get("/nope"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("404: %d", resp.StatusCode)
	}
	for _, p := range []string{"/static/app.css", "/static/app.js", "/static/icon.svg", "/static/htmx.min.js"} {
		if resp, _ := h.get(p); resp.StatusCode != 200 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
}

func TestLLMsTxtIsPublicPlainText(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	h.ui.LLMs(rec, httptest.NewRequest(http.MethodGet, "/llms.txt", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" ||
		!strings.HasPrefix(rec.Body.String(), "# LANventory") {
		t.Fatalf("llms.txt: %d %q %.40q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}
