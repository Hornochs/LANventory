# LANventory – notes for AI-assisted contributions

LANventory is an inventory system for LAN parties: assets with inventory number, category,
name and serial number, configurable fields per category, and tracking of DGUV V3 electrical
inspections (German focus). One Go binary, SQLite, server-rendered web UI.

**Everything in this repository is in English** – code, comments, docs, commit messages. The
web UI is bilingual; German strings live only in `internal/web/i18n.go`. The repository is
public: never commit credentials, hostnames, IP addresses, internal paths or names of specific
LAN parties.

## Read first

1. `docs/ROADMAP.md` – phases with numbered steps; "Current status" says what is done and
   which step is next.
2. `docs/architecture.md` – package layout, request flow, data model, security mechanisms.
   Read this instead of re-exploring the code.
3. `docs/concepts.md` – domain concepts (assets, categories and fields, inventory numbers,
   status, DGUV V3).
4. `docs/adr/` – decision records; read the index before changing a design decision.
5. `docs/security.md` – threat model and mitigations.
6. `docs/configuration.md` – configuration keys (generated, see below).
7. `docs/testing.md` – unit tests, linter and the browser end-to-end scripts.

## "Continue" / "mach weiter"

The user may simply say "mach weiter" (or "continue") in a new chat; no further context is
needed, everything is in the files above. Then:

1. Read "Current status" in `docs/ROADMAP.md`.
2. Implement the step named under **Next** (several steps if they are small).
3. Run `go vet ./...` and `go test ./...`.
4. Update "Current status", `docs/architecture.md` (structural changes) and `CHANGELOG.md`
   (`[Unreleased]`).
5. Report briefly what is done and what comes next.

Do not commit or push unless explicitly asked.

## Rules that must not be broken

- **Secrets are encrypted and never logged.** Anything secret in the database (MFA seeds, API
  tokens' secrets) is AES-GCM encrypted via `internal/secret` or hashed; never in logs, the
  audit log, responses or pre-filled forms. Passwords only as Argon2id hashes.
- **No inline scripts or styles** in the UI (CSP). Put code and styles into
  `internal/web/static/`; use `data-*` attributes instead of `onclick`.
- **Escape all user input.** Output only through `html/template`; never build HTML from
  strings.
- **State changes need CSRF token and same-origin check** (`csrf` middleware); the session cookie
  stays `SameSite=Strict`.
- **Check roles on the server** (`requireRole`, `User.Can`); never rely on hidden buttons. Keep
  the last-admin protection.
- **No change to configuration or data model without matching docs**
  (run `go run ./tools/gendocs`, update `docs/config.example.yaml`, `docs/architecture.md`). Never change a
  released migration – add a new file.
- **Generated docs.** `docs/configuration.md`, `docs/config.schema.json` and
  `docs/llms-full.txt` are generated: run `go run ./tools/gendocs` after changing
  `internal/config/config.go` (field doc comments and `enum` tags are the source), `llms.txt`
  or the docs it includes; a test (`tools/gendocs`) fails when they are stale.
- **Keep `llms.txt` in sync** with the API and `docs/concepts.md` (mark planned parts as
  planned until they exist). It is embedded by `llms.go` and served at `/llms.txt`.
- **UI text only via keys in `internal/web/i18n.go`**, German and English.
- **No private data** (hostnames, IPs, credentials, names of real events) in code, tests or
  docs.

## Commands

```bash
go vet ./...
go test ./...                       # unit tests
go test -short ./...                # skip slower tests
go run ./tools/gendocs              # regenerate configuration.md, config.schema.json, llms-full.txt
CGO_ENABLED=0 go build -ldflags "-X github.com/lan-dot-party/lanventory/pkg/version.Version=dev" \
  -o bin/lanventory ./cmd/lanventory
./bin/lanventory -c config.yaml config init
echo 'a-long-password' | ./bin/lanventory -c config.yaml user create admin --role admin
./bin/lanventory -c config.yaml server
golangci-lint run                   # v2.15.0, as in CI (config: .golangci.yml)
python3 -I tools/e2e/<script>.py <base-url> <user> <password> <screenshot-dir>   # see docs/testing.md
```

`go.mod` names the Go version (`go 1.26.0`) and toolchain (`go1.26.11`); an older installed Go
downloads that toolchain automatically. Do not set `GOTOOLCHAIN=local` with an older Go. When
updating dependencies, keep `go 1.26.x`: refuse module versions that need a newer Go (`go get`
reports "requires go >= …"). The race detector needs a C compiler and runs in CI only.

## Releases

Create and publish a release with tag `vX.Y.Z` in the GitHub web UI (write the notes there).
`.github/workflows/release.yml` then builds with GoReleaser (`.goreleaser.yaml`): binaries and
archives for linux amd64/arm64/armv7, `.deb`, `.rpm`, `.pkg.tar.zst` and `.apk` packages,
`SHA256SUMS` and the multi-arch image `ghcr.io/<owner>/lanventory`. Every amd64 package is first
installed, started and removed in a fresh container (`packaging/test-install.sh`); only then
are the files attached, the image pushed and the build provenance attested. The build is
reproducible (`-trimpath`, commit timestamps).

## Conventions

- Go 1.26; no dependency that requires a newer Go. Go supports the two newest major
  releases; move on to the next one (all lan.party Go tools together) before 1.26 drops out.
- Logging with `log/slog`; wrap errors with context (`fmt.Errorf("…: %w", err)`).
- Timestamps in the database as Unix seconds (UTC).
- UI texts only via keys in `internal/web/i18n.go` (English and German, German first).
- Commit messages in English, imperative mood.
