# internal/router/turntype — AGENTS

> **Mirror notice.** Verbatim sync with [CLAUDE.md](CLAUDE.md). **Update both together** — divergence = bug.

Inbound turn-type classifier. Read [root CLAUDE.md](../../../CLAUDE.md) first.

## What it does

Classifies inbound requests into:

- `MainLoop`
- `ToolResult` — proxy short-circuits to the session pin (these actions' embeddings are mostly noise)
- `SubAgentDispatch`
- `Compaction` — proxy forces Haiku
- `Probe` — proxy bypasses routing entirely
- `TitleGen` — Claude Code sidebar-title generation; hard-pinned AND skips session-pin creation (an anchored pin here would leak the cheap-model decision into the real conversation that follows ~25ms later)
- `Classifier` — short-form classification call (e.g. Claude Code's security monitor); hard-pinned AND skips session-pin creation

Used by [`../../proxy`](../../proxy) to keep the action loop cheap + correct.

## Invariants

- **Compaction's fingerprint is checked in the system prompt AND the last user message.** Claude Code 2.x appends the canonical "your task is to create a detailed summary" instruction to the trailing turn (next to the last `tool_result`) rather than the system prompt; a system-only check falls through to `ToolResult` and routes summarization through the scorer. In a user message the phrase must co-occur with the template's "do not call any tools" clause, inside a bounded prefix scan — on its own it is text a human could type.
- **Pure, no I/O.** Static classifier over `router.Request` shape.
- **No upstream dependency in the inner ring.** Don't import providers, postgres, or proxy.
