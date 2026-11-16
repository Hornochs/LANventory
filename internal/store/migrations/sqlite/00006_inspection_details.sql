-- +goose Up
-- Inspection details found by comparing a real inspection protocol (step 6.6):
-- comparison qualifiers for readings beyond the instrument's range (">299"),
-- touch current, measuring method of the leakage current, inspection reason,
-- test device ID and calibration date. Existing rows keep the defaults.

ALTER TABLE dguv_inspections ADD COLUMN pe_resistance_cmp TEXT NOT NULL DEFAULT '' CHECK (pe_resistance_cmp IN ('', '<', '>'));
ALTER TABLE dguv_inspections ADD COLUMN insulation_cmp TEXT NOT NULL DEFAULT '' CHECK (insulation_cmp IN ('', '<', '>'));
ALTER TABLE dguv_inspections ADD COLUMN leakage_cmp TEXT NOT NULL DEFAULT '' CHECK (leakage_cmp IN ('', '<', '>'));
ALTER TABLE dguv_inspections ADD COLUMN touch_ma REAL CHECK (touch_ma IS NULL OR touch_ma >= 0);
ALTER TABLE dguv_inspections ADD COLUMN touch_cmp TEXT NOT NULL DEFAULT '' CHECK (touch_cmp IN ('', '<', '>'));
ALTER TABLE dguv_inspections ADD COLUMN leakage_method TEXT NOT NULL DEFAULT '' CHECK (leakage_method IN ('', 'direct', 'differential', 'alternative'));
ALTER TABLE dguv_inspections ADD COLUMN reason TEXT NOT NULL DEFAULT '' CHECK (reason IN ('', 'initial', 'repeat', 'repair'));
ALTER TABLE dguv_inspections ADD COLUMN test_device_id TEXT NOT NULL DEFAULT '';
ALTER TABLE dguv_inspections ADD COLUMN test_device_calibrated_until TEXT;       -- YYYY-MM-DD, NULL = not recorded

-- +goose Down
ALTER TABLE dguv_inspections DROP COLUMN test_device_calibrated_until;
ALTER TABLE dguv_inspections DROP COLUMN test_device_id;
ALTER TABLE dguv_inspections DROP COLUMN reason;
ALTER TABLE dguv_inspections DROP COLUMN leakage_method;
ALTER TABLE dguv_inspections DROP COLUMN touch_cmp;
ALTER TABLE dguv_inspections DROP COLUMN touch_ma;
ALTER TABLE dguv_inspections DROP COLUMN leakage_cmp;
ALTER TABLE dguv_inspections DROP COLUMN insulation_cmp;
ALTER TABLE dguv_inspections DROP COLUMN pe_resistance_cmp;
