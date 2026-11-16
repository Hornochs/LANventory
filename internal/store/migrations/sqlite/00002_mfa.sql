-- +goose Up
-- Second factors (phase 2). Secrets: totp_secret is AES-GCM encrypted with
-- secret.key (associated data "totp|<user id>"); recovery codes are stored as
-- HMACs under a subkey of secret.key. Passkeys store only public keys.
ALTER TABLE users ADD COLUMN totp_secret BLOB;                          -- NULL = no TOTP; set but not enabled = enrollment pending
ALTER TABLE users ADD COLUMN totp_enabled_at INTEGER;                   -- Unix seconds; NULL = not active
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0; -- last accepted time step (replay protection)
ALTER TABLE users ADD COLUMN webauthn_handle BLOB;                      -- random WebAuthn user handle, created on first passkey
CREATE UNIQUE INDEX users_webauthn_handle ON users (webauthn_handle) WHERE webauthn_handle IS NOT NULL;

-- pending = 1: password accepted, second factor still missing; such a session
-- grants access to the second-factor page only.
ALTER TABLE sessions ADD COLUMN pending INTEGER NOT NULL DEFAULT 0;
-- auth_at: time of the last full authentication (login or re-authentication).
ALTER TABLE sessions ADD COLUMN auth_at INTEGER NOT NULL DEFAULT 0;

CREATE TABLE recovery_codes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_mac   TEXT    NOT NULL,  -- hex HMAC-SHA256 of the normalised code
    created_at INTEGER NOT NULL,
    used_at    INTEGER
);
CREATE INDEX recovery_codes_user ON recovery_codes (user_id);

CREATE TABLE webauthn_credentials (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id BLOB    NOT NULL UNIQUE,
    name          TEXT    NOT NULL,
    credential    TEXT    NOT NULL,  -- JSON of webauthn.Credential (public key, flags, sign count)
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER
);
CREATE INDEX webauthn_credentials_user ON webauthn_credentials (user_id);

-- Instance settings changed in the UI (for example the MFA policy).
CREATE TABLE settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE settings;
DROP TABLE webauthn_credentials;
DROP TABLE recovery_codes;
ALTER TABLE sessions DROP COLUMN auth_at;
ALTER TABLE sessions DROP COLUMN pending;
DROP INDEX users_webauthn_handle;
ALTER TABLE users DROP COLUMN webauthn_handle;
ALTER TABLE users DROP COLUMN totp_last_step;
ALTER TABLE users DROP COLUMN totp_enabled_at;
ALTER TABLE users DROP COLUMN totp_secret;
