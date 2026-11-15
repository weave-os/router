# internal/credentials — AGENTS

> **Mirror notice.** Generated from [CLAUDE.md](CLAUDE.md). Edit CLAUDE.md, then run `make generate-agent-guides`; CI rejects drift.

Credential Brokerage bounded context: which upstream providers a request holds credentials for (deployment key, BYOK, client-supplied, subscription). Read [root CLAUDE.md](../../CLAUDE.md) and [proxy CLAUDE.md](../proxy/CLAUDE.md) first.

## Rules

- **Inner ring, I/O-free.** Pure functions over explicit inputs; no context-key reads. The orchestrator in `internal/proxy` derives request facts from its context and passes them in `EligibilityRequest`.
- **`Broker` is the port; `Brokerage` is the deployment configuration that implements it.** Wire the deployment facts from `cmd/router/main.go`.
- **`Resolve` picks the credential an attempt spends** from `requestcontext.RequestFacts` and the orchestrator's `Suppression`; it returns the pick (or `Clear`) instead of writing it.
- **`internal/requestcontext` stays the provider-adapter boundary.** Resolved credentials reach adapters there, filled by the orchestrator from the `Resolution`; this package never writes context.
- **Never widen a credential across surfaces.** A client-supplied key enables only the inbound surface's provider, never on router-keyed requests, and a BYOK gateway displaces every other provider.
- Behavior here is pinned by the credential golden scenarios in `internal/proxy/golden_credentials_test.go`; a refactor must leave them byte-identical.
