#!/bin/sh
set -eu

case "${POCKET_MODE:-demo}" in
    demo) set -- -demo -data /var/lib/pocket-agent/demo-sessions ;;
    live)
        if [ -z "${POCKET_BASE_URL:-}" ] || [ -z "${POCKET_API_KEY:-}" ] || [ -z "${POCKET_MODEL:-}" ]; then
            echo 'Live mode requires POCKET_BASE_URL, POCKET_API_KEY and POCKET_MODEL.' >&2
            exit 1
        fi
        case "$POCKET_BASE_URL" in
            https://*) ;;
            *) echo 'Live mode requires an HTTPS model endpoint.' >&2; exit 1 ;;
        esac
        set -- -data /var/lib/pocket-agent/sessions
        ;;
    *) echo 'POCKET_MODE must be demo or live.' >&2; exit 1 ;;
esac

case "${POCKET_ENABLE_WEB:-0}" in
    0) ;;
    1) set -- "$@" -enable-web ;;
    *) echo 'POCKET_ENABLE_WEB must be 0 or 1.' >&2; exit 1 ;;
esac

exec /opt/pocket-agent/current/pocket-agent -listen 127.0.0.1:9991 "$@"
