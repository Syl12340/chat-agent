# Third-party design and prompt attribution

## coder/websocket 1.8.15

Source: https://github.com/coder/websocket/tree/v1.8.15

Copyright (c) 2025 Coder. Licensed under the ISC License; the complete notice
is retained in [licenses/coder-websocket-ISC.txt](licenses/coder-websocket-ISC.txt).

The Go package is used only by the optional QQ Open Platform gateway adapter in
`internal/channel/qqofficial`. It provides the WebSocket framing and JSON
transport; chat-agent continues to own identity binding, session routing,
authorization, durable delivery settlement and Agent execution.

## DeepSeek Harness

Source: https://github.com/deepseek-ai/deepseek-harness

Reference revision: `8bd073392eef6f54e921a093b95076248eb08cbb` (2026-11-03).

Copyright (c) 2026 DeepSeek. Licensed under the MIT License; the complete
upstream notice is retained in [licenses/DeepSeek-Harness-MIT.txt](licenses/DeepSeek-Harness-MIT.txt).

chat-agent independently implements its runtime in Go. Its ordered prompt
sections, explicit overrides, step-scoped tool admission, separation of model
schemas from host metadata, guarded serial tool pipeline, and separation of
stable instructions from dynamic reference snapshots are adapted from the
contracts and organization described in these upstream files:

- `packages/core/system-prompt/src/index.ts`
- `packages/core/tools/src/contracts.ts`
- `packages/core/agent-loop/src/runtime-context.ts`
- The corresponding core package READMEs and `docs/architecture.md`.

The adaptations live in `internal/probe/prompt.go`, `registry.go`, `assembly.go`,
`executor.go`, and the colocated business tool usage instructions under
`internal/api`. They do not embed the upstream TypeScript runtime, Cordis,
plugins, tool implementations, or its full system prompt. chat-agent's own
role isolation, review workflow, authorization constraints and bounded execution
remain authoritative. No compatibility with the DSH plugin API is claimed.

Additional design reference: DeepSeek Harness commit
`fcc0ad462172522aaf5c93a7b2e01f6b9f0b0159` (MIT).

- `packages/core/tools/src/presentation.ts` and `contracts.ts` inform
  `internal/probe/presentation.go` and the host tool-card projection.
- `packages/sandbox/sandbox` informs the unavailable-state contract for
  `plugins/builtin/workspace`, its explicit read-only/workspace-write modes and
  its fail-closed behavior. The CA implementation is Go code: it combines
  Landlock filesystem rules, a seccomp socket-creation denial, resource limits
  and a per-call scratch directory; it does not copy or embed the upstream
  TypeScript sandbox runtime.
- `packages/jobs/tool-jobs/src/index.ts` informs the separation of job
  submission, observation and cancellation in `internal/api/image_jobs.go`.
- `packages/llm/llm/src/api-key.ts` and
  `packages/llm/llm-deepseek-api-key/src/{config,index}.ts` inform the printable
  API-key validation and per-request route snapshot used by
  `internal/modelconfig` and `internal/api/model_configs.go`. Credentials remain
  server-side, mutations affect subsequent requests, and a missing/deleted route
  fails explicitly instead of silently falling back.

These are Go and JavaScript adaptations of the cited contracts.

## Aurabot QQ

Source: https://github.com/aureth-labs/dsh-aurabot-qq

Reference revision: `0a0ccb1cc401dee00ce0faac2cffbf57d0efecda`.

Copyright (c) 2026 Aurabot contributors. Licensed under the MIT License; the
complete notice is retained in
[licenses/Aurabot-MIT.txt](licenses/Aurabot-MIT.txt).

The optional `|||` conversational-bubble convention and the rule that the
model, rather than a user-facing mode switch, chooses when a casual answer
benefits from several bubbles are adapted from `src/chat/reply.js` and the
reply pipeline in `src/chat/hub.js`. chat-agent keeps Markdown-heavy answers
atomic, caps a reply at three bubbles, adds no artificial delivery delay, and
does not embed the DSH/Cordis or QQ runtimes.

Additional source review revision:
`5eb5b9463f5f0ce14964a2c7d5dcba44ad67fabe` (2026-11-10, MIT). The message
coalescing, steer-or-queue boundary, delivery settlement, approval queue and
purpose scheduling implementations were inspected for the design documented in
`docs/reference-architecture-dsh-go-astrbot-aurabot.md`. No additional source
from that revision has been copied by this documentation change.

## AstrBot

Source: https://github.com/AstrBotDevs/AstrBot

Reference revision: `6b10af75e118714335aff3aab047bf7689fa1388`.

AstrBot is licensed under AGPL-3.0. Its provider-management, durable task and
typed media designs were inspected as architectural references only. No
AstrBot source code or assets are copied into chat-agent.

Additional source review revision:
`68acac91b1ab92c2ac4cfd811c0898913a1391c8` (2026-11-10, AGPL-3.0). Its typed
runtime events, serial session/turn ownership, steering receipts, portable
input/output components and expiring file-token service informed only the
architecture document above. No code or assets from this revision are copied.

## dsh-Go

Source: https://github.com/ccch1mneyyy/dsh-Go

Reference revision: `a9877420ab9aee121134faf01b6a92a39ce0b05b`
(v1.0.15, 2026-11-10).

The repository root is licensed under AGPL-3.0. Its Dart Carrier/Mux and remote
connection, pairing, trust, capability and resynchronization implementations
were inspected as architectural references only. No AGPL source code or assets
are copied into chat-agent.

The upstream `sdk/plugin-kit` directory carries a separate MIT License,
Copyright (c) 2026 DSH-TUI (ccch1mneyyy). Its declarative view schemas,
surface budgets, capability degradation and effect-validation design informed
CA's independently written `internal/presentation` and `web/ui/presentation.js`.
No dsh-Go Dart or TypeScript source, schema file, generated code or asset is
copied into those implementations. If selected upstream files are introduced
later, their source and target paths, modifications and complete MIT notice must
be recorded at that time.

## KaTeX 0.19.1

Source: https://github.com/KaTeX/KaTeX/releases/tag/v0.19.1

The pinned browser module is bundled in `web/vendor/katex/katex.mjs` for local
mathematical rendering, using native MathML. It is not an Agent runtime.
The MIT copyright and permission notice are retained in
[licenses/KaTeX-MIT.txt](licenses/KaTeX-MIT.txt) and in the embedded
`web/vendor/katex/LICENSE.txt`. Package integrity and module hash are recorded in
[the vendor readme](web/vendor/katex/README.md).
