#!/bin/sh
# Run as root on the Linux target with a release ID and expected binary SHA-256.
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
release=${1:?release ID required}
expected=${2:?binary SHA-256 required}
case "$release" in ''|*[!a-zA-Z0-9_-]*) echo 'Invalid release ID.' >&2; exit 1 ;; esac
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s  %s\n' "$expected" "$here/pocket-agent" | sha256sum -c -

if ! getent passwd pocket-agent >/dev/null; then
    useradd --system --user-group --home-dir /var/lib/pocket-agent --shell /usr/sbin/nologin pocket-agent
fi
install -d -m 0755 /opt/pocket-agent /opt/pocket-agent/releases
target=/opt/pocket-agent/releases/$release
[ ! -e "$target" ] || { echo 'Release already exists; use a new release ID.' >&2; exit 1; }
install -d -m 0755 "$target"
install -m 0755 "$here/pocket-agent" "$target/pocket-agent"
install -m 0755 "$here/start.sh" "$target/start.sh"
install -d -o root -g pocket-agent -m 0750 /etc/pocket-agent
if [ ! -e /etc/pocket-agent/pocket-agent.env ]; then
    cat "$here/pocket-agent.env.example" > /etc/pocket-agent/pocket-agent.env
    printf '\nPOCKET_ACCESS_TOKEN=' >> /etc/pocket-agent/pocket-agent.env
    openssl rand -hex 32 >> /etc/pocket-agent/pocket-agent.env
    chmod 0600 /etc/pocket-agent/pocket-agent.env
fi
install -d -o pocket-agent -g pocket-agent -m 0700 /var/lib/pocket-agent
ln -s "$target" /opt/pocket-agent/current.next
mv -Tf /opt/pocket-agent/current.next /opt/pocket-agent/current
install -m 0644 "$here/pocket-agent.service" /etc/systemd/system/pocket-agent.service
systemd-analyze verify /etc/systemd/system/pocket-agent.service
systemctl daemon-reload
systemctl enable pocket-agent.service
systemctl restart pocket-agent.service
attempt=0
until curl --fail --silent --max-time 2 http://127.0.0.1:9991/healthz >/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 15 ]; then
        systemctl status pocket-agent.service --no-pager
        exit 1
    fi
    sleep 1
done
printf 'Installed release %s; listening on 127.0.0.1:9991.\n' "$release"
systemctl is-active pocket-agent.service
