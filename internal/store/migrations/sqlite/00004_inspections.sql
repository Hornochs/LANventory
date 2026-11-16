-- +goose Up
-- DGUV V3 inspections (phase 4). Inspections are records: they are created and
-- may be deleted by an admin, but never edited.

CREATE TABLE dguv_inspections (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    asset_id          INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
    inspected_on      TEXT    NOT NULL,                 -- YYYY-MM-DD
    inspector_name    TEXT    NOT NULL,                 -- may be an external electrician
    recorded_by       INTEGER REFERENCES users(id) ON DELETE SET NULL,
    result            TEXT    NOT NULL CHECK (result IN ('pass', 'fail')),
    protection_class  TEXT    CHECK (protection_class IS NULL OR protection_class IN ('I', 'II', 'III')),
    visual_ok         INTEGER NOT NULL DEFAULT 0,       -- visual inspection passed
    function_ok       INTEGER NOT NULL DEFAULT 0,       -- functional test passed
    pe_resistance_ohm REAL    CHECK (pe_resistance_ohm IS NULL OR pe_resistance_ohm >= 0),
    insulation_mohm   REAL    CHECK (insulation_mohm IS NULL OR insulation_mohm >= 0),
    leakage_ma        REAL    CHECK (leakage_ma IS NULL OR leakage_ma >= 0),
    test_device       TEXT    NOT NULL DEFAULT '',
    remark            TEXT    NOT NULL DEFAULT '',
    interval_months   INTEGER NOT NULL,                 -- the interval that was applied
    next_due          TEXT    NOT NULL,                 -- YYYY-MM-DD
    created_at        INTEGER NOT NULL
);
CREATE INDEX idx_dguv_inspections_asset ON dguv_inspections (asset_id, inspected_on);
CREATE INDEX idx_dguv_inspections_date ON dguv_inspections (inspected_on);

-- +goose Down
DROP TABLE dguv_inspections;
