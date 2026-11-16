// Package web serves the server-rendered management UI (html/template, a small
// app.js, no inline scripts).
//
// Everything except /login, /health and /static is behind a login. All output
// is escaped by html/template and the CSP forbids inline scripts.
package web

import (
	"crypto/subtle"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/mfa"
	"github.com/lan-dot-party/lanventory/internal/store"
	"github.com/lan-dot-party/lanventory/pkg/version"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Server is the web UI.
type Server struct {
	*app.App
	pages  map[string]*template.Template
	login  *auth.LoginLimiter
	reauth *auth.LoginLimiter
	// wa is the WebAuthn relying party; nil when public_url does not allow
	// passkeys (see mfa.NewWebAuthn).
	wa         *webauthn.WebAuthn
	ceremonies *mfa.Ceremonies
	// imports holds uploaded CSV files between the steps of the import wizard.
	imports *importStates
}

var pageFiles = []string{"login", "dashboard", "account", "error", "users", "audit",
	"mfa", "security", "recovery", "reauth", "admin_security",
	"locations", "categories", "category", "assets", "asset", "asset_form", "asset_new",
	"inspection", "inspection_form", "inspections", "inspections_due", "tokens", "admin_tokens",
	"import_upload", "import_map", "import_preview", "import_result",
	"admin_data", "data_preview", "data_result", "bulk_form", "bulk_preview"}

// New parses all templates.
func New(a *app.App) (*Server, error) {
	s := &Server{App: a, pages: map[string]*template.Template{}, login: auth.NewLoginLimiter(), reauth: auth.NewLoginLimiter(),
		ceremonies: mfa.NewCeremonies(), imports: newImportStates()}
	wa, err := mfa.NewWebAuthn(a.Cfg.PublicURL, "LANventory")
	switch {
	case err == nil:
		s.wa = wa
	case errors.Is(err, mfa.ErrPasskeysUnavailable):
		a.Log.Info("passkeys disabled", "reason", err)
	default:
		return nil, fmt.Errorf("webauthn: %w", err)
	}
	for _, name := range pageFiles {
		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS,
			"templates/layout.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("parse template %s: %w", name, err)
		}
		s.pages[name] = t
	}
	// The inspection protocol is a standalone print page without the app layout.
	t, err := template.New("protocol").Funcs(funcs).ParseFS(templateFS, "templates/protocol.html")
	if err != nil {
		return nil, fmt.Errorf("parse template protocol: %w", err)
	}
	s.pages["protocol"] = t
	return s, nil
}

var funcs = template.FuncMap{
	"themes":      func() []string { return store.Themes },
	"roles":       func() []string { return store.Roles },
	"mfaPolicies": func() []string { return store.MFAPolicies },
	"statuses":    func() []string { return store.Statuses },
	"ftypes":      func() []string { return store.FieldTypes },
	"num":         fmtNum,
	"meas":        fmtMeasure,
	"date":        fmtDate,
	// dict2 passes the page and a list of preview rows to the shared row table.
	"dict2": func(p *Page, rows []previewRow) any {
		return struct {
			P    *Page
			Rows []previewRow
		}{p, rows}
	},
}

