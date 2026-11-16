# API guide

LANventory has a JSON REST API under `/api/v1`. The OpenAPI 3.1 specification is the single source
of truth: it is served at `/api/openapi.json` (and `/api/openapi.yaml`), rendered at `/api/` and
kept in the repository at [`internal/api/openapi.yaml`](../internal/api/openapi.yaml). Tests check
that every route is documented and vice versa, and call every endpoint – including error cases –
validating status code, content type and body against the specification
([ADR 0007](adr/0007-openapi-single-source-of-truth.md)). The API uses the same store functions and
validation as the web UI, so both enforce the same rules.

## Authentication

Users create tokens in the web UI under **Account → API tokens**; the secret is shown exactly once.

```
Authorization: Bearer lvk_3Fq9…
```

- The prefix `lvk_` (followed by 40 random characters) makes tokens easy to spot in logs and secret
  scanners. Only a SHA-256 hash is stored; a lost token cannot be recovered, only replaced.
- Tokens can expire (optional, in days) and be revoked at any time by their owner or an admin.
  Creating a token requires a recent authentication in the web UI.
- A token acts as **its owner**, with at most the owner's *current* rights: the effective
  permission is the token's scope AND the owner's role and flags at request time. Demoting or
  disabling the owner takes effect immediately (a disabled owner makes the token invalid).
- A logged-in browser session may call the API as well (complete sessions only, not one that still
  waits for a second factor). Rights follow the role like in the UI; unsafe methods need the header
  `X-CSRF-Token` with the session's token and a same-origin request. If the MFA policy requires a
  second factor the user lacks, the API answers 403 `mfa_required`.
- Requests are rate limited per token (`api.rate_limit`, `api.rate_burst`); excess requests get
  HTTP 429 with `Retry-After`.

### Scopes

| Scope | Grants | Also needs the owner to be |
| --- | --- | --- |
| `read:assets` | read locations, categories and assets (including the DGUV state of an asset) | any role |
| `write:assets` | create, update and delete assets and locations | editor; deleting an asset: admin |
| `read:inspections` | read inspections and the due overview | any role |
| `write:inspections` | record inspections, delete an inspection | `can_inspect` flag; deleting: admin |

Categories are read-only in API v1; admins manage categories and fields in the web UI.
`GET /api/v1/me` needs no scope and shows who you are and which scopes are effective right now.

## Conventions

