# Changelog

All notable changes to this project are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-11-16

First release. Built with Go 1.26.

### Added

- Real-world inspection data (step 6.6), found by comparing a real inspection protocol: readings
  beyond the instrument's range with a `<` or `>` qualifier (`>299` MΩ; form, API and display),
  touch current as its own measurement (`touch_ma`, separate from the protective conductor /
  leakage current), measuring method of the leakage current, inspection reason, test device ID
  and calibration date (the form prefills the test device and warns when the calibration ended
  before the inspection). Migration `00006_inspection_details.sql`; also in the protocol, the CSV
  export, the REST API, OpenAPI and the JSON data export/import (optional properties of format
  version 1).
- CSV import of past inspections: optional inspection columns (with German header aliases and
  values) record one inspection per row for DGUV assets, idempotent on re-import, only for users
  with the inspection right, audited with `via: import`.
- Bulk edit of assets (step 6.9): select assets in the list (checkboxes, select all, or all
  matches of the filter) and change category, location, status, manufacturer, model, purchase
  date, DGUV interval, note and custom fields for all of them, with a preview (dry run) and one
  all-or-nothing transaction; audit entries with `via: bulk`. `PATCH /api/v1/assets` does the same
  through the API (`ids`, `changes`, `dry_run`; errors per asset).
- Data export and import (step 6.8): `/admin/data` (admins) downloads the whole inventory,
  including custom fields and all inspections, as one JSON file and imports such a file into
  another instance (preview, all or nothing, existing inventory numbers skipped or abort, dry
  run); `lanventory data export` and `lanventory data import`. Users, tokens, settings and the
  audit log are not transferred.
- Changing the category of an asset (edit form and `PATCH /api/v1/assets/{id}`): custom values move
  to fields with the same key, the others are listed and dropped; number and inspections stay.
- Optional category prefix: a category may have no prefix (several may), its generated numbers
  drop the prefix and the separator after it. A `inventory.number_format` without `{prefix}`, for
  example `{seq:04}` for `0001`, `0002`, …, uses one running number for all categories; taken
  (imported) numbers are skipped.
- Release documentation (step 6.3): README as landing page with the package repository, GitHub
  release, Docker and quick start instructions, a rewritten `docs/installation.md` (package
  contents, upgrade, uninstall, Docker, backup) and the new `docs/testing.md` (unit tests,
  linter, browser end-to-end scripts). Stale "planned" and phase wording removed.
- Package repository hook (step 6.4): `.github/workflows/repo-lan-party.yml` notifies
  repo.lan.party (`repository_dispatch` to `lan-dot-party/repo` with the secret
  `REPO_DISPATCH_TOKEN`) after a successful Release workflow and when a release is deleted.
- Browser end-to-end scripts in `tools/e2e/` (Selenium and Chromium, run by hand against a live
  instance): `passkey_e2e.py`, `inventory_e2e.py`, `inspection_e2e.py`, `api_e2e.py` and
  `import_e2e.py`; see `docs/testing.md`.

- CSV export of assets (phase 6.2): `/assets.csv` with the list's filters and sort, all matching
  rows (cap 50 000), core columns, DGUV state and one `field:<key>` column per custom field, UTF-8,
  CRLF, formula guard shared with the inspection export, link "CSV export" on the asset list.
- CSV import (phase 6.1): wizard at `/assets/import` for editors and admins (upload up to 5 MB,
  delimiter and encoding detection incl. Windows-1252, column mapping with automatic suggestions
  from English and German headers, preview with per-row messages, one transaction, "all or
  nothing" or "skip invalid rows", create missing locations, skip or update existing inventory
  numbers). Uploads are kept in memory only (token bound to user and session, 30 minutes).
  Audit entries `asset.import` and `asset.create`/`asset.update` with `via: import`. The export
  round-trips. Note for reverse proxies: allow request bodies of about 6 MB
  (`docs/reverse-proxy.md`).
- REST API v1 (phase 5): `/api/v1` for locations, categories (read-only), assets (incl.
  lookup by inventory number), inspections and the due overview, errors as RFC 9457
  `application/problem+json`, OpenAPI 3.1 spec (`/api/openapi.json`, `/api/openapi.yaml`) as the
  single source of truth with HTML docs at `/api/`, test-validated against the spec
  (kin-openapi as test dependency).
