// Package api implements the JSON REST API under /api/v1.
//
// The OpenAPI document in openapi.yaml is the single source of truth for
// paths, parameters and response shapes; the tests check the routes against it
// and validate the responses (docs/adr/0007). The API reuses the store
// functions (and their validation) of the web UI, so both enforce the same
// rules.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/store"
)

// API serves /api.
type API struct {
	*app.App
	limiter *auth.RateLimiter
}

// New creates the API handler.
func New(a *app.App) *API {
	return &API{App: a, limiter: auth.NewRateLimiter(a.Cfg.API.RateLimit, a.Cfg.API.RateBurst)}
}

// Paging limits of list endpoints.
const (
	DefaultPerPage = store.DefaultPerPage
	MaxPerPage     = 200
)

// Routes returns the router for /api (mount it at /api).
func (a *API) Routes() http.Handler { return a.routes() }

// routesForSpec builds the router for the spec test (no App needed).
func (a *API) routesForSpec() chi.Router { return a.routes() }

// What a route needs from the caller.
var (
	needReadAssets       = need{scope: auth.ScopeReadAssets, role: store.RoleViewer}
	needWriteAssets      = need{scope: auth.ScopeWriteAssets, role: store.RoleEditor}
	needDeleteAssets     = need{scope: auth.ScopeWriteAssets, role: store.RoleAdmin}
	needReadInspections  = need{scope: auth.ScopeReadInspections, role: store.RoleViewer}
	needInspect          = need{scope: auth.ScopeWriteInspections, role: store.RoleViewer, inspect: true}
	needDeleteInspection = need{scope: auth.ScopeWriteInspections, role: store.RoleAdmin}
)

func (a *API) routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", a.docs)
	r.Get("/openapi.json", a.openAPIJSON)
	r.Get("/openapi.yaml", a.openAPIYAML)
	r.Route("/v1", func(r chi.Router) {
		r.With(a.authenticated).Get("/me", a.me)

		r.With(a.require(needReadAssets)).Get("/locations", a.listLocations)
		r.With(a.require(needReadAssets)).Get("/locations/{id}", a.getLocation)
		r.With(a.require(needWriteAssets)).Post("/locations", a.createLocation)
		r.With(a.require(needWriteAssets)).Patch("/locations/{id}", a.updateLocation)
		r.With(a.require(needWriteAssets)).Delete("/locations/{id}", a.deleteLocation)

		r.With(a.require(needReadAssets)).Get("/categories", a.listCategories)
		r.With(a.require(needReadAssets)).Get("/categories/{id}", a.getCategory)

		r.With(a.require(needReadAssets)).Get("/assets", a.listAssets)
		r.With(a.require(needReadAssets)).Get("/assets/by-number/{inventory_no}", a.getAssetByNumber)
		r.With(a.require(needReadAssets)).Get("/assets/{id}", a.getAsset)
		r.With(a.require(needWriteAssets)).Post("/assets", a.createAsset)
		r.With(a.require(needWriteAssets)).Patch("/assets", a.bulkUpdateAssets)
		r.With(a.require(needWriteAssets)).Patch("/assets/{id}", a.updateAsset)
		r.With(a.require(needDeleteAssets)).Delete("/assets/{id}", a.deleteAsset)

		r.With(a.require(needReadInspections)).Get("/assets/{id}/inspections", a.listAssetInspections)
		r.With(a.require(needInspect)).Post("/assets/{id}/inspections", a.createInspection)
		r.With(a.require(needReadInspections)).Get("/inspections", a.listInspections)
		r.With(a.require(needReadInspections)).Get("/inspections/due", a.listDue)
		r.With(a.require(needReadInspections)).Get("/inspections/{id}", a.getInspection)
		r.With(a.require(needDeleteInspection)).Delete("/inspections/{id}", a.deleteInspection)

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			problem(w, http.StatusNotFound, "not_found", "no such endpoint")
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			problem(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed for this endpoint")
		})
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		problem(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return r
}

type ctxKey int

const keyPrincipal ctxKey = 0

// principal is who calls the API: a token (acting as its owner) or a logged-in
// browser session. user is always the current state of the acting user.
type principal struct {
	token *store.APIToken
	user  *store.User
}

// need describes what an endpoint requires: the token scope, the minimum role
// of the user and optionally the can_inspect flag. A token must have the scope
// AND its owner must currently be allowed; a session only needs the user rights.
type need struct {
	scope   string
	role    string
	inspect bool
}

// check returns "" when the principal may do it, else the error code.
func (p principal) check(n need) string {
	if p.token != nil && !p.token.HasScope(n.scope) {
		return "insufficient_scope"
	}
	if !p.user.Can(n.role) || (n.inspect && !p.user.CanInspect) {
		return "forbidden"
	}
	return ""
}

func principalOf(ctx context.Context) principal {
	p, _ := ctx.Value(keyPrincipal).(principal)
	return p
}

// authenticate resolves a bearer token or a browser session and applies the
// rate limit. It writes the problem response itself.
func (a *API) authenticate(w http.ResponseWriter, r *http.Request) (principal, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		raw, ok := strings.CutPrefix(h, "Bearer ")
		raw = strings.TrimSpace(raw)
		if !ok || !strings.HasPrefix(raw, auth.APITokenPrefix) {
			unauthorized(w, "invalid_token", "expected Authorization: Bearer "+auth.APITokenPrefix+"...")
			return principal{}, false
		}
		tok, err := a.DB.APITokenByHash(r.Context(), auth.HashToken(raw))
		if err != nil || tok.Revoked || tok.Expired(time.Now()) {
			unauthorized(w, "invalid_token", "token unknown, revoked or expired")
			return principal{}, false
		}
		owner, err := a.DB.UserByID(r.Context(), tok.UserID)
		if err != nil || owner.Disabled {
			unauthorized(w, "invalid_token", "token unknown, revoked or expired")
			return principal{}, false
		}
		if !a.limiter.Allow("token:" + strconv.FormatInt(tok.ID, 10)) {
			rateLimited(w)
			return principal{}, false
		}
		if err := a.DB.TouchAPIToken(context.WithoutCancel(r.Context()), tok.ID); err != nil {
			a.Log.Warn("record token use", "err", err)
		}
		return principal{token: tok, user: owner}, true
	}

	u, s := app.User(r.Context()), app.Session(r.Context())
	if u == nil || s == nil {
		detail := "send Authorization: Bearer " + auth.APITokenPrefix + "... or log in"
		if app.PendingUser(r.Context()) != nil {
			detail = "the login is not complete: the second factor is missing"
		}
		unauthorized(w, "unauthorized", detail)
		return principal{}, false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		tok := r.Header.Get("X-CSRF-Token")
		if !a.SameOrigin(r) || subtle.ConstantTimeCompare([]byte(tok), []byte(s.CSRFToken)) != 1 {
			problem(w, http.StatusForbidden, "csrf_failed", "state-changing requests with a browser session need the header X-CSRF-Token and a same-origin request")
			return principal{}, false
		}
	}
	// Same rule as the web UI: a user who must have a second factor but has
	// none cannot use the session for anything.
	policy, err := a.DB.Setting(r.Context(), store.SettingMFAPolicy, store.MFAPolicyOff)
	if err != nil {
		a.Log.Error("read MFA policy", "err", err)
		policy = store.MFAPolicyOff
	}
	if store.MFARequired(policy, u) {
		st, err := a.DB.MFAStatus(r.Context(), u.ID)
		if err != nil {
			a.internal(w, r, err)
			return principal{}, false
		}
		if !st.Enabled() {
			problem(w, http.StatusForbidden, "mfa_required", "a second factor is required: set it up in the web UI under Account > Sign-in & security")
			return principal{}, false
		}
	}
	if !a.limiter.Allow("user:" + strconv.FormatInt(u.ID, 10)) {
		rateLimited(w)
		return principal{}, false
	}
	return principal{user: u}, true
}

