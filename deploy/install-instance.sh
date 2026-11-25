#!/bin/sh
# Initial provisioning for one private instance. Run from a verified release stage.
set -eu
umask 077
die() { printf '%s\n' "$*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || die 'Run as root.'
name=${1:?instance name required}
http=${2:?loopback HTTP port required}
release=${3:?release ID required}
sha=${4:?binary SHA-256 required}
wire=${5:-}
case "$name" in *[!a-z0-9-]*|-*|*-|releases) die 'Use an instance name with lowercase letters, digits and internal hyphens.' ;; esac
[ "${#name}" -le 24 ] || die 'Instance name must be at most 24 characters.'
case "$release" in *[!a-zA-Z0-9_-]*) die 'Invalid release ID.' ;; esac
case "$sha" in *[!a-f0-9]*) die 'Invalid SHA-256.' ;; esac
[ "${#sha}" = 64 ] || die 'Invalid SHA-256.'
validate_port() {
    case "$1" in *[!0-9]*|0*|'') die 'Invalid listener port.' ;; esac
    [ "${#1}" -le 5 ] && [ "$1" -ge 1024 ] && [ "$1" -le 65535 ] || die 'Invalid listener port.'
}
validate_port "$http"
if [ -n "$wire" ]; then
    validate_port "$wire"
    [ "$http" != "$wire" ] || die 'HTTP and encrypted transport require separate ports.'
fi
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
printf '%s  %s\n' "$sha" "$here/chat-agent" | sha256sum -c -
exec 9>/run/lock/chat-agent-instances.lock
flock -x 9
base=/opt/chat-agent/instances
config=/etc/chat-agent-instances/$name
state=/var/lib/chat-agent-instances/$name
home=$base/$name
user=ca-$name
unit=chat-agent@$name.service
for path in "$config" "$state" "$home"; do
    [ ! -e "$path" ] && [ ! -L "$path" ] || die 'Instance already has files. Use its existing configuration.'
done
if getent passwd "$user" >/dev/null || getent group "$user" >/dev/null; then
    die 'The instance account name is already in use.'
fi
for port in "$http" $wire; do
    [ -z "$(ss -H -ltn "sport = :$port")" ] || die "Port $port is already listening."
    for previous in /etc/chat-agent-instances/*/instance.env; do
        [ -f "$previous" ] || continue
        if grep -Eq "^CHAT_(HTTP|WIRE)_LISTEN=127\.0\.0\.1:$port$" "$previous"; then
            die "Port $port belongs to another instance."
        fi
    done
done

install -d -m 0755 "$base" "$base/releases" /etc/chat-agent-instances /var/lib/chat-agent-instances
target=$base/releases/$release
if [ -e "$target" ]; then
    printf '%s  %s\n' "$sha" "$target/chat-agent" | sha256sum -c -
    cmp -s "$target/start.sh" "$here/start.sh" || die 'Release ID already has a different launcher.'
else
    stage=$(mktemp -d "$base/releases/.stage.XXXXXXXX")
    trap 'rm -f "$stage/chat-agent" "$stage/start.sh"; rmdir "$stage" 2>/dev/null || true' EXIT
    install -m 0755 "$here/chat-agent" "$stage/chat-agent"
    install -m 0755 "$here/start.sh" "$stage/start.sh"
    chmod 0755 "$stage"
    mv -T "$stage" "$target"
    trap - EXIT
fi

useradd --system --user-group --home-dir "$state" --shell /usr/sbin/nologin "$user"
install -d -m 0750 -o root -g "$user" "$config"
install -d -m 0700 -o "$user" -g "$user" "$state" "$state/data" "$state/workspace"
install -d -m 0755 "$home"
ln -s "$target" "$home/current"

claim=$(openssl rand -hex 32)
digest=$(printf '%s' "$claim" | sha256sum | cut -d ' ' -f 1)
expires=$(date -d 'tomorrow' +%s)
printf '%s\n' "$claim" >"$config/owner-claim"
{
    printf 'CHAT_MODE=demo\nCHAT_ACCESS_TOKEN=%s\n' "$(openssl rand -hex 32)"
    printf 'CHAT_HTTP_LISTEN=127.0.0.1:%s\nCHAT_DATA_DIR=%s/data\nCHAT_WORKSPACE_ROOT=%s/workspace\n' "$http" "$state" "$state"
    printf 'CHAT_INSTANCE_CLAIM_HASH=%s\nCHAT_INSTANCE_CLAIM_EXPIRES=%s\n' "$digest" "$expires"
    if [ -n "$wire" ]; then
        openssl ecparam -name prime256v1 -genkey -noout -out "$config/signing.key"
        chown root:"$user" "$config/signing.key"
        chmod 0640 "$config/signing.key"
        openssl pkey -in "$config/signing.key" -pubout -out "$config/server-public.pem"
        chmod 0644 "$config/server-public.pem"
        printf 'CHAT_WIRE_LISTEN=127.0.0.1:%s\nCHAT_WIRE_KEY=%s/signing.key\n' "$wire" "$config"
    fi
} >"$config/instance.env"
install -m 0644 "$here/chat-agent@.service" /etc/systemd/system/chat-agent@.service
systemd-analyze verify "$unit"
systemctl daemon-reload
systemctl enable --now "$unit"
for delay in 1 1 1 1 2 2 3; do
    if curl -fsS --max-time 2 "http://127.0.0.1:$http/healthz" >/dev/null && systemctl is-active --quiet "$unit"; then
        printf 'Instance %s listening on loopback port %s. Owner claim file: %s/owner-claim\n' "$name" "$http" "$config"
        exit 0
    fi
    sleep "$delay"
done
die "Instance failed its readiness check. Inspect journalctl -u $unit."
