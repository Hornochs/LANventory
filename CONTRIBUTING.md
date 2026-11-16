# Contributing

Thank you for helping! Issues and pull requests are welcome. For larger changes, open an issue
first so we can agree on the approach. The plan of record is [docs/ROADMAP.md](docs/ROADMAP.md);
refer to its step numbers in issues and pull requests.

## Development setup

You need Go 1.26 or newer (`go.mod` names the toolchain; an older Go downloads it automatically
unless `GOTOOLCHAIN=local` is set).

```bash
go vet ./...
go test ./...
CGO_ENABLED=0 go build -o bin/lanventory ./cmd/lanventory
./bin/lanventory -c config.yaml config init
echo 'a-long-password' | ./bin/lanventory -c config.yaml user create admin --role admin
./bin/lanventory -c config.yaml server
```

## Tests

`go test ./...` and `golangci-lint run` (v2.15.0, as in CI) must pass. The browser end-to-end
scripts in `tools/e2e/` are run by hand against a live instance. Details in
[docs/testing.md](docs/testing.md).

## Ground rules

- **English** for code, comments, docs and commit messages (imperative mood). UI texts are
  keys in `internal/web/i18n.go` with German and English text.
- **No inline JavaScript or CSS** in templates; the Content Security Policy forbids it. Put
  code into `internal/web/static/`.
- **Escape all user input.** Use `html/template`; never build HTML from strings.
- **Keep the security rules** in [CLAUDE.md](CLAUDE.md) ("Rules that must not be broken") and
  [docs/security.md](docs/security.md).
- **No GPL dependencies.** LANventory is MIT-licensed.
- **No private data.** No hostnames, IP addresses, credentials or names of real events in code,
  tests, fixtures or docs.
- **Docs with code.** Configuration changes go into `docs/configuration.md` (generated: `go run ./tools/gendocs`), structural changes
  into `docs/architecture.md`, user-visible changes into `CHANGELOG.md`.
- **Migrations are immutable** once released; add a new file instead.

## Translations

Every key in `internal/web/i18n.go` has a German and an English text. A test fails when a
template uses a key that does not exist.

## Packages

`.goreleaser.yaml` builds all packages. To check locally, install
[GoReleaser v2](https://goreleaser.com/install/) and run:

```bash
goreleaser check
goreleaser release --snapshot --clean --skip=publish,docker
sh packaging/test-install.sh deb dist/lanventory_*_amd64.deb   # inside a Debian container, as root
```
