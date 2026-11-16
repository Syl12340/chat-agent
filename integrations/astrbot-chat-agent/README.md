# AstrBot transport gateway

This optional AstrBot plugin forwards admitted IM messages to chat-agent and
stops AstrBot's own LLM pipeline for those messages. chat-agent remains the
only conversation/model/memory/tool runtime. The plugin then polls CA's durable
outbox and uses AstrBot's `Context.send_message()` to deliver replies.

Initial scope is text-only private chat through AstrBot's `aiocqhttp` adapter.
Groups and additional platforms are disabled by default and must be enabled
only after their identity, audience and send-by-session behavior is verified.

Configure the same random 32+ character secret on both sides:

- CA: `ASTRBOT_BRIDGE_ENABLED=true`, `ASTRBOT_BRIDGE_SECRET=...`
- plugin: `enabled=true`, loopback `base_url`, the same `secret`

Keep loopback HTTP when both processes share a host. A remote bridge requires a
private TLS tunnel; do not expose these hooks as a public unauthenticated API.

The plugin is licensed under AGPL-3.0-or-later because it imports and runs
against AstrBot's plugin API. No AstrBot source file or asset is copied here.
See `licenses/AstrBot-AGPL-3.0.txt` in the chat-agent repository.
