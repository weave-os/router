# internal/credentials — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Credential Brokerage bounded context: which upstream providers a request holds credentials for (deployment key, BYOK, client-supplied, subscription). Read [root CLAUDE.md](../../CLAUDE.md) and [proxy CLAUDE.md](../proxy/CLAUDE.md) first.

## Rules

- **Inner ring, I/O-free.** Pure functions over explicit inputs; no context-key reads. The orchestrator in `internal/proxy` derives request facts from its context and passes them in `EligibilityRequest`.
- **`Broker` is the port; `Brokerage` is the deployment configuration that implements it.** Wire the deployment facts from `cmd/router/main.go`.
- **Observed subscription usage comes through the `SubscriptionUsage` port** (implemented by `usage.Observer`); the orchestrator passes the token and `ClaudeSubscriptionFacts` it derived.
- **`internal/requestcontext` stays the provider-adapter boundary.** Resolved credentials reach adapters there, filled once before dispatch; this package only decides eligibility.
- **Never widen a credential across surfaces.** A client-supplied key enables only the inbound surface's provider, never on router-keyed requests, and a BYOK gateway displaces every other provider.
- Behavior here is pinned by the credential golden scenarios in `internal/proxy/golden_credentials_test.go`; a refactor must leave them byte-identical.
