# cmd — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Composition root. Only place that constructs concrete adapters + wires them together. Read [root CLAUDE.md](../CLAUDE.md) first.

## Rules

- **`cmd/router/main.go` wires admission, policy runtime, providers and persistence.** The shared stable fleet serves Default/Boost/Max; ordinary internal serving credentials are admitted only in the internal fleet. Scoped internal test-plan grants are the stable-fleet exception.
- Keep `main.go` focused on wiring. Today's helpers:
  - `buildClusterScorer` — per-version Scorer assembly + embedder warmup
  - `buildExploringRouter` — optionally wraps the cluster router in `banditexplore` (env-flag gated; off by default)
  - `buildSemanticCache` — response-cache assembly
  - `buildOtelEmitter` — OTel span exporter
  - `runSessionPinSweep` — TTL sweep loop
  - `resolveHardPinModel` / `resolveCompactionModel` / `resolveDefaultBaselineModel` / `resolveAvailableModels` — boot-time model resolution
  - `registerDeploymentKeyedProvider` — shared "resolve key → build client → log" registration for the providers whose gating collapses to that shape (Fireworks, Makora, Together, Bedrock, Google); OpenRouter and Anthropic/OpenAI stay bespoke
  - small env parsers (e.g. `envVarHint`, `parseEnvInt`, `parseEnvFloat`, `parseEnvDurationMs`)
- **No more heuristic-fallback router.** If cluster routing fails to boot, `main.go` panics. Misconfiguration must abort the process rather than silently degrade.
- Validate deployment target overrides through `policy.Registry.ValidateDeployment` after provider/model configuration is resolved. A partial or catalog-incompatible explicit target is a startup error, not permission to substitute a default.
- **Never introduce DI container, reflection-based wiring, or service locator.** Composition = plain Go function calls.
- **`panic` is reserved for startup-time fail-fast** (`config.MustGet`, cluster-scorer boot failure, invalid `ROUTER_DEPLOYMENT_MODE`). Never panic on request path.

## Deployment-mode dispatch

`ROUTER_DEPLOYMENT_MODE` is read at boot:

- `selfhosted` (default): mounts dashboard at `/ui/*`, `/admin/v1/*` API, dashboard cookie auth. Provider keys read from env vars.
- `managed`: dashboard + `/admin/v1/*` not mounted. Billing-backed managed deployments load platform keys and enforce balance checks. Only managed deployments without billing remain BYOK-only with empty deployment keys; a billing boot-check error defaults to billing-enabled, not BYOK-only.
- Any other value → panic at boot.

Provider registration:

- Every provider goes into `providerMap` regardless of mode.
- `envKeyedProviders` (parallel set) tracks which providers have a deployment-level key configured so the hard-pin resolver knows what's safe to pin to.
- Provider credential mode follows `byokOnly` in `cmd/router/main.go`, not `managed` alone. Preserve installation BYOK opt-in and client subscription routing when changing registration.

Single source of truth for provider→env-var mapping = `providers.APIKeyEnvVars` in [`../internal/providers/provider.go`](../internal/providers/provider.go). Admin `/config` view reads it so it can't drift from actual wiring.

`cmd/router-warmup` emits a full-catalog request plan by default. Live warmup
requires `-execute`, a fleet origin and `ROUTER_WARMUP_API_KEY`. It chooses each
model's least supported declared effort and reports all failures. Keep it broad;
do not execute live warmup as a unit-test or local validation step.
