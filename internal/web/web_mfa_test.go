package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/mfa"
	"github.com/lan-dot-party/lanventory/internal/store"
)

func (h *harness) userID(name string) int64 {
	h.t.Helper()
	u, err := h.db.UserByName(context.Background(), name)
	if err != nil {
		h.t.Fatal(err)
	}
	return u.ID
}

// totpSecret returns the decrypted (pending or active) TOTP secret.
func (h *harness) totpSecret(name string) []byte {
	h.t.Helper()
	id := h.userID(name)
	enc, _, _, err := h.db.TOTPSecret(context.Background(), id)
	if err != nil || enc == nil {
		h.t.Fatalf("no TOTP secret: %v", err)
	}
	secret, err := h.ui.Key.Decrypt(enc, store.TOTPAAD(id))
	if err != nil {
		h.t.Fatal(err)
	}
	return secret
}

// csrfFrom extracts the session CSRF token from a page.
func (h *harness) csrfFrom(path string) {
	h.t.Helper()
	_, body := h.get(path)
	m := reCSRF.FindStringSubmatch(body)
	if m == nil {
		h.t.Fatalf("no CSRF token on %s", path)
	}
	h.csrf = m[1]
}

var reRecoveryCode = regexp.MustCompile(`<code>([2-9A-Z]{5}-[2-9A-Z]{5})</code>`)

// enrollTOTP sets up TOTP for the logged-in user and returns the secret and
// the recovery codes shown once.
func (h *harness) enrollTOTP(name string) ([]byte, []string) {
	h.t.Helper()
	if resp := h.postOK("/account/totp/setup", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("setup status %d", resp.StatusCode)
	}
	_, body := h.get("/account/security")
	if !strings.Contains(body, "data:image/png;base64,") {
		h.t.Fatal("no QR code during enrollment")
	}
	secret := h.totpSecret(name)
	form := url.Values{"code": {mfa.Code(secret, mfa.Step(time.Now()))}, "_csrf": {h.csrf}}
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/account/totp/confirm", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.srv.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	page, _ := io.ReadAll(resp.Body)
	codes := reRecoveryCode.FindAllStringSubmatch(string(page), -1)
	if resp.StatusCode != http.StatusOK || len(codes) != mfa.RecoveryCodeCount {
		h.t.Fatalf("confirm: status %d, %d codes", resp.StatusCode, len(codes))
	}
	out := make([]string, len(codes))
	for i, c := range codes {
		out[i] = c[1]
	}
	return secret, out
}

// ageAuth moves the session's last authentication into the past.
func (h *harness) ageAuth(d time.Duration) {
	h.t.Helper()
	u, _ := url.Parse(h.srv.URL)
	for _, c := range h.client.Jar.Cookies(u) {
		if c.Name == app.SessionCookie {
			if err := h.db.SetSessionAuth(context.Background(), auth.HashToken(c.Value), time.Now().Add(-d)); err != nil {
				h.t.Fatal(err)
			}
			return
		}
	}
	h.t.Fatal("no session cookie")
}

