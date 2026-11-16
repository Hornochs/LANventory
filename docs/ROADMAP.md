# Roadmap

LANventory is built in phases. Steps are numbered so that status notes, issues and commits can
refer to them ("step 3.2"). A step is **done** when

- `go vet ./...` and `go test ./...` pass (and CI is green once the repository is pushed),
- the "Current status" section below names it as done,
- [architecture.md](architecture.md) describes any structural change it made, and
- [CHANGELOG.md](../CHANGELOG.md) mentions it under `[Unreleased]`.

To continue the work, read "Current status", take the step under **Next**, implement it,
then update this file, `architecture.md` and the changelog (see [CLAUDE.md](../CLAUDE.md)).

## Current status

_Last updated: 2026-11-16_

| Phase | State |
|---|---|
| 0 – Scaffold | done |
| 1 – Users and password login | done |
| 2 – MFA and passkeys | done |
| 3 – Inventory core | done |
| 4 – DGUV V3 inspections | done |
| 5 – REST API v1 | done |
| 6 – CSV import/export and release v0.1.0 | done |

**Done:** 0.1–0.5, 1.1–1.5, 2.1–2.8, 3.1–3.6, 4.1–4.5, 5.1–5.4, 6.1–6.9.

**Released:** v0.1.0 (2026-11-16).

