# LANventory

**Inventory for LAN parties – with the paperwork for the electrical safety inspections (DGUV V3)
built in.**

LANventory tracks your equipment (assets) with inventory number, category, name and serial
number, lets you define the fields each category needs, and keeps the records of the periodic
inspections that German law requires for electrical equipment.

> Status: release v0.1.0 in preparation, see [docs/ROADMAP.md](docs/ROADMAP.md) and the
> [changelog](CHANGELOG.md).

## What it does

- **Assets with your own fields.** Every item has an inventory number (generated per category
  prefix, for example `SL-00042`, or entered by hand), category, name, serial number, location
  and status (active, defective, retired, lost). Each category defines its own fields (text,
  number with unit, select, yes/no, date) in a field editor; German templates such as power
  strip or cable drum are seeded once. Search, filters, sorting and paging, and a change history
  per asset.
- **DGUV V3 inspections.** Record inspections with measurements (protective conductor and
  insulation resistance, leakage current), see what is overdue, due soon or never inspected,
  print an A4 protocol and export the inspections as CSV. LANventory shows guidance hints but
  never decides pass or fail, and does not replace a qualified electrician.
- **CSV import and export.** Export the (filtered) asset list; import files from Excel,
  including Windows-1252 and semicolons, with column mapping and a preview before anything is
  written.
- **REST API.** JSON API under `/api/v1` with scoped tokens, errors as RFC 9457 problem
  documents and an OpenAPI 3.1 description. See [docs/api.md](docs/api.md).
- **Safe by design.** Users with roles viewer, editor and admin; Argon2id passwords, login
  lockout, optional second factors (TOTP, recovery codes) and passkeys (WebAuthn), an MFA policy
  admins can enforce, an audit log, a strict Content Security Policy, CSRF protection and
  encrypted secrets. See [docs/security.md](docs/security.md).
- **Easy to run.** One static binary, SQLite, German and English web UI (language and light/dark
  theme per user), packages for Debian/Ubuntu, Fedora/RHEL, Arch and Alpine, and a Docker image.
  Works behind a reverse proxy on a single domain and port.

## Install

### From the lan.party package repository (recommended)

Set up the repository once, then install and update the package with your system's package manager.
Instructions for Debian/Ubuntu, Fedora/RHEL/Rocky, Arch Linux and Alpine: **https://repo.lan.party**

```sh
curl -fsSL https://repo.lan.party/setup.sh | sudo sh   # or follow the manual steps on repo.lan.party

sudo apt install lanventory      # Debian / Ubuntu
sudo dnf install lanventory      # Fedora / RHEL / Rocky
sudo pacman -S lanventory        # Arch Linux
sudo apk add lanventory          # Alpine
```

New releases then arrive with your normal system updates (`apt upgrade`, `dnf upgrade`, `pacman -Syu`, `apk upgrade`).

### Manual download from GitHub releases

