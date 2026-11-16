package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// APIToken is a bearer token of the REST API. Only the hash of the secret is
// stored. A token acts for its owner (UserID).
type APIToken struct {
	ID        int64
	Name      string
	Prefix    string
	TokenHash string
	Scopes    []string
	UserID    int64
	// OwnerName is filled by the list functions.
	OwnerName  string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	Revoked    bool
}

// HasScope reports whether the token carries scope.
func (t *APIToken) HasScope(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// Expired reports whether the token has expired at now.
func (t *APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && !now.Before(*t.ExpiresAt)
}

const tokenCols = `t.id, t.name, t.prefix, t.token_hash, t.scopes, t.user_id, COALESCE(u.username, ''), t.created_at, t.expires_at,
	t.last_used_at, t.revoked`
const tokenFrom = ` FROM api_tokens t JOIN users u ON u.id = t.user_id`

func scanToken(row interface{ Scan(...any) error }) (*APIToken, error) {
	var t APIToken
	var scopes string
	var created int64
	var exp, used sql.NullInt64
	var revoked int
	err := row.Scan(&t.ID, &t.Name, &t.Prefix, &t.TokenHash, &scopes, &t.UserID, &t.OwnerName, &created, &exp, &used, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.Scopes = strings.Fields(scopes)
	t.CreatedAt = fromUnix(created)
	t.ExpiresAt = fromNullUnix(exp)
	t.LastUsedAt = fromNullUnix(used)
	t.Revoked = revoked != 0
	return &t, nil
}

// CreateAPIToken stores a token (TokenHash, Prefix, Scopes and UserID set).
func (db *DB) CreateAPIToken(ctx context.Context, t *APIToken) error {
	t.CreatedAt = Now()
	var exp any
	if t.ExpiresAt != nil {
		exp = unix(*t.ExpiresAt)
	}
	res, err := db.sql.ExecContext(ctx, `INSERT INTO api_tokens (name, prefix, token_hash, scopes, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, t.Name, t.Prefix, t.TokenHash, strings.Join(t.Scopes, " "), t.UserID, unix(t.CreatedAt), exp)
	if err != nil {
		return err
	}
	t.ID, err = res.LastInsertId()
	return err
}

// APITokenByHash returns the token with the given SHA-256 hash.
func (db *DB) APITokenByHash(ctx context.Context, hash string) (*APIToken, error) {
	return scanToken(db.sql.QueryRowContext(ctx, `SELECT `+tokenCols+tokenFrom+` WHERE t.token_hash = ?`, hash))
}

// APITokenByID returns a token by ID.
func (db *DB) APITokenByID(ctx context.Context, id int64) (*APIToken, error) {
	return scanToken(db.sql.QueryRowContext(ctx, `SELECT `+tokenCols+tokenFrom+` WHERE t.id = ?`, id))
}

func (db *DB) listTokens(ctx context.Context, where string, args ...any) ([]*APIToken, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT `+tokenCols+tokenFrom+where+` ORDER BY t.revoked, t.created_at DESC, t.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListAPITokens returns all tokens (admin view).
func (db *DB) ListAPITokens(ctx context.Context) ([]*APIToken, error) {
	return db.listTokens(ctx, "")
}

// ListUserAPITokens returns the tokens of one user.
func (db *DB) ListUserAPITokens(ctx context.Context, userID int64) ([]*APIToken, error) {
	return db.listTokens(ctx, ` WHERE t.user_id = ?`, userID)
}

// RevokeAPIToken revokes a token immediately. It returns ErrNotFound for an
// unknown ID; revoking twice is not an error.
func (db *DB) RevokeAPIToken(ctx context.Context, id int64) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE api_tokens SET revoked = 1 WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAPIToken records the use of a token (at most once per minute).
func (db *DB) TouchAPIToken(ctx context.Context, id int64) error {
	now := unix(Now())
	_, err := db.sql.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now, id, now-60)
	return err
}