// mfaLogin logs in with password and then posts a code to /login/mfa.
func (h *harness) mfaLogin(name, code string) *http.Response {
	h.t.Helper()
	resp := h.tryLogin(name, "correct horse battery")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login/mfa") {
		h.t.Fatalf("password step: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	h.csrfFrom("/login/mfa")
	return h.postOK("/login/mfa", url.Values{"code": {code}, "next": {"/"}})
}

func TestTOTPLoginFlow(t *testing.T) {
	h := newHarness(t)
	h.user("anna", store.RoleEditor)
	h.login("anna")
	secret, codes := h.enrollTOTP("anna")

	// Password alone now yields a pending session that cannot reach pages.
	c := h.other()
	resp := c.tryLogin("anna", "correct horse battery")
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/login/mfa") {
		t.Fatalf("redirect after password: %s", loc)
	}
	if resp, _ := c.get("/"); resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("pending session reached the dashboard: %d", resp.StatusCode)
	}
	if resp, _ := c.get("/account/security"); resp.StatusCode != http.StatusSeeOther {
		t.Fatal("pending session reached the security page")
	}
	c.csrfFrom("/login/mfa")
	if resp := c.postOK("/login/mfa", url.Values{"code": {"000000"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}

	// The code used for enrollment cannot be replayed; use the next step.
	next := mfa.Code(secret, mfa.Step(time.Now())+1)
	if resp := c.postOK("/login/mfa", url.Values{"code": {next}, "next": {"/"}}); resp.StatusCode != http.StatusSeeOther ||
		resp.Header.Get("Location") != "/" {
		t.Fatalf("valid code: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := c.get("/"); resp.StatusCode != http.StatusOK {
		t.Fatalf("dashboard after MFA: %d", resp.StatusCode)
	}

	// The same code again (replay) fails.
	d := h.other()
	if resp := d.mfaLogin("anna", next); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed code accepted: %d", resp.StatusCode)
	}

	// A recovery code works once, in any case and with spaces.
	e := h.other()
	if resp := e.mfaLogin("anna", strings.ToLower(strings.Replace(codes[0], "-", " ", 1))); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recovery code: %d", resp.StatusCode)
	}
	f := h.other()
	if resp := f.mfaLogin("anna", codes[0]); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("recovery code reused: %d", resp.StatusCode)
	}
	if st, _ := h.db.MFAStatus(context.Background(), h.userID("anna")); st.RecoveryLeft != mfa.RecoveryCodeCount-1 {
		t.Fatalf("recovery left %d", st.RecoveryLeft)
	}
}

