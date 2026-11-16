-- +goose Up
-- API tokens (phase 5). Only the SHA-256 hash of a token is stored; the
-- plaintext is shown once at creation. A token acts for its owner with at most
-- the owner's current rights, limited further by its scopes.

CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL,
    prefix       TEXT    NOT NULL,                 -- first characters, for recognition
    token_hash   TEXT    NOT NULL UNIQUE,          -- SHA-256 hex of the token
    scopes       TEXT    NOT NULL,                 -- space-separated
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,                          -- NULL = never
    last_used_at INTEGER,
    revoked      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_api_tokens_user ON api_tokens (user_id);

-- +goose Down
DROP TABLE api_tokens;
