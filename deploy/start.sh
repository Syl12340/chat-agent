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

# The turn budget covers every model call in one turn, not just one request: a
# turn that runs a tool and then asks the model to read the result spends it
# twice, and a 32 KiB tool result spends a lot more of it. 30s is the built-in
# default and is too tight for that on a live model with thinking enabled.
if [ -n "${CHAT_TURN_TIMEOUT:-}" ]; then
    set -- "$@" -timeout "$CHAT_TURN_TIMEOUT"
fi

exec /opt/chat-agent/current/chat-agent -listen 127.0.0.1:9991 "$@"