func TestMFAPolicyEnforcement(t *testing.T) {
	h := newHarness(t)
	h.user("root", store.RoleAdmin)
	h.user("vera", store.RoleViewer)
	ctx := context.Background()
	if err := h.db.SetSetting(ctx, store.SettingMFAPolicy, store.MFAPolicyAdmins); err != nil {
		t.Fatal(err)
	}
	// Viewer: not affected.
	v := h.other()
	v.login("vera")
	// Admin without a second factor: everything except enrollment is blocked.
	resp := h.tryLogin("root", "correct horse battery")
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := h.get("/admin/users"); resp.Header.Get("Location") != "/account/security?err=mfa_required" {
		t.Fatalf("admin not sent to enrollment: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	h.csrfFrom("/account/security")
	h.enrollTOTP("root")
	if resp, _ := h.get("/admin/users"); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin blocked after enrollment: %d", resp.StatusCode)
	}
	// The only factor cannot be removed while required.
	if resp := h.postOK("/account/totp/disable", url.Values{}); resp.Header.Get("Location") != "/account/security?err=mfa_keep" {
		t.Fatalf("last factor removed: %s", resp.Header.Get("Location"))
	}
}

func TestSensitiveActionsNeedRecentAuth(t *testing.T) {
	h := newHarness(t)
	h.user("anna", store.RoleAdmin)
	h.login("anna")
	// Age the session's authentication.
	h.ageAuth(time.Hour)
	for _, path := range []string{"/account/totp/setup", "/account/recovery/regenerate", "/admin/security"} {
		resp := h.postOK(path, url.Values{"mfa_policy": {"all"}})
		if !strings.HasPrefix(resp.Header.Get("Location"), "/reauth?next=") {
			t.Errorf("%s without recent auth: %d %s", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// Passkey JSON endpoints answer with a redirect hint.
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/account/passkeys/options", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", h.csrf)
	req.Header.Set("Origin", h.srv.URL)
	if resp, err := h.client.Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("passkey options without recent auth: %v %v", resp, err)
	}
	// Wrong password: refused.
	if resp := h.postOK("/reauth", url.Values{"password": {"nope"}, "next": {"/admin/security"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reauth with wrong password: %d", resp.StatusCode)
	}
	if resp := h.postOK("/reauth", url.Values{"password": {"correct horse battery"}, "next": {"/admin/security"}}); resp.Header.Get("Location") != "/admin/security" {
		t.Fatalf("reauth: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Invalid policy values are rejected.
	if resp := h.postOK("/admin/security", url.Values{"mfa_policy": {"sometimes"}}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy: %d", resp.StatusCode)
	}
	if resp := h.postOK("/admin/security", url.Values{"mfa_policy": {"all"}}); resp.Header.Get("Location") != "/admin/security?ok=saved" {
		t.Fatalf("save policy after reauth: %s", resp.Header.Get("Location"))
	}
	if p, _ := h.db.Setting(context.Background(), store.SettingMFAPolicy, ""); p != store.MFAPolicyAll {
		t.Fatal("policy not saved")
	}
	// The admin has no second factor yet, so the new policy applies at once.
	if resp, _ := h.get("/"); resp.Header.Get("Location") != "/account/security?err=mfa_required" {
		t.Fatalf("policy not enforced: %s", resp.Header.Get("Location"))
	}
}

func TestReauthNeedsSecondFactor(t *testing.T) {
	h := newHarness(t)
	h.user("anna", store.RoleEditor)
	h.login("anna")
	secret, _ := h.enrollTOTP("anna")
	if resp := h.postOK("/reauth", url.Values{"password": {"correct horse battery"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reauth without code: %d", resp.StatusCode)
	}
	code := mfa.Code(secret, mfa.Step(time.Now())+1)
	if resp := h.postOK("/reauth", url.Values{"password": {"correct horse battery"}, "code": {code}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("reauth with code: %d", resp.StatusCode)
	}
}

func TestAdminResetsMFA(t *testing.T) {
	h := newHarness(t)
	h.user("root", store.RoleAdmin)
	h.user("anna", store.RoleEditor)
	a := h.other()
	a.login("anna")
	a.enrollTOTP("anna")

	h.login("root")
	if resp := a.postOK("/admin/users/"+itoa(h.userID("anna"))+"/mfa-reset", url.Values{}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-admin reset: %d", resp.StatusCode)
	}
	_, body := h.get("/admin/users")
	if !strings.Contains(body, "TOTP") {
		t.Fatal("users page does not show the second factor")
	}
	if resp := h.postOK("/admin/users/"+itoa(h.userID("anna"))+"/mfa-reset", url.Values{}); resp.Header.Get("Location") != "/admin/users?ok=mfa_reset" {
		t.Fatalf("reset: %s", resp.Header.Get("Location"))
	}
	if st, _ := h.db.MFAStatus(context.Background(), h.userID("anna")); st.Enabled() || st.RecoveryLeft != 0 {
		t.Fatal("factors left after reset")
	}
	// Anna's sessions ended.
	if resp, _ := a.get("/"); resp.StatusCode != http.StatusSeeOther {
		t.Fatal("session survived the reset")
	}
}

func TestPasskeyEndpointsWithoutHTTPS(t *testing.T) {
	h := newHarness(t) // public_url is http://127.0.0.1:…, so passkeys are off
	h.user("anna", store.RoleEditor)
	_, body := h.get("/login")
	if strings.Contains(body, `data-passkey="get"`) {
		t.Fatal("passkey button shown although passkeys are unavailable")
	}
	m := reLoginCSRF.FindStringSubmatch(body)
	post := func(hdr map[string]string) int {
		req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/login/passkey/options", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(map[string]string{"Origin": h.srv.URL}); code != http.StatusForbidden {
		t.Fatalf("missing login CSRF: %d", code)
	}
	if code := post(map[string]string{"Origin": "https://evil.example", "X-Login-CSRF": m[1]}); code != http.StatusForbidden {
		t.Fatalf("foreign origin: %d", code)
	}
	if code := post(map[string]string{"Origin": h.srv.URL, "X-Login-CSRF": m[1]}); code != http.StatusConflict {
		t.Fatalf("passkeys unavailable: %d", code)
	}
}

func TestPasskeysEnabledWithHTTPSURL(t *testing.T) {
	ui := newHarness(t).ui
	if ui.wa != nil {
		t.Fatal("harness unexpectedly has passkeys")
	}
	wa, err := mfa.NewWebAuthn("https://inventory.example.org", "LANventory")
	if err != nil || wa.Config.RPID != "inventory.example.org" || wa.Config.RPOrigins[0] != "https://inventory.example.org" {
		t.Fatalf("relying party: %v %+v", err, wa)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
