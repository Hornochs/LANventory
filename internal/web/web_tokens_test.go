package web

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/store"
)

var reNewToken = regexp.MustCompile(`<code class="secret">(lvk_[0-9A-Za-z]{40})</code>`)

func tokenForm(name string, scopes ...string) url.Values {
	return url.Values{"name": {name}, "scope": scopes}
}

func (h *harness) tokensOf(name string) []*store.APIToken {
	h.t.Helper()
	list, err := h.db.ListUserAPITokens(context.Background(), h.userID(name))
	if err != nil {
		h.t.Fatal(err)
	}
	return list
}

func TestTokenCreateShownOnce(t *testing.T) {
	h := newHarness(t)
	h.user("vera", store.RoleViewer)
	h.login("vera")

	resp, body := h.postBody("/account/tokens", tokenForm("label printer", "read:assets", "read:inspections"))
	status(t, resp.StatusCode, 200, "create")
	m := reNewToken.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the new token is not shown: %s", body)
	}
	plain := m[1]
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("the token page may be cached")
	}
	// Stored as hash only; known to the owner by name, prefix and scopes.
	list := h.tokensOf("vera")
	if len(list) != 1 || list[0].TokenHash != auth.HashToken(plain) || list[0].Prefix != plain[:8] ||
		strings.Join(list[0].Scopes, " ") != "read:assets read:inspections" || list[0].ExpiresAt != nil || list[0].Revoked {
		t.Fatalf("stored token: %+v", list)
	}
	// Later views show the prefix, never the token.
	_, page := h.get("/account/tokens")
	if strings.Contains(page, plain) || !strings.Contains(page, plain[:8]) || !strings.Contains(page, "label printer") {
		t.Error("the token list leaks the secret or lacks the prefix")
	}
	// Audited without secret.
	var found bool
	entries, _ := h.db.ListAudit(context.Background(), 50)
	for _, e := range entries {
		if strings.Contains(e.RawDetails, plain) || strings.Contains(e.RawDetails, auth.HashToken(plain)) {
			t.Errorf("secret in audit entry %s", e.Action)
		}
		if e.Action == "token.create" && e.Username == "vera" && e.Details["name"] == "label printer" && e.Details["prefix"] == plain[:8] {
			found = true
		}
	}
	if !found {
		t.Error("no token.create audit entry")
	}
}

