package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// User roles, from least to most privileged.
const (
	// RoleViewer may look at the inventory.
	RoleViewer = "viewer"
	// RoleEditor may also change the inventory.
	RoleEditor = "editor"
	// RoleAdmin also manages users, categories and settings.
	RoleAdmin = "admin"
)

// Roles lists the valid roles in ascending order.
var Roles = []string{RoleViewer, RoleEditor, RoleAdmin}

// RoleLevel orders roles for permission checks: viewer < editor < admin.
func RoleLevel(role string) int {
	switch role {
	case RoleViewer:
		return 1
	case RoleEditor:
		return 2
	case RoleAdmin:
		return 3
	}
	return 0
}

// ErrExists is returned when a unique name is taken.
var ErrExists = errors.New("already exists")

// User is a web user.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	// CanInspect marks users authorized to record DGUV inspections.
	CanInspect bool
	// Theme is the UI color theme: auto, light or dark.
	Theme       string
	Lang        string
	Disabled    bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastLoginAt *time.Time
}

// Can reports whether the user has at least the given role.
func (u *User) Can(role string) bool { return u != nil && RoleLevel(u.Role) >= RoleLevel(role) }

const userCols = `id, username, password_hash, role, can_inspect, theme, lang, disabled, created_at, updated_at, last_login_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var created, updated int64
	var last sql.NullInt64
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.CanInspect, &u.Theme, &u.Lang, &u.Disabled,
		&created, &updated, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = fromUnix(created)
	u.UpdatedAt = fromUnix(updated)
	u.LastLoginAt = fromNullUnix(last)
	return &u, nil
}

func isUnique(err error) bool { return err != nil && strings.Contains(err.Error(), "UNIQUE") }

// CreateUser inserts a user.
func (db *DB) CreateUser(ctx context.Context, u *User) error {
	if RoleLevel(u.Role) == 0 {
		return errors.New("invalid role")
	}
	u.Username = strings.TrimSpace(u.Username)
	if u.Username == "" || len(u.Username) > 64 {
		return errors.New("username must have 1 to 64 characters")
	}
	if u.Lang == "" {
		u.Lang = "en"
	}
	if u.Theme == "" {
		u.Theme = "auto"
	}
	u.CreatedAt = Now()
	u.UpdatedAt = u.CreatedAt
	res, err := db.sql.ExecContext(ctx, `INSERT INTO users (username, password_hash, role, can_inspect, theme, lang,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, u.Username, u.PasswordHash, u.Role,
		boolInt(u.CanInspect), u.Theme, u.Lang, unix(u.CreatedAt), unix(u.UpdatedAt))
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	u.ID, err = res.LastInsertId()
	return err
}

// UserByName returns a user by username (case-insensitive).
func (db *DB) UserByName(ctx context.Context, name string) (*User, error) {
	return scanUser(db.sql.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, strings.TrimSpace(name)))
}

// UserByID returns a user.
func (db *DB) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(db.sql.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// ListUsers returns all users.
func (db *DB) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of users.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountAdmins returns the number of enabled admins.
func (db *DB) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&n)
	return n, err
}

// UpdateUser saves role, inspection authorization and disabled state.
// Disabling ends all sessions.
func (db *DB) UpdateUser(ctx context.Context, id int64, role string, canInspect, disabled bool) error {
	if RoleLevel(role) == 0 {
		return errors.New("invalid role")
	}
	return db.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, `UPDATE users SET role = ?, can_inspect = ?, disabled = ?, updated_at = ?
			WHERE id = ?`, role, boolInt(canInspect), boolInt(disabled), unix(Now()), id); err != nil {
			return err
		}
		if !disabled {
			return nil
		}
		_, err := tx.tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		return err
	})
}

