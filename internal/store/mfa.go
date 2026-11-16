package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

// MFAStatus summarises the second factors of a user.
type MFAStatus struct {
	TOTP bool
	// TOTPPending is set while an enrollment waits for the first code.
	TOTPPending bool
	Passkeys    int
	// RecoveryLeft counts unused recovery codes.
	RecoveryLeft int
}

// Enabled reports whether the user has at least one second factor.
func (m MFAStatus) Enabled() bool { return m.TOTP || m.Passkeys > 0 }

// MFAStatus returns the second-factor summary of a user.
func (db *DB) MFAStatus(ctx context.Context, userID int64) (MFAStatus, error) {
	var st MFAStatus
	var hasSecret, enabled int
	err := db.sql.QueryRowContext(ctx, `SELECT
		totp_secret IS NOT NULL, totp_enabled_at IS NOT NULL,
		(SELECT COUNT(*) FROM webauthn_credentials w WHERE w.user_id = u.id),
		(SELECT COUNT(*) FROM recovery_codes r WHERE r.user_id = u.id AND r.used_at IS NULL)
		FROM users u WHERE u.id = ?`, userID).Scan(&hasSecret, &enabled, &st.Passkeys, &st.RecoveryLeft)
	if errors.Is(err, sql.ErrNoRows) {
		return st, ErrNotFound
	}
	if err != nil {
		return st, err
	}
	st.TOTP = enabled == 1
	st.TOTPPending = hasSecret == 1 && enabled == 0
	return st, nil
}

// MFAStatusAll returns the summary for every user (for the users page).
func (db *DB) MFAStatusAll(ctx context.Context) (map[int64]MFAStatus, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT u.id, u.totp_enabled_at IS NOT NULL,
		(SELECT COUNT(*) FROM webauthn_credentials w WHERE w.user_id = u.id) FROM users u`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]MFAStatus{}
	for rows.Next() {
		var id int64
		var st MFAStatus
		if err := rows.Scan(&id, &st.TOTP, &st.Passkeys); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// TOTPSecret returns the encrypted TOTP secret, whether it is enabled, and
// the last accepted time step.
func (db *DB) TOTPSecret(ctx context.Context, userID int64) (enc []byte, enabled bool, lastStep int64, err error) {
	var en sql.NullInt64
	err = db.sql.QueryRowContext(ctx, `SELECT totp_secret, totp_enabled_at, totp_last_step FROM users WHERE id = ?`,
		userID).Scan(&enc, &en, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, 0, ErrNotFound
	}
	return enc, en.Valid, lastStep, err
}

// SetPendingTOTP stores a new, not yet confirmed TOTP secret (encrypted). It
// refuses to replace an active one.
func (db *DB) SetPendingTOTP(ctx context.Context, userID int64, enc []byte) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE users SET totp_secret = ?, totp_last_step = 0, updated_at = ?
		WHERE id = ? AND totp_enabled_at IS NULL`, enc, unix(Now()), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrExists
	}
	return nil
}

// EnableTOTP activates the pending secret after the first valid code.
func (db *DB) EnableTOTP(ctx context.Context, userID, step int64) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET totp_enabled_at = ?, totp_last_step = ?, updated_at = ?
		WHERE id = ? AND totp_secret IS NOT NULL`, unix(Now()), step, unix(Now()), userID)
	return err
}

// UseTOTPStep records an accepted time step. It fails (ErrReplay) when the
// step is not newer than the stored one, so two concurrent requests cannot
// both use the same code.
func (db *DB) UseTOTPStep(ctx context.Context, userID, step int64) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`,
		step, userID, step)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrReplay
	}
	return nil
}

// ErrReplay is returned when a one-time value was already used.
var ErrReplay = errors.New("already used")

// DisableTOTP removes the TOTP secret.
func (db *DB) DisableTOTP(ctx context.Context, userID int64) error {
	_, err := db.sql.ExecContext(ctx, `UPDATE users SET totp_secret = NULL, totp_enabled_at = NULL, totp_last_step = 0,
		updated_at = ? WHERE id = ?`, unix(Now()), userID)
	return err
}

// ReplaceRecoveryCodes deletes all recovery codes of a user and stores new
// ones (as hex MACs).
func (db *DB) ReplaceRecoveryCodes(ctx context.Context, userID int64, macs []string) error {
	return db.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
			return err
		}
		now := unix(Now())
		for _, m := range macs {
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_mac, created_at) VALUES (?, ?, ?)`,
				userID, m, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// UseRecoveryCode marks an unused code with the given MAC as used. It returns
// ErrNotFound when there is no such unused code.
func (db *DB) UseRecoveryCode(ctx context.Context, userID int64, mac string) error {
	res, err := db.sql.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_mac = ? AND used_at IS NULL`,
		unix(Now()), userID, mac)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// DeleteRecoveryCodes removes all recovery codes of a user.
func (db *DB) DeleteRecoveryCodes(ctx context.Context, userID int64) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
	return err
}

