# Concepts

The domain model of LANventory. Tables and routes are described in
[architecture.md](architecture.md); this page explains what the words mean. Everything described
here is implemented; ideas for later are in [ROADMAP.md](ROADMAP.md).

## Assets

An asset is **one physical item** that you own and can point at: a power strip, a switch, a
cable drum, a monitor. It has

- an inventory number (unique),
- a category,
- a name, and optionally manufacturer, model and serial number,
- a location and a status,
- optional purchase date and a free note,
- the custom fields defined by its category (for example the length of a cable drum).

LANventory tracks **single assets only**. A box of 50 identical patch cables is either 50
assets or not in the inventory at all; there are no quantities or stock levels
(see [ADR 0004](adr/0004-single-assets-only.md)). The benefit is that every item has its own
history, status and inspection record, which DGUV V3 requires anyway.

## Categories and field definitions

A category groups similar assets and defines what you record about them:

- a **name** and an optional **prefix** for inventory numbers (for example `SL`),
- whether **DGUV V3 inspection** is required and the default **interval in months** (default 12),
- any number of **field definitions**.

A field definition has a key, a label, a type, an optional unit, a required flag and an order.
Types:

| Type | Value | Notes |
|---|---|---|
| `text` | free text | |
| `number` | decimal number | optional unit, for example `m` for metres |
| `select` | one of a fixed list of options | |
| `bool` | yes / no | |
| `date` | calendar date | |

Values are validated against the type when an asset is saved. They are stored one row per
value ([ADR 0003](adr/0003-configurable-fields-as-eav.md)), so they can be filtered and
searched. Numbers are numbers: the unit lives in the field definition, not in the value.
Seeded templates (power strip, cable drum, power cable, network cable, switch, monitor,
PC/server; German names) are created once on first start and can be edited or deleted; deleted
ones do not come back.

Rules of the field editor: a field's key is fixed after creation; its type cannot change while
values exist; a select option that is in use cannot be removed; deleting a field deletes its
values; a category cannot be deleted while assets use it. Numbers accept a comma as decimal
separator and are stored with a dot.

The category of an existing asset can be changed in the edit form (or via the API): values move
to fields of the new category with the same key when they are valid there, the others are dropped
and listed before saving; the inventory number and the inspections stay.

## Inventory numbers

Numbers are generated from `inventory.number_format`, default `{prefix}-{seq:05}`:
`{prefix}` is the category prefix, `{seq:05}` a running number padded to five digits, giving
`SL-00042`. The format must contain `{seq}`.

- **Without prefix:** the prefix is optional. A category without prefix drops `{prefix}` and the
  separator after it (`-`, `_`, `/`, `.` or a space), so the default format gives `00042`.
  Categories with a prefix have one sequence per prefix; all categories without prefix share one.
- **Plain running numbers:** a format without `{prefix}`, for example `{seq:04}` for `0001`,
  `0002`, …, uses a single sequence for all categories, whatever their prefixes.
- **Existing numbers:** numbers that are already taken (entered by hand or imported) are skipped,
  so after importing `0001`–`0250` the next generated number is `0251`.

A number can be **overridden manually** when
creating an asset, for example to adopt an existing labelling scheme; it must stay unique.
Changing the format later, or a category's prefix, does not renumber existing assets. Numbers
are reserved in the same transaction that creates the asset; a number that is already taken
(for example a manual one) is skipped. A manual number cannot be changed after creation.

## Locations

Locations are a **flat list** such as "Storage 1" and "Storage 2", with an optional
description. There is no hierarchy (room, shelf, box). An asset has at most one location.

## Status

| Status | Meaning |
|---|---|
| `active` | in use or in stock, available |
| `defective` | broken, waiting for repair; failed inspections lead here |
| `retired` | taken out of service on purpose; kept for the records |
| `lost` | cannot be found |

Assets are not deleted when they leave the inventory; they are retired or marked lost, so the
history stays intact. Status changes are written to the asset's change history.

## DGUV V3 inspections

In Germany, portable electrical equipment used in a workplace or at events must be inspected
periodically according to DGUV Vorschrift 3, using the test procedures of DIN VDE 0701-0702.
LANventory supports the paperwork:

- A category can require inspection and sets the **interval** in months (default 12). A single
  asset can override the interval.
- An **inspection** records the date, the inspector's name (prefilled with the current user; may
  be an external electrician), the result (**pass** or **fail**, decided by the inspector), the
  protection class, whether the visual inspection and the functional test passed, the
  **reason** (before first use, repeat inspection, after repair; the form preselects "repeat"
  for an asset that has inspections, "initial" otherwise; empty = not recorded), the
  measurements (protective conductor resistance in ohm, insulation resistance in megohm,
  protective conductor or leakage current in milliampere with its measuring method direct,
  differential or alternative (substitute leakage current), and the **touch current** in
  milliampere; all optional, non-negative, comma or dot), the **test device** with its ID or
  serial number and calibration date, and a remark. The applied interval and the next due date
  are stored with it. The form prefills the test device, ID and calibration date from the
  user's last inspection and shows a non-blocking hint when the calibration ended before the
  inspection date.