func TestTokenScopesFollowRole(t *testing.T) {
	h := newHarness(t)
	h.user("vera", store.RoleViewer)
	h.user("eli", store.RoleEditor)
	h.inspUser("ines", store.RoleViewer)
	cases := []struct {
		user   string
		scope  string
		allowd bool
	}{
		{"vera", "read:assets", true}, {"vera", "read:inspections", true}, {"vera", "write:assets", false}, {"vera", "write:inspections", false},
		{"eli", "write:assets", true}, {"eli", "write:inspections", false},
		{"ines", "write:inspections", true}, {"ines", "write:assets", false},
	}
	for _, c := range cases {
		x := h.other()
		x.login(c.user)
		// The form offers exactly the allowed scopes.
		_, page := x.get("/account/tokens")
		if offered := strings.Contains(page, `value="`+c.scope+`"`); offered != c.allowd {
			t.Errorf("%s: %s offered=%v, want %v", c.user, c.scope, offered, c.allowd)
		}
		before := len(h.tokensOf(c.user))
		resp := x.postOK("/account/tokens", tokenForm("t", c.scope))
		after := len(h.tokensOf(c.user))
		if c.allowd && (resp.StatusCode != 200 || after != before+1) {
			t.Errorf("%s: %s refused (%d)", c.user, c.scope, resp.StatusCode)
		}
		if !c.allowd && (resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Location"), "err=token_scopes") || after != before) {
			t.Errorf("%s: %s not refused: %d %s", c.user, c.scope, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

func TestTokenCreateValidation(t *testing.T) {
	h := newHarness(t)
	h.user("vera", store.RoleViewer)
	h.login("vera")
	bad := map[string]url.Values{
		"token_name":    tokenForm("", "read:assets"),
		"token_name2":   tokenForm(strings.Repeat("x", 101), "read:assets"),
		"token_scopes":  tokenForm("n"),
		"token_scopes2": tokenForm("n", "read:assets", "bogus:scope"),
		"token_expiry":  {"name": {"n"}, "scope": {"read:assets"}, "expires_days": {"0"}},
		"token_expiry2": {"name": {"n"}, "scope": {"read:assets"}, "expires_days": {"99999"}},
		"token_expiry3": {"name": {"n"}, "scope": {"read:assets"}, "expires_days": {"soon"}},
	}
	for want, form := range bad {
		resp := h.postOK("/account/tokens", form)
		if resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Location"), "err="+strings.TrimRight(want, "23")) {
			t.Errorf("%s: %d %s", want, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if n := len(h.tokensOf("vera")); n != 0 {
		t.Errorf("%d tokens created by invalid requests", n)
	}
	// Expiry in days.
	resp, _ := h.postBody("/account/tokens", url.Values{"name": {"short"}, "scope": {"read:assets"}, "expires_days": {"30"}})
	status(t, resp.StatusCode, 200, "with expiry")
	list := h.tokensOf("vera")
	if len(list) != 1 || list[0].ExpiresAt == nil {
		t.Fatalf("expiry: %+v", list)
	}
	if d := time.Until(*list[0].ExpiresAt); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("expiry in %v", d)
	}
	// The page shows the expiry and, once passed, the expired badge.
	_, page := h.get("/account/tokens")
	if strings.Contains(page, `class="badge badge-warn"`) {
		t.Error("a valid token is shown as expired")
	}
}

func TestTokenCreateNeedsRecentAuth(t *testing.T) {
	h := newHarness(t)
	h.user("vera", store.RoleViewer)
	h.login("vera")
	h.ageAuth(time.Hour)
	resp := h.postOK("/account/tokens", tokenForm("n", "read:assets"))
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/reauth") {
		t.Fatalf("stale login: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if n := len(h.tokensOf("vera")); n != 0 {
		t.Error("a token was created without recent authentication")
	}
	// No CSRF token or foreign origin: refused as everywhere else.
	h2 := newHarness(t)
	h2.user("vera", store.RoleViewer)
	h2.login("vera")
	status(t, h2.post("/account/tokens", tokenForm("n", "read:assets"), map[string]string{"Origin": h2.srv.URL}).StatusCode, 403, "no CSRF token")
	f := tokenForm("n", "read:assets")
	f.Set("_csrf", h2.csrf)
	status(t, h2.post("/account/tokens", f, map[string]string{"Origin": "https://evil.example"}).StatusCode, 403, "foreign origin")
}

func TestTokenRevoke(t *testing.T) {
	h := newHarness(t)
	h.user("vera", store.RoleViewer)
	h.user("eli", store.RoleEditor)
	h.user("ada", store.RoleAdmin)
	h.login("vera")
	h.postBody("/account/tokens", tokenForm("mine", "read:assets"))
	mine := h.tokensOf("vera")[0]
	e := h.other()
	e.login("eli")
	e.postBody("/account/tokens", tokenForm("theirs", "read:assets"))
	theirs := e.tokensOf("eli")[0]

	// Others' tokens cannot be revoked through the own-token route.
	status(t, h.postOK("/account/tokens/"+idStr(theirs.ID)+"/revoke", url.Values{}).StatusCode, 404, "revoke foreign")
	status(t, h.postOK("/account/tokens/9999/revoke", url.Values{}).StatusCode, 404, "revoke unknown")
	got, _ := h.db.APITokenByID(context.Background(), theirs.ID)
	if got.Revoked {
		t.Fatal("a foreign token was revoked")
	}
	// The admin page is for admins only.
	resp, _ := h.get("/admin/tokens")
	status(t, resp.StatusCode, 403, "viewer admin tokens")
	status(t, h.postOK("/admin/tokens/"+idStr(theirs.ID)+"/revoke", url.Values{}).StatusCode, 403, "viewer admin revoke")

	resp = h.postOK("/account/tokens/"+idStr(mine.ID)+"/revoke", url.Values{})
	if resp.StatusCode != 303 || !strings.Contains(resp.Header.Get("Location"), "ok=token_revoked") {
		t.Errorf("revoke: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := h.db.APITokenByID(context.Background(), mine.ID); !got.Revoked {
		t.Error("token not revoked")
	}
	_, page := h.get("/account/tokens")
	if !strings.Contains(page, "badge-muted") {
		t.Error("revoked token is not marked")
	}

	// An admin sees all tokens and revokes any.
	a := h.other()
	a.login("ada")
	resp, page = a.get("/admin/tokens")
	status(t, resp.StatusCode, 200, "admin tokens")
	if !strings.Contains(page, "mine") || !strings.Contains(page, "theirs") || !strings.Contains(page, "eli") {
		t.Error("admin list lacks tokens or owners")
	}
	resp = a.postOK("/admin/tokens/"+idStr(theirs.ID)+"/revoke", url.Values{})
	if resp.StatusCode != 303 || !strings.HasPrefix(resp.Header.Get("Location"), "/admin/tokens") {
		t.Errorf("admin revoke: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got, _ := h.db.APITokenByID(context.Background(), theirs.ID); !got.Revoked {
		t.Error("admin revoke had no effect")
	}
	var n int
	entries, _ := h.db.ListAudit(context.Background(), 50)
	for _, en := range entries {
		if en.Action == "token.revoke" {
			n++
			if en.Details["name"] == nil || en.Details["prefix"] == nil {
				t.Errorf("token.revoke details: %v", en.Details)
			}
		}
	}
	if n != 2 {
		t.Errorf("%d token.revoke entries, want 2", n)
	}
}

func TestTokenPagesGerman(t *testing.T) {
	h := newHarness(t)
	h.user("ada", store.RoleAdmin)
	h.langDE("ada")
	h.login("ada")
	_, page := h.get("/account/tokens")
	for _, want := range []string{"API-Tokens", "Token erzeugen", "Bereiche"} {
		if !strings.Contains(page, want) {
			t.Errorf("German token page lacks %q", want)
		}
	}
	_, page = h.get("/admin/tokens")
	if !strings.Contains(page, "API-Tokens aller Benutzer") {
		t.Error("German admin page lacks its title")
	}
	_, page = h.get("/account")
	if !strings.Contains(page, `href="/account/tokens"`) {
		t.Error("account page does not link to the tokens")
	}
}
