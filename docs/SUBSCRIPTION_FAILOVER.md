# Subscription failover — current state, drift, and unification plan

Status: design proposal. Nothing here is implemented yet.

The router can serve a turn on three kinds of credential:

1. the caller's **Claude Code subscription** (`sk-ant-oat…` OAuth token),
2. the caller's **Codex / ChatGPT subscription** (OAuth token + ChatGPT account id),
3. a **paid key** — the deployment's own provider key, or a per-request BYOK key.

"Subscription failover" is the behavior that keeps a turn alive when the caller's
subscription cannot serve it: the router suppresses the subscription credential,
re-resolves credentials, and retries the same model on a paid key.

That behavior was built twice. Anthropic ingress has had it for a while
([`../internal/proxy/usage_bypass.go`](../internal/proxy/usage_bypass.go) plus the rescue chain in
`ProxyMessages`); the OpenAI/Codex copy was added later in PR #1243
(`internal/proxy/codex_failover.go` plus a rescue block in
`ProxyOpenAIChatCompletion`) and deliberately mirrors the Anthropic one by hand.
Hand-mirroring is why they already differ. This document records exactly how,
which differences are provider-essential, which are accidents, and how to
converge them without a big-bang refactor.

**Reading order for a newcomer:** [Stage-by-stage side-by-side](#stage-by-stage-side-by-side)
→ [Drift table](#drift-table-with-verdicts) → [Bugs](#bugs-and-latent-failure-modes)
→ [Proposed shape](#proposed-shape) → [Migration plan](#migration-plan).

## Vocabulary

| Term | Meaning |
|---|---|
| **spent** | The plan window is exhausted. Upstream says so (429 / quota error body) or the usage observer already knows. Retrying the same credential is pointless until reset. |
| **token rejected** | The OAuth credential itself is bad (expired, revoked, wrong account). Retrying is pointless, period. |
| **retryable** | Generic transient upstream fault (`providers.IsRetryable`): 5xx, 408, transport error, idle timeout. |
| **suppression** | A context flag that makes credential resolution skip the subscription credential for this provider, so the paid key wins instead. |
| **rescue** | Re-dispatching the same routed model, in the same turn, under suppression. |
| **prelude committed** | Provider output already reached the client; no rescue is possible. |
| **prelude sent** | The buffered 200 + routing marker reached the client, but no provider output has; a rescue is still possible, but a JSON error envelope is not. |

## Stage-by-stage side-by-side

Anthropic column = `ProxyMessages` on `main`. Codex column = `ProxyOpenAIChatCompletion`
as of PR #1243 (also covers the Responses ingress, which shares that function).

| Stage | Anthropic (Claude subscription) | OpenAI (Codex subscription) |
|---|---|---|
| Subscription credential shape | `sk-ant-oat…` bearer, from the dedicated subscription header or an inbound OAuth bearer | OAuth bearer **plus** non-empty ChatGPT account id; anything API-key-shaped is rejected as a subscription |
| "Is this turn served on a subscription?" | `servedOnSubscription(ctx)` — resolved credential is OAuth, or a managed pool lease is serving | `servedOnCodexSubscription(ctx)` — resolved credential is OAuth **and** carries an account id |
| Model coverage gate | none at resolution time (any Anthropic model) | `codexSubscriptionCoversModel(model)`; an uncovered model never resolves to the Codex token |
| Paid-fallback probe | `anthropicFallbackKeyAvailable` — BYOK Anthropic key or deployment Anthropic key | `openaiFallbackKeyAvailable` — same shape, OpenAI provider |
| Pre-dispatch suppression | `claudeSubscriptionExhausted` = fallback key exists **and** observer says the plan window bound → suppress before resolving credentials | `codexSubscriptionExhausted` = same two conditions → suppress before resolving credentials |
| Subscription-only mode (credits depleted) | refuses with `ErrCreditsExhaustedSubscriptionUnavailable` when not subscription-served **or** the observer says the sub is already spent; skipped entirely when usage-bypass is engaged; then pins bindings to the routed provider | refuses only when not subscription-served; no observer check, no usage-bypass carve-out |
| Rescue eligibility | provider is Anthropic, not agent-shadow, subscription-served, not subscription-only, fallback key available, baseline rescue has not already run | provider is OpenAI, Codex-subscription-served, not a blind-experiment passthrough, not subscription-only, fallback key available |
| Rescue trigger | `providers.IsRetryable(err)` **or** `anthropicOAuthCredentialRejected(err)`, and the prelude is not committed | `providers.IsRetryable(err)` **or** `codexOAuthCredentialRejected(err)`, and the prelude is not committed |
| "token rejected" classifier | buffered 401/403 **whose JSON `error.type` is `authentication_error` or `permission_error`** | buffered 401/403, any body |
| "plan spent" signal | Anthropic returns 429 `rate_limit_error`; it is treated as generic-retryable, never recorded from the error itself. Exhaustion is learned only from response headers | `codexQuotaExhaustion` parses the error body: `error.type` ∈ {`usage_limit_reached`, `insufficient_quota`}, with optional `resets_at` epoch |
| Usage-observer write on failure | none from the error path; `providers.ObserveUpstreamHeaders` records `anthropic-ratelimit-unified-{5h,7d}-*` from whatever response carried them | `recordCodexQuotaExhaustion` synthesizes a `usage.Snapshot` with `UsedPercent: 1` and `ResetAt` from the body, in addition to header observation of `x-codex-{primary,secondary}-*` |
| Reset / TTL semantics | window length and reset come from the upstream headers, so a weekly window ages out weekly | synthetic record hardcodes a 5-hour window even when `resets_at` is a week away — see [B2](#b2-codex-synthetic-quota-record-hardcodes-a-5-hour-window) |
| Suppression key | `withSuppressedClaudeSubscription` | `withSuppressedCodexSubscription` |
| Credential re-resolution after suppression | `resolveAndInjectCredentials(subCtx, anthropic, model, headers)` | `resolveAndInjectCredentials(subCtx, openai, model, headers)` |
| Held-error ownership | primary dispatch defers the flush when a later rescue may run; whichever rescue declines to run flushes; guarded by a `deferredErrFlushed` latch and skipped for managed-pool errors | same latch, same defer, **no** managed-pool guard |
| Flush renderer (primary dispatch) | prelude-sent → Anthropic SSE `event: error`; otherwise `flushUpstreamErrorAsAnthropic` | Responses prelude sent → nothing; stream prelude sent → OpenAI SSE `data:` error frame; otherwise `flushBufferedIfPresent` |
| Flush renderer (rescue dispatch) | bare `flushUpstreamErrorAsAnthropic` — loses the prelude-sent branch | bare `flushBufferedIfPresent` — loses both the Responses and the prelude-sent branch (see [B1](#b1-the-rescue-dispatch-flushes-through-the-wrong-renderer)) |
| Telemetry | `cost.subscription_served`, `dispatch.subscription_failover`, `dispatch.failover_used`, `subscription_failover` log field | same four, driven by `codexFailoverUsed` |
| Billing attribution | billing key / `subscription_served` reflect the credential that actually paid after the rescue | same |
| Ingress-specific extras | Claude Code system-identity block injection (provider adapter), Anthropic SSE envelopes, baseline + sibling rescues that share the same held error | Responses passthrough writer and its separate prelude buffer, OpenAI SSE frames, cyber-refusal rescue that shares the same held error |

## Drift table with verdicts

Verdict legend: **essential** = a real provider difference, keep it behind a
provider policy; **drift** = accidental divergence to converge; **bug** = defect,
listed again under [Bugs](#bugs-and-latent-failure-modes).

| # | Difference | Verdict | Notes |
|---|---|---|---|
| D1 | Codex requires an account id to count as subscription-served; Anthropic requires only OAuth | **essential** | Codex upstream rejects the token without the account header. Keep as a policy predicate. |
| D2 | Codex gates on model coverage (`codexSubscriptionCoversModel`); Anthropic does not | **essential** | A ChatGPT plan covers only the Codex model family; a Claude plan covers the Anthropic catalog the caller can reach. Model coverage belongs in the policy. |
| D3 | Claude Code identity system block prepended for subscription turns | **essential** | Anthropic 429s a subscription call whose leading system block does not identify as Claude Code, regardless of quota. Lives in the provider adapter and must stay there. |
| D4 | "Plan spent" is a header signal for Anthropic and an error-body signal for Codex | **essential** | Anthropic publishes `anthropic-ratelimit-unified-*` on every response; the ChatGPT backend reports quota in the error body with no headers. Both must remain, behind one `Classify`/`RecordExhaustion` policy pair. |
| D5 | OAuth-rejection breadth: Anthropic checks `error.type`, Codex accepts any 401/403 | **drift** | The narrow form is the correct default; a 403 that is not a credential problem (policy/geo block) should stay terminal rather than silently spending a paid key. Converge on "status **and** provider-recognized body type", with the Codex body types filled in. |
| D6 | Anthropic's subscription-only refusal also fires when the observer says the sub is spent; Codex's does not | **drift** | Without it, a subscription-only Codex turn with a known-spent plan is dispatched anyway and comes back as a raw upstream 429 instead of the controlled 402. |
| D7 | Anthropic's subscription-only refusal is skipped under usage-bypass; Codex has no such carve-out | **drift (low)** | Codex usage-bypass turns take a different path today, so this is latent rather than live — but the two guards should read identically once shared. |
| D8 | Anthropic rescue requires `!agentShadowMode`; the OpenAI ingress has no shadow mode | **essential** | Shadow-mode is an Anthropic-ingress concept. Stays ingress-side. |
| D9 | Codex rescue requires `!routeRes.BlindExperimentPassthrough`; Anthropic's does not | **drift** | A rescue changes the *credential*, not the model, so a blind experiment is not invalidated by it. Pick one rule; recommendation is to drop the check on the Codex side, after confirming with whoever owns blind experiments. |
| D10 | Anthropic rescue requires `!baselineAttempted` | **essential** | Ordering constraint of the Anthropic-only baseline rescue; belongs to the ingress chain, not to subscription policy. |
| D11 | Codex writes a synthetic exhausted snapshot to the observer; Anthropic never writes from the error path | **drift** | Anthropic's 429 `rate_limit_error` frequently arrives without usable unified headers (notably the identity-block 429). Nothing is recorded, so the next turn re-dispatches the spent token and eats another 429. Anthropic wants the same "record what the error told us" hook. |
| D12 | Managed-pool errors suppress the deferred flush on Anthropic only | **bug** ([B3](#b3-openai-ingress-can-double-write-a-managed-pool-error)) | |
| D13 | Rescue dispatch uses a bare flush renderer on both sides | **bug** ([B1](#b1-the-rescue-dispatch-flushes-through-the-wrong-renderer)) | Symmetric, so it reads as "intended" in both copies; it is not. |
| D14 | Codex synthetic window is always 5 hours | **bug** ([B2](#b2-codex-synthetic-quota-record-hardcodes-a-5-hour-window)) | |
| D15 | Codex reads the token via `presentSubscriptionTokens`, Anthropic via the same helper — already shared | no drift | Worth noting: the token-detection layer is already common ground, which is why the shared policy is smaller than it looks. |
| D16 | Telemetry field names and billing re-attribution | no drift | Both emit the same four signals. Keep them by moving the emission into the shared helper's result. |

### Is Gemini/Google a third subscription surface?

Not today. `internal/subscriptions` defines exactly two pool providers (`claude`,
`codex`); `internal/auth`'s subscription provider enum matches; the Gemini
ingress explicitly has no consumer-subscription concept. Google's native
provider exists for wire reasons (`thought_signature` on Gemini 3.x), not for
subscription credentials.

A Gemini CLI / Google AI plan surface is plausible later, and it is the reason
the proposal below is a registry keyed by provider rather than a two-armed
`if`. Adding it should mean writing one policy value and one test file — no
ingress edits.

## Bugs and latent failure modes

### B1: the rescue dispatch flushes through the wrong renderer

Both ingresses build a prelude-aware flush closure for the **primary** dispatch,
then pass a **bare** renderer to the rescue dispatch:

```go
// primary dispatch (Anthropic)
flushErr: func(w http.ResponseWriter, err error) {
        if env.Stream() && preludeBuf.PreludeSent() {
                _ = emitAnthropicSSEErrorEvent(w, err)
                return
        }
        flushUpstreamErrorAsAnthropic(w, err)
},

// subscription rescue dispatch (Anthropic)
flushErr: flushUpstreamErrorAsAnthropic,
```

The Codex rescue does the same with `flushBufferedIfPresent`, and additionally
drops the `isResponses && responsesPreludeBuf.PreludeSent()` early return.

The rescue runs whenever the prelude is **not committed**, which is not the same
as "nothing on the wire": `PreludeSent()` can be true while `Committed()` is
false (the buffered 200 + routing marker was released, no provider bytes yet).
In that state a failed rescue calls `WriteHeader` on an already-written response
(logged `superfluous WriteHeader` at best) and appends a JSON error envelope to
what the client is parsing as an SSE stream — or, on the Responses ingress,
writes a chat-shaped envelope into a Responses stream.

Severity: medium. Requires a prelude-sent-but-uncommitted turn whose rescue also
fails. Fix: pass the same closure to both dispatches (one variable, used twice).
Small and clearly correct — a good candidate for the standalone fix PR.

### B2: Codex synthetic quota record hardcodes a 5-hour window

```go
const codexQuotaWindowMinutes = 5 * 60
...
Primary: usage.Window{UsedPercent: 1, WindowMinutes: codexQuotaWindowMinutes, ResetAt: resetAt},
```

`resets_at` from a weekly (secondary) limit lands in the primary window with a
5-hour length. The observer's freshness rule keeps a reading authoritative for
the life of its window, so the exhausted reading ages out after 5 hours and the
router optimistically re-dispatches a subscription that is spent for another six
days — one wasted upstream 429 per turn until the header path re-learns it.

Fix: derive the window from `resets_at` (clamped to the known primary/secondary
lengths), or record into the secondary window when the reset is beyond the
primary window. Needs a small decision, so not a drive-by fix.

### B3: OpenAI ingress can double-write a managed-pool error

`ErrSubscriptionPoolExhausted` / `ErrSubscriptionPoolUnavailable` are rendered by
the handler via `ClassifyDispatchError` (429 / 503 with a fixed message).
`ProxyMessages` therefore refuses to flush them itself:

```go
if deferredErrFlushed || subscriptionPoolFailure { return }
```

`ProxyOpenAIChatCompletion` has no such guard. Its deferred flush is reachable
whenever a rescue was viable (cyber-refusal rescue, or the new Codex rescue), so
a managed-pool failure can be written once by the proxy and then again by the
handler. In practice the proxy's write is a no-op for non-buffered errors
(`flushBufferedIfPresent` ignores anything that is not an `UpstreamErrorResponse`),
which is what keeps this latent — but it is a latch difference, not a design
decision, and it becomes live the moment a pool error arrives buffered.

### B4: Anthropic never records exhaustion learned from an error

See D11. Anthropic's spent-plan knowledge comes only from response headers. A
429 that carries no unified headers teaches the observer nothing, so the
pre-dispatch suppression never engages and every subsequent turn pays another
round-trip before rescuing. The Codex copy is the better behavior here; the
shared policy should give Anthropic the same hook.

### B5: broad Codex 401/403 classification can spend a paid key on a non-credential error

`codexOAuthCredentialRejected` returns true for *any* buffered 401/403. A 403
that means "this account is not allowed to use this model" or a policy block is
then treated as a rejected token: the router suppresses the subscription and
spends the caller's paid key on a request that will fail the same way. Anthropic's
narrow classifier already avoids this. See D5.

### Checked and **not** found

- **Suppression stranding a turn with no credential.** Both pre-dispatch checks
  require a fallback key before suppressing, and `resolveAndInjectCredentials`
  clears credentials only on the suppressed provider — covered by
  `TestResolveAndInjectCredentials_Suppressed*`.
- **Paid key spent in subscription-only mode.** Both rescues are gated on
  `!billing.SubscriptionOnlyFromContext(ctx)`, and subscription-only pins the
  bindings to the routed provider. `TestSubscriptionOnly_OpenAI_SubFailure_NoPaidFailover`
  covers the Codex side.
- **BYOK misclassified as a subscription.** `CodexSubscriptionCreds` rejects
  `sk-`/API-key-shaped tokens and requires an account id; the Anthropic side
  requires the `sk-ant-oat` prefix.
- **Double flush of the ordinary upstream error.** The `deferredErrFlushed`
  latch plus "whichever rescue declines to run flushes" is correct on both sides.
- **Cross-provider suppression leakage.** Suppression keys are provider-scoped
  and tested (`TestResolveAndInjectCredentials_ClaudeSuppressionLeavesCodexIntact`).

## Proposed shape

Two pieces: a **policy value per provider** (what a subscription means, how its
failures classify) and a **shared rescue helper** (the orchestration both
ingresses run). Nothing new crosses a layer boundary: the policy is a value in
`internal/proxy`, provider wire knowledge stays in `internal/providers/*`,
`internal/proxy/usage` stays in-memory and I/O-free, and dispatch mechanics stay
in `internal/dispatch`.

### Policy

```go
// subscription_policy.go (package proxy)

// subscriptionFailureKind is the provider-agnostic meaning of an upstream
// failure on a subscription-served turn.
type subscriptionFailureKind uint8

const (
    subscriptionFailureNone subscriptionFailureKind = iota
    subscriptionFailureSpent         // plan window bound; retry after reset
    subscriptionFailureTokenRejected // OAuth credential is bad
    subscriptionFailureRetryable     // generic transient fault
)

// subscriptionOutcome is what the policy learned from one failure.
type subscriptionOutcome struct {
    Kind    subscriptionFailureKind
    ResetAt time.Time // zero when unknown
}

// subscriptionPolicy is the per-provider knowledge the shared rescue needs.
// One value per subscription surface, held in a map on Service.
type subscriptionPolicy interface {
    Provider() string

    // ServedOnSubscription reports whether the credential resolved for this
    // turn is this provider's subscription credential.
    ServedOnSubscription(ctx context.Context) bool

    // Covers reports whether the plan can serve the routed model.
    Covers(model string) bool

    // Token returns the caller's present subscription token for observer
    // keying, or "" when none is present.
    Token(ctx context.Context, headers http.Header) string

    // Classify maps an upstream error to its subscription meaning.
    Classify(err error) subscriptionOutcome

    // Suppress returns a context in which credential resolution skips this
    // provider's subscription credential.
    Suppress(ctx context.Context) context.Context

    // Suppressed reports whether Suppress already applied to ctx.
    Suppressed(ctx context.Context) bool
}
```

Two implementations, `claudeSubscriptionPolicy` and `codexSubscriptionPolicy`,
each ~40 lines and each just a re-homing of functions that exist today
(`servedOnSubscription`, `codexSubscriptionCoversModel`, `presentSubscriptionTokens`,
`anthropicOAuthCredentialRejected` / `codexOAuthCredentialRejected`,
`codexQuotaExhaustion`, the two `withSuppressed*` helpers). A third
implementation is the whole cost of adding a Gemini plan later.

### Service-side glue

```go
// Service holds one policy per subscription surface.
func (s *Service) subscriptionPolicyFor(provider string) (subscriptionPolicy, bool)

// subscriptionFailoverPlan is the decision made once, before dispatch.
type subscriptionFailoverPlan struct {
    policy           subscriptionPolicy
    rescueEligible   bool // subscription-served, paid fallback exists, not subscription-only
    fallbackKey      bool
    subscriptionOnly bool
}

// planSubscriptionFailover answers "can this turn be rescued?" and performs the
// pre-dispatch suppression when the observer already knows the plan is spent.
// Returns the possibly-suppressed context.
func (s *Service) planSubscriptionFailover(
    ctx context.Context,
    provider, model string,
    headers http.Header,
) (context.Context, subscriptionFailoverPlan)

// observedExhausted reports the observer's verdict for the caller's token on
// this provider, independent of fallback-key availability. Subscription-only
// refusal uses this; suppression uses it AND fallbackKey.
func (s *Service) observedExhausted(ctx context.Context, p subscriptionPolicy, headers http.Header) bool
```

### The shared rescue

```go
// subscriptionRescue is the ingress-supplied wiring for one rescue attempt.
type subscriptionRescue struct {
    plan     subscriptionFailoverPlan
    decision router.Decision
    headers  http.Header

    // committed reports whether provider output already reached the client.
    committed func() bool
    // dispatch re-runs the turn under the suppressed context. The ingress owns
    // the attempt closure, bindings, flush renderer, purpose, and origin, so
    // wire format and prelude semantics stay ingress-side.
    dispatch func(ctx context.Context) (winnerIdx int, err error)
}

type subscriptionRescueResult struct {
    Ran        bool
    Served     bool // rescue dispatched and succeeded on the paid key
    WinnerIdx  int
    Err        error
}

// rescueOnPaidKey records what the failure taught the usage observer, decides
// whether a rescue is warranted, suppresses the subscription, and runs the
// ingress-supplied dispatch. It never writes to the client.
func (s *Service) rescueOnPaidKey(
    ctx context.Context,
    in subscriptionRescue,
    upstreamErr error,
) subscriptionRescueResult
```

Call site, both ingresses, identical:

```go
res := s.rescueOnPaidKey(ctx, subscriptionRescue{
    plan:      failoverPlan,
    decision:  decision,
    headers:   r.Header,
    committed: preludeBuf.Committed,
    dispatch: func(subCtx context.Context) (int, error) {
        subCtx = resolveAndInjectCredentials(subCtx, decision.Provider, decision.Model, r.Header)
        return s.dispatchWithFallback(subCtx, failoverInputs{
            w: contentSink, buf: preludeBuf, initialDecision: decision,
            bindings: subBindings, attempt: subAttempt,
            flushErr: flushErr, // the SAME closure the primary dispatch used
            deferFlushOnExhaustion: nextRescueViable,
            purpose: routeRes.dispatchPurpose(surfacePurpose),
            origin:  routeRes.dispatchOrigin(decision),
        })
    },
}, proxyErr)
if res.Ran {
    winnerIdx, proxyErr = res.WinnerIdx, res.Err
    subscriptionFailoverUsed = res.Served
}
```

### What the shared helper owns

- rescue eligibility (subscription-served, paid fallback exists, not
  subscription-only, plan covers the model, prelude uncommitted);
- honoring subscription-only mode — never spend a paid key there;
- calling `Classify` and recording what it learned in the usage observer, so the
  *next* turn suppresses pre-dispatch instead of paying another round-trip;
- applying the provider-scoped suppression and requiring a re-resolve;
- reporting whether the rescue served, for telemetry and billing re-attribution.

### What stays provider-specific (in the policy)

- what a subscription credential looks like (OAuth vs OAuth + account id);
- which models the plan covers;
- error-body and header dialects: `authentication_error` / `permission_error`
  vs `usage_limit_reached` / `insufficient_quota`, `resets_at` vs
  `anthropic-ratelimit-unified-*` vs `x-codex-*`;
- the suppression context key.

### What stays ingress-specific (in `ProxyMessages` / `ProxyOpenAIChatCompletion`)

- request preparation and cross-format translation;
- the prelude buffer, the Responses passthrough writer and its separate buffer;
- every flush renderer: Anthropic SSE `event: error`, OpenAI SSE `data:` frames,
  `flushUpstreamErrorAsAnthropic`, `flushBufferedIfPresent`;
- the held-error latch and the ordering of the *other* rescues (baseline,
  sibling, cyber-refusal) that share the same held error;
- shadow-mode and blind-experiment gates that are ingress concepts.

Explicitly **not** proposed: moving classification into `internal/dispatch` (it
would need provider-body knowledge it must not have), or moving suppression into
`internal/providers` (it is a routing decision, not wire I/O).

## Migration plan

Ordered, each step independently reviewable and revertable. Estimates are in
Devin sessions.

**Prerequisite:** PR #1243 lands first. Refactoring toward a copy that is not on
`main` is not reviewable.

### Phase 0 — characterization tests (0.5 session)

No production change. Add a table-driven `subscription_failover_parity_test.go`
asserting today's behavior for both ingresses over the same scenario matrix:
{spent, token-rejected, retryable, unrelated-4xx} × {fallback key / none} ×
{subscription-only on/off} × {non-stream, stream pre-prelude, stream prelude-sent}.
Existing coverage to extend rather than duplicate:
`subscription_exhaustion_internal_test.go`, `subscription_oauth_reject_internal_test.go`,
`subscription_suppression_credential_internal_test.go`, `subscription_only_openai_test.go`,
`codex_failover_internal_test.go`, `usage_bypass_internal_test.go`.
Every later phase must leave this file green and unedited except where a drift
row is deliberately closed.

### Phase 1 — B1 flush-renderer fix (0.25 session)

Standalone bug fix, independent of the refactor: hoist each ingress's flush
closure into a variable and pass it to the rescue dispatch too. New test: a
prelude-sent, uncommitted turn whose rescue also fails emits an SSE error frame
and no JSON envelope, on both ingresses.

### Phase 2 — introduce the policy interface, no behavior change (1 session)

Add `subscription_policy.go` with the interface and the two implementations,
each delegating to the existing functions. Wire a `map[string]subscriptionPolicy`
onto `Service`. Ingresses still call the old helpers. Proof: the new unit tests
assert the policies return exactly what the old helpers return, for the same
inputs; parity suite unchanged.

### Phase 3 — route both ingresses through the policy (1 session)

Replace direct calls to `servedOnCodexSubscription`, `codexSubscriptionCoversModel`,
`anthropicOAuthCredentialRejected`, `codexOAuthCredentialRejected`, and the
`withSuppressed*` helpers with policy calls. Delete the now-unreferenced
duplicates. Still no behavior change; parity suite is the proof.

### Phase 4 — shared eligibility and pre-dispatch suppression (1 session)

Introduce `planSubscriptionFailover` and use it on both ingresses. This is where
**D6** and **D7** close: the subscription-only refusal becomes one code path, so
Codex gets the observed-exhausted check and the usage-bypass carve-out. Parity
rows for those two scenarios flip from "Codex dispatches and 429s" to "controlled
402"; update them in the same PR with the reason in the diff.

### Phase 5 — shared rescue helper (1.5 sessions)

Introduce `rescueOnPaidKey` and move both rescue blocks onto it. Highest-risk
phase: it touches held-error ownership. Guardrails: the ingress keeps its own
`flushErr` and `deferFlushOnExhaustion`, the helper never writes to the client,
and the parity suite covers all three prelude states. Fix **B3** here by moving
the managed-pool latch into shared code.

### Phase 6 — unify classification and observer recording (1 session)

Close **D5** (narrow the Codex 401/403 classifier to recognized body types),
**D11**/**B4** (give Anthropic the record-from-error hook), and **B2** (derive
the Codex synthetic window from `resets_at`). These are behavior changes with
real upside; each gets its own test and could be split into three PRs if review
prefers.

### Phase 7 — cleanup and observability (0.5 session)

Delete `codex_failover.go` and the Anthropic-side leftovers, fold the guidance
into [`../internal/proxy/CLAUDE.md`](../internal/proxy/CLAUDE.md) (then
`make generate-agent-guides`), and add one counter per
`(provider, failure kind, rescued)` so the next drift shows up in dashboards
rather than in a bug report.

Total: ~6.75 Devin sessions, of which phases 0–5 are behavior-preserving.

### Is this one safe PR?

No. Phases 0–3 could reasonably be one PR (~2 sessions) and are genuinely
low-risk. Phase 5 should not ride along: held-error ownership across four
rescues on the Anthropic ingress is the part most likely to produce a
client-visible regression (dropped error, double flush, envelope in the wrong
wire format), and it deserves to be revertable on its own. Phase 6 changes
behavior deliberately and must be separable for rollback.

The one thing worth pulling forward is Phase 1 — the flush-renderer fix is small,
clearly correct, and independent of everything else.
