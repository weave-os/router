# internal/proxy/usage — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Per-credential subscription rate-limit headroom observer. Inner-ring, I/O-free. Read [root CLAUDE.md](../../../CLAUDE.md) first.

## What it does

Tracks the most recent rate-limit utilization each subscription backend reports on every response, keyed by a salted hash of the credential:

- **Claude** (`api.anthropic.com`, OAuth) — `anthropic-ratelimit-unified-{5h,7d}-*` quota windows and the plain `representative-claim: overage` together with `overage-in-use: true` (active paid usage), via `ParseAnthropicUnifiedHeaders`. The paid signal can arrive without either quota window. `seven_day_overage_included` is a distinct claim with unvalidated billing semantics.
- **Codex** (`chatgpt.com/backend-api/codex`) — `x-codex-{primary,secondary}-*`, via `ParseCodexHeaders`.

Quota observations guide temporary account cooldown and reset. They never prove included-only billing: provider-enforced prevention is required before any OAuth dispatch.

## Consumers (both in package `proxy`)

- **Subscription source selection** ([`../subscription_observation.go`](../subscription_observation.go)): observes quota to rotate eligible physical accounts in personal/shared order; no catalog cost discount or optimistic cold-start funding bonus.
- **Per-installation strict pass-through gate** ([`../usage_bypass.go`](../usage_bypass.go)): when an installation has `usage_bypass_enabled` and the requested model is covered by the caller's Claude or Codex subscription below `usage_bypass_threshold` (or nothing has been observed yet — cold start) and the provider transport enforces included-only subscription dispatch, the request passes straight through to that model — no routing, no substitution, no billing debit. It takes precedence over pins, the planner, and a policy sidecar's `authoritative_per_turn_selection` (which settles which model serves a *routed* turn, not whether the turn is routed at all). Unsupported adapters fall through to normal routing or refusal. Once that subscription crosses the threshold the normal routing path (including physical-account selection) resumes. Strict opt-in: off until the customer enables it in the dashboard.
- **Claude overage and exhaustion fallback** ([`../usage_bypass.go`](../usage_bypass.go) `claudeSubscriptionExhausted`): either an exhausted window or the plain paid overage claim suppresses the caller's Claude token. The latter is a successful but customer-billable lane, so it must leave the subscription even without a 429. Codex exhaustion uses [`../codex_failover.go`](../codex_failover.go). Managed accounts observed on paid overage are always skipped without persisting quota exhaustion; there is no last-resort overage seat, so the turn goes to another subscription, authorized API capacity, or fails. Paid overage is excluded from cost-neutral `subscription_served` telemetry and analytics.

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