Each [GitHub release](https://github.com/lan-dot-party/lanventory/releases) has `.deb`, `.rpm`,
Arch (`.pkg.tar.zst`) and Alpine (`.apk`) packages and tarballs with the binary for amd64, arm64
and armv7, plus a `SHA256SUMS` file:

```bash
curl -LO https://github.com/lan-dot-party/lanventory/releases/download/vX.Y.Z/lanventory_X.Y.Z_amd64.deb
sudo apt install ./lanventory_X.Y.Z_amd64.deb
sudo nano /etc/lanventory/config.yaml              # listen, public_url
sudo lanventory user create admin --role admin
sudo systemctl enable --now lanventory
```

Docker (multi-arch image):

```bash
docker run -d --name lanventory -p 9060:9060 \
  -e LANVENTORY_PUBLIC_URL=https://inventory.example.org \
  -v lanventory:/var/lib/lanventory ghcr.io/lan-dot-party/lanventory:latest
docker exec -it lanventory lanventory user create admin --role admin
```

With Docker Compose (named volume or local path): [`compose.yaml`](compose.yaml),
[`compose.bind.yaml`](compose.bind.yaml) and
[docs/installation.md](docs/installation.md#docker-compose).

Details, upgrade, uninstall and backup: [docs/installation.md](docs/installation.md).

## Quick start

From source (Go 1.26 or newer):

```bash
CGO_ENABLED=0 go build -o bin/lanventory ./cmd/lanventory
./bin/lanventory -c config.yaml config init          # writes config.yaml and secret.key
echo 'a-long-password' | ./bin/lanventory -c config.yaml user create admin --role admin
./bin/lanventory -c config.yaml server               # http://127.0.0.1:9060
```

For real use put a reverse proxy with **HTTPS** in front and set `public_url` in `config.yaml`
to the address browsers use: every form is checked against it, session cookies become `Secure`,
and **passkeys only work over HTTPS** with a stable host name. See
[docs/reverse-proxy.md](docs/reverse-proxy.md) (also: allow request bodies of about 6 MB for the
CSV import).

## API

```bash
curl -H "Authorization: Bearer $TOKEN" "https://inventory.example/api/v1/assets?q=drum"
```

Create tokens under Account > API tokens. The OpenAPI spec is served at `/api/openapi.json`,
the human-readable docs at `/api/`; the guide is [docs/api.md](docs/api.md). A machine-readable
summary for LLMs is at `/llms.txt`.

## Documentation

| Document | Content |
|---|---|
| [docs/installation.md](docs/installation.md) | Packages, Docker, upgrade, backup |
| [docs/configuration.md](docs/configuration.md) | `config.yaml` reference ([example](docs/config.example.yaml)) |
| [docs/reverse-proxy.md](docs/reverse-proxy.md) | nginx and other proxies, HTTPS, upload size |
| [docs/concepts.md](docs/concepts.md) | Assets, categories and fields, numbers, status, DGUV V3 |
| [docs/api.md](docs/api.md) | REST API guide |
| [docs/security.md](docs/security.md) | Threat model, mitigations, residual risks |
| [docs/architecture.md](docs/architecture.md) | Packages, request flow, data model |
| [docs/testing.md](docs/testing.md) | Unit tests, linter, browser end-to-end scripts |
| [docs/adr/README.md](docs/adr/README.md) | Decision records |
| [docs/ROADMAP.md](docs/ROADMAP.md), [CHANGELOG.md](CHANGELOG.md) | Plan, status and changes |
| [llms.txt](llms.txt), [docs/llms-full.txt](docs/llms-full.txt) | Documentation for LLMs |

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md), [docs/testing.md](docs/testing.md) and
[CLAUDE.md](CLAUDE.md).

```bash
go vet ./...
go test ./...
```

## Related projects

LANventory is part of the lan-dot-party tool suite with
[ERS-Proxy](https://github.com/lan-dot-party/ERS-Proxy),
[LANLeecher](https://github.com/lan-dot-party/LANLeecher),
[LANPilot](https://github.com/lan-dot-party/LANPilot) and
[FlowGauge](https://github.com/lan-dot-party/FlowGauge).

## License

MIT, see [LICENSE](LICENSE). Copyright (c) 2026 lan-dot-party.

LANventory documents inspections and reminds you of due dates; it does not perform or validate
them. You remain responsible for the safety of your equipment.

### Third-party software

- [htmx](https://htmx.org/) 2.0.4, `internal/web/static/htmx.min.js` (0BSD), vendored without
  changes.
- Go modules (see `go.mod` and `go.sum` for the full list including transitive dependencies):
  [chi](https://github.com/go-chi/chi) (MIT), [cobra](https://github.com/spf13/cobra)
  (Apache-2.0), [goose](https://github.com/pressly/goose) (MIT),
  [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) (BSD-3-Clause),
  [golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto) and
  [x/term](https://pkg.go.dev/golang.org/x/term) (BSD-3-Clause),
  [yaml.v3](https://github.com/go-yaml/yaml) (MIT and Apache-2.0),
  [go-webauthn](https://github.com/go-webauthn/webauthn) (BSD-3-Clause),
  [skip2/go-qrcode](https://github.com/skip2/go-qrcode) (MIT).
- [kin-openapi](https://github.com/getkin/kin-openapi) (MIT) is used by tests only and is not
  part of the binary.
