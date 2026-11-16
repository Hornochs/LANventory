# Installation

## From the lan.party package repository (recommended)

Set up the repository once (Debian/Ubuntu, Fedora/RHEL/Rocky, Arch Linux, Alpine; instructions
at https://repo.lan.party), then install and update LANventory with your package manager:

```sh
sudo apt install lanventory      # Debian / Ubuntu
sudo dnf install lanventory      # Fedora / RHEL / Rocky
sudo pacman -S lanventory        # Arch Linux
sudo apk add lanventory          # Alpine
```

## Packages from GitHub releases

Each [release](https://github.com/lan-dot-party/lanventory/releases) has `.deb`, `.rpm`, Arch
(`.pkg.tar.zst`) and Alpine (`.apk`) packages for amd64, arm64 and armv7, tarballs with the
binary, and a `SHA256SUMS` file.

```bash
# Debian / Ubuntu
sudo apt install ./lanventory_<version>_amd64.deb
# Fedora / RHEL
sudo dnf install ./lanventory_<version>_amd64.rpm
# Arch Linux
sudo pacman -U ./lanventory_<version>_amd64.pkg.tar.zst
# Alpine (the package is not signed with a key known to apk)
sudo apk add --allow-untrusted ./lanventory_<version>_amd64.apk
```

### What the package installs

| Path | Content |
|---|---|
| `/usr/bin/lanventory` | the binary |
| `/etc/lanventory/config.yaml` | configuration, mode 0640 `root:lanventory`, kept on updates |
| `/etc/lanventory/secret.key` | AES-256 key for secrets in the database, created once on first install |
| `/var/lib/lanventory/` | data directory (SQLite database), owned by `lanventory`, mode 0700 |
| `/usr/lib/systemd/system/lanventory.service` | hardened systemd unit `lanventory.service` |
| `/usr/lib/sysusers.d/lanventory.conf` | system user and group `lanventory` |
| `/usr/share/doc/lanventory/` | README, example configuration, reverse proxy guide |

The post-install script creates the `lanventory` system user (via systemd-sysusers, or
`useradd`/`adduser` as a fallback), the directories and `secret.key`. The service is **not
started automatically**, because you need to configure it first:

```bash
sudoedit /etc/lanventory/config.yaml                       # listen, public_url
sudo lanventory user create admin --role admin             # first admin; asks for the password
sudo systemctl enable --now lanventory
```

Open the address in `public_url` and log in. The package config listens on `127.0.0.1:9060`;
put a reverse proxy with HTTPS in front ([reverse-proxy.md](reverse-proxy.md)) and set
`public_url` to the address browsers use. Passkeys need HTTPS. Options are listed in
[configuration.md](configuration.md). Extra environment variables for the service can go into
`/etc/lanventory/lanventory.env`.

### Upgrade

Install the new package. The configuration and `secret.key` are kept, a running service is
restarted, and database migrations run when the service starts. Take a backup first (see below);
migrations are not reversible.

### Uninstall

Removing the package (`apt remove`, `dnf remove`, `pacman -R`, `apk del`) stops and disables the
service but keeps the data and the configuration. `apt purge` additionally deletes
`/var/lib/lanventory`, `/etc/lanventory` and the `lanventory` user. Back up first: this is
irreversible.

## Docker

The multi-arch image (amd64, arm64, armv7) is `ghcr.io/lan-dot-party/lanventory`:

```bash
docker run -d --name lanventory -p 9060:9060 \
  -e LANVENTORY_PUBLIC_URL=https://inventory.example.org \
  -v lanventory:/var/lib/lanventory ghcr.io/lan-dot-party/lanventory:latest
docker exec -it lanventory lanventory user create admin --role admin
```

- The database and `secret.key` live in the volume `/var/lib/lanventory`. Keep the volume: without
  it the data and the key are gone.
- The image's configuration is `/etc/lanventory/config.yaml` (`LANVENTORY_CONFIG` points to it).
  It listens on `0.0.0.0:9060`. Set `LANVENTORY_PUBLIC_URL`, `LANVENTORY_LISTEN` and
  `LANVENTORY_LOG_LEVEL`, or mount your own file to `/etc/lanventory/config.yaml`.
- A health check calls `GET /health` every 30 seconds.
- Tags: the version (for example `0.1.0`) and `latest` for non-prerelease versions. Upgrade
  with `docker pull`, then recreate the container; migrations run on start.

### Docker Compose

The repository contains two Compose files. Both run the same service (port bound to
`127.0.0.1:9060`, `restart: unless-stopped`) and differ only in where the data lives:

| File | Data | Use it when |
|------|------|-------------|
| [`compose.yaml`](../compose.yaml) | Named Docker volume `lanventory-data` | You want the simplest setup and let Docker manage the data. |
| [`compose.bind.yaml`](../compose.bind.yaml) | Local directory `./data` | You want the data visible in a host directory and easy to back up with normal tools. |

Edit `LANVENTORY_PUBLIC_URL` (and `TZ`) in the file first. For production, replace the `latest`
tag by a version tag such as `0.1.0`.

Named volume (`compose.yaml`):

```bash
docker compose up -d
docker compose exec lanventory lanventory user create admin --role admin
docker compose logs -f lanventory
docker compose pull && docker compose up -d      # update; migrations run on start
docker compose exec lanventory lanventory backup
```

Local path (`compose.bind.yaml`): the container runs as user `lanventory` (UID 100, GID 101), so
the directory must belong to it before the first start, otherwise the container cannot write:

```bash
mkdir -p data && sudo chown 100:101 data
docker compose -f compose.bind.yaml up -d
docker compose -f compose.bind.yaml exec lanventory lanventory user create admin --role admin
docker compose -f compose.bind.yaml logs -f lanventory
docker compose -f compose.bind.yaml pull && docker compose -f compose.bind.yaml up -d
```

Backup: `lanventory backup` writes a consistent copy of the database while the server runs into
`/var/lib/lanventory/backups/lanventory-<time>.db`, that is, inside the data directory (the
volume, or `./data/backups` in the bind variant). With the bind variant you can simply back up
`./data` with your usual tools, preferably the `backups` directory plus `secret.key`. Never copy
the live `lanventory.db` while the container runs without using the backup command.

`secret.key` lives in the data directory next to the database. Back it up together with the
database: without the key, TOTP secrets and recovery codes in the database are unusable.

The port is bound to the loopback interface of the host, for a reverse proxy on the same host.
Set `LANVENTORY_PUBLIC_URL` to the exact HTTPS origin users open in the browser: it is needed for
secure cookies and passkeys. To expose the port directly, use `"9060:9060"` in `ports`.

The reverse proxy reaches the container over the Docker network, so LANventory sees the address of
the Docker bridge (for example `172.18.0.1`), not `127.0.0.1`. The built-in configuration only
trusts `X-Forwarded-For` from `127.0.0.1`; otherwise the audit log and the login rate limit see
every user with the proxy's address. Mount your own `config.yaml` (the commented line in the
Compose files) and add the Docker network to `trusted_proxies`, for example
`trusted_proxies: [127.0.0.1, "::1", 172.16.0.0/12]` (CIDR ranges are accepted).

## From source

You need Go 1.26 or newer (an older Go downloads the toolchain named in `go.mod`).

```bash
CGO_ENABLED=0 go build -o bin/lanventory ./cmd/lanventory
./bin/lanventory -c config.yaml config init      # writes config.yaml and secret.key
echo 'a-long-password' | ./bin/lanventory -c config.yaml user create admin --role admin
./bin/lanventory -c config.yaml server
```

## Backup

The state is the SQLite database and `secret.key`; they belong together (without the key, TOTP
secrets and recovery codes in the database are unusable). Copy `secret.key` once, and run
`lanventory backup` regularly: it writes a consistent copy while the server runs into a
`backups` directory next to the database, or to the file you name:

```bash
sudo -u lanventory lanventory backup                  # /var/lib/lanventory/backups/lanventory-<time>.db
sudo -u lanventory lanventory backup /srv/lv.db
```

Restore by stopping the service and putting the file in place of the database.

## Moving data to another instance

To move the inventory, including all inspections, from one instance to another (for example from a
test instance to production), use the data export, not a database copy:

1. On the old instance, as admin: **Administration → Data → Download export**, or
   `lanventory data export -o export.json`.
2. On the new instance, as admin: **Administration → Data**, upload the file, look at the preview
   and confirm, or `lanventory data import --dry-run export.json` followed by
   `lanventory data import export.json`.

Keep "adopt the settings of existing categories" switched on (the default): a new instance starts
with template categories whose settings (for example a 12-month interval or a required length)
would otherwise win over the ones in the file.

The import is all or nothing and never changes existing assets (inventory numbers that exist are
skipped, or abort the import with `--on-conflict abort`). Users, sessions, API tokens, settings and
the audit log are **not** transferred; create the users on the new instance. Inspections are
attributed to the user of the same name there, otherwise to the importing user. Details:
[concepts.md](concepts.md#data-export-and-import-json).
