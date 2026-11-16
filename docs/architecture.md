# Architecture

LANventory is one Go binary: an HTTP server with a server-rendered web UI (`html/template`,
a small `app.js`, no inline scripts), a SQLite database and a command line. Read this file
instead of re-exploring the code.

## Packages

```
llms.go                    embeds llms.txt (served at GET /llms.txt)
internal/api/              REST API /api/v1: api.go (routes, token/session auth, problem+json,
                           audit), helpers.go, locations/categories/assets/bulk/inspections/me.go,
                           docs.go (HTML docs, spec), openapi.yaml (embedded, single source)
tools/e2e/                 browser end-to-end tests (Selenium + system Chromium):
                           passkey_e2e.py, inventory_e2e.py, inspection_e2e.py, api_e2e.py,
                           import_e2e.py, bulk_e2e.py (see testing.md)
tools/gendocs/             generates docs/configuration.md, config.schema.json, llms-full.txt
                           from internal/config/config.go; a test checks they are current
cmd/lanventory/            main.go
cmd/lanventory/cmd/        cobra commands: root.go (config, logging, env), server.go,
                           admin.go (config init, migrate, backup, user …, user reset-mfa,
                           mfa policy), data.go (data export, data import)
pkg/version/               Version, Commit, BuildDate (set with ldflags)
internal/app/              App (config, DB, key, logger), client IP, session context,
                           SameOrigin, Audit, SecurityHeaders (CSP), RequestLog
internal/auth/             Argon2id, random tokens, LoginLimiter, RateLimiter
internal/secret/           AES-256-GCM with associated data, keyed MAC (subkey), secret.key
internal/mfa/              TOTP (RFC 6238), recovery codes, WebAuthn relying party from
                           public_url, in-memory ceremony store
internal/config/           YAML config, defaults, validation, Example (= docs/config.example.yaml)
internal/store/            SQLite (modernc), goose migrations, users, sessions, audit log,
                           mfa.go (TOTP, recovery codes, passkeys, settings, MFA policy)
                           locations.go, categories.go (with field definitions), assets.go,
                           assetlist.go (search, filters, sorting, paging, counts, audit by
                           object), numbers.go (number format and sequence), seed.go (category
                           templates), validate.go (ValidationError, value normalization)
                           dguv.go (next due date, due state, guidance limits; pure functions),
                           inspections.go (inspections, due overview, list/export queries)
                           assetexport.go (asset CSV export query), csvfile.go (CSV decoding and
                           parsing for the import), import.go (mapping suggestion, import run),
                           dataexport.go (JSON format types, ExportData), dataimport.go
                           (ParseDataExport, ImportData: full transfer incl. inspections),
                           tokens.go (API tokens)
                           bulk.go (BulkUpdateAssets: change many assets in one transaction,
                           AssetIDsByFilter, AssetsByIDs)
  migrations/sqlite/       00001_init.sql, 00002_mfa.sql, 00003_inventory.sql,
                           00004_inspections.sql, 00005_api_tokens.sql,
                           00006_inspection_details.sql
internal/web/              chi router, handlers, i18n.go, templates/, static/
```

## Request flow

1. `chi` middlewares: `Recoverer`, `RequestLog`, `GetHead`.
2. `GET /health` answers JSON `{"status":"ok","version":"…"}` (no login, no secrets).
   `GET /llms.txt` serves the embedded `llms.txt` as `text/plain` (public, `web.Server.LLMs`).
3. Everything else passes `SecurityHeaders` (CSP and friends) and `SessionMiddleware`, which
   resolves the cookie `lv_session` (hashed with SHA-256, looked up in `sessions`), extends the
   session after half of its lifetime and puts session and user into the request context.