// Routes returns the UI router.
func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	static, _ := fs.Sub(staticFS, "static")
	r.Handle("/static/*", http.StripPrefix("/static/", cacheStatic(http.FileServerFS(static))))
	r.Get("/login", s.loginPage)
	r.Post("/login", s.loginSubmit)
	// Passwordless login; protected by the login page's double-submit token.
	r.Post("/login/passkey/options", s.passkeyLoginOptions)
	r.Post("/login/passkey/finish", s.passkeyLoginFinish)

	// Second factor after the password (pending session).
	r.Group(func(r chi.Router) {
		r.Use(s.requirePending, s.csrf)
		r.Get("/login/mfa", s.mfaPage)
		r.Post("/login/mfa", s.mfaSubmit)
		r.Post("/login/mfa/passkey/options", s.passkeyMFAOptions)
		r.Post("/login/mfa/passkey/finish", s.passkeyMFAFinish)
		r.Post("/login/mfa/cancel", s.logout)
	})

	r.Group(func(r chi.Router) {
		r.Use(s.requireLogin, s.importBody, s.csrf, s.enforceMFA)
		r.Post("/logout", s.logout)
		r.Get("/", s.dashboard)
		r.Get("/account", s.account)
		r.Post("/account/password", s.accountPassword)
		r.Post("/account/prefs", s.accountPrefs)
		r.Post("/account/theme", s.accountTheme)
		r.Get("/account/security", s.security)
		r.Get("/account/tokens", s.tokens)
		r.Post("/account/tokens/{id}/revoke", s.tokenRevoke(false))
		r.With(s.requireRecentAuth("/account/tokens")).Post("/account/tokens", s.tokenCreate)
		r.Get("/reauth", s.reauthPage)
		r.Post("/reauth", s.reauthSubmit)
		r.Post("/reauth/passkey/options", s.passkeyReauthOptions)
		r.Post("/reauth/passkey/finish", s.passkeyReauthFinish)

		// Changes to the own second factors need a recent authentication.
		r.Group(func(r chi.Router) {
			r.Use(s.requireRecentAuth("/account/security"))
			r.Post("/account/totp/setup", s.totpSetup)
			r.Post("/account/totp/confirm", s.totpConfirm)
			r.Post("/account/totp/disable", s.totpDisable)
			r.Post("/account/recovery/regenerate", s.recoveryRegenerate)
			r.Post("/account/passkeys/options", s.passkeyRegisterOptions)
			r.Post("/account/passkeys/finish", s.passkeyRegisterFinish)
			r.Post("/account/passkeys/{id}/delete", s.passkeyDelete)
		})

		// Inventory: everybody reads; editors change assets and locations;
		// admins manage categories and fields and delete assets.
		r.Get("/assets", s.assets)
		r.Get("/assets.csv", s.assetsCSV)
		r.Get("/locations", s.locations)
		r.Get("/categories", s.categories)
		r.Get("/categories/{id}", s.category)
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(store.RoleEditor))
			r.Get("/assets/new", s.assetNew)
			r.Get("/assets/import", s.importPage)
			r.Post("/assets/import", s.importUpload)
			r.Post("/assets/import/map", s.importMapBack)
			r.Post("/assets/import/preview", s.importPreview)
			r.Post("/assets/import/confirm", s.importConfirm)
			r.Post("/assets", s.assetCreate)
			r.Post("/assets/bulk/edit", s.bulkEdit)
			r.Post("/assets/bulk/preview", s.bulkPreview)
			r.Post("/assets/bulk/apply", s.bulkApply)
			r.Get("/assets/{id}/edit", s.assetEdit)
			r.Post("/assets/{id}", s.assetUpdate)
			r.Post("/locations", s.locationCreate)
			r.Post("/locations/{id}", s.locationUpdate)
			r.Post("/locations/{id}/delete", s.locationDelete)
		})
		r.Get("/assets/{id}", s.assetDetail)

		// DGUV V3: everybody reads; recording needs the can_inspect flag
		// (any role); admins delete.
		r.Get("/inspections", s.inspections)
		r.Get("/inspections.csv", s.inspectionsCSV)
		r.Get("/inspections/due", s.inspectionsDue)
		r.Get("/inspections/{id}", s.inspectionDetail)
		r.Get("/inspections/{id}/print", s.inspectionPrint)
		r.Group(func(r chi.Router) {
			r.Use(s.requireInspector)
			r.Get("/assets/{id}/inspections/new", s.inspectionNew)
			r.Post("/assets/{id}/inspections", s.inspectionCreate)
		})
		r.With(s.requireRole(store.RoleAdmin)).Post("/inspections/{id}/delete", s.inspectionDelete)
		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(store.RoleAdmin))
			r.Post("/assets/{id}/delete", s.assetDelete)
			r.Post("/categories", s.categoryCreate)
			r.Post("/categories/{id}", s.categoryUpdate)
			r.Post("/categories/{id}/delete", s.categoryDelete)
			r.Post("/categories/{id}/fields", s.fieldCreate)
			r.Post("/categories/{id}/fields/{fid}", s.fieldUpdate)
			r.Post("/categories/{id}/fields/{fid}/delete", s.fieldDelete)
			r.Post("/categories/{id}/fields/{fid}/move", s.fieldMove)
		})

		r.Group(func(r chi.Router) {
			r.Use(s.requireRole(store.RoleAdmin))
			r.Get("/admin/users", s.users)
			r.Post("/admin/users", s.userCreate)
			r.Post("/admin/users/{id}", s.userUpdate)
			r.Post("/admin/users/{id}/password", s.userPassword)
			r.Post("/admin/users/{id}/delete", s.userDelete)
			r.Get("/admin/audit", s.audit)
			r.Get("/admin/tokens", s.adminTokens)
			r.Post("/admin/tokens/{id}/revoke", s.tokenRevoke(true))
			r.Get("/admin/security", s.adminSecurity)
			// Data export and import: everything needs a recent authentication
			// (the export contains the whole inventory, the import changes it).
			r.Group(func(r chi.Router) {
				r.Use(s.requireRecentAuth("/admin/data"))
				r.Get("/admin/data", s.dataPage)
				r.Get("/admin/data/export.json", s.dataExport)
				r.Post("/admin/data/import", s.dataUpload)
				r.Post("/admin/data/preview", s.dataPreview)
				r.Post("/admin/data/confirm", s.dataConfirm)
			})
			r.With(s.requireRecentAuth("/admin/security")).Post("/admin/security", s.adminSecuritySave)
			r.With(s.requireRecentAuth("/admin/users")).Post("/admin/users/{id}/mfa-reset", s.userResetMFA)
		})
	})
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
	})
	return r
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.User(r.Context()) == nil {
			if r.Header.Get("Accept") == "application/json" {
				http.Error(w, `{"error":"login required"}`, http.StatusUnauthorized)
				return
			}
			// A link from another site arrives without the SameSite=Strict
			// cookie. Reload the start page once from our own origin so that the
			// cookie is sent; only for "/".
			if r.URL.Path == "/" && r.Method == http.MethodGet && r.Header.Get("Sec-Fetch-Site") == "cross-site" &&
				r.URL.Query().Get("r") == "" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				fmt.Fprint(w, `<!doctype html><meta http-equiv="refresh" content="0;url=/?r=1"><title>LANventory</title>`)
				return
			}
			next := r.URL.RequestURI()
			http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireRole allows only users with at least the given role.
