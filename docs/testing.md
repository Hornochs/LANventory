# Testing

LANventory has three layers of tests: Go unit and handler tests (run in CI), the linter (run in
CI) and browser end-to-end scripts (run by hand against a live instance).

## Unit and handler tests

```bash
go vet ./...
go test ./...            # everything
go test -short ./...     # skip the slower tests
```

- Package tests cover `auth`, `secret`, `mfa`, `config`, `store` and the CSV decoding.
- Web tests in `internal/web` drive the real router through `httptest`: login, lockout, CSRF and
  origin checks, roles for every route group, forms and validation, escaping, security headers,
  absence of inline code in templates, template keys versus `i18n.go`, DGUV inspections, CSV
  export and import.
- `internal/api` tests check every route against the OpenAPI document and validate every
  response with kin-openapi (a test-only dependency).
- `tools/gendocs` has a freshness test: it fails when `docs/configuration.md`,
  `docs/config.schema.json` or `docs/llms-full.txt` are out of date. Fix it with
  `go run ./tools/gendocs`.
- The race detector (`go test -race ./...`) needs a C compiler and runs in CI only.

If your Go is newer than the one in `go.mod`, set `GOTOOLCHAIN=local` so that `go get` does not
pull module versions that need a newer toolchain.

## Linter

CI uses golangci-lint v1.64.8:

```bash
golangci-lint run
```

## Browser end-to-end tests

The scripts in `tools/e2e/` drive a real Chromium with Selenium against a running instance and
take screenshots. They are not part of `go test` and not run in CI, because they need a live
server and a browser.

### Prerequisites

- Python 3 with Selenium: `pip install selenium` (or your distribution's `python3-selenium`).
- Chromium and a matching chromedriver. Defaults: `/usr/bin/chromium` and
  `/usr/bin/chromedriver`; override with the environment variables `CHROMIUM` and
  `CHROMEDRIVER`. `HEADLESS=0` shows the browser window.
- A running LANventory (see [installation.md](installation.md)) and a **throwaway instance or
  account**: the scripts create and delete assets, locations and tokens, and they change the UI
  language of the account.
- An output directory for the screenshots (the fourth argument).

Accounts:

| Script | Account needed |
|---|---|
| `passkey_e2e.py` | a throwaway user without second factors; the instance must be served over HTTPS (passkeys need a secure context and a matching `public_url`) |
| `inventory_e2e.py` | an admin without a second factor |
| `inspection_e2e.py` | an admin without a second factor and with the "may record DGUV inspections" flag (`can_inspect`) |
| `api_e2e.py` | an admin without a second factor and with the `can_inspect` flag |
| `import_e2e.py` | an admin without a second factor |
| `bulk_e2e.py` | an admin without a second factor |

Create a suitable local instance with the commands from the README quick start; for the
passkey script put a reverse proxy with HTTPS in front (see
[reverse-proxy.md](reverse-proxy.md)) and set `public_url` accordingly. After the passkey run,
remove the account's factors with `lanventory user reset-mfa <name>` or delete the user.

### Scripts

All scripts take the same arguments and stop with an error at the first failed check:

```bash
python3 -I tools/e2e/<script>.py <base-url> <user> <password> <screenshot-dir>
```

| Script | What it covers |
|---|---|
| `passkey_e2e.py` | Password login, passkey registration with a virtual WebAuthn authenticator, passwordless login, passkey as second factor, re-authentication, TOTP enrollment and login, recovery codes page; screenshots in light and dark theme |
| `inventory_e2e.py` | Create a location and an asset in a seeded category with a custom field, generated inventory number, edit and change history, htmx search that updates the list without a page load and pushes the filters into the URL; screenshots; cleanup |
| `inspection_e2e.py` | German UI, a power strip shown as never inspected on the due page, a failed inspection with guidance flag (asset becomes defective), a passed inspection, next due date, script-free printable protocol, inspection list and CSV export; screenshots; cleanup |
| `api_e2e.py` | Create an API token with all scopes (shown once), call the API with it (me, categories, create an asset with a custom field, lookup by number, record an inspection, due list), problem+json errors, revoke the token and see it rejected; screenshots of the token page and the API docs |
| `bulk_e2e.py` | Select assets with the checkboxes (select all, counter, selection after an htmx swap), bulk-change location and status, preview (nothing written yet), apply, flash message, history entries; screenshots; cleanup |
| `import_e2e.py` | CSV import with a Windows-1252, semicolon-separated file with German headers: automatic mapping, preview with one invalid row, import with "skip invalid rows", imported assets in the list, CSV export of the filtered list; screenshots; cleanup |

Usage lines as in the scripts' docstrings:

```bash
python3 -I tools/e2e/passkey_e2e.py    <base-url> <user> <password> <screenshot-dir>
python3 -I tools/e2e/inventory_e2e.py  <base-url> <admin-user> <password> <screenshot-dir>
python3 -I tools/e2e/inspection_e2e.py <base-url> <admin-user> <password> <screenshot-dir>
python3 -I tools/e2e/api_e2e.py        <base-url> <admin-user> <password> <screenshot-dir>
python3 -I tools/e2e/import_e2e.py     <base-url> <admin-user> <password> <screenshot-dir>
python3 -I tools/e2e/bulk_e2e.py       <base-url> <admin-user> <password> <screenshot-dir>
```

Do not commit screenshots or credentials; use a throwaway password and keep the output outside
the repository.
