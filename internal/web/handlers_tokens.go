package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/store"
)

// maxTokenDays is the longest expiry offered for an API token.
const maxTokenDays = 3650

type tokensData struct {
	Tokens []*store.APIToken
	// Scopes are the scopes the user may grant (limited by role and flags).
	Scopes []string
	// NewToken is the secret of a token that was just created; shown once.
	NewToken string
	// Admin marks the list of all users' tokens.
	Admin bool
	Now   time.Time
}

// allowedScopes lists the scopes a user's tokens can actually use: write
// scopes only make sense for roles and flags that allow the writes.
func allowedScopes(u *store.User) []string {
	var out []string
	for _, sc := range auth.AllScopes {
		switch {
		case sc == auth.ScopeWriteAssets && !u.Can(store.RoleEditor):
		case sc == auth.ScopeWriteInspections && !u.CanInspect:
		default:
			out = append(out, sc)
		}
	}
	return out
}

func (s *Server) tokensPage(w http.ResponseWriter, r *http.Request, newToken string) {
	u := app.User(r.Context())
	list, err := s.DB.ListUserAPITokens(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := tokensData{Tokens: list, Scopes: allowedScopes(u), NewToken: newToken, Now: time.Now()}
	s.render(w, r, "tokens", s.page(r, "tokens.title", "account", d))
}

func (s *Server) tokens(w http.ResponseWriter, r *http.Request) { s.tokensPage(w, r, "") }

func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	u := app.User(r.Context())
	name := strings.TrimSpace(r.PostFormValue("name"))
	if n := len([]rune(name)); n < 1 || n > 100 || strings.ContainsAny(name, "\r\n\t") {
		redirect(w, r, "/account/tokens?err=token_name")
		return
	}
	allowed := map[string]bool{}
	for _, sc := range allowedScopes(u) {
		allowed[sc] = true
	}
	var scopes []string
	for _, sc := range auth.AllScopes { // canonical order, no duplicates
		for _, got := range r.PostForm["scope"] {
			if got == sc {
				if !allowed[sc] {
					redirect(w, r, "/account/tokens?err=token_scopes")
					return
				}
				scopes = append(scopes, sc)
				break
			}
		}
	}
	if len(scopes) == 0 || len(scopes) != len(r.PostForm["scope"]) {
		redirect(w, r, "/account/tokens?err=token_scopes")
		return
	}
	var exp *time.Time
	if v := strings.TrimSpace(r.PostFormValue("expires_days")); v != "" {
		days, err := strconv.Atoi(v)
		if err != nil || days < 1 || days > maxTokenDays {
			redirect(w, r, "/account/tokens?err=token_expiry")
			return
		}
		t := store.Now().AddDate(0, 0, days)
		exp = &t
	}
	plain := auth.NewAPIToken()
	t := &store.APIToken{Name: name, Prefix: plain[:8], TokenHash: auth.HashToken(plain), Scopes: scopes, UserID: u.ID, ExpiresAt: exp}
	if err := s.DB.CreateAPIToken(r.Context(), t); err != nil {
		s.fail(w, r, err)
		return
	}
	details := map[string]any{"name": name, "prefix": t.Prefix, "scopes": strings.Join(scopes, " ")}
	if exp != nil {
		details["expires_at"] = exp.Format(time.RFC3339)
	}
	s.Audit(r, "token.create", "token", idStr(t.ID), details)
	// The secret is shown once, as the response to this POST (like recovery codes).
	s.tokensPage(w, r, plain)
}

// tokenRevoke revokes a token: users their own, admins any (on /admin/tokens).
func (s *Server) tokenRevoke(admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		back := "/account/tokens"
		if admin {
			back = "/admin/tokens"
		}
		id, ok := idParam(r)
		if !ok {
			s.errorPage(w, r, http.StatusNotFound, "error.not_found")
			return
		}
		t, err := s.DB.APITokenByID(r.Context(), id)
		if err != nil || (!admin && t.UserID != app.User(r.Context()).ID) {
			s.errorPage(w, r, http.StatusNotFound, "error.not_found")
			return
		}
		if err := s.DB.RevokeAPIToken(r.Context(), id); err != nil {
			s.fail(w, r, err)
			return
		}
		s.Audit(r, "token.revoke", "token", idStr(id), map[string]any{"name": t.Name, "prefix": t.Prefix, "owner": t.OwnerName})
		redirect(w, r, back+"?ok=token_revoked")
	}
}

func (s *Server) adminTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.DB.ListAPITokens(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "admin_tokens", s.page(r, "admin_tokens.title", "admin", tokensData{Tokens: list, Admin: true, Now: time.Now()}))
}