func (s *Server) requireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !app.User(r.Context()).Can(role) {
				s.errorPage(w, r, http.StatusForbidden, "error.forbidden")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireInspector allows only users with the can_inspect flag (any role).
func (s *Server) requireInspector(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := app.User(r.Context()); u == nil || !u.CanInspect {
			s.errorPage(w, r, http.StatusForbidden, "error.forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrf checks the session's token and the Origin on every state-changing request.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !s.SameOrigin(r) {
				s.Log.Warn("rejected cross-origin request", "path", r.URL.Path, "origin", r.Header.Get("Origin"),
					"expected", s.Cfg.Origin(), "hint", "check public_url if this is a legitimate request")
				s.errorPage(w, r, http.StatusForbidden, "error.origin")
				return
			}
			sess := app.Session(r.Context())
			token := r.Header.Get("X-CSRF-Token")
			if token == "" {
				token = r.PostFormValue("_csrf")
			}
			if sess == nil || subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRFToken)) != 1 {
				s.errorPage(w, r, http.StatusForbidden, "error.csrf")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Page is passed to every template.
type Page struct {
	Lang    string
	User    *store.User
	CSRF    string
	Title   string
	Nav     string
	Flash   string
	Error   string
	Version string
	// Theme is the color theme: auto, light or dark.
	Theme string
	// Path is the current request URI, to return to after a theme change.
	Path string
	// Sub marks the active entry of the inventory sub navigation.
	Sub string
	// HTMX loads htmx on this page (only pages that need it).
	HTMX bool
	// Insecure is set when the UI is used without HTTPS.
	Insecure bool
	Data     any
}

// NextTheme is the theme the toggle in the top bar switches to.
func (p *Page) NextTheme() string {
	switch p.Theme {
	case "auto":
		return "light"
	case "light":
		return "dark"
	}
	return "auto"
}

// T translates a key.
func (p *Page) T(key string, args ...any) string { return translate(p.Lang, key, args...) }

// Can reports whether the user has a role (for templates).
func (p *Page) Can(role string) bool { return p.User.Can(role) }

func (s *Server) page(r *http.Request, titleKey, nav string, data any) *Page {
	lang := language(r)
	p := &Page{Lang: lang, User: app.User(r.Context()), Nav: nav, Version: version.Version,
		Theme: theme(r), Path: r.URL.RequestURI(), Data: data, Insecure: !s.secureRequest(r)}
	if titleKey != "" {
		p.Title = translate(lang, titleKey)
	}
	if sess := app.Session(r.Context()); sess != nil {
		p.CSRF = sess.CSRFToken
	}
	if ok := r.URL.Query().Get("ok"); ok == "bulk" {
		// "n assets changed" carries the count in the query string.
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		if n == 1 {
			p.Flash = p.T("flash.bulk_one")
		} else if n >= 0 {
			p.Flash = p.T("flash.bulk", n)
		}
	} else if ok != "" && hasKey("flash."+ok) {
		p.Flash = p.T("flash." + ok)
	}
	if e := r.URL.Query().Get("err"); e != "" && hasKey("error."+e) {
		p.Error = p.T("error." + e)
	} else if code, ok := strings.CutPrefix(e, "verr_"); ok && hasKey("verr."+code) {
		p.Error = p.T("verr." + code)
		if vf := r.URL.Query().Get("vf"); hasKey("vf." + vf) {
			p.Error = p.T("vf."+vf) + ": " + p.Error
		}
	}
	return p
}

// secureRequest reports whether the browser talks HTTPS to us: directly, or
// through a reverse proxy (public_url is https).
func (s *Server) secureRequest(r *http.Request) bool {
	return r.TLS != nil || s.Cfg.SecureCookies()
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, p *Page) {
	s.renderStatus(w, r, http.StatusOK, name, p)
}

func (s *Server) renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, p *Page) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "unknown template "+name, http.StatusInternalServerError)
		return
	}
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		s.Log.Error("render", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(buf.String())) // the client may have gone away
}

