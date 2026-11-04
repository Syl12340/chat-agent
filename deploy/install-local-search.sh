#!/bin/sh
# Run before install.sh from a staged release containing pocket-search.
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
release=${1:?release ID required}
expected=${2:?search binary SHA-256 required}
case "$release" in ''|*[!a-zA-Z0-9_-]*) exit 1 ;; esac
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s  %s\n' "$expected" "$here/pocket-search" | sha256sum -c -
if [ -n "$(ss -lntH sport = :9993)" ] && ! systemctl is-active --quiet pocket-search; then
    echo 'Loopback port 9993 is already used by another service.' >&2; exit 1
fi
if ! getent passwd pocket-search >/dev/null; then
    useradd --system --user-group --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin pocket-search
fi
install -d -m 0755 /opt/pocket-search /opt/pocket-search/releases
target=/opt/pocket-search/releases/$release
[ ! -e "$target" ] || { echo 'Search release already exists.' >&2; exit 1; }
install -d -m 0755 "$target"
install -m 0755 "$here/pocket-search" "$target/pocket-search"
install -d -m 0700 /etc/pocket-search
if [ ! -e /etc/pocket-search/search.env ]; then
    printf 'POCKET_SEARCH_TOKEN=' > /etc/pocket-search/search.env
    openssl rand -hex 32 >> /etc/pocket-search/search.env
    chmod 0600 /etc/pocket-search/search.env
fi
ln -s "$target" /opt/pocket-search/current.next
mv -Tf /opt/pocket-search/current.next /opt/pocket-search/current
install -m 0644 "$here/pocket-search.service" /etc/systemd/system/pocket-search.service
install -d -m 0755 /etc/systemd/system/pocket-agent.service.d
printf '[Unit]\nWants=pocket-search.service\nAfter=pocket-search.service\n' > /etc/systemd/system/pocket-agent.service.d/20-local-search.conf
chmod 0644 /etc/systemd/system/pocket-agent.service.d/20-local-search.conf
systemctl daemon-reload
systemctl enable pocket-search
systemctl restart pocket-search
attempt=0
until curl --fail --silent --max-time 2 http://127.0.0.1:9993/healthz >/dev/null; do
    attempt=$((attempt+1)); [ "$attempt" -lt 10 ] || exit 1; sleep 1
done
python3 - <<'PY'
from pathlib import Path
import os
p=Path('/etc/pocket-agent/pocket-agent.env')
old=p.read_text()
backup=p.with_name('pocket-agent.env.before-local-search')
if not backup.exists():
    backup.write_text(old); backup.chmod(0o600)
token=next(line.split('=',1)[1] for line in Path('/etc/pocket-search/search.env').read_text().splitlines() if line.startswith('POCKET_SEARCH_TOKEN='))
assert len(token)>=32 and token.isascii() and token.isalnum()
changes={'POCKET_ENABLE_WEB':'1','POCKET_LOCAL_SEARCH_URL':'http://127.0.0.1:9993/search','POCKET_LOCAL_SEARCH_KEY':token,'POCKET_SEARCH_URL':'','POCKET_SEARCH_KEY':''}
lines=[line for line in old.splitlines() if line.split('=',1)[0] not in changes]
lines.extend(k+'='+v for k,v in changes.items())
nextfile=p.with_name('pocket-agent.env.search-next')
nextfile.write_text('\n'.join(lines)+'\n');nextfile.chmod(0o600);os.replace(nextfile,p)
PY
printf 'Local search ready on 127.0.0.1:9993; Agent configuration prepared.\n'
