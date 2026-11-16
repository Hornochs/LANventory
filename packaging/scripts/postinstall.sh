#!/bin/sh
# LANventory post-installation script (deb, rpm, archlinux, apk).
# Creates the system user, the data directory and secret.key. The service is
# not started automatically: the admin configures it first.
set -e

if ! id -u lanventory >/dev/null 2>&1; then
    if command -v systemd-sysusers >/dev/null 2>&1 && [ -f /usr/lib/sysusers.d/lanventory.conf ]; then
        systemd-sysusers /usr/lib/sysusers.d/lanventory.conf
    elif command -v useradd >/dev/null 2>&1; then
        useradd --system --home-dir /var/lib/lanventory --no-create-home \
            --shell /usr/sbin/nologin --user-group lanventory 2>/dev/null ||
            useradd --system --home-dir /var/lib/lanventory --no-create-home --shell /sbin/nologin lanventory
    else
        addgroup -S lanventory 2>/dev/null || true
        adduser -S -D -H -h /var/lib/lanventory -s /sbin/nologin -G lanventory lanventory
    fi
fi

mkdir -p /var/lib/lanventory /etc/lanventory
chown lanventory:lanventory /var/lib/lanventory
chmod 0700 /var/lib/lanventory
chown root:lanventory /etc/lanventory
chmod 0750 /etc/lanventory
chown root:lanventory /etc/lanventory/config.yaml
chmod 0640 /etc/lanventory/config.yaml

# The key encrypts secrets (MFA seeds) in the database. It is created
# once and never replaced by updates.
if [ ! -s /etc/lanventory/secret.key ]; then
    umask 027
    od -An -tx1 -N32 /dev/urandom | tr -d ' \n' > /etc/lanventory/secret.key
    echo >> /etc/lanventory/secret.key
    chown root:lanventory /etc/lanventory/secret.key
    chmod 0640 /etc/lanventory/secret.key
fi

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    systemctl daemon-reload || true
    # Updates restart a running service; migrations run on start.
    systemctl try-restart lanventory.service || true
fi

if [ ! -f /var/lib/lanventory/lanventory.db ]; then
    cat <<'EOF'

LANventory is installed. Next steps:
  1. Check the configuration: sudo nano /etc/lanventory/config.yaml   (listen, public_url)
  2. Create the first admin:  sudo lanventory user create admin --role admin
  3. Start the service:       sudo systemctl enable --now lanventory
  4. Log in at the public URL.
     Behind a reverse proxy: see /usr/share/doc/lanventory/README.md (public_url!)

EOF
fi
exit 0
