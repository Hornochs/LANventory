package api

import (
	"net/http"
	"time"

	"github.com/lan-dot-party/lanventory/internal/auth"
)

type meUser struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	Role       string `json:"role"`
	CanInspect bool   `json:"can_inspect"`
}

type meToken struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Prefix    string     `json:"prefix"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at"`
}

type meResponse struct {
	User meUser `json:"user"`
	// Auth is "token" or "session".
	Auth  string   `json:"auth"`
	Token *meToken `json:"token"`
	// EffectiveScopes are the scopes the caller can actually use: the
	// token's scopes (all four for a session) limited by the user's rights.
	EffectiveScopes []string `json:"effective_scopes"`
}

// scopeNeeds maps each scope to the weakest right that makes it useful.
var scopeNeeds = map[string]need{
	auth.ScopeReadAssets:       needReadAssets,
	auth.ScopeWriteAssets:      needWriteAssets,
	auth.ScopeReadInspections:  needReadInspections,
	auth.ScopeWriteInspections: needInspect,
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	resp := meResponse{User: meUser{p.user.ID, p.user.Username, p.user.Role, p.user.CanInspect}, Auth: "session",
		EffectiveScopes: []string{}}
	if p.token != nil {
		resp.Auth = "token"
		resp.Token = &meToken{p.token.ID, p.token.Name, p.token.Prefix, p.token.Scopes, p.token.ExpiresAt}
	}
	for _, s := range auth.AllScopes {
		if p.check(scopeNeeds[s]) == "" {
			resp.EffectiveScopes = append(resp.EffectiveScopes, s)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
