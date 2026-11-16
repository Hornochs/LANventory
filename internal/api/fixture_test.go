package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lan-dot-party/lanventory/internal/app"
	"github.com/lan-dot-party/lanventory/internal/auth"
	"github.com/lan-dot-party/lanventory/internal/config"
	"github.com/lan-dot-party/lanventory/internal/secret"
	"github.com/lan-dot-party/lanventory/internal/store"
)

const testOrigin = "http://127.0.0.1:9060"

// fixture is the API on a temporary database with users, a category with
// every field type, a location and a few assets.
type fixture struct {
	t       *testing.T
	db      *store.DB
	app     *app.App
	handler http.Handler
	path    string

	admin, editor, viewer, inspector *store.User
	loc                              *store.Location
	drum, mug                        *store.Category // drum requires DGUV V3
	fields                           map[string]store.Field
	drum1, drum2, mug1               *store.Asset
	insp                             *store.Inspection
	// full is an admin token with every scope.
	full string
}

func newFixtureCfg(t *testing.T, mod func(*config.Config)) *fixture {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	path := filepath.Join(t.TempDir(), "api.db")
	db, err := store.Open(store.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx, log); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	key, _ := secret.New(raw)
	cfg := config.Default()
	cfg.PublicURL = testOrigin
	cfg.API.RateLimit, cfg.API.RateBurst = 1000, 1000
	if mod != nil {
		mod(cfg)
	}
	a := app.New(cfg, db, key, log)
	f := &fixture{t: t, db: db, app: a, path: path, fields: map[string]store.Field{}}
	r := chi.NewRouter()
	r.With(a.SecurityHeaders, a.SessionMiddleware).Mount("/api", New(a).Routes())
	f.handler = r

	mk := func(name, role string, inspect bool) *store.User {
		u := &store.User{Username: name, PasswordHash: "x", Role: role, CanInspect: inspect}
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	f.admin, f.editor, f.viewer, f.inspector = mk("ada", store.RoleAdmin, true), mk("eli", store.RoleEditor, false),
		mk("vera", store.RoleViewer, false), mk("ines", store.RoleViewer, true)

	f.loc = &store.Location{Name: "Storage 1", Description: "Basement"}
	must(t, db.CreateLocation(ctx, f.loc))
	f.drum = &store.Category{Name: "Cable drum", Prefix: "XD", DGUVRequired: true, DGUVIntervalMonths: 12}
	must(t, db.CreateCategory(ctx, f.drum))
	f.mug = &store.Category{Name: "Mug", Prefix: "XM", DGUVIntervalMonths: 12}
	must(t, db.CreateCategory(ctx, f.mug))
	for _, fd := range []store.Field{
		{Key: "length", Label: "Length", Type: store.FieldNumber, Unit: "m"},
		{Key: "color", Label: "Color", Type: store.FieldSelect, Options: []string{"red", "blue"}},
		{Key: "sealed", Label: "Sealed", Type: store.FieldBool},
		{Key: "bought", Label: "Bought", Type: store.FieldDate},
		{Key: "label", Label: "Label", Type: store.FieldText, Required: true},
	} {
		fd.CategoryID = f.drum.ID
		fd := fd
		must(t, db.CreateField(ctx, &fd))
		f.fields[fd.Key] = fd
	}
	f.drum1 = f.asset(f.drum, "Drum 25 m", map[string]string{"length": "25", "color": "red", "sealed": "1", "label": "A"})
	f.drum2 = f.asset(f.drum, "Drum 50 m", map[string]string{"length": "50", "color": "blue", "label": "B"})
	f.mug1 = f.asset(f.mug, "Mug", nil)
	f.insp = &store.Inspection{AssetID: f.drum1.ID, InspectedOn: "2025-12-01", InspectorName: "Eli Ectric", Result: store.ResultPass,
		ProtectionClass: "I", VisualOK: true, FunctionOK: true, PEInput: "0,12", InsulationInput: "20", LeakageInput: "0.4", RecordedBy: f.admin.ID}
	if _, _, err := db.CreateInspection(ctx, f.insp, false, false); err != nil {
		t.Fatal(err)
	}
	f.full = f.token(f.admin, auth.AllScopes...)
	return f
}

func newFixture(t *testing.T) *fixture { return newFixtureCfg(t, nil) }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// asset creates an asset directly in the store.
func (f *fixture) asset(cat *store.Category, name string, vals map[string]string) *store.Asset {
	f.t.Helper()
	a := &store.Asset{CategoryID: cat.ID, Name: name, LocationID: f.loc.ID, Values: map[int64]string{}}
	for k, v := range vals {
		a.Values[f.fields[k].ID] = v
	}
	if _, err := f.db.CreateAsset(context.Background(), a, f.app.Cfg.Inventory.NumberFormat); err != nil {
		f.t.Fatal(err)
	}
	return a
}

// token creates an API token for u and returns its plaintext.
func (f *fixture) token(u *store.User, scopes ...string) string {
	f.t.Helper()
	t, _ := f.tokenRow(u, nil, scopes...)
	return t
}

func (f *fixture) tokenRow(u *store.User, exp *time.Time, scopes ...string) (string, *store.APIToken) {
	f.t.Helper()
	plain := auth.NewAPIToken()
	tok := &store.APIToken{Name: "test", Prefix: plain[:8], TokenHash: auth.HashToken(plain), Scopes: scopes, UserID: u.ID, ExpiresAt: exp}
	must(f.t, f.db.CreateAPIToken(context.Background(), tok))
	return plain, tok
}

// session creates a login session for u and returns the cookie value and CSRF token.
func (f *fixture) session(u *store.User, pending bool) (cookie, csrf string) {
	f.t.Helper()
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	cookie, csrf = hex.EncodeToString(b), "csrf-"+hex.EncodeToString(b)
	s := &store.Session{ID: auth.HashToken(cookie), UserID: u.ID, CSRFToken: csrf, ExpiresAt: time.Now().Add(time.Hour),
		Pending: pending, AuthAt: store.Now()}
	must(f.t, f.db.CreateSession(context.Background(), s))
	return cookie, csrf
}

// req describes one API call.
type req struct {
	method, path string
	token        string
	body         string
	headers      map[string]string
	cookie       string
}

func (f *fixture) do(r req) *httptest.ResponseRecorder {
	f.t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	if r.body != "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	if r.token != "" {
		hr.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.cookie != "" {
		hr.AddCookie(&http.Cookie{Name: app.SessionCookie, Value: r.cookie})
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, hr)
	return rec
}

// call is a shortcut for a bearer call.
func (f *fixture) call(method, path, token, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	return f.do(req{method: method, path: path, token: token, body: body})
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&m); err != nil {
		t.Fatalf("response is not a JSON object: %v: %s", err, rec.Body.String())
	}
	return m
}

func expect(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) map[string]any {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body.String())
	}
	if status == http.StatusNoContent {
		return nil
	}
	m := decode(t, rec)
	if code != "" {
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("content type %q, want application/problem+json", ct)
		}
		if m["code"] != code {
			t.Errorf("code %v, want %s (%s)", m["code"], code, rec.Body.String())
		}
	}
	return m
}

// lastAudit returns the newest audit entry with the action.
func (f *fixture) lastAudit(action string) *store.AuditEntry {
	f.t.Helper()
	list, err := f.db.ListAudit(context.Background(), 200)
	if err != nil {
		f.t.Fatal(err)
	}
	for i := range list {
		if list[i].Action == action {
			return &list[i]
		}
	}
	return nil
}
