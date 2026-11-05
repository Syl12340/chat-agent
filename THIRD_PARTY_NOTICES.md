# Third-party design and prompt attribution

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

## KaTeX 0.19.1

Source: https://github.com/KaTeX/KaTeX/releases/tag/v0.19.1

The pinned browser module is bundled in `web/vendor/katex/katex.mjs` for local
mathematical rendering, using native MathML. It is not an Agent runtime.
The MIT copyright and permission notice are retained in
[licenses/KaTeX-MIT.txt](licenses/KaTeX-MIT.txt) and in the embedded
`web/vendor/katex/LICENSE.txt`. Package integrity and module hash are recorded in
[the vendor readme](web/vendor/katex/README.md).
