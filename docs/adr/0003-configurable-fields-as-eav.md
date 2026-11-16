# 0003 – Configurable category fields stored as EAV

**Status:** accepted

## Context

Every category needs its own fields (length for a cable drum, number of ports for a switch),
and admins define them at run time. Options: a fixed schema with many nullable columns, one
JSON column per asset, or entity-attribute-value (EAV) rows.

## Decision

Field definitions live in `category_fields`; values live in `asset_field_values` with one row
per asset and field (`asset_id`, `field_id`, `value`). Values are validated against the field
type (text, number, select, bool, date) before they are written.

## Consequences

- Validation per type happens in one place and values stay consistent when a definition is
  edited.
- Values can be filtered and sorted with ordinary SQL, which JSON columns in SQLite make
  awkward and slow.
- Adding or removing a field needs no schema migration.
- Reading an asset needs a second query (or a join), and values are stored as text and
  converted; acceptable at this scale.
- Changing a field's type needs an explicit rule for existing values.