// renderFragment renders one named template of a page without the layout
// (htmx partial responses).
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, page, name string, p *Page) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown template "+page, http.StatusInternalServerError)
		return
	}
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, name, p); err != nil {
		s.Log.Error("render", "template", page, "fragment", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(buf.String())) // the client may have gone away
}

// renderProtocol renders the standalone print page (no layout, no scripts).
func (s *Server) renderProtocol(w http.ResponseWriter, p *Page) {
	var buf strings.Builder
	if err := s.pages["protocol"].ExecuteTemplate(&buf, "protocol", p); err != nil {
		s.Log.Error("render", "template", "protocol", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(buf.String())) // the client may have gone away
}

func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, status int, key string) {
	p := s.page(r, "", "", nil)
	p.Error = p.T(key)
	s.renderStatus(w, r, status, "error", p)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound, "error.not_found")
		return
	}
	s.Log.Error("request failed", "path", r.URL.Path, "err", err)
	s.errorPage(w, r, http.StatusInternalServerError, "error.internal")
}

func redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// themeCookie keeps the theme for the login page after logout.
const themeCookie = "lv_theme"

// theme picks the user's theme, else the cookie's, else auto.
func theme(r *http.Request) string {
	if u := app.User(r.Context()); u != nil && store.ValidTheme(u.Theme) {
		return u.Theme
	}
	if c, err := r.Cookie(themeCookie); err == nil && store.ValidTheme(c.Value) {
		return c.Value
	}
	return "auto"
}

// language picks the user's language, else the browser's, else English.
func language(r *http.Request) string {
	if u := app.User(r.Context()); u != nil && (u.Lang == "de" || u.Lang == "en") {
		return u.Lang
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "de") {
		return "de"
	}
	return "en"
}
