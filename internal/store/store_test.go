package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// openTest returns a migrated database in a temporary directory.
func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(Options{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRoleLevels(t *testing.T) {
	if !(RoleLevel(RoleViewer) < RoleLevel(RoleEditor) && RoleLevel(RoleEditor) < RoleLevel(RoleAdmin) && RoleLevel("x") == 0) {
		t.Fatal("role order broken")
	}
	var nilUser *User
	if nilUser.Can(RoleViewer) {
		t.Fatal("nil user has a role")
	}
	ed := &User{Role: RoleEditor}
	if !ed.Can(RoleViewer) || !ed.Can(RoleEditor) || ed.Can(RoleAdmin) {
		t.Fatal("editor permissions wrong")
	}
}

func TestUsersAndSessions(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	u := &User{Username: "Admin", PasswordHash: "x", Role: RoleAdmin, CanInspect: true}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(ctx, &User{Username: "admin", PasswordHash: "y", Role: RoleViewer}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate (case-insensitive) username: %v", err)
	}
	if err := db.CreateUser(ctx, &User{Username: "bob", PasswordHash: "y", Role: "root"}); err == nil {
		t.Fatal("invalid role accepted")
	}
	got, err := db.UserByName(ctx, "ADMIN")
	if err != nil || got.ID != u.ID || !got.Can(RoleAdmin) || !got.Can(RoleViewer) || !got.CanInspect {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if n, _ := db.CountUsers(ctx); n != 1 {
		t.Fatalf("users: %d", n)
	}

	s := &Session{ID: "h1", UserID: u.ID, CSRFToken: "c", ExpiresAt: Now().Add(time.Hour)}
	if err := db.CreateSession(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.GetSession(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if !db.SessionExists(ctx, "h1") {
		t.Fatal("session missing")
	}
	// A password change ends all sessions.
	if err := db.SetPassword(ctx, u.ID, "z"); err != nil {
		t.Fatal(err)
	}
	if db.SessionExists(ctx, "h1") {
		t.Fatal("session survived a password change")
	}
	// Disabling a user ends sessions too.
	_ = db.CreateSession(ctx, &Session{ID: "h2", UserID: u.ID, CSRFToken: "c", ExpiresAt: Now().Add(time.Hour)})
	if err := db.UpdateUser(ctx, u.ID, RoleAdmin, true, true); err != nil {
		t.Fatal(err)
	}
	if db.SessionExists(ctx, "h2") {
		t.Fatal("session survived disabling the user")
	}
	if n, _ := db.CountAdmins(ctx); n != 0 {
		t.Fatalf("disabled admin counted: %d", n)
	}
	// Expired sessions are not valid and get purged.
	_ = db.CreateSession(ctx, &Session{ID: "old", UserID: u.ID, CSRFToken: "c", ExpiresAt: Now().Add(-time.Minute)})
	if db.SessionExists(ctx, "old") {
		t.Fatal("expired session valid")
	}
	if err := db.PurgeSessions(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestUserPrefsAndDelete(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	u := &User{Username: "eve", PasswordHash: "x", Role: RoleEditor}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserPrefs(ctx, u.ID, "de", "dark"); err != nil {
		t.Fatal(err)
	}
	got, _ := db.UserByID(ctx, u.ID)
	if got.Lang != "de" || got.Theme != "dark" || got.CanInspect {
		t.Fatalf("prefs: %+v", got)
	}
	if err := db.TouchLogin(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = db.UserByID(ctx, u.ID); got.LastLoginAt == nil {
		t.Fatal("last login not set")
	}
	if err := db.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UserByID(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted user: %v", err)
	}
}

func TestAuditAndBackup(t *testing.T) {
	ctx := context.Background()
	db := openTest(t)
	u := &User{Username: "admin", PasswordHash: "x", Role: RoleAdmin}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := db.Audit(ctx, AuditEntry{Action: "user.create", ObjectType: "user", ObjectID: "1",
		Details: map[string]any{"actor": "cli", "role": "admin"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Audit(ctx, AuditEntry{UserID: u.ID, Action: "login", ObjectType: "user", ObjectID: "1"}); err != nil {
		t.Fatal(err)
	}
	entries, err := db.ListAudit(ctx, 10)
	if err != nil || len(entries) != 2 || entries[1].Action != "user.create" || entries[1].Actor() != "cli" ||
		entries[0].Actor() != "admin" {
		t.Fatalf("audit: %+v %v", entries, err)
	}
	// Deleting the user keeps the log line (user_id becomes NULL).
	if err := db.DeleteUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if entries, _ = db.ListAudit(ctx, 10); len(entries) != 2 || entries[0].UserID != 0 {
		t.Fatalf("audit after user delete: %+v", entries)
	}
	target := filepath.Join(t.TempDir(), "backup.db")
	if err := db.Backup(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := db.Backup(ctx, target); err == nil {
		t.Fatal("backup overwrote an existing file")
	}
	v, err := db.SchemaVersion(ctx)
	if err != nil || v < 1 {
		t.Fatalf("schema version %d %v", v, err)
	}
}
