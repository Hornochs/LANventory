-- +goose Up
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('viewer', 'editor', 'admin')),
    can_inspect   INTEGER NOT NULL DEFAULT 0,      -- authorized to record DGUV inspections
    theme         TEXT    NOT NULL DEFAULT 'auto', -- auto, light, dark
    lang          TEXT    NOT NULL DEFAULT 'en',   -- de, en
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,                -- Unix seconds, UTC
    updated_at    INTEGER NOT NULL,
    last_login_at INTEGER
);

CREATE TABLE sessions (
    id         TEXT    PRIMARY KEY,  -- SHA-256 of the cookie value
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER NOT NULL,
    user_id     INTEGER REFERENCES users(id) ON DELETE SET NULL,
    action      TEXT    NOT NULL,
    object_type TEXT    NOT NULL DEFAULT '',
    object_id   TEXT    NOT NULL DEFAULT '',
    details     TEXT    NOT NULL DEFAULT '{}'  -- JSON; never secrets
);
CREATE INDEX audit_log_ts ON audit_log (ts);

-- +goose Down
DROP TABLE audit_log;
DROP TABLE sessions;
DROP TABLE users;
