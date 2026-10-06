# internal/router/turntype — AGENTS

> **Mirror notice.** Generated from [CLAUDE.md](CLAUDE.md). Edit CLAUDE.md, then run `make generate-agent-guides`; CI rejects drift.

Inbound turn-type classifier. Read [root CLAUDE.md](../../../CLAUDE.md) first.

## What it does

Classifies inbound requests into:

- `MainLoop`
- `ToolResult` — proxy short-circuits to the session pin (these actions' embeddings are mostly noise)
- `SubAgentDispatch`
- `Compaction` — harness-issued compaction turn (Claude Code's summary instruction, or Codex's "CONTEXT CHECKPOINT COMPACTION" handoff prompt); hard-pinned
- `Probe` — preserves the requested model by default, with no automatic session-pin creation or cross-model failover; explicit deployment or `/force-model` overrides retain their own routing behavior. Same-model credential fallback remains available, so success does not establish that a particular subscription credential has quota.
- `TitleGen` — harness sidebar-title generation; scored independently by default unless explicitly deployment-pinned or overridden by `/force-model`, with no automatic conversation-pin reads/writes or routing marker. An anchored pin here would leak the title decision into the real conversation that follows ~25ms later. Claude Code is identified by its title JSON schema; Codex title requests are trusted only on Codex Responses ingress and match either the closed title schema or Conductor's fresh hidden-session prompt fingerprint. OpenCode 2.x title calls use the no-tools, bounded title-agent prompt from an identified OpenCode caller.
- `Recap` — Claude Code's away-summary turn (the "user stepped away and is coming back" instruction appended as the trailing user message of a forked conversation); scored like `Classifier`, never reads or writes the session pin, and renders without the routing marker or `/rf` footer, since it prints beneath the reply the user just read. Anthropic format only, trailing turn only, and the instruction must open the message (after any `<system-reminder>` blocks) so a user quoting it stays `MainLoop`
- `Classifier` — short-form classification call (e.g. Claude Code's security monitor); scored like a main-loop turn (it is a fresh window with its own system prompt) but never reads or writes a session pin

Used by [`../../proxy`](../../proxy) to keep the action loop cheap + correct.

## Invariants

- **Compaction's fingerprint is checked in the system prompt AND the last user message.** Claude Code 2.x appends the canonical "your task is to create a detailed summary" instruction to the trailing turn (next to the last `tool_result`) rather than the system prompt; a system-only check falls through to `ToolResult` and routes summarization through the scorer. In a user message the phrase must co-occur with the template's "do not call any tools" clause, inside a bounded prefix scan — on its own it is text a human could type.
- **Codex's compaction is detected on any wire format, trailing user message only.** Codex issues "You are performing a CONTEXT CHECKPOINT COMPACTION" as the last user turn (e.g. on a mid-thread model switch, regardless of context size); the phrase is distinctive enough to match alone, within the same bounded prefix scan. Only the trailing turn is checked because the post-compaction resume turn quotes the summary and must stay `MainLoop`.
- **OpenCode title calls use the native v2 prompt shape.** `Detect` recognizes the no-tools, bounded title-agent prompt only for identified OpenCode callers, because OpenCode titles the session with the reply’s first line. An OpenCode child session (`X-OpenCode-Session` plus `X-Parent-Session-Id`, parsed by `proxy.ClientIdentityFromHeaders` for OpenCode callers only) maps to `SubAgentDispatch`, since its body carries the full tool registry and has no fingerprint. No plugin-supplied lifecycle header is used; other turns use the general request-shape heuristics.
- **Pure, no I/O.** Static classifier over `router.Request` shape.
- **No upstream dependency in the inner ring.** Don't import providers, postgres, or proxy.
