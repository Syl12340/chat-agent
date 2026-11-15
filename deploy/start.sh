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
# twice. The built-in default is a continuous loop with a five-minute wall-clock
# budget. Operators may restore explicit count ceilings without changing code.
if [ -n "${CHAT_TURN_TIMEOUT:-}" ]; then
    set -- "$@" -timeout "$CHAT_TURN_TIMEOUT"
fi
if [ -n "${CHAT_MAX_TURNS:-}" ]; then
    set -- "$@" -max-turns "$CHAT_MAX_TURNS"
fi
if [ -n "${CHAT_MAX_TOOLS:-}" ]; then
    set -- "$@" -max-tools "$CHAT_MAX_TOOLS"
fi

exec /opt/chat-agent/current/chat-agent -listen 127.0.0.1:9991 "$@"