4. `internal/web` routes:
   - Public: `/login`, `/static/*`, `POST /login/passkey/{options,finish}` (passwordless; checked
     with the login page's double-submit token `lv_login` sent as `X-Login-CSRF` + `SameOrigin`).
   - Pending sessions (password accepted, second factor missing; `SessionMiddleware` puts them
     into the context with `app.PendingUser`, so `app.User` stays nil): `requirePending` +
     `csrf` for `/login/mfa`, `/login/mfa/passkey/{options,finish}`, `/login/mfa/cancel`.
   - Everything else: `requireLogin`, `csrf` (form field `_csrf` or header `X-CSRF-Token`, plus
     `SameOrigin`) and `enforceMFA` (redirects to `/account/security` when the MFA policy requires
     a second factor the user lacks; enrollment routes are exempt). `/admin/*` additionally uses
     `requireRole(admin)`. `requireRecentAuth` (session `auth_at` younger than 10 minutes, else
     `/reauth`) guards `/account/{totp,recovery,passkeys}/*` POSTs, `POST /admin/security` and
     `POST /admin/users/{id}/mfa-reset`.
   - Inventory (all behind `requireLogin`): `GET /assets`, `/assets/{id}`, `/locations`,
     `/categories`, `/categories/{id}` for every role. `requireRole(editor)`: `GET /assets/new`,
     `POST /assets`, `GET /assets/{id}/edit`, `POST /assets/{id}`, `POST /locations`,
     `POST /locations/{id}[/delete]`. `requireRole(admin)`: `POST /assets/{id}/delete`,
     `POST /categories[/{id}[/delete]]`, `POST /categories/{id}/fields[/{fid}[/delete|/move]]`.
     `GET /assets` answers htmx requests (`HX-Request: true`, not a history restore) with the
     `asset_fragment` template only (`Server.renderFragment`); `Page.HTMX` makes the layout load
     htmx and its config on that page only.
   - Bulk edit (`requireRole(editor)`): `POST /assets/bulk/edit` (form; the selection comes as
     repeated `id` fields or, with `all_matches=1`, as the list filter `filter` that is resolved
     with `AssetIDsByFilter`, at most 1000, and is carried on as hidden ids), `POST
     /assets/bulk/preview` (dry run through `store.BulkUpdateAssets`, errors grouped per field),
     `POST /assets/bulk/apply` (real run; audit `asset.update` per changed asset with `via: bulk`;
     redirect to `/assets?ok=bulk&n=<changed>`). Form input travels as `chg_<property>` (tick) and
     `v_<property>`, custom fields as `chg_f_<key>`/`v_f_<key>`, `note_mode` (replace/append).
     `handlers_bulk.go`, templates `bulk_form.html`, `bulk_preview.html`; the list's checkboxes,
     select-all and counter are bound by delegation in `app.js` (`data-bulk-*` attributes) and the
     bulk `<form id="bulk-form">` lives outside the htmx-swapped `#results`.
   - CSV (behind `requireLogin`): `GET /assets.csv` for every role (list filters, `csvWriter` with
     the formula guard shared with `/inspections.csv`). Import for editors and admins
     (`requireRole(editor)`): `GET /assets/import` (upload form), `POST /assets/import`
     (multipart upload; answers with the mapping page), `POST /assets/import/map` (mapping page
     again), `POST /assets/import/preview`, `POST /assets/import/confirm`. `importBody` (before
     `csrf`) limits and parses the upload body so that the CSRF check can read the form. The parsed
     upload is kept in `Server.imports` (`importStates`, memory only: token bound to user and
     session, one per user, at most 16, TTL 30 minutes, claim flag); the steps carry only the
     token. `store.RunImport` plans (rolled-back transaction) and performs the import (one
     transaction, `createAsset`/`updateAsset` shared with `CreateAsset`/`UpdateAsset`).
   - Data export/import (admins; `requireRole(admin)` plus `requireRecentAuth("/admin/data")` on
     all of them): `GET /admin/data` (page), `GET /admin/data/export.json` (download
     `lanventory-export-YYYY-MM-DD.json`, audit `data.export`), `POST /admin/data/import`
     (multipart upload, answers with the preview; `importBody` limits it to 5 MB like the CSV
     upload), `POST /admin/data/preview` (dry run again with the chosen conflict mode),
     `POST /admin/data/confirm` (import; audit `asset.create` per asset with `via: data_import`
     and `data.import`). The parsed upload lives in `Server.imports` like the CSV upload (a state
     holds `Table` or `Data`; the CSV wizard refuses data tokens). `store.ImportData` runs in one
     transaction and writes inspections as records (no recomputation); `DryRun` rolls back.
   - DGUV V3 (behind `requireLogin`): `GET /inspections`, `/inspections.csv`, `/inspections/due`,
     `/inspections/{id}`, `/inspections/{id}/print` for every role. `requireInspector`
     (`can_inspect`, independent of the role): `GET /assets/{id}/inspections/new`,
     `POST /assets/{id}/inspections`. `requireRole(admin)`: `POST /inspections/{id}/delete`.
     `/inspections/due` answers htmx requests with the `due_results` fragment like `/assets`. The
     protocol page (`protocol.html`, parsed on its own, `Server.renderProtocol`) has no layout and
     no script and loads only `static/print.css`.
   - Passkey endpoints speak JSON (`webauthn.js` posts with `X-CSRF-Token`/`X-Login-CSRF`); a
     running ceremony is identified by the short-lived cookie `lv_wa`.
   - REST API (`internal/api`, mounted at `/api` in `server.go` behind `SecurityHeaders` and
     `SessionMiddleware`, outside the web router): `/api/` docs, `/api/openapi.json|yaml`,
     `/api/v1/*`. Authentication per request: `Authorization: Bearer lvk_…` (hash lookup,
     revoked/expired/disabled owner → 401, per-token rate limit) or a full session (unsafe
     methods need `X-CSRF-Token` + same origin; MFA policy enforced; pending sessions rejected).
     Each route declares a `need` (scope, minimum role, `can_inspect`); a token needs the scope
     AND the owner's current rights. Errors are `application/problem+json`.
5. Handlers load data from `store`, build a `Page` and render `layout` with the page template.
   Texts come from `i18n.go` via `Page.T`.

Roles are ordered viewer < editor < admin (`store.RoleLevel`, `User.Can(role)`). The flag
`can_inspect` is independent of the role.

## Data model

Timestamps are Unix seconds (UTC). Migration `00001_init.sql`:

| Table | Columns |
|---|---|
| `users` | `id`, `username` (unique, case-insensitive), `password_hash` (Argon2id PHC string), `role` (`viewer`, `editor`, `admin`), `can_inspect`, `theme` (`auto`, `light`, `dark`), `lang` (`de`, `en`), `disabled`, `created_at`, `updated_at`, `last_login_at` |
| `sessions` | `id` (SHA-256 of the cookie value), `user_id` (cascade), `csrf_token`, `created_at`, `expires_at` |
| `audit_log` | `id`, `ts`, `user_id` (nullable, set to NULL when the user is deleted), `action`, `object_type`, `object_id`, `details` (JSON text; keys `actor` and `ip` are used by the UI; never secrets) |

Migration `00002_mfa.sql` (phase 2):

| Table / column | Purpose |
|---|---|
| `users.totp_secret` | TOTP secret, AES-GCM with associated data `totp|<user id>`; NULL = none; set with `totp_enabled_at` NULL = enrollment pending |
| `users.totp_enabled_at`, `users.totp_last_step` | activation time; last accepted time step (replay protection) |
| `users.webauthn_handle` | random 32-byte WebAuthn user handle (unique), created with the first passkey |
| `sessions.pending`, `sessions.auth_at` | second factor still missing (5-minute session); time of the last full authentication |
| `recovery_codes` | `user_id`, `code_mac` (hex HMAC-SHA256 under a subkey of `secret.key`), `created_at`, `used_at` |
| `webauthn_credentials` | `user_id`, `credential_id` (unique), `name`, `credential` (JSON of `webauthn.Credential`: public key, flags, sign count), `created_at`, `last_used_at` |
| `settings` | `key`, `value`, `updated_at`; currently `mfa_policy` (`off`, `admins`, `all`) |

Migration `00003_inventory.sql` (phase 3):

| Table | Columns |
|---|---|
| `locations` | `id`, `name` (unique, case-insensitive), `description`, timestamps |
| `categories` | `id`, `name` (unique, case-insensitive), `prefix` (`A–Z0–9`, 0–8; unique unless empty, partial index), `dguv_required`, `dguv_interval_months` (1–120, default 12), timestamps |
| `category_fields` | `id`, `category_id` (cascade), `key` (unique per category), `label`, `type` (`text`, `number`, `select`, `bool`, `date`), `unit`, `options` (JSON array, select only), `required`, `position` |
| `assets` | `id`, `inventory_no` (unique, case-insensitive), `category_id` (restrict), `name`, `serial_no`, `manufacturer`, `model`, `location_id` (restrict, nullable), `status` (`active`, `defective`, `retired`, `lost`), `note`, `purchase_date` (`YYYY-MM-DD`), `dguv_interval_months` (override), timestamps; indexes on category, location, status, serial number |
| `asset_field_values` | `asset_id` (cascade), `field_id` (cascade), `value` (normalized text); primary key `(asset_id, field_id)` |
| `number_sequences` | `prefix`, `next`: running number per category prefix; `''` is shared by categories without prefix and by formats without `{prefix}` |

`settings` additionally holds `seeded_categories` (category templates were created once). The
asset history is not a table: it is the audit log filtered by `object_type = 'asset'`; `asset.*`
entries carry `changes` (`{field: [old, new]}`) in their details.

Migration `00004_inspections.sql` (phase 4):

| Table | Columns |
|---|---|
| `dguv_inspections` | `id`, `asset_id` (cascade), `inspected_on` (`YYYY-MM-DD`), `inspector_name` (free text, may be external), `recorded_by` (user, set NULL on delete), `result` (`pass`, `fail`), `protection_class` (`I`, `II`, `III`, nullable), `visual_ok`, `function_ok`, `pe_resistance_ohm`, `insulation_mohm`, `leakage_ma` (REAL, nullable, non-negative), `test_device`, `remark`, `interval_months` (the interval applied), `next_due` (`YYYY-MM-DD`), `created_at`; indexes `(asset_id, inspected_on)` and `(inspected_on)` |

Inspections are never updated. The state of an asset derives from its latest inspection
(`inspected_on`, then `id`); deleting that inspection falls back to the previous one. Audit
entries `inspection.create` and `inspection.delete` (object type `inspection`); status changes
caused by an inspection are `asset.update` entries with `via: inspection`. Configuration
`organization.name` is printed on protocols.

Migration `00006_inspection_details.sql` (step 6.6) extends `dguv_inspections` (all with defaults, so
existing rows stay valid):

| Column | Meaning |
|---|---|
| `pe_resistance_cmp`, `insulation_cmp`, `leakage_cmp`, `touch_cmp` | comparison qualifier of the reading: `''` (exact), `'<'` or `'>'` (CHECK) |
| `touch_ma` | touch current, REAL, nullable, non-negative (0–1000); `leakage_ma` stays the protective conductor / leakage current |
| `leakage_method` | `''`, `direct`, `differential`, `alternative` (CHECK) |
| `reason` | inspection reason: `''` (not recorded), `initial`, `repeat`, `repair` (CHECK) |
| `test_device_id` | test device ID or serial number (max 100) |
| `test_device_calibrated_until` | `YYYY-MM-DD`, nullable |

The CSV import (`store/import.go`) writes inspections through the same `insertInspection` as the form
and the JSON import; its inspection columns need `ImportOptions.AllowInspections` (the user's
`can_inspect`).

Migration `00005_api_tokens.sql` (phase 5):

| Table | Columns |
|---|---|
| `api_tokens` | `id`, `name`, `prefix` (first 8 characters, for display), `token_hash` (unique, SHA-256 hex), `scopes` (space-separated), `user_id` (owner, cascade), `created_at`, `expires_at` (nullable), `last_used_at`, `revoked` |

Web routes for tokens: `GET /account/tokens`, `POST /account/tokens` (`requireRecentAuth`),
`POST /account/tokens/{id}/revoke` (own tokens), `GET /admin/tokens`, `POST
/admin/tokens/{id}/revoke` (admin). Audit entries `token.create`, `token.revoke`; changes made
through the API carry `via: api` (`via: api_bulk` for `PATCH /api/v1/assets`) and `token_id` in their details. Config keys `api.rate_limit`
and `api.rate_burst`.

## Security mechanisms

See [security.md](security.md). In short: Argon2id, lockout, hashed session IDs, `SameSite=Strict`
cookies, CSRF token plus same-origin check, strict CSP, escaped output, encrypted secrets.

## Tests

How to run the unit, handler and browser end-to-end tests: [testing.md](testing.md).

`go test ./...` – unit tests for auth, secret, mfa (RFC 6238 vectors, skew, replay, recovery
codes, relying-party URL rules, ceremonies), config and store; web tests drive the real
router through `httptest` (login, lockout, CSRF and origin, roles, user management, last-admin
protection, security headers, template keys and absence of inline code; inventory: role
checks for every route group, location and category/field editor flows, form validation and
XSS escaping, history after create/edit/status change, list filters, URL round trip, htmx
fragment versus full page; store tests cover CRUD, constraints, seeding once, value
normalization, number generation (also 20 concurrent creates), search/filter queries, type-change
and delete protection; TOTP login with replay
and recovery codes, pending sessions, MFA policy, re-authentication, admin MFA reset, passkey
endpoint guards). The WebAuthn ceremony itself is tested in a browser: `tools/e2e/passkey_e2e.py` (Selenium,
Chrome DevTools virtual authenticator; passkey registration, passwordless login, passkey as
second factor, passkey re-authentication, TOTP enrollment and login); the other end-to-end scripts
cover the inventory, inspections, the API and the CSV import. The race detector runs
in CI only. Phase 4: store tests cover next-due arithmetic (month ends, leap years, override), due
state boundaries, exclusions, inspection creation with the status change in one transaction,
refusals, delete and cascade and the list/export filters; web tests cover the `can_inspect`
check (403, also for admins; viewers with the flag allowed), refusals, validation re-render,
guidance hints, the script-free protocol, CSV columns, escaping and formula protection, the
htmx fragment of the due page, dashboard counts equal to the overview and German number and date
formatting.

Phase 5: `internal/api` tests (`spec_test.go`, `validate_test.go`, `api_test.go`, fixture in
`fixture_test.go`) check routes against `openapi.yaml`, validate every response (and the request
bodies of successful calls) with kin-openapi, and cover scopes, owner role changes, token states,
hash-only storage, rate limit, sessions with CSRF/pending MFA/MFA policy, validation errors,
by-number lookup, number generation, inspection rules and audit entries; `web_tokens_test.go`
covers the token pages.

Phase 6: store tests cover delimiter sniffing, encodings (Windows-1252 bytes, UTF-8 BOM, UTF-16),
header auto-mapping, value parsing, duplicates, generated numbers, skip vs update, category
mismatch, all-or-nothing vs skip-invalid, locations; web tests cover roles (viewers: 403 for the
import, export allowed), the 5 MB limit, CSRF, token binding to user and session, state expiry,
the full flow with a Windows-1252 file, escaped cells in the preview, audit entries, export
columns/escaping/guard/filters/sort and the export → import round trip.

Bulk edit (step 6.9): store tests (`bulk_test.go`) cover location/status for several assets,
simple fields, note append, custom values by key, category moves with carried and lost values,
required fields of the target category (errors for exactly the affected assets, nothing written),
dry run, the 1000 limit, unknown ids and the filter resolution; web tests (`web_bulk_test.go`)
cover roles (viewers: no checkboxes, 403), the form → preview → apply flow with CSRF, "all
matches" and its limit, and audit entries with `via: bulk`; API tests cover `PATCH
/api/v1/assets` (success, dry run, per-asset errors, request errors, scope and role) and the spec
conformance.

Data export/import (step 6.8): store tests cover the round trip (export → import into a fresh
database → export again, equal except `exported_at`; with and without seeded categories),
conflicts (prefix, field type), `skip` versus `abort`, dry run, value paths in errors, unknown
format and version; web tests cover admin-only access, download headers, CSRF, recent
authentication, token binding and the upload → preview → confirm flow with audit entries.

## Third-party code

Go: `go-webauthn/webauthn` is pinned to v0.15.0, the last release that supports Go 1.24
(v0.16+ needs Go 1.25); Dependabot ignores newer versions until the toolchain moves.
`skip2/go-qrcode` renders the TOTP QR code. `getkin/kin-openapi` v0.133.0 is a test-only
dependency (response validation against the OpenAPI document).

## Third-party files

`internal/web/static/htmx.min.js` is htmx 2.0.4, vendored without changes. Only the asset list
loads it (`Page.HTMX`), with `<meta name="htmx-config">` turning off indicator styles, eval and
script tags (strict CSP).
