# internal/proxy/usage — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Per-credential subscription rate-limit headroom observer. Inner-ring, I/O-free. Read [root CLAUDE.md](../../../CLAUDE.md) first.

## What it does

Tracks the most recent rate-limit utilization each subscription backend reports on every response, keyed by a salted hash of the credential:

- **Claude** (`api.anthropic.com`, OAuth) — `anthropic-ratelimit-unified-{5h,7d}-*` quota windows and the plain `representative-claim: overage` together with `overage-in-use: true` (active paid usage), via `ParseAnthropicUnifiedHeaders`. The paid signal can arrive without either quota window. `seven_day_overage_included` is a distinct claim with unvalidated billing semantics.
- **Codex** (`chatgpt.com/backend-api/codex`) — `x-codex-{primary,secondary}-*`, via `ParseCodexHeaders`.

Quota observations guide temporary account cooldown and reset. Only Claude accounts identified as Enterprise by the authenticated provider profile use Enterprise-only billing. Pro, Max, Team and unknown plans are never identified from quota or overage observations. Organizations may explicitly opt out to restore Weave-funded fallback for verified Enterprise accounts. These observations cannot guarantee that a request never crosses into provider-enabled extra usage.

Configured native Anthropic BYOK takes precedence over both caller OAuth and linked Claude accounts, including unknown quota. It disables Claude subscription bypass and does not require subscription exhaustion proof to dispatch on that key. This prevents a cold OAuth probe from already billing Enterprise overage; a BYOK credential failure never authorizes a subscription fallback. Codex subscription precedence is unchanged.

## Consumers (both in package `proxy`)

- **Subscription source selection** ([`../subscription_observation.go`](../subscription_observation.go)): observes quota to rotate eligible physical accounts in personal/shared order; no catalog cost discount or optimistic cold-start funding bonus.
- **Per-installation strict pass-through gate** ([`../usage_bypass.go`](../usage_bypass.go)): when an installation has `usage_bypass_enabled` and the requested model is covered by the caller's Claude or Codex subscription below `usage_bypass_threshold` (or nothing has been observed yet — cold start) and the provider adapter supports native subscription OAuth, the request passes straight through to that model — no routing, no substitution, no billing debit. It takes precedence over pins, the planner, and a policy sidecar's `authoritative_per_turn_selection` (which settles which model serves a *routed* turn, not whether the turn is routed at all). Unsupported adapters fall through to normal routing or refusal. Once that subscription crosses the threshold the normal routing path (including physical-account selection) resumes. Strict opt-in: off until the customer enables it in the dashboard.
- **Claude overage and exhaustion fallback** ([`../usage_bypass.go`](../usage_bypass.go) `claudeSubscriptionExhausted`): the authenticated profile's `organization.organization_type` is the exclusive source of Claude plan identity. `claude_enterprise_only` applies only to verified Enterprise accounts. Their tokens and managed accounts stay eligible through observed quota and paid overage, and their turns do not fall back to paid API capacity. Ordinary Pro, Max and Team accounts retain exhaustion suppression, account rotation and authorized paid fallback even when the organization flag is true. Profile failures leave identity unknown and cannot authorize paid fallback. Explicit BYOK and credits-only configuration retain precedence. A verified Enterprise seat can still spend its own overage when Weave credits or a spend cap prohibit paid API fallback; this is never inferred from missing quota windows. Codex exhaustion uses [`../codex_failover.go`](../codex_failover.go). Paid overage remains excluded from cost-neutral `subscription_served` telemetry and analytics.

## Why it exists

Subscription customers (Claude Code / Codex logged-in flows) want unused, perishable plan quota spent on their own subscription — not silently redirected to a cheaper substitute, and not billed by us — until they actually approach their cap.

## Invariants

- **Pure in-memory state, no persistence.**
- Entries keyed by `usage.CredentialKey` (HMAC-SHA256 prefix of the token under a process-scoped salt) so logs + metrics never see the raw token.
- A reading stays authoritative for the life of its binding quota window (`freshFor`), not a flat short TTL — a near-cap reading must not age out and re-read as optimistic slack while the window is still exhausted. When the upstream reports a `*-reset` instant (`Window.ResetAt`), `freshFor` expires the reading at that reset (clamped to the window length) rather than a full window from observation — so an exhausted reading, and any exhaustion suppression keyed off it, lifts when the plan actually refills instead of lagging it by days.
- An overage-only reading stays authoritative through the unified plan reset when reported. Without a reported plan reset, it remains authoritative until a new observation establishes headroom. The overage credit-window reset does not restore subscription headroom.
- Per-window merge on `Record`: a response reporting only one window must not erase the other window's last-known utilization.
- Periodic `Sweep` (driven by the composition root on a ticker) bounds memory; the package spawns no goroutines of its own.
- I/O-free per the inner-ring rule — just structs, maps, a mutex, and an injected clock.