// WebAuthnHandle returns the user's WebAuthn user handle, creating a random
// one on first use.
func (db *DB) WebAuthnHandle(ctx context.Context, userID int64) ([]byte, error) {
	var h []byte
	if err := db.sql.QueryRowContext(ctx, `SELECT webauthn_handle FROM users WHERE id = ?`, userID).Scan(&h); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if len(h) > 0 {
		return h, nil
	}
	h = make([]byte, 32)
	if _, err := rand.Read(h); err != nil {
		return nil, err
	}
	if _, err := db.sql.ExecContext(ctx, `UPDATE users SET webauthn_handle = ? WHERE id = ? AND webauthn_handle IS NULL`,
		h, userID); err != nil {
		return nil, err
	}
	// Re-read in case a concurrent request set it first.
	err := db.sql.QueryRowContext(ctx, `SELECT webauthn_handle FROM users WHERE id = ?`, userID).Scan(&h)
	return h, err
}

// UserByWebAuthnHandle finds the user for a discoverable passkey login.
func (db *DB) UserByWebAuthnHandle(ctx context.Context, handle []byte) (*User, error) {
	if len(handle) == 0 {
		return nil, ErrNotFound
	}
	return scanUser(db.sql.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE webauthn_handle = ?`, handle))
}

// Passkey is a stored WebAuthn credential.
type Passkey struct {
	ID         int64
	UserID     int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	Credential webauthn.Credential
}

// ListPasskeys returns the passkeys of a user, oldest first.
func (db *DB) ListPasskeys(ctx context.Context, userID int64) ([]Passkey, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT id, user_id, name, credential, created_at, last_used_at
		FROM webauthn_credentials WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		var p Passkey
		var raw string
		var created int64
		var last sql.NullInt64
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &raw, &created, &last); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p.Credential); err != nil {
			return nil, fmt.Errorf("passkey %d: %w", p.ID, err)
		}
		p.CreatedAt = fromUnix(created)
		p.LastUsedAt = fromNullUnix(last)
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddPasskey stores a new credential.
func (db *DB) AddPasskey(ctx context.Context, userID int64, name string, c *webauthn.Credential) (int64, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	res, err := db.sql.ExecContext(ctx, `INSERT INTO webauthn_credentials (user_id, credential_id, name, credential, created_at)
		VALUES (?, ?, ?, ?, ?)`, userID, c.ID, name, string(raw), unix(Now()))
	if isUnique(err) {
		return 0, ErrExists
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdatePasskeyUse stores the credential after a login (sign counter, flags)
// and the time of use.
func (db *DB) UpdatePasskeyUse(ctx context.Context, userID int64, c *webauthn.Credential) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = db.sql.ExecContext(ctx, `UPDATE webauthn_credentials SET credential = ?, last_used_at = ?
		WHERE user_id = ? AND credential_id = ?`, string(raw), unix(Now()), userID, c.ID)
	return err
}

// DeletePasskey removes one passkey of a user.
func (db *DB) DeletePasskey(ctx context.Context, userID, id int64) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// ResetMFA removes every second factor of a user and ends their sessions
// (admin action for a lost device).
func (db *DB) ResetMFA(ctx context.Context, userID int64) error {
	return db.InTx(ctx, func(tx *Tx) error {
		for _, q := range []string{
			`UPDATE users SET totp_secret = NULL, totp_enabled_at = NULL, totp_last_step = 0 WHERE id = ?`,
			`DELETE FROM recovery_codes WHERE user_id = ?`,
			`DELETE FROM webauthn_credentials WHERE user_id = ?`,
			`DELETE FROM sessions WHERE user_id = ?`,
		} {
			if _, err := tx.tx.ExecContext(ctx, q, userID); err != nil {
				return err
			}
		}
		return nil
	})
}

// Setting keys.
const (
	// SettingMFAPolicy is who must use a second factor: off, admins, all.
	SettingMFAPolicy = "mfa_policy"
)

// MFA policies.
const (
	MFAPolicyOff    = "off"
	MFAPolicyAdmins = "admins"
	MFAPolicyAll    = "all"
)

// MFAPolicies lists the valid policies.
var MFAPolicies = []string{MFAPolicyOff, MFAPolicyAdmins, MFAPolicyAll}

// Setting returns a setting or def when it is not set.
func (db *DB) Setting(ctx context.Context, key, def string) (string, error) {
	var v string
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

// SetSetting stores a setting.
func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, unix(Now()))
	return err
}

// MFARequired reports whether the policy requires a second factor for u.
func MFARequired(policy string, u *User) bool {
	switch policy {
	case MFAPolicyAll:
		return true
	case MFAPolicyAdmins:
		return u.Can(RoleAdmin)
	}
	return false
}

// TOTPAAD is the associated data that binds an encrypted TOTP secret to its user.
func TOTPAAD(userID int64) []byte { return []byte("totp|" + strconv.FormatInt(userID, 10)) }