- Scoped API tokens (`lvk_…`, SHA-256 hash only; scopes `read:assets`, `write:assets`,
  `read:inspections`, `write:inspections`; effective rights = scope and owner's current role),
  migration `00005_api_tokens.sql`, pages Account > API tokens and Admin > API tokens, audit
  entries `token.create`/`token.revoke` and `via: api` on API changes, per-token rate limit
  (`api.rate_limit`, `api.rate_burst`). Browser sessions may call the API (CSRF header).
- `llms.txt` API section, `docs/api.md`; `docs/llms-full.txt` now includes the API guide and
  the OpenAPI spec.
- Project scaffold (phase 0): Go module, CLI (`server`, `config init`, `migrate`, `backup`,
  `user create|passwd|list|delete`, `version`), YAML configuration with `LANVENTORY_*` environment
  overrides, SQLite store with embedded migrations, AES-256-GCM `secret.key`, `/health`
  endpoint (JSON status and version).
- Users and password login (phase 1): roles viewer, editor and admin, the `can_inspect` flag for
  DGUV inspectors, Argon2id password hashes, server-side sessions, login lockout, CSRF and
  same-origin checks, account page (password, language, color theme), admin pages for users and
  the audit log, a placeholder overview.
- Strict Content Security Policy without inline scripts or styles; German and English UI.
- Migration `00001_init.sql`: tables `users`, `sessions` and `audit_log`.
- Config section `inventory.number_format`.
- Packaging: binaries, `.deb`, `.rpm`, Arch and Alpine packages, multi-arch Docker image, systemd
  unit with hardening; CI, security scans and Dependabot.
- Vendored htmx 2.0.4.
- Documentation tooling (step 0.5): `llms.txt` served at `/llms.txt`, `tools/gendocs` generating
  `docs/configuration.md`, `docs/config.schema.json` and `docs/llms-full.txt` (with a freshness
  test), `docs/concepts.md`, decision records in `docs/adr/`, detailed roadmap.
- Second factors (phase 2): TOTP with QR enrollment and replay protection, single-use recovery
  codes, passkeys (WebAuthn) as second factor and for passwordless login, MFA policy
  (`off`/`admins`/`all`) on `/admin/security` and via `lanventory mfa policy`, re-authentication
  for security changes, admin MFA reset (web and `lanventory user reset-mfa`), "Sign-in &
  security" page. Migration `00002_mfa.sql` (TOTP columns, pending sessions, `recovery_codes`,
  `webauthn_credentials`, `settings`). New dependencies `go-webauthn/webauthn` v0.15.0 and
  `skip2/go-qrcode`.
- Inventory core (phase 3): locations (flat list), categories with a field editor (text, number,
  select, bool, date; add, edit, delete, reorder), category templates seeded once, assets with
  the category's custom fields validated per type, automatic inventory numbers per category prefix
  (`inventory.number_format`, transactional, manual override), asset list with search, filters
  (category, location, status, select/bool fields), sorting and paging driven by the URL and
  updated with htmx (full pages work without JavaScript), asset detail page with a change history
  built from the audit log, dashboard with counts per status and category, "Inventory" navigation.
  Migration `00003_inventory.sql` (`locations`, `categories`, `category_fields`, `assets`,
  `asset_field_values`, `number_sequences`). htmx is now loaded on the asset list.
- DGUV V3 inspections (phase 4): record inspections with measurements (`can_inspect` only;
  inspections are records and cannot be edited, admins can delete them), next due date from the
  category or per-asset interval, optional "set asset to defective", non-blocking guidance hints
  for common DIN VDE 0701-0702 limits, due overview (`/inspections/due`, htmx), inspection list
  with filters and CSV export (`/inspections.csv`), printable A4 protocol without scripts
  (`/inspections/{id}/print`, `static/print.css`), DGUV widget on the dashboard, inspection card
  on the asset page. Migration `00004_inspections.sql`. New config key `organization.name`
  (header of the protocol).

### Security

- Passwords only as Argon2id hashes; login lockout by client IP and user name; server-side
  sessions with hashed IDs and `SameSite=Strict` cookies; CSRF token plus same-origin check on
  every state change; strict Content Security Policy without inline scripts or styles; all
  output escaped.
- Secrets in the database (TOTP seeds) are AES-256-GCM encrypted with a key in `secret.key`;
  recovery codes and API tokens are stored as hashes or MACs only; secrets never appear in logs
  or the audit log.
- Second factors and API tokens: re-authentication for security changes, MFA policy, token
  scopes limited by the owner's current rights, per-token rate limit.
- CSV export guards cells against spreadsheet formula injection; the import keeps uploads in
  memory only.
- Systemd unit with sandboxing; release packages are install-tested and the build provenance of
  the artifacts is attested.

### Fixed

- Numbers are shown with a decimal comma in the German UI (asset detail, history); dates are
  shown as `DD.MM.YYYY` in German on the inspection pages and the asset detail.

[Unreleased]: https://github.com/lan-dot-party/LANventory/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/lan-dot-party/LANventory/releases/tag/v0.1.0