// authenticated requires a valid principal without a particular right.
func (a *API) authenticated(next http.Handler) http.Handler {
	return a.require(need{})(next)
}

// require authenticates and checks n.
func (a *API) require(n need) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := a.authenticate(w, r)
			if !ok {
				return
			}
			if n.scope != "" {
				switch p.check(n) {
				case "insufficient_scope":
					problem(w, http.StatusForbidden, "insufficient_scope", "this endpoint needs the token scope "+n.scope)
					return
				case "forbidden":
					detail := "your role does not allow this"
					if p.token != nil {
						detail = "the token owner's current role or rights do not allow this"
					}
					problem(w, http.StatusForbidden, "forbidden", detail)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyPrincipal, p)))
		})
	}
}

// Problem is an RFC 9457 problem detail.
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Code is a stable machine-readable error code.
	Code string `json:"code"`
	// Errors maps invalid request fields to a short code (validation_failed only).
	Errors map[string]string `json:"errors,omitempty"`
}

func problemWith(w http.ResponseWriter, status int, code, detail string, errs map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: "urn:lanventory:problem:" + code, Title: http.StatusText(status), Status: status,
		Detail: detail, Code: code, Errors: errs})
}

func problem(w http.ResponseWriter, status int, code, detail string) {
	problemWith(w, status, code, detail, nil)
}

func unauthorized(w http.ResponseWriter, code, detail string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="lanventory"`)
	problem(w, http.StatusUnauthorized, code, detail)
}

func rateLimited(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	problem(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded; retry after the time in Retry-After")
}

func validationFailed(w http.ResponseWriter, errs map[string]string) {
	problemWith(w, http.StatusUnprocessableEntity, "validation_failed", "the request is well-formed but its values are invalid", errs)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // the client may have gone away
}

func (a *API) internal(w http.ResponseWriter, r *http.Request, err error) {
	a.Log.Error("api error", "method", r.Method, "path", r.URL.Path, "err", err)
	problem(w, http.StatusInternalServerError, "internal", "internal error, see the server log")
}

// fail maps store errors to problems: not found, validation, in use; anything
// else is an internal error.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *store.ValidationError
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem(w, http.StatusNotFound, "not_found", "no such resource")
	case errors.As(err, &ve):
		validationFailed(w, apiErrors(ve.Fields))
	case errors.Is(err, store.ErrInUse):
		problem(w, http.StatusConflict, "conflict", "the resource is in use")
	default:
		a.internal(w, r, err)
	}
}

// audit writes an audit entry for the acting user (the token's owner) with
// the details via=api and, for tokens, the token ID (never the token).
func (a *API) audit(r *http.Request, action, objectType, objectID string, details map[string]any) {
	p := principalOf(r.Context())
	d := map[string]any{}
	for k, v := range details {
		d[k] = v
	}
	if _, ok := d["via"]; !ok {
		d["via"] = "api"
	}
	d["ip"] = a.ClientIP(r)
	if p.token != nil {
		d["token_id"] = p.token.ID
	}
	e := store.AuditEntry{UserID: p.user.ID, Action: action, ObjectType: objectType, ObjectID: objectID, Details: d}
	if err := a.DB.Audit(context.WithoutCancel(r.Context()), e); err != nil {
		a.Log.Error("audit log", "err", err, "action", action)
	}
}
