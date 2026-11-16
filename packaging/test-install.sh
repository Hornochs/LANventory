#!/bin/sh
# Installs a package in a clean container, starts LANventory with the packaged
# configuration, checks /health and removes the package again.
#
#   sh packaging/test-install.sh deb|rpm|archlinux|apk PACKAGE_FILE
set -eu

format=$1
pkg=$2

fail() {
    echo "FAIL: $*" >&2
    [ -f /tmp/lanventory.log ] && cat /tmp/lanventory.log >&2
    exit 1
}

echo "== install $pkg"
case "$format" in
    deb) apt-get install -y "$pkg" ;;
    rpm) if command -v dnf >/dev/null; then dnf install -y "$pkg"; else rpm -i "$pkg"; fi ;;
    archlinux) pacman -U --noconfirm "$pkg" ;;
    apk) apk add --allow-untrusted "$pkg" ;;
    *) fail "unknown format $format" ;;
esac

echo "== installed files"
lanventory version
id lanventory || fail "user lanventory missing"
[ -f /etc/lanventory/config.yaml ] || fail "config missing"
[ -s /etc/lanventory/secret.key ] || fail "secret.key missing"
[ "$(stat -c %a /etc/lanventory/secret.key)" = 640 ] || fail "secret.key mode is not 640"
[ "$(stat -c %U /var/lib/lanventory)" = lanventory ] || fail "/var/lib/lanventory not owned by lanventory"
[ -f /usr/lib/systemd/system/lanventory.service ] || fail "systemd unit missing"

echo "== admin user (as root, files must stay usable for the service)"
echo "smoke-test-password" | lanventory -c /etc/lanventory/config.yaml user create admin --role admin
[ "$(stat -c %U /var/lib/lanventory/lanventory.db)" = lanventory ] || fail "database owned by root after CLI use"

echo "== start server as lanventory"
su -s /bin/sh lanventory -c 'LANVENTORY_CONFIG=/etc/lanventory/config.yaml /usr/bin/lanventory server' \
    >/tmp/lanventory.log 2>&1 &
pid=$!
ok=
for _ in $(seq 1 40); do
    if out=$(wget -q -O - http://127.0.0.1:9060/health 2>/dev/null || curl -fsS http://127.0.0.1:9060/health 2>/dev/null); then
        case "$out" in *'"status":"ok"'*) ok=1; break ;; esac
    fi
    sleep 0.5
done
[ -n "$ok" ] || fail "server did not become healthy"
code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9060/login 2>/dev/null || echo 200)
[ "$code" = 200 ] || fail "/login returned $code"
kill "$pid"
wait "$pid" 2>/dev/null || true

echo "== remove"
case "$format" in
    deb) apt-get remove -y lanventory ;;
    rpm) if command -v dnf >/dev/null; then dnf remove -y lanventory; else rpm -e lanventory; fi ;;
    archlinux) pacman -R --noconfirm lanventory ;;
    apk) apk del lanventory ;;
esac
# Check the file, not `command -v`: the shell has cached the path since
# `lanventory version` above and would still report the removed binary.
[ ! -e /usr/bin/lanventory ] || fail "binary still installed"
[ -f /var/lib/lanventory/lanventory.db ] || fail "data was deleted on removal"
echo "OK: $pkg"