- **Readings beyond the instrument's range.** A measurement may start with `>` or `<` (space,
  comma or dot decimals allowed): `>299` MOhm, `< 0,01` mA. The number is stored as a number and
  the qualifier next to it (`pe_resistance_cmp`, `insulation_cmp`, `leakage_cmp`, `touch_cmp`:
  empty, `<` or `>`); it is displayed as "> 299 MΩ". Guidance compares the number as it is (">299"
  insulation is not low, "<0,01" leakage is not high). The API takes numbers plus the `*_cmp`
  properties, and also a string such as `">299"` for the measurement.
- The **next due date** is the inspection date plus the interval in calendar months; a day that
  does not exist in the target month moves to its last day (31 January + 1 month = 28 or 29
  February). Due states: *overdue* when the due date is before today, *due soon* when it is
  today or within the next 30 days, *never inspected* without any inspection. Retired and lost
  assets, and categories without DGUV, are not listed as due; defective assets are.
- **Inspections are records**: they cannot be edited. Only an admin can delete one (audited).
- Only users with the `can_inspect` flag can record inspections; the flag is independent of the
  role. Only assets of categories that require DGUV V3 and are not retired or lost can be
  inspected. A failed inspection can set the asset to defective in the same step (opt-in, off by default); a passed one
  can set a defective asset back to active.
- **Guidance hints.** LANventory never decides pass or fail. It only shows a hint next to
  values outside simplified limits in the spirit of DIN VDE 0701-0702: protective conductor
  resistance above 0.3 ohm, insulation resistance below 1 megohm (class I) or 2 megohm (class
  II), protective conductor / leakage current above 3.5 mA (class I), touch current above 0.5 mA
  (classes I and II). These limits are simplified and live in one place (`internal/store/dguv.go`); the
  standard's detailed rules (for example the cable length) apply and the inspector decides.
- A **protocol** (`/inspections/{id}/print`) can be printed per inspection; the list of
  inspections can be exported as CSV with the same filters.

**Disclaimer:** LANventory documents inspections and reminds you of due dates. It does not
perform or validate them and does not replace a qualified electrician (Elektrofachkraft).
You remain responsible for the safety of your equipment and for complying with the rules that
apply to you.

## CSV import and export

**Export.** `/assets.csv` (every role) downloads the assets of the list with the same filters
and sort (`q`, `category`, `location`, `status`, `f_<key>`, `sort`, `dir`), all matching rows up
to 50 000. UTF-8 without BOM, comma, CRLF, one header row. Columns, in this order:

| Column | Content |
|---|---|
| `inventory_no`, `category`, `category_prefix`, `name`, `serial_no`, `manufacturer`, `model` | as stored |
| `location` | location name |
| `status` | `active`, `defective`, `retired`, `lost` |
| `purchase_date` | `YYYY-MM-DD` |
| `dguv_interval_months` | the asset's own override, empty if none |
| `note` | free text |
| `last_inspected_on`, `next_due` | latest inspection; `next_due` empty without DGUV requirement or inspection |
| `field:<key>` | one column per custom field of the categories in the result (sorted by category name, then position; empty where the field does not apply); numbers with a dot, booleans `1`/`0`, dates `YYYY-MM-DD` |

Every cell that starts with `=`, `+`, `-`, `@`, tab or CR gets a leading apostrophe so that
spreadsheets do not run it as a formula (this also applies to the inspection export); the
import removes that apostrophe again.

**Import.** Editors and admins upload a CSV file (at most 5 MB, 20 000 rows) at
`/assets/import`; the first row must be the header. Delimiter (comma, semicolon, tab) and
encoding (UTF-8, Windows-1252, as written by Excel on German Windows) are detected unless
chosen. The mapping step assigns each column to *ignore*, a core column or `field:<key>`
(pre-selected by our export headers, German names such as Inventarnummer, Kategorie,
Bezeichnung, Seriennummer, Hersteller, Modell, Standort, Status, Kaufdatum, Bemerkung, and field
labels such as "Länge"). Options: default category and location, "all or nothing" or "skip invalid
rows", create missing locations, and what to do with an existing inventory number (skip or
update). The preview lists the first 200 rows with the action (create, update, skip, error) and a
message per faulty cell, then everything is written in one transaction.

