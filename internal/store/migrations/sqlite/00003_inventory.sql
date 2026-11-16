-- +goose Up
-- Inventory core (phase 3): locations, categories with configurable fields,
-- assets and their field values (EAV, see docs/adr/0003).

CREATE TABLE locations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE categories (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    prefix               TEXT    NOT NULL DEFAULT '',  -- inventory number prefix, uppercase A-Z0-9, 0-8 characters; '' = none
    dguv_required        INTEGER NOT NULL DEFAULT 0,
    dguv_interval_months INTEGER NOT NULL DEFAULT 12 CHECK (dguv_interval_months BETWEEN 1 AND 120),
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

-- Prefixes are unique, but several categories may have none.
CREATE UNIQUE INDEX categories_prefix ON categories (prefix) WHERE prefix <> '';

CREATE TABLE category_fields (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    category_id INTEGER NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    key         TEXT    NOT NULL,                   -- [a-z][a-z0-9_]{0,31}, used in URLs
    label       TEXT    NOT NULL,
    type        TEXT    NOT NULL CHECK (type IN ('text', 'number', 'select', 'bool', 'date')),
    unit        TEXT    NOT NULL DEFAULT '',
    options     TEXT    NOT NULL DEFAULT '',        -- JSON array of strings, select only
    required    INTEGER NOT NULL DEFAULT 0,
    position    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (category_id, key)
);

CREATE TABLE assets (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    inventory_no         TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    category_id          INTEGER NOT NULL REFERENCES categories(id) ON DELETE RESTRICT,
    name                 TEXT    NOT NULL,
    serial_no            TEXT    NOT NULL DEFAULT '',
    manufacturer         TEXT    NOT NULL DEFAULT '',
    model                TEXT    NOT NULL DEFAULT '',
    location_id          INTEGER REFERENCES locations(id) ON DELETE RESTRICT,
    status               TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'defective', 'retired', 'lost')),
    note                 TEXT    NOT NULL DEFAULT '',
    purchase_date        TEXT,                      -- YYYY-MM-DD
    dguv_interval_months INTEGER CHECK (dguv_interval_months IS NULL OR dguv_interval_months BETWEEN 1 AND 120),
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);
CREATE INDEX assets_category ON assets (category_id);
CREATE INDEX assets_location ON assets (location_id);
CREATE INDEX assets_status   ON assets (status);
CREATE INDEX assets_serial   ON assets (serial_no);

CREATE TABLE asset_field_values (
    asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    field_id INTEGER NOT NULL REFERENCES category_fields(id) ON DELETE CASCADE,
    value    TEXT    NOT NULL,                      -- normalized per field type
    PRIMARY KEY (asset_id, field_id)
);
CREATE INDEX asset_field_values_field ON asset_field_values (field_id);

-- Next running number per category prefix (inventory number generation).
CREATE TABLE number_sequences (
    prefix TEXT    PRIMARY KEY,
    next   INTEGER NOT NULL
);

-- +goose Down
DROP TABLE number_sequences;
DROP TABLE asset_field_values;
DROP TABLE assets;
DROP TABLE category_fields;
DROP TABLE categories;
DROP TABLE locations;