// SetUserPrefs changes language and theme of a user.
func (db *DB) SetUserPrefs(ctx context.Context, id int64, lang, theme string) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET lang = ?, theme = ?, updated_at = ? WHERE id = ?`,
		lang, theme, unix(Now()), id)
	return err
}

// Themes are the valid UI color themes.
var Themes = []string{"auto", "light", "dark"}

// ValidTheme reports whether t is a known theme.
func ValidTheme(t string) bool {
	for _, v := range Themes {
		if t == v {
			return true
		}
	}
	return false
}

// SetPassword replaces the password hash and ends all sessions of the user.
func (db *DB) SetPassword(ctx context.Context, id int64, hash string) error {
	return db.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
			hash, unix(Now()), id); err != nil {
			return err
		}
		_, err := tx.tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		return err
	})
}

// DeleteUser removes a user and their sessions.
func (db *DB) DeleteUser(ctx context.Context, id int64) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// TouchLogin records a successful login.
func (db *DB) TouchLogin(ctx context.Context, id int64) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, unix(Now()), id)
	return err
}

// Session is a login session. ID is the SHA-256 hash of the cookie value.
type Session struct {
	ID        string
	UserID    int64
	CSRFToken string
	ExpiresAt time.Time
	// Pending marks a session whose password was accepted but whose second
	// factor is still missing. It grants access to the second-factor page only.
	Pending bool
	// AuthAt is the time of the last full authentication (login or
	// re-authentication); sensitive actions require it to be recent.
	AuthAt time.Time
}

// CreateSession stores a session.
func (db *DB) CreateSession(ctx context.Context, s *Session) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO sessions (id, user_id, csrf_token, created_at, expires_at, pending, auth_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.UserID, s.CSRFToken, unix(Now()), unix(s.ExpiresAt), boolInt(s.Pending), unix(s.AuthAt))
	return err
}

// GetSession returns a valid session with its user.
func (db *DB) GetSession(ctx context.Context, id string) (*Session, *User, error) {
	var s Session
	var exp, authAt int64
	err := db.sql.QueryRowContext(ctx, `SELECT id, user_id, csrf_token, expires_at, pending, auth_at FROM sessions
		WHERE id = ? AND expires_at > ?`,
		id, unix(Now())).Scan(&s.ID, &s.UserID, &s.CSRFToken, &exp, &s.Pending, &authAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	s.ExpiresAt = fromUnix(exp)
	s.AuthAt = fromUnix(authAt)
	u, err := db.UserByID(ctx, s.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u.Disabled {
		return nil, nil, ErrNotFound
	}
	return &s, u, nil
}

// SessionExists reports whether a session is still valid.
func (db *DB) SessionExists(ctx context.Context, id string) bool {
	var n int
	err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id = ? AND s.expires_at > ? AND u.disabled = 0`, id, unix(Now())).Scan(&n)
	return err == nil && n == 1
}

// ExtendSession moves the expiry of a session.
func (db *DB) ExtendSession(ctx context.Context, id string, expires time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE id = ?`, unix(expires), id)
	return err
}

// SetSessionAuth records a re-authentication.
func (db *DB) SetSessionAuth(ctx context.Context, id string, at time.Time) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE sessions SET auth_at = ? WHERE id = ?`, unix(at), id)
	return err
}

// DeleteUserSessions ends all sessions of a user except keep (may be "").
func (db *DB) DeleteUserSessions(ctx context.Context, userID int64, keep string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id <> ?`, userID, keep)
	return err
}

// DeleteSession ends a session.
func (db *DB) DeleteSession(ctx context.Context, id string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// PurgeSessions deletes expired sessions.
func (db *DB) PurgeSessions(ctx context.Context) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, unix(Now()))
	return err
}

// AuditEntry is one line of the audit log. UserID 0 means no user (system,
// CLI or an unauthenticated request). Details are marshalled to JSON; never
// put secrets into them. The keys "actor" and "ip" are used by the UI.
type AuditEntry struct {
	ID         int64
	TS         time.Time
	UserID     int64
	Username   string // filled by ListAudit from the users table
	Action     string
	ObjectType string
	ObjectID   string
	Details    map[string]any
	// RawDetails is the stored JSON (set by ListAudit).
	RawDetails string
}

// Actor returns the user name, or the "actor" detail (for example "cli").
func (e AuditEntry) Actor() string {
	if e.Username != "" {
		return e.Username
	}
	if e.Details != nil {
		if a, ok := e.Details["actor"].(string); ok {
			return a
		}
	}
	return ""
}

// Audit writes an audit log entry.
func (db *DB) Audit(ctx context.Context, e AuditEntry) error {
	var uid any
	if e.UserID != 0 {
		uid = e.UserID
	}
	d := []byte("{}")
	if len(e.Details) > 0 {
		var err error
		if d, err = json.Marshal(e.Details); err != nil {
			return err
		}
	}
	_, err := db.sql.ExecContext(ctx, `INSERT INTO audit_log (ts, user_id, action, object_type, object_id, details)
		VALUES (?, ?, ?, ?, ?, ?)`, unix(Now()), uid, e.Action, e.ObjectType, e.ObjectID, string(d))
	return err
}

// ListAudit returns the newest audit entries.
func (db *DB) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT a.id, a.ts, COALESCE(a.user_id, 0), COALESCE(u.username, ''),
		a.action, a.object_type, a.object_id, a.details
		FROM audit_log a LEFT JOIN users u ON u.id = a.user_id ORDER BY a.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts int64
		if err := rows.Scan(&e.ID, &ts, &e.UserID, &e.Username, &e.Action, &e.ObjectType, &e.ObjectID, &e.RawDetails); err != nil {
			return nil, err
		}
		e.TS = fromUnix(ts)
		_ = json.Unmarshal([]byte(e.RawDetails), &e.Details) // a malformed row is shown raw
		out = append(out, e)
	}
	return out, rows.Err()
}

// IP returns the client address recorded with the entry, if any.
func (e AuditEntry) IP() string {
	if e.Details != nil {
		if ip, ok := e.Details["ip"].(string); ok {
			return ip
		}
	}
	return ""
}
