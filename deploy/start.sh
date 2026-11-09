#!/bin/sh
set -eu

case "${CHAT_MODE:-demo}" in
    demo) set -- -demo -data /var/lib/chat-agent/demo-sessions ;;
    live)
        if [ -z "${CHAT_BASE_URL:-}" ] || [ -z "${CHAT_API_KEY:-}" ] || [ -z "${CHAT_MODEL:-}" ]; then
            echo 'Live mode requires CHAT_BASE_URL, CHAT_API_KEY and CHAT_MODEL.' >&2
            exit 1
        fi
        case "$CHAT_BASE_URL" in
            https://*) ;;
            *) echo 'Live mode requires an HTTPS model endpoint.' >&2; exit 1 ;;
        esac
        set -- -data /var/lib/chat-agent/sessions
        ;;
    *) echo 'CHAT_MODE must be demo or live.' >&2; exit 1 ;;
esac

case "${CHAT_ENABLE_WEB:-0}" in
    0) ;;
    1) set -- "$@" -enable-web ;;
    *) echo 'CHAT_ENABLE_WEB must be 0 or 1.' >&2; exit 1 ;;
esac

exec /opt/chat-agent/current/chat-agent -listen 127.0.0.1:9991 "$@"
