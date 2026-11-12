#!/bin/sh
# Run as root on the Linux target with a release ID and expected binary SHA-256.
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
release=${1:?release ID required}
expected=${2:?binary SHA-256 required}
case "$release" in ''|*[!a-zA-Z0-9_-]*) echo 'Invalid release ID.' >&2; exit 1 ;; esac
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s  %s\n' "$expected" "$here/chat-agent" | sha256sum -c -

if ! getent passwd chat-agent >/dev/null; then
    useradd --system --user-group --home-dir /var/lib/chat-agent --shell /usr/sbin/nologin chat-agent
fi
install -d -m 0755 /opt/chat-agent /opt/chat-agent/releases
target=/opt/chat-agent/releases/$release
[ ! -e "$target" ] || { echo 'Release already exists; use a new release ID.' >&2; exit 1; }
install -d -m 0755 "$target"
install -m 0755 "$here/chat-agent" "$target/chat-agent"
install -m 0755 "$here/start.sh" "$target/start.sh"
install -d -o root -g chat-agent -m 0750 /etc/chat-agent
if [ ! -e /etc/chat-agent/chat-agent.env ]; then
    cat "$here/chat-agent.env.example" > /etc/chat-agent/chat-agent.env
    printf '\nCHAT_ACCESS_TOKEN=' >> /etc/chat-agent/chat-agent.env
    openssl rand -hex 32 >> /etc/chat-agent/chat-agent.env
    chmod 0600 /etc/chat-agent/chat-agent.env
fi
install -d -o chat-agent -g chat-agent -m 0700 /var/lib/chat-agent
ln -s "$target" /opt/chat-agent/current.next
mv -Tf /opt/chat-agent/current.next /opt/chat-agent/current
install -m 0644 "$here/chat-agent.service" /etc/systemd/system/chat-agent.service
systemd-analyze verify /etc/systemd/system/chat-agent.service
systemctl daemon-reload
systemctl enable chat-agent.service
# Each store takes an exclusive lock file on startup and does not remove it when the
# process is killed hard. A plain restart then fails with "store lock unavailable",
# systemd retries, hits its start limit and leaves the service down — so stop first,
# wait for the process to actually leave, and clear the locks it left behind.
systemctl stop chat-agent.service 2>/dev/null || true
waited=0
while pgrep -x chat-agent >/dev/null 2>&1 && [ "$waited" -lt 10 ]; do
    waited=$((waited + 1))
    sleep 1
done
find /var/lib/chat-agent -name '*.lock' -delete 2>/dev/null || true
systemctl reset-failed chat-agent.service 2>/dev/null || true
systemctl restart chat-agent.service
attempt=0
until curl --fail --silent --max-time 2 http://127.0.0.1:9991/healthz >/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 15 ]; then
        systemctl status chat-agent.service --no-pager
        exit 1
    fi
    sleep 1
done
printf 'Installed release %s; listening on 127.0.0.1:9991.\n' "$release"
systemctl is-active chat-agent.service
