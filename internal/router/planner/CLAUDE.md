# internal/router/planner — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Prism-style cache-aware EV policy. Decides STAY (preserve pinned model's upstream prompt cache) vs SWITCH (take cluster scorer's fresh decision + eat one-time cache miss) per action. Read [root CLAUDE.md](../../../CLAUDE.md) first.

## Contract

```
Decide(pin, fresh router.Decision, estimated tokens, available models) → STAY | SWITCH
```

- **Pure function.** No DB lookups, no provider calls. No clock — the proxy computes cache warmth (it owns the clock) and passes it in as `Inputs.PinCacheCold`.
- Inputs are values: pin row, fresh decision, estimated token count, available-model set resolved at boot, and a cache-cold flag.

## Math, briefly

Compares expected per-action savings over the remaining horizon against the eviction cost of warming a new cache. The **tier-upgrade guard** fires when STAY would clearly under-serve the prompt — uses [`../catalog`](../catalog)'s Low/Mid/High tier to overturn a cost-driven "stay" when the fresh decision is in a strictly higher tier than the pin.

**Cache-warmth gate.** The cache-read multipliers and eviction cost only apply while the pin's upstream cache is warm. When `Inputs.PinCacheCold` is set (the pinned provider's cache TTL has lapsed — short and best-effort on the OSS compat providers vs Anthropic's 1h window; see [`../../providers`](../../providers).`CacheTTLFor`), both sides are priced uncached so raw economics + the tier guard decide, instead of a phantom cache gluing the session to a stale pin. The zero value means "assume warm", preserving the original behavior.

**Corrected economics (`ROUTER_SWITCH_CORRECTED_ECONOMICS`, default off).** The
legacy math prices the whole prompt at the cache-read multiplier and drops the
uncached remainder. Real cacheable share is 0.77-0.91, so 10-25% of a large
prompt is billed at full rate -- and that tail is where the raw price gap
between two models applies undiscounted. Measured over 30 days of production,
the legacy model understates a turn's cost by ~11x (cost-weighted bias -91%,
of which the k=1 assumption is 52 points).

When armed, each side is priced at its effective warm rate

```
r(k) = price * (1 - k*(1 - m))
```

with `k = CacheablePrefixTokens / EstimatedInputTokens`; eviction is charged as
the cache WRITE paid in place of the read, `k * tokens * price_fresh * (w - m)`,
not `(1 - m)`; and `PriorOutputTokens` adds the output term the legacy path is
blind to. `k` comes from the pin's own previous turn -- persistence beat a
trained gradient-boosted model on 154k production turns, so the router carries
the observation rather than a predictor. With no prefix evidence `k` falls back
to 1, which collapses the corrected rate exactly onto the legacy one.

The horizon is deliberately unchanged. It is wrong (`3` against an
exposure-weighted remaining horizon of ~253) but a sweep showed correcting it is
worth ~1.4 points against the economics' ~12, so it is a follow-up, not part of
this change.

Evidence: `router-internal/eval/cache_eviction/` in the WorkWeave repo (E0-E6);
`corrected_test.go` cross-validates the Go implementation against that harness's
Python reference to 1e-9.

## Invariants

- **Pure.** Anything network-touching belongs in `proxy.Service`, not here.
- **Tests cover EV math without spinning anything up.** Use in-memory fixtures; no `httptest`, no Postgres.

## What NOT to do

- **Don't add a runtime override that mutates α-blend.** Cost weighting is baked into the cluster scorer at training time. Per-request cost knobs are a separate (P1) feature, not a planner responsibility.
- **Don't read pricing from anywhere but [`../catalog`](../catalog).** Single source of truth — the OTel emitter and planner must agree.