- JSON, snake_case. Timestamps are RFC 3339 in UTC, dates are `YYYY-MM-DD`, numbers are JSON numbers.
- Custom field values are an object `fields` keyed by the field key: `number` fields as numbers (the
  unit is in the category's field definition), `bool` as booleans, `text`, `date` and `select` as
  strings. Unset fields are absent.
- Lists return `{"items": [...], "page": 1, "per_page": 50, "total": 123}`. Parameters: `page`
  (from 1) and `per_page` (default 50, maximum 200). A page past the end is empty.
- `PATCH` is a partial update: only the sent properties change, `null` clears optional values. The
  inventory number of an asset cannot change (422 `immutable`). Another `category_id` moves the
  asset: custom values carry over to fields with the same key (when valid for the new type), others
  are dropped; `fields` then refer to the new category, whose required fields must be filled. The
  number and the inspections stay; the move is in the asset history.
- Without `inventory_no`, a new asset gets the next number (of its category prefix, if the format
  uses one; the prefix may be empty), generated from
  `inventory.number_format`; a manual number must be unique.
- Every change is written to the audit log with the token's owner as actor and `"via": "api"` plus
  the token ID (never the token).

## Errors

Errors are `application/problem+json` (RFC 9457) with a stable `code`:

```json
{"type": "urn:lanventory:problem:validation_failed", "title": "Unprocessable Entity", "status": 422,
 "code": "validation_failed", "detail": "the request is well-formed but its values are invalid",
 "errors": {"name": "required", "fields.length": "invalid_number"}}
```

| Status | `code` | Meaning |
| --- | --- | --- |
| 400 | `invalid_parameter`, `invalid_json` | bad query parameter; body is not one JSON object |
| 401 | `invalid_token`, `unauthorized` | token unknown, revoked, expired or owner disabled; no (complete) session |
| 403 | `insufficient_scope` | the token lacks the scope |
| 403 | `forbidden` | the owner's current role or flags do not allow it |
| 403 | `csrf_failed`, `mfa_required` | session without CSRF header or same origin; second factor missing |
| 404 | `not_found` | no such resource or endpoint |
| 405 | `method_not_allowed` | for example writing categories |
| 409 | `conflict` | deleting a location that assets use |
| 413, 415 | `payload_too_large`, `unsupported_media_type` | body over 1 MiB; not `application/json` |
| 422 | `validation_failed` | invalid values; `errors` maps field names (custom fields as `fields.<key>`) to codes such as `required`, `too_long`, `taken`, `invalid_date`, `invalid_number`, `invalid_option`, `invalid_location`, `invalid_status`, `immutable`, `unknown_field`, `invalid_type` |
| 422 | `not_inspectable`, `asset_inactive` | inspection of an asset whose category needs no DGUV V3, or that is retired or lost |
| 429 | `rate_limited` | per-token rate limit |

## Endpoints

| Method & path | Scope | Purpose |
| --- | --- | --- |
| `GET /api/v1/me` | – | Acting user, authentication kind, token, effective scopes |
| `GET /api/v1/locations`, `GET /locations/{id}` | read:assets | Locations (flat list, with asset counts) |
| `POST /api/v1/locations`, `PATCH /locations/{id}`, `DELETE /locations/{id}` | write:assets | Create, change, delete (409 while assets use it) |
| `GET /api/v1/categories`, `GET /categories/{id}` | read:assets | Categories with field definitions, `dguv_required`, `dguv_interval_months` |
| `GET /api/v1/assets` | read:assets | List and search: `q`, `category`, `location`, `status`, `f_<key>` (with `category`), `sort`, `dir` |
| `GET /api/v1/assets/{id}`, `GET /assets/by-number/{inventory_no}` | read:assets | One asset with `fields` and `dguv` (`state`, `last_inspected_on`, `next_due`) |
| `POST /api/v1/assets`, `PATCH /assets/{id}` | write:assets | Create (`category_id`, `name` required), partial update |
| `PATCH /api/v1/assets` | write:assets | Bulk update: `ids` (at most 1000), `changes`, `dry_run`; all or nothing (see below) |
| `DELETE /api/v1/assets/{id}` | write:assets, admin | Delete an asset (retiring it with `status: retired` keeps the history) |
| `GET /api/v1/inspections` | read:inspections | List: `from`, `to`, `result`, `category`, `location`, `asset`, `inspector` |
| `GET /api/v1/inspections/{id}`, `GET /assets/{id}/inspections` | read:inspections | One inspection; the inspections of an asset |
| `GET /api/v1/inspections/due` | read:inspections | Overdue, due within 30 days, never inspected: `state`, `category`, `location` |
| `POST /api/v1/assets/{id}/inspections` | write:inspections, `can_inspect` | Record an inspection |
| `DELETE /api/v1/inspections/{id}` | write:inspections, admin | Delete an inspection (audited) |

Inspections are records and cannot be edited. They follow the rules of the web form: only assets of
categories that require DGUV V3 and that are not retired or lost; the date may not be more than one day
in the future; measurements (`pe_resistance_ohm`, `insulation_mohm`, `leakage_ma` for the protective
conductor / leakage current, `touch_ma` for the touch current) are optional non-negative numbers.
A reading beyond the instrument's range is a number plus a qualifier property `pe_resistance_cmp`,
`insulation_cmp`, `leakage_cmp` or `touch_cmp` (`""`, `"<"`, `">"`); for convenience the measurement
may also be a string such as `">299"` or `"< 0,01"`, which sets the qualifier (a contradicting
explicit one is a 422 `invalid_cmp`). Further optional properties: `reason` (`initial`, `repeat`,
`repair`), `leakage_method` (`direct`, `differential`, `alternative`), `test_device`,
`test_device_id` and `test_device_calibrated_until` (date). Responses always contain all of them
(empty string, or null for numbers and the date, when not recorded). Guidance compares the numbers as they are. `inspected_on` defaults to today (UTC) and `inspector_name` to the token owner.
LANventory never derives pass or fail from measurements; `guidance` lists non-binding hints. A failed
result sets an active asset to defective when `set_defective` is `true`; a passed result sets a
defective asset back to active when `set_active` is `true`. The response contains `asset_status`
(`previous`, `current`). LANventory documents inspections; it does not replace a qualified electrician.

## Examples

```bash
export TOKEN=lvk_...
BASE=https://inventory.example

# who am I, and what may I do?
curl -H "Authorization: Bearer $TOKEN" $BASE/api/v1/me

# search assets
curl -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/assets?q=drum&status=active&per_page=20"

# look up a label scan
curl -H "Authorization: Bearer $TOKEN" $BASE/api/v1/assets/by-number/KT-00012

# create an asset (inventory number generated)
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"category_id": 2, "name": "Drum 25 m", "location_id": 1, "fields": {"length": 25}}' \
  $BASE/api/v1/assets

# mark it defective
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"status": "defective"}' $BASE/api/v1/assets/12

# move three assets to location 3 and retire them in one call (add "dry_run": true to only check)
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"ids": [12, 13, 14], "changes": {"location_id": 3, "status": "retired"}}' $BASE/api/v1/assets

# record a DGUV V3 inspection
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"result": "pass", "protection_class": "I", "visual_ok": true, "function_ok": true, "pe_resistance_ohm": 0.12, "insulation_mohm": ">299", "touch_ma": 0.2, "reason": "repeat"}' \
  $BASE/api/v1/assets/12/inspections

# what is overdue?
curl -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/inspections/due?state=overdue"
```