Accepted values: category by name or prefix; status in English or German (aktiv, defekt,
ausgemustert, verloren); booleans 1/0, ja/nein, yes/no, true/false, x; numbers with comma or dot;
dates `YYYY-MM-DD` or `DD.MM.YYYY`. Everything passes the same validation as the forms. An empty
inventory number is generated; a number that occurs twice in the file is an error for the later
rows. With *update*, an explicit category must match the existing asset; mapped empty cells clear
values. The export of the asset list can be imported again unchanged.

**Past inspections in the CSV import.** Optional inspection columns turn a row into asset plus
one inspection, like a row of an electrician's inspection protocol: `inspected_on`,
`inspector_name`, `result`, `reason`, `protection_class`, `visual_ok`, `function_ok`,
`pe_resistance_ohm`, `insulation_mohm`, `leakage_ma`, `leakage_method`, `touch_ma`,
`test_device`, `test_device_id`, `test_device_calibrated_until`, `remark`. German headers are
mapped automatically (Prüfdatum, Prüfer, Ergebnis, Prüfanlass, Schutzklasse,
Sichtprüfung/Besichtigung, Funktionsprüfung, RPE (protective conductor resistance), Riso
(insulation), ISL/IPE (protective conductor current), Verfahren/Messverfahren, IB (touch current),
Prüfgerät, Prüfmittel-ID, Kalibriert bis, Bemerkung; in a file with inspection columns "Bemerkung" is the inspection's
remark, otherwise the asset's note). Values: result `pass`/`fail` or bestanden, nicht bestanden,
i.O., nicht i.O., ja, nein; class `I`, `II`, `III` or "SK I"; method Dir, Diff, AM (or direkt,
Differenzstrom, Ersatzableitstrom); reason in English or German words (Wiederholungsprüfung …);
measurements as `>299`, `<0,01`, with `-` or empty meaning not measured. Rules: a row with
`inspected_on` and `result` records an inspection of the created or updated asset (existing
numbers only in *update* mode); the category must require DGUV V3 (else a row error); inspector,
date and result are required; the next due date is computed from the interval as usual; an
identical inspection (same asset, date and result) is not duplicated, so a re-import is
idempotent; the asset status is **not** changed by a failed result (the `status` column can set
it); retired and lost assets may receive past inspections. One row holds one inspection, so
several inspections of one device need several import runs. Only users with the `can_inspect`
flag can import these columns; for others the mapping step refuses them. The preview and the
result page show the number of inspections recorded (and already present); audit entries
`inspection.create` with `via: import`.

## Data export and import (JSON)

The CSV import and export handle the asset list. To **move the whole inventory** from one
instance to another (for example from a test instance to production) there is a second, lossless
format: one JSON file with locations, categories with their field definitions, assets with
custom field values and all DGUV inspections.

**Export.** Admins download it at `/admin/data` (`GET /admin/data/export.json`, file
`lanventory-export-YYYY-MM-DD.json`), or run `lanventory data export [-o file]`. Layout:

```json
{"format": "lanventory-export", "version": 1, "exported_at": "…", "app_version": "…",
 "locations": [{"name": "", "description": ""}],
 "categories": [{"name": "", "prefix": "", "dguv_required": true, "dguv_interval_months": 24,
                 "fields": [{"key": "", "label": "", "type": "number", "unit": "", "options": [], "required": false}]}],
 "assets": [{"inventory_no": "", "category": "<name>", "name": "", "serial_no": "", "manufacturer": "",
             "model": "", "location": "<name or empty>", "status": "active", "note": "",
             "purchase_date": null, "dguv_interval_months": null, "created_at": "…", "updated_at": "…",
             "fields": {"<key>": "<value as stored>"},
             "inspections": [{"inspected_on": "", "inspector_name": "", "recorded_by": "<username or empty>",
                              "result": "pass", "protection_class": "", "visual_ok": true, "function_ok": true,
                              "pe_resistance_ohm": null, "insulation_mohm": null, "leakage_ma": null,
                              "test_device": "", "remark": "", "interval_months": 12, "next_due": "", "created_at": "…",
                              "pe_resistance_cmp": "", "insulation_cmp": "", "leakage_cmp": "", "leakage_method": "",
                              "touch_ma": null, "touch_cmp": "", "reason": "", "test_device_id": "",
                              "test_device_calibrated_until": ""}]}]}
```

The last nine inspection properties (qualifiers, method, touch current, reason, test device ID and
calibration date) are optional: files without them still import (empty values).

Values are exported as stored (booleans `"1"`/`"0"`, numbers with a dot), fields in their
position order, assets sorted by inventory number, inspections by date, timestamps RFC 3339 UTC.
Users, sessions, API tokens, settings, the audit log and all secrets are **not** exported.

**Import.** Admins upload the file at `/admin/data` (at most 5 MB) or run
`lanventory data import [--dry-run] [--on-conflict skip|abort] [--update-categories=false] <file>`. The web UI first shows a
preview (a dry run in a rolled-back transaction: what would be created, skipped inventory numbers,
warnings, problems); only the confirmation writes, in one transaction, all or nothing. The
export, the upload and the confirmation need a recent authentication.

- Unknown `format` or `version` is refused. Unknown JSON keys are refused.
- **Locations** are matched by name (case-insensitive); missing ones are created, existing ones
  keep their description.
- **Categories** are matched by name (case-insensitive). A missing category is created with its
  prefix, DGUV settings and fields; a prefix already used by a *different* category is a conflict.
  For an existing category, **"adopt the settings of existing categories"** (default on, CLI
  `--update-categories`) takes prefix, DGUV flag and interval, and label, unit and required flag
  of matching fields from the file – a fresh instance has seeded template categories (for example
  a 12-month interval) that would otherwise keep their defaults; a required flag is not switched on
  for a category that already has assets. With the option off, the existing category keeps its
  settings and the differences are reported as warnings. Its fields are matched by key, missing fields are added (as optional when the category already has assets),
  missing select options are added, and a field with the same key but another type is a conflict.
- **Assets** are matched by inventory number (case-insensitive). Existing numbers are skipped and
  listed (`skip`, default) or abort the whole import (`abort`); existing assets are never changed.
  New assets keep their number, status (also retired and lost), `created_at` and `updated_at`.
  Numbers are not generated; the running number simply skips taken numbers later. Values pass the
  same validation as the forms; a problem is reported with its place, for example
  `assets[12].fields.length_m`. A required flag is not enforced on import, so that assets that
  predate a required field can be moved.
- **Inspections** are imported only for newly created assets, as records with the stored interval
  and next due date (not recomputed). Result, protection class, dates and measurement ranges are
  validated; the "asset must be active and inspectable" rule does not apply. `recorded_by` is the
  user with that name if it exists, else the importing user (none for the command line).
- Audit: one `asset.create` entry per created asset (details `via: data_import`) and one
  `data.import` summary; the export writes `data.export`.

**Moving data.** Export on the old instance, import on the new one (users are created there
separately). A new instance already has the seeded categories; categories with the same name are
merged as described above. Run a dry run first.

## Bulk edit

Editors and admins change several assets at once. In the asset list they tick rows (the header
checkbox selects the page; "select all N matches of the filter" selects every asset matching the
current filter, at most 1000), then "Edit selected". The form shows the selected inventory numbers
and, for every property, a "change" tick plus the input; only ticked properties change. Properties:
category, location (– clears it), status, manufacturer, model, purchase date, DGUV interval (empty =
category default), note (replace, or append as a new line) and custom fields. Name, serial number
and inventory number are deliberately not bulk-editable.

- **Custom fields** can be changed when all selected assets end up in one category: they share one,
  or the category change is ticked (choose the category and press "Load fields"; the fields of the
  target category appear). An empty value clears the field. A key the asset's category lacks is an
  error for that asset.
- **Category move** works as for a single asset: values carry over to fields with the same key when
  valid, the others are lost (listed in the preview), required fields of the target category must
  be filled (set them in the same form).
- **Preview and apply.** The preview is a dry run: per changed asset the changes old → new, lost
  values and, if the change is invalid for some assets, the errors grouped per field and the assets
  concerned. Nothing is written until "Apply"; the whole change is one transaction (all or
  nothing). Assets that already have the values are left alone. Each changed asset gets an
  `asset.update` audit entry with `via: bulk`; the API uses `via: api_bulk`.
- **API.** `PATCH /api/v1/assets` with `ids`, `changes` (API names, `fields` by key, `note_append`)
  and `dry_run`; validation errors are keyed `"<asset id>.<field>"`.

## Roles


| Role | May |
|---|---|
| `viewer` | read everything |
| `editor` | additionally create and change assets and locations |
| `admin` | additionally manage categories and their fields, delete assets, manage users and see the audit log |

`can_inspect` allows recording inspections for any role that can log in. Authorization is
always checked on the server.

**API tokens** act as their owner with scopes `read:assets`, `write:assets`, `read:inspections`
and `write:inspections`; a token can never do more than its owner may at that moment (writing
needs editor, deleting assets or inspections admin, recording inspections `can_inspect`).
Categories are read-only in the API. See [api.md](api.md).

## Units and formats

Timestamps are Unix seconds (UTC) in the database and RFC 3339 UTC in the API. Dates in the UI
follow the user's language (German `DD.MM.YYYY`, English `YYYY-MM-DD`; form inputs and CSV use
`YYYY-MM-DD`), and so do decimal separators (German comma, English dot; both are accepted as
input). Measures are plain numbers; the unit comes from the field
definition or, for inspections, is fixed (ohm, megohm, milliampere).
