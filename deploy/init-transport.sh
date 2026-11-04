#!/bin/sh
# Generate this deployment's P-256 signing identity on the server.
# No private key is printed or copied to a client. Requires root and OpenSSL.
set -eu
umask 077
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
dir=/etc/pocket-agent/transport
[ ! -e "$dir" ] || { echo 'Transport directory already exists; refusing to replace keys.' >&2; exit 1; }
install -d -m 0750 -o root -g pocket-agent /etc/pocket-agent "$dir"
openssl ecparam -name prime256v1 -genkey -noout -out "$dir/signing.key"
openssl pkey -in "$dir/signing.key" -pubout -out "$dir/server-public.pem"
chown root:pocket-agent "$dir/signing.key"
chmod 0640 "$dir/signing.key"
chmod 0644 "$dir/server-public.pem"
printf 'Public identity fingerprint: '
openssl pkey -pubin -in "$dir/server-public.pem" -outform DER | openssl dgst -sha256
printf 'Signing key created; distribute only server-public.pem over trusted SSH.\n'
