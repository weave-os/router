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
- `HarnessMeta` — main-turn skill/command invocation referencing harness primitives; proxy clamps these to a strong Claude-family model (route up, never down); detection is deliberately narrow (command markers + harness keyword gate)
- `SubAgentHarnessMeta` — sub-agent dispatch whose prompt references harness primitives; same clamp as `HarnessMeta` (route up, never down); detection is deliberately narrow (harness keyword gate over a bounded prompt prefix)
- `Recovery` — tool-result turn recovering from a deferred-tool/`InputValidationError` failure; same clamp (route up, never down); detection is deliberately narrow (`InputValidationError` + deferred-tool context)

`TurnType.Base()` maps the three harness variants back to their underlying shape (`MainLoop` / `SubAgentDispatch` / `ToolResult`) for call sites that must stay behavior-compatible with the pre-harness vocabulary — notably the policy sidecar, whose turn-type labels must remain stable. `TurnType.HarnessEscalation()` reports whether the proxy's escalation clamp applies.

Used by [`../../proxy`](../../proxy) to keep the action loop cheap + correct.

## Invariants

- **Pure, no I/O.** Static classifier over `router.Request` shape.
- **No upstream dependency in the inner ring.** Don't import providers, postgres, or proxy.
