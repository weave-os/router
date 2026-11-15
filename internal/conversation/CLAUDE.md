# internal/conversation — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Conversation Lifecycle bounded context: loop, no-progress, text-repetition, spiral and struggle detection over the inbound conversation, and the slash-command acknowledgments the router answers itself. Read [root CLAUDE.md](../../CLAUDE.md) and [proxy CLAUDE.md](../proxy/CLAUDE.md) first.

## Rules

- **Inner ring, I/O-free.** Detectors are pure functions of the request envelope; trackers are in-memory, per-replica caches. No HTTP, no session-pin writes, no context-key reads.
- **Ports for durable state.** `LoopEscalationStore`, `SpiralShadowStore` and `StruggleShadowStore` are implemented by `postgres.TelemetryRepo` and wired from `cmd/router/main.go`.
- **Commands render here, apply in proxy.** `RouterSessionMessage`, `RouterModelsMessage` and `RouterFeedbackAck` take every fact as a parameter (including the client's directive prefix); command handlers that read or write pins and stores stay in `internal/proxy`.
- **The orchestrator acts on a detection.** Writing the synthetic stop, expiring or pinning the session, and recording events stay in `internal/proxy` until those handlers move here behind explicit ports.
- Behavior here is pinned by the conversation golden scenarios in `internal/proxy/golden_conversation_test.go`; a refactor must leave them byte-identical.