**Next:** items from [Later](#later).

## Phase 0 – Scaffold

- [x] 0.1 Go module, `pkg/version`, CLI skeleton (`server`, `config init`, `migrate`, `backup`,
  `user …`, `version`), config loading with environment overrides.
- [x] 0.2 SQLite store with embedded goose migrations, `VACUUM INTO` backup, AES-256-GCM
  `secret.key`.
- [x] 0.3 Web skeleton: chi router, embedded templates and static files, strict CSP, `/health`,
  German/English texts in `i18n.go`, vendored htmx.
- [x] 0.4 Packaging and CI: GoReleaser (binaries, deb, rpm, Arch, apk, Docker image), systemd
  unit with hardening, install test, CI, security workflow, Dependabot.
- [x] 0.5 Documentation tooling: `llms.txt` (embedded by `llms.go`, served publicly at
  `/llms.txt`), `tools/gendocs` generating `docs/configuration.md`, `docs/config.schema.json`
  and `docs/llms-full.txt` from `internal/config/config.go` (a test fails when they are stale),
  `docs/concepts.md` and the decision records in `docs/adr/`.

## Phase 1 – Users and password login

- [x] 1.1 `users`, `sessions` and `audit_log` tables (migration `00001_init.sql`), Argon2id
  hashing, server-side sessions with hashed IDs.
- [x] 1.2 Login and logout with lockout (`LoginLimiter`), dummy hash against user enumeration,
  CSRF token and same-origin check on every state change.
- [x] 1.3 Roles viewer < editor < admin, `can_inspect` flag, role checks in the router.
- [x] 1.4 Account page (password, language, color theme) and admin users page (create, role,
  inspection right, disable, reset password, delete; the last admin is protected).
- [x] 1.5 Audit log page and a placeholder overview with user count.

## Phase 2 – MFA and passkeys

Passkeys need HTTPS and a stable `public_url` (see [reverse-proxy.md](reverse-proxy.md)).
Design reasons: [ADR 0006](adr/0006-mfa-totp-and-passkeys.md). As built (details in
[architecture.md](architecture.md) and [security.md](security.md)):

- [x] 2.1 **TOTP second factor.** Migration `00002_mfa.sql` adds `users.totp_secret` (AES-GCM,
  associated data `totp|<user id>`), `totp_enabled_at` and `totp_last_step`. Own RFC 6238
  implementation in `internal/mfa` (SHA-1, 6 digits, 30 s, ±1 step, RFC test vectors). Enrollment
  on `/account/security`: QR code (PNG data URI) plus grouped manual key; active only after the
  first valid code. A used step can never be used again (conditional `UPDATE … WHERE
  totp_last_step < ?`, safe against races). After the password the session is *pending*
  (`sessions.pending`, 5 min) and only `/login/mfa` is reachable; failures count toward the
  login lockout (keys IP and user).
- [x] 2.2 **Recovery codes.** Table `recovery_codes`. 10 codes `XXXXX-XXXXX` (unambiguous
  alphabet, ~49 bits) are issued when TOTP is enabled or on request, shown once as the response
  to the POST, stored as HMAC-SHA256 under a subkey of `secret.key` (`secret.Key.MAC`), so a
  database copy alone does not allow offline guessing. Input is normalised (case, spaces,
  confusable characters). Regenerating invalidates the old set; codes are dropped when the
  last second factor is removed.
- [x] 2.3 **Passkeys as a second factor.** Table `webauthn_credentials` (credential JSON with
  public key, flags and sign count; name; created/last used) and `users.webauthn_handle`
  (random 32-byte user handle). `go-webauthn` v0.15.0 (the last release supporting Go 1.24) and
  the own script `static/webauthn.js`. Register, list and delete on `/account/security`;
  resident key required, attestation none. Ceremonies are kept in memory for 5 minutes, bound
  to purpose and user, single use (`mfa.Ceremonies`). RP ID and origin derive from
  `public_url`; without HTTPS (or `http://localhost`) passkeys are disabled and the UI says why.
- [x] 2.4 **Passwordless login with passkeys.** "Sign in with a passkey" on the login page:
  discoverable login with user verification required, protected by the login page's
  double-submit token and the same-origin check; same lockout and audit as the password path;
  disabled users are refused.
- [x] 2.5 **Enforcement policy.** Setting `mfa_policy` (`off` / `admins` / `all`) in the new
  `settings` table, changed on `/admin/security` or with `lanventory mfa policy`, audited.
  Affected users without a second factor are redirected to `/account/security` on every
  request except the enrollment routes; the last factor cannot be removed while required.
- [x] 2.6 **Re-authentication.** `sessions.auth_at` stores the last full authentication.
  Changing second factors and the MFA policy and resetting another user's MFA require it to
  be younger than 10 minutes; otherwise `/reauth` asks for a passkey (user verification
  required) or the password plus a TOTP/recovery code (password alone if the user has no
  second factor) and returns to the page. User management keeps its per-action password
  confirmation from phase 1.
- [x] 2.7 **Admin reset and docs.** "Reset 2FA" on the users page and `lanventory user
  reset-mfa <name>` remove TOTP, passkeys and recovery codes and end the user's sessions.
  All MFA events are audited without secrets (`mfa.*`, `login` with method, `login.mfa_failed`,
  `reauth`, `settings.mfa_policy`). Unit and handler tests cover TOTP, replay, recovery codes,
  pending sessions, policy, re-authentication and reset; the passkey ceremony itself is tested
  end to end in a browser with a virtual authenticator (`tools/e2e/`, see 2.8).
- [x] 2.8 **Browser end-to-end test for passkeys.** Selenium script in `tools/e2e/` using the
  Chrome DevTools virtual authenticator: register a passkey, log out, passwordless login,
  password + passkey as second factor, re-authentication with a passkey; screenshots in light
  and dark theme. Also covers TOTP enrollment and TOTP login. Run:
  `python3 -I tools/e2e/passkey_e2e.py <https-base-url> <throwaway-user> <password> <screenshot-dir>`.

## Phase 3 – Inventory core

Migration `00003_inventory.sql`; code in `internal/store` (`locations.go`, `categories.go`,
`assets.go`, `assetlist.go`, `numbers.go`, `seed.go`, `validate.go`) and `internal/web`
(`handlers_inventory.go`, `handlers_assets.go`). Roles: everybody reads; editors create and edit
assets and manage locations; admins manage categories and fields and delete assets (all checked
server-side). As built:

- [x] 3.1 **Locations.** Table `locations` (name unique, case-insensitive; description). Page
  `/locations` (read-only for viewers; editors create, rename, delete). Delete is refused while
  assets use the location. Audit `location.create|update|delete`.
- [x] 3.2 **Categories and field editor.** Tables `categories` (prefix uppercase A–Z0–9, 1–8
  characters, unique) and `category_fields` (key `[a-z][a-z0-9_]{0,31}` unique per category;
  types text/number/select/bool/date; unit; options as JSON; required; position). Pages
  `/categories` and `/categories/{id}` (read-only for non-admins): create, edit, delete, add,
  edit, delete and reorder fields with up/down buttons (no JavaScript). The key is fixed after
  creation. The type cannot change while values exist, an option that is in use cannot be
  removed, a category with assets cannot be deleted; deleting a field deletes its values (confirm
  dialog). Seven German templates (Steckerleiste, Kabeltrommel, Kaltgerätekabel, Netzwerkkabel,
  Switch, Monitor, PC / Server) are created once at the end of `Migrate`, guarded by the setting
  `seeded_categories`, and stay deleted when an admin removes them. Audit `category.create|update|
  delete` and `category.field_create|field_update|field_delete|field_move`.
- [x] 3.3 **Assets with dynamic fields.** Tables `assets` and `asset_field_values` (EAV). Values
  are normalized per type: number to a canonical decimal string (comma accepted), bool `1`/`0`,
  date `YYYY-MM-DD`, select one of the options, text up to 500 characters. Form at
  `/assets/new?category=ID` (a category picker without the parameter); validation errors
  re-render the form (HTTP 422) with the input kept and a message per field. The inventory number
  cannot be changed after creation. (The category could not be changed either until 6.7.) Audit `asset.create|update|delete`.
- [x] 3.4 **List with search and filters.** `/assets`: search words (each must match number,
  name, serial number, manufacturer, model or a field value; `LIKE` with escaped wildcards, so
  case-insensitive for ASCII only), filters category, location, status and, with a category, its
  select/bool fields (`f_<key>`), sort by number, name, category, location, status or update time,
  50 per page. All state is in the URL (GET form). The form uses htmx (`hx-get`, `hx-push-url`,
  300 ms delay on input); a request with `HX-Request: true` gets only the results fragment
  (and the extra filters as an out-of-band swap), a history restore request gets the full page.
  htmx is loaded only on this page, with `includeIndicatorStyles`, `allowEval` and
  `allowScriptTags` off.
- [x] 3.5 **Detail page with change history.** `/assets/{id}` shows all values with labels and
  units; the history comes from `audit_log` (`object_type` `asset`), newest first, with user,
  time and old to new values (custom fields by label; status changes as badges). Create and
  update store a `changes` map `{field: [old, new]}` in the audit details (custom fields as
  `field:<key>`, the location by name); an update without changes writes no entry.
- [x] 3.6 **Automatic inventory numbers.** `inventory.number_format` is applied in the same
  transaction that reads and increments `number_sequences` for the category prefix; numbers that
  are already taken (manual ones) are skipped. A manual number (optional form field) that exists
  yields the message "Already taken". A changed category prefix does not renumber assets and
  starts its own sequence. Tests cover per-prefix sequences, 20 concurrent creates, override and
  collision, and formats with and without padding.

## Phase 4 – DGUV V3

See [concepts.md](concepts.md) for the rules and the disclaimer. Migration
`00004_inspections.sql`; code in `internal/store` (`dguv.go` pure rules, `inspections.go`) and
`internal/web` (`handlers_inspections.go`, templates `inspection*.html`, `protocol.html`,
`static/print.css`). As built:

- [x] 4.1 **Record an inspection.** Table `dguv_inspections` (see architecture.md). Form at
  `/assets/{id}/inspections/new`, restricted by the middleware `requireInspector` (`can_inspect`,
  any role, including admins; checked server-side). The inspector name is prefilled with the
  current user and may be an external electrician. Next due = inspection date plus the applied
  interval (asset override, else category) in calendar months, clamped to the month end
  (`store.NextDue`, table tests); the interval is stored with the record. Measurements are
  optional non-negative numbers (comma or dot) with upper bounds (1000 ohm, 10000 megohm,
  1000 mA). Pass or fail is never derived from them; instead non-blocking guidance hints use
  simplified DIN VDE 0701-0702 limits from one place (`store.Limit*`, `store.Guidance`). Only
  assets of DGUV categories that are not retired or lost can be inspected (HTTP 422 otherwise).
  A failed result can set an active asset to defective, a passed one a defective asset back to
  active (checkboxes, defaults checked/unchecked), in the same transaction; the status change is
  audited as `asset.update` (shown in the asset history). Inspections cannot be edited; admins
  delete them (confirm, audit `inspection.delete`). Audit `inspection.create` carries asset,
  result and next due date. The inspection date may not be more than one day in the future.
- [x] 4.2 **Due and overdue overview.** `/inspections/due`: overdue (`next_due < today`), due soon
  (`today <= next_due <= today + 30 days`) and never inspected, of the latest inspection per
  asset; categories without DGUV, retired and lost assets are excluded (defective ones are
  included). Filters category, location and state; never inspected first, then by due date. htmx
  like `/assets` (fragment on `HX-Request`). "Today" is the UTC date. No paging.
- [x] 4.3 **Printable protocol.** `/inspections/{id}/print`: standalone template without layout,
  navigation or scripts, A4 print CSS in `static/print.css`, organisation name from the new
  config key `organization.name`, asset data with category fields, measurements with units and
  the guidance hints, signature lines and the disclaimer (German or English). The page tells to
  use the browser's print function (no JavaScript).
- [x] 4.4 **CSV export of inspections.** `/inspections.csv` with the filters of `/inspections`
  (date range, result, category, location, inspector text): UTF-8 without BOM, header row,
  RFC 4180 via `encoding/csv` (CRLF), dates `YYYY-MM-DD`, `created_at` RFC 3339 UTC, cells
  starting with `=`, `+`, `-`, `@`, tab or CR get a leading apostrophe. Download name
  `lanventory-inspections-YYYY-MM-DD.csv`.
- [x] 4.5 **Dashboard due-widget.** Counts overdue, due within 30 days and never inspected link
  to `/inspections/due?state=…`; they come from `store.CountDue`, which shares its query with the
  overview. Asset counts by status and category stay. Visible to all roles.
- Also: numbers are shown with a decimal comma in German (`num` template function, fixes the
  phase 3 display), dates as `DD.MM.YYYY` in German (`date`); inputs accept both. Inventory
  sub navigation: "Due inspections" and "Inspections".

## Phase 5 – REST API v1

Design: [ADR 0007](adr/0007-openapi-single-source-of-truth.md). Code in `internal/api`
(`api.go` auth and routes, `helpers.go`, `locations.go`, `categories.go`, `assets.go`,
`inspections.go`, `me.go`, `docs.go`, `openapi.yaml`); guide in [api.md](api.md). As built:

- [x] 5.1 **JSON API.** `/api/v1/{locations,categories,assets,inspections}` with list (paging,
  filters), get, create, update and delete where the UI allows it; errors as
  `application/problem+json`; browser sessions need `X-CSRF-Token`. Tests for each endpoint,
  roles and error format.
- [x] 5.2 **OpenAPI description.** `internal/api/openapi.yaml` embedded, served at
  `/api/openapi.json`, with human docs at `/api/`. Tests: every route is in the spec and vice
  versa, responses validate against the spec.
- [x] 5.3 **Scoped API tokens.** Table `api_tokens` (prefix `lvk_`, SHA-256 hash only, name,
  scopes `read:assets`, `write:assets`, `read:inspections`, `write:inspections`, expiry, last
  used). Admin and user pages to create (shown once) and revoke tokens; `Authorization: Bearer`
  auth and per-token rate limit. Tests: scope enforcement (401/403), revocation, hash only in
  the database.
- [x] 5.4 **`llms.txt` update.** Remove the "planned" marks from the API sections of `llms.txt`
  once the API exists, link the live docs, and regenerate `docs/llms-full.txt` with the API
  guide and the OpenAPI spec (extend `tools/gendocs`).
- As built: the API is mounted at `/api` in `cmd/lanventory/cmd/server.go` behind
  `SecurityHeaders` and `SessionMiddleware`, outside the web router. It calls the same store
  functions (and thus validation, number generation, inspection rules) as the UI; no business
  rule is duplicated. Only small audit-entry builders are repeated, with `via: api` and the
  token ID (actor = token owner). Endpoints: `/me`, locations (CRUD, 409 in use), categories
  (read-only), assets (list with `q`/`category`/`location`/`status`/`f_<key>`/`sort`/`dir`, get, by
  number, create, partial PATCH, delete), inspections (list, get, per asset, due, record, delete).
  Decisions where the spec left room: PATCH is partial with `null` clearing, unknown properties
  and a changed `category_id`/`inventory_no` give 422 (`unknown_field`, `immutable`); validation
  `errors` use API names (`location_id`, `fields.<key>`) and are reported in one round per
  stage (type errors before store rules); a page past the end is empty instead of clamped;
  `inspected_on` and `inspector_name` default to today and the token owner, `set_defective`
  defaults to false like the form (changed in 6.5); `inspections/due` is paged in memory; asset lists carry no
  `dguv` object (only single-asset responses); the MFA policy applies to browser sessions only,
  not tokens; `invalid_parameter`/`invalid_json`/`unsupported_media_type` were added to the codes.
- As built (tokens): migration `00005_api_tokens.sql`; tokens `lvk_` + 40 base62 characters,
  SHA-256 hash only, effective permission = scope AND the owner's current role/flags, disabled
  owner/revoked/expired give 401. User page `/account/tokens` (create needs a recent
  authentication, secret shown once as the POST response, revoke own tokens), admin page
  `/admin/tokens` (list all, revoke any); audit `token.create`/`token.revoke` without secrets.
  Config `api.rate_limit` (10/s) and `api.rate_burst` (30) per token (sessions per user).
- As built (tests): `spec_test.go` (routes vs. spec), `validate_test.go` (every operation incl.
  errors, request and response validated with kin-openapi, a test-only dependency; the spec
  document itself is not run through `Validate` because kin-openapi 0.133 rejects the 3.1 type
  `"null"`), `api_test.go` (scopes 401/403, owner downgrade, revoked/expired/disabled, hash only,
  rate limit, session + CSRF + pending MFA + MFA policy, validation shape, by-number, auto
  numbers, inspection rules, audit `via=api`), `web/web_tokens_test.go` (token UI).

## Phase 6 – CSV import/export and release v0.1.0

- [x] 6.1 **CSV import.** Wizard at `/assets/import` (editors and admins), code in
  `internal/store` (`csvfile.go` decoding and parsing, `import.go` mapping suggestion and the
  run) and `internal/web` (`handlers_import.go`, `importstate.go`, templates `import_*.html`).
  Upload (at most 5 MB, delimiter and encoding auto or chosen) → column mapping → preview →
  import. Tests: delimiter sniffing, encodings, header auto-mapping, value parsing, duplicates,
  skip vs update, all-or-nothing vs skip-invalid, locations, upload limit, token binding, the
  full flow, escaping and the round trip with 6.2. As built:
  - *File.* Auto encoding: valid UTF-8 (BOM stripped) is UTF-8, UTF-16 only with a byte order
    mark, otherwise Windows-1252 with a hand-written table for 0x80–0x9F (no new dependency).
    Auto delimiter: the most frequent of `;` `,` tab in the first line outside quotes (tie: `;`,
    then `,`). At most 20 000 data rows and 100 columns; fully empty rows are dropped.
  - *Mapping.* Targets are the export headers (`inventory_no`, `category`, `name`, `serial_no`,
    `manufacturer`, `model`, `location`, `status`, `purchase_date`, `dguv_interval_months`,
    `note`) and `field:<key>` for the fields of all categories (a key shared by several
    categories is one target). Pre-selection by our headers, `field:<key>`, German and English
    names and field labels/keys; each target at most once, the first column wins. The export
    columns `category_prefix`, `last_inspected_on`, `next_due` are ignored.
  - *Values.* Category by name or prefix (case-insensitive); status English or German
    (aktiv, defekt, ausgemustert, verloren/verschollen); bool 1/0/ja/nein/yes/no/true/false/x;
    numbers with comma or dot; dates `YYYY-MM-DD` and `DD.MM.YYYY`; select options match
    case-insensitively. A cell that starts with an apostrophe followed by `=+-@` tab or CR is
    read without the apostrophe (the export's formula guard), so exports round-trip. All values
    then go through the normal store validation (`validateAsset`), so UI, API and import share
    the rules.
  - *Rows.* Empty inventory number: generated in the same transaction (same generator). A
    number repeated in the file is an error for the later rows (case-insensitive). Existing
    number: *skip* (default) or *update*: the mapped columns replace the values (an empty cell
    clears the value, except status, which stays), unmapped columns stay; an explicit category
    cell that differs from the asset is a row error (no category column: no check); an update
    without differences counts as skip ("unchanged"). The default category applies to new rows
    without a category; the default location to new assets with an empty location cell or
    without a location column. A field that does not belong to the row's category with a
    non-empty cell is a row error.
  - *Run.* The preview is the real run inside a transaction that is rolled back, so counts and
    even generated numbers match the import (`store.RunImport(…, commit=false)`); the import runs
    in one transaction. All-or-nothing with any error writes nothing (the confirm button is
    hidden, a forced confirm answers 422); skip-invalid writes the valid rows. Locations are
    created inside the transaction (option, default on) and vanish on rollback.
  - *State.* The parsed upload lives in memory only (`importStates`, like `mfa.Ceremonies`):
    random 192-bit token bound to user and session, one upload per user, at most 16, TTL 30
    minutes after last use, a claim flag against double submits. Only the token travels in
    hidden fields; every step is a POST with CSRF. The mapping page is the response to the
    upload POST (no tokens in URLs).
  - *Audit.* One `asset.import` entry (created, updated, skipped, invalid, options) plus
    `asset.create`/`asset.update` per asset with `via: import` and the usual `changes`.
  - *Upload limit.* `importBody` limits and parses the multipart body before the CSRF check
    (editors only); over 5 MB (plus form fields) answers 413. Reverse proxies need a matching
    `client_max_body_size` (see [reverse-proxy.md](reverse-proxy.md)).
- [x] 6.2 **CSV export of assets.** `GET /assets.csv` (every role) with exactly the list's
  filters and sort (`q`, `category`, `location`, `status`, `f_<key>`, `sort`, `dir`; no paging),
  linked as "CSV export" from the list (the link sits in the results fragment, so it follows
  the filters). Columns: `inventory_no`, `category`, `category_prefix`, `name`, `serial_no`,
  `manufacturer`, `model`, `location`, `status`, `purchase_date`, `dguv_interval_months` (the
  asset's override), `note`, `last_inspected_on`, `next_due` (stored due date of the latest
  inspection; empty without DGUV requirement or inspection), then `field:<key>` per custom field
  (union over the categories in the result, sorted by category name then position; one filtered
  category gives just its fields). Numbers with a dot, bools `1`/`0`, dates `YYYY-MM-DD`, UTF-8
  without BOM, comma, CRLF. Hard cap `store.MaxExportRows` = 50 000 rows (the response then
  carries the header `X-Export-Truncated`). The formula guard lives in one helper
  (`internal/web/csv.go`, `csvWriter`) used by this and the inspection export; it covers every
  cell including header cells. File name `lanventory-assets-YYYY-MM-DD.csv`. Tests: columns,
  escaping, guard, filters, sort, link, viewer access, round trip (export → import into a fresh
  database → byte-identical re-export).
- [x] 6.3 **Documentation pass and release v0.1.0.** Review of all docs, `goreleaser release
  --snapshot --clean --skip=publish,docker` locally, release created in the GitHub web UI after
  approval. As built: docs reviewed against the code (README as landing page, installation
  guide, new [testing.md](testing.md)); `goreleaser release --snapshot` builds all archives and
  deb/rpm/apk/Arch packages locally; the release itself is created in the GitHub web UI after
  approval.
- [x] 6.4 **Package repository (repo.lan.party).** As built (this repository's side):
  `.github/workflows/repo-lan-party.yml` notifies repo.lan.party (`repository_dispatch` to
  `lan-dot-party/repo` via the secret `REPO_DISPATCH_TOKEN`) after a successful Release workflow
  and when a release is deleted; the tool entry in the repository's `tools.toml` is added when
  v0.1.0 is published.
- [x] 6.5 **Optional category prefix.** Organisations that number all assets `0001`, `0002`, …
  without category codes: the prefix may be empty (unique only when set, partial index in the
  still unreleased migration 00003); an empty prefix drops `{prefix}` and the following separator;
  a format without `{prefix}` (for example `{seq:04}`) uses one shared sequence; taken numbers are
  skipped.
- [x] 6.6 **Real-world inspection data.** A real inspection protocol (an electrician's Excel
  form, one row per device, 165 devices, test instrument per VDE 0413; kept out of the repository)
  was compared with the model. Most fields fit and the simplified guidance limits match DIN VDE
  0701-0702; the inspector also failed devices the limits would not flag, so LANventory must not
  derive the result. Gaps found: readings beyond the range (`>299` MΩ), inspection reason, method
  of the leakage current, touch current separate from the protective conductor current, test
  device ID and calibration date, and an import of past inspections. As built:
  - Migration `00006_inspection_details.sql`: `pe_resistance_cmp`, `insulation_cmp`,
    `leakage_cmp`, `touch_cmp` (`''`, `<`, `>`), `touch_ma`, `leakage_method` (direct,
    differential, alternative), `reason` (initial, repeat, repair), `test_device_id`,
    `test_device_calibrated_until`. All empty by default.
  - Input accepts a leading `>`/`<` (`>299`, `< 0,01`), displayed as "> 299 MΩ". Guidance compares
    the number as it is; touch current above 0.5 mA flags for classes I and II, `leakage_ma` keeps
    the class I limit of 3.5 mA (the former class II flag on `leakage_ma` moved to `touch_ma`).
  - Form: reason preselected (repeat when the asset has inspections), test device, ID and
    calibration prefilled from the user's last inspection, non-blocking calibration hint
    (`app.js`, server-rendered start state). Also in the detail page, protocol, inspections CSV,
    REST API and OpenAPI (`*_cmp` properties, a string like `">299"` is accepted as input) and
    the JSON data export/import (optional properties of format version 1, no version bump).
  - CSV import: optional inspection columns record one inspection per row (German aliases and
    values), only for DGUV categories, identical inspections (asset, date, result) not duplicated,
    only for users with `can_inspect` (`imperr.inspection_forbidden`), preview and result count the
    inspections, audit `inspection.create` with `via: import`. A failed result does not change the
    status. Tests in `store`, `web` and `api`; `tools/e2e/inspection_e2e.py` adjusted (not run).
- [x] 6.7 **Change the category of an asset.** Edit form: "Change category" (GET
  `/assets/{id}/edit?category=ID`) shows the new category's fields with the carried-over values
  (`store.CarryValues`: same key and valid for the new type) and lists the values that would be
  lost; saving (hidden `category`) moves the asset in one transaction, required fields of the new
  category apply, the audit entry records `category` and `field:<key>` changes by key. API:
  `PATCH` with another `category_id` does the same. Number and inspections stay.
- [x] 6.8 **Data export and import (JSON, with inspections).** Moves a whole inventory from one
  instance to another (for example test to production). As built: `store.ExportData` /
  `store.ImportData` (`dataexport.go`, `dataimport.go`), format `lanventory-export` version 1
  (locations, categories with fields, assets with values and inspections; no users, sessions,
  tokens, settings or audit log). Import in one transaction: locations and categories matched by
  name and created when missing (prefix clash and field type mismatch are conflicts, differing
  settings of existing categories are adopted from the file with `UpdateCategories` – default on in
  the UI and CLI, because a fresh instance has seeded templates – or reported as warnings; missing
  fields and select options are added),
  assets matched by inventory number (`skip` reports, `abort` fails), new assets keep number,
  status and timestamps, inspections are inserted as stored for new assets only, `recorded_by` maps
  to the user of that name or the importing user; problems carry a path such as
  `assets[12].fields.length_m`; `DryRun` rolls back. Web (admins, recent authentication):
  `/admin/data` with download, upload, preview (re-check with skip/abort), confirm and result,
  upload kept in the CSV import's stash; CLI `lanventory data export [-o file]` and
  `lanventory data import [--dry-run] [--on-conflict skip|abort] [--update-categories] <file>`. Audit `data.export`,
  `data.import` and `asset.create` per asset. No migration.
- [x] 6.9 **Bulk edit of assets.** Change several assets at once (category, location, status,
  manufacturer, model, purchase date, DGUV interval, note and custom fields; not name, serial
  number or inventory number). As built: `store.BulkUpdateAssets` (`internal/store/bulk.go`) applies
  an optional-part `BulkChange` to up to 1000 assets in one transaction through the same
  `updateAsset` as a single edit (so category moves carry values by key and required fields are
  validated), collects validation errors per asset (nothing is written when any occurs) and
  returns per asset the audit changes and the values lost on a category move; dry run rolls back;
  `AssetIDsByFilter` and `AssetsByIDs` support the UI. Web (editors, admins): checkbox column,
  select-all and "n selected" bar in the asset list (`app.js`, delegated, survives htmx swaps),
  "all matches of the filter" (resolved server-side, at most 1000); `POST /assets/bulk/edit`
  (form with a "change" tick per property, custom fields when the assets end in one category,
  "Load fields" after choosing a category), `/assets/bulk/preview` (dry run, per asset old → new,
  lost values, errors grouped per field), `/assets/bulk/apply` (audit `asset.update` per changed
  asset with `via: bulk`, flash "n assets changed"). API: `PATCH /api/v1/assets` with `ids`,
  `changes`, `dry_run` (errors keyed `"<asset id>.<field>"`, audit `via: api_bulk`). Browser test
  `tools/e2e/bulk_e2e.py`. No migration.

## Later

- QR/barcode labels for assets (label printing) and scanning them with a phone.
- German number input formatting in forms.
- Events and packing lists with check-out and check-in.
- Lending assets to persons.
- Photos and attachments on assets.
- Inspection protocol as PDF.
- Multiple locations per event.
- RCD/PRCD test (type, trip current, trip time) in the inspection record.
- Result "disposed" as its own inspection result (today: fail plus status retired).
