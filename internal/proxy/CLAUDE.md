# internal/proxy — CLAUDE

> **Mirror notice.** Source for generated [AGENTS.md](AGENTS.md). Edit this file, then run `make generate-agent-guides`; CI rejects drift.

Routing/dispatch service. Per-action orchestrator that composes scorer, planner, handover, cache, sessionpin, catalog, turntype, usage, billing, providers, and translate. Read [root CLAUDE.md](../../CLAUDE.md) first.

## Surface

`*proxy.Service` in [`service.go`](service.go) exposes:

- `Route` — returns `router.Decision`
- `ProxyMessages` — Anthropic Messages surface
- `ProxyOpenAIChatCompletion` — OpenAI Chat Completions
- `ProxyGemini` — Gemini native generateContent

Plus the action loop, handover adapter, cache writer, and session-key derivation in sibling files.

## Adding a method to `*proxy.Service`

1. **Define method on `*proxy.Service`.** No I/O directly here — push into a provider adapter or repo. Inner-ring imports (`router`, `providers`, `translate`, `observability`, `internal/router/*`, `internal/proxy/usage`) + small utility libs are fine.
2. **If you need new repo methods**, surface them as an interface in the inner-ring package, implement in `internal/postgres/`. Example: `sessionpin.Store` in [`../router/sessionpin/store.go`](../router/sessionpin/store.go), implemented by `postgres.SessionPinRepo`.
3. **Update `service_test.go` fakes** to satisfy the expanded interface. Real assertions on return values, not "mock called with X".

## Per-action flow (cache-aware action routing)

The per-action flow is more than "scorer → dispatch". Pinned session, planner verdict, optional handover summary, and the semantic response cache all sit between the inbound request and the upstream provider. Packages are intentionally small + single-purpose so each is unit-testable without the others. ("Action" = one upstream API request; see [../../docs/SEMANTICS.md](../../docs/SEMANTICS.md). The `Stage` column below is a pipeline stage *within* one action, not a router step.)

| Stage | Package | Notes |
|---|---|---|
| Turn-type classification | [`../router/turntype`](../router/turntype) | MainLoop / ToolResult / SubAgentDispatch / Compaction / Probe |
| Session-pin lookup | [`../router/sessionpin`](../router/sessionpin) | Sticky `(api_key_id, session_key, role)` pin |
| Fresh routing decision | [`../router/cluster`](../router/cluster) | Cluster scorer argmax |
| STAY vs SWITCH | [`../router/planner`](../router/planner) | Cache-aware EV policy |
| Handover summary on SWITCH | [`../router/handover`](../router/handover) | Bounds switch-action input cost |
| Semantic response cache | [`../router/cache`](../router/cache) | Cross-request, non-streaming only |
| Subscription strict pass-through gate | [`usage`](usage) | See [`usage/CLAUDE.md`](usage/CLAUDE.md) |

The provider-backed `Summarizer` implementation for handover lives in [`handover.go`](handover.go); the inner-ring `handover` package only defines the contract. On summarizer timeout or error, proxy keeps the full prior history unchanged (it does **not** trim) — a pricier switch action beats silently dropping the conversation the switched-to model needs.

## Proactive context-window compaction

`ProxyMessages` / `ProxyOpenAIChatCompletion` / `ProxyGeminiGenerateContent` call [`maybeCompact`](compaction.go) **before** routing so an over-long session is compacted rather than dead-ending in the scorer with no eligible provider. It engages when the estimate reaches `ROUTER_COMPACTION_PCT` (default 0.85) of the largest eligible model's window and runs Claude Code's tiered cascade: (1) `ClearOldToolResults` — local, clears stale tool results; (2) structured 9-section summary via a **window-aware** Anthropic-family summarizer (`SummarizeForCompaction`; the session's active Anthropic pin when it is mid-tier or better — the model that ran the conversation, prompt cache warm — else `ROUTER_COMPACTION_MODEL`, default `claude-sonnet-4-6`, else `claude-fable-5` when the history won't fit) rewritten with `RewriteForCompaction(summary, recentTurns)`; (3) progressive `TrimLastNMessages` rescue. If even the trimmed floor overflows, it returns `ErrContextWindowExceeded` → HTTP 413 (distinct from the "no provider keys" `ErrNoEligibleProvider`). The summary call is billed as a `_precompaction_summary` ledger row. The cascade is **harness-aware** (`compactionPolicyFor(ClientIdentity.ClientApp)`): Claude Code auto-compacts itself at `window − 13K` against the *requested* model, so its turns defer to the client while the routable pool can serve that window (the router still compacts when the pool is smaller or the request already overflows); Codex / Gemini CLI / unknown harnesses always get the router cascade. Native `/v1/responses` passthrough bytes are still not rewritten. A harness's own compaction turn (`turntype.Compaction`, Claude Code's or Codex's) is never rewritten; `compactionHardPin` pins it to the session's Anthropic model (else the compaction model) — except that a Codex thread a non-Anthropic model has been serving (active thread pin or `_hmm_history` row, mid-tier or better) stays on that model, since the turn is Responses-format and its summary replaces the thread's history — unless the operator set `ROUTER_HARD_PIN_MODEL`, which keeps the generic hard-pin. Trigger below the window (not at overflow) is load-bearing: a summarizer can only ingest a history that still fits *some* model.

## Model-restriction layers

Three distinct restrictions compose, and the layering is deliberate — do not
collapse them.

| Layer | Scope | Polarity | Where enforced |
|---|---|---|---|
| `allowed_models` | org (installation) | fail-closed | desugared into `ExcludedModels` by `excludedModelsForRequest` |
| `excluded_models` | org (installation) | fail-closed | scorer + policy resolver |
| `global_automatic_routing_exclusions` | deployment | fail-open (soft) | `AutomaticExcludedModels`: scorer, policy resolver, and every automatic-pin gate |
| `cluster_model_lists` | API key (org default) | fail-open | `policy.ApplyClusterArmOverrides` |
| `model_router_user_cluster_model_lists` | router user | fail-open | same, after `mergeClusterOverrides` |
| subscription plan-aware routing | router user | fail-open on unknown/all-exhausted state | request-scoped exclusions from `withPlanAwareSubscriptionModels` |

**The allowlist is desugared, not separately filtered.** `excludedModelsForRequest`
adds every routable model absent from a non-empty allowlist to the exclusion
set, so all six existing enforcement sites honor it with no new filter loops.
`router.Request.AllowedModels` exists only so errors and diagnostics can name
the allowlist instead of dumping the desugared exclusion list.

**The deployment-wide automatic exclusion is soft, and is a separate request
field for that reason.** `global_automatic_routing_exclusions` is the Weave
control plane's list of models the router may not *choose*; the same models
still serve an explicit `/force-model` pin, which is what makes it safe to
disable a model without stranding a debugging or eval session. Folding it into
`ExcludedModels` would have made it hard — that set also rejects a forced pin
(`forcedModelBinding`) — so it travels as `router.Request.AutomaticExcludedModels`
and every enforcement site treats an emptied pool as "ignore me this turn"
rather than an error. It reaches the request path through a ~1min TTL cache
(`globalAutomaticExclusionCache`) that serves its last good snapshot on a
refresh failure and fails open on a cold one; there is no invalidation topic
because the existing one is keyed per installation.

**Automatic reuse of a model is not just fresh selection.** A disable has to
reach sessions already pinned, so `automaticPinEligible` and the pin-drop guard
in `runTurnLoop` cover tool-result stickies, planner STAY, HMM EV stays, expiry
re-anchors, post-command continuations, band swap, sibling failover, the policy
deadline default, and loop/struggle escalation — every path where the router
picked the model. `forcedPinEligible` deliberately does not.

**A wholly non-routable allowlist is rejected at the admin API.** Membership
validation for `PUT /admin/v1/allowed-models` is catalog-wide on purpose —
force-model and hard-pin reach rows the router never scores — but the
desugaring only excludes over `routableUniverse()`. A list naming *only*
non-routable rows (an unbound provider, or a tierless passthrough-only row)
therefore excludes the entire pool and 400s every routed request with
`ErrAllowlistEmptiesPool`. The handler calls `Service.RoutableModels()` and
refuses to save such a list, deferring to the unknown-model error first so a
typo is not reported as a routability problem. Keep that accessor and the
desugaring reading the same universe. `availableModels` is the generic
`RoutingTargetSet` plus `HMMRoutingTargetSet` when an HMM sidecar is wired,
so an HMM-only allowlist is not rejected as emptying the pool.

**The fail-open/fail-closed asymmetry is load-bearing.** An org allowlist is a
compliance control (a breach is worse than an outage); a user's per-cluster
selection is a preference (hard-failing a turn because a personal pick went
stale is a terrible trade). This composes safely ONLY because the allowlist
binds upstream in the resolver, while the preference binds downstream in the
override layer — falling open there falls back to a selection that is already
allowlist-constrained. **Never enforce the allowlist in the override layer**;
fail-open would silently defeat it.

**Per-cluster lists intersect, never override.** `mergeClusterOverrides`
intersects a user's selection with the API-key-scoped list. A plain override
would let an individual re-admit a model the org deliberately removed —
privilege escalation through an admin control.

**Subscription plan-aware routing is an overlay, not a roster mutation.** When
enabled, the request observes the user's Claude and Codex plan families. If at
least one plan has headroom, models covered only by exhausted plans are added
to the request's hard exclusions. If every linked plan is exhausted, the
overlay contributes no exclusions and normal paid/BYOK routing resumes. Unknown
state also contributes no exclusions; only reliable quota exhaustion changes
eligibility. The global HMM roster remains unchanged.

**Two paths deliberately bypass `excluded_models` and need explicit allowlist
handling:** `usageBypassEngaged` (consults `SafetyExcludedModels`, since
exclusions are a preference the bypass may override — but an allowlist is not)
and `ROUTER_EXCLUDED_MODELS` (an operator escape hatch that short-circuits
`excludedModelsForRequest` entirely; intentional, so an operator debugging a
deployment is not constrained by one org's config).

**A BYOK gateway overrides all of it.** When the installation has its own
gateway key (`anthropic_gateway` / `openai_gateway`),
`enabledProvidersForRequest` returns only those gateways and
`router.Request.GatewayProviders` puts the resolver in gateway-exclusive mode:
vendor bindings are dropped, `excluded_providers` becomes a no-op, and the only
routable models are the ones a gateway key's `model_aliases` names. A tenant
that wired its own endpoint mandated it — routing around it is a compliance
break, and excluding every vendor by hand (the old lever) is what silently
emptied one org's candidate set. A key with no aliases therefore serves
nothing: resolution comes back empty with `ExclusionGatewayNotServed` and
dispatch answers `policy.ErrGatewayServesNoDeployedModel` (HTTP 400, "add
aliases") instead of reporting the router as unavailable. Deployment-keyed
gateways are excluded from this: a self-hosted deployment keyed for a gateway
still serves the catalog's own gateway bindings.

**A gateway's `model_not_found` is remembered per (endpoint, model).** An
alias can name a model the endpoint does not actually publish — a Snowflake
Cortex key aliasing `grok-4.6` made every title-gen turn resolve to it, eat an
upstream 404, and recover only through a sibling-failover hop (3.7s-43.2s added
latency per turn, prod 2026-08-28). `rememberGatewayLacksModel` records the pair
on that 404 and `gatewayUnservedModelsForRequest` folds it into
`excludedModelsForRequest`, so later turns resolve around the alias instead of
re-buying it. Scoped tightly on purpose: gateway providers only (a vendor still
has catalog bindings to walk), and a model stays routable unless **every**
gateway key aliasing it has refused, since a second endpoint may serve it. The
alias itself is still the customer-side fix — this only caps the bill at one 404.

**The hard-pin tier resolves against the same bindings.** Probe/title-gen/
classifier/compaction turns bypass the scorer, so `hardPinResolver` gets its
own `HardPinRequest` carrying `CustomBindings` + `GatewayProviders` and selects
via `cluster.FastestModelForRequest`. Without them a gateway-only installation
resolved nothing and every such turn 503'd `ErrClusterUnavailable` ("cluster
scorer failed") while its scored turns routed fine — prod 2026-08-26. An empty
result under a gateway now reports `ErrGatewayServesNoDeployedModel` for the
same reason the resolver does: the alias list is the thing to fix.

## Translation

`proxy.Service` is the **only caller of [`../translate`](../translate)**. Keep providers ignorant of cross-format concerns. See [translate/CLAUDE.md](../translate/CLAUDE.md) for the recipe.

## OpenAI endpoint selection (chat/completions vs Responses)

`translate.UseOpenAIResponsesAPI` decides which OpenAI surface an attempt
POSTs to, and the two OpenAI providers get deliberately different rules.

**Direct OpenAI takes every turn it can express.** OpenAI documents Responses
as the API new integrations build on, it is the only endpoint that will serve a
reasoning model a function tool (chat/completions 400s that combination from
gpt-5.4 on — the prod incident that started this), and it is the only one that
round-trips encrypted reasoning across turns, which is also where the
prompt-cache win comes from. The exception is a turn whose parameters have no
Responses equivalent (`env.RequiresChatCompletionsParams`): stop sequences are
the whole set today, and such a turn stays on chat/completions rather than
silently dropping them. Reasoning targets are exempt from that check because
they reject `stop` on chat/completions too, so it is already dropped for them.

**OpenAI-compatible gateways stay narrow** — reasoning tool turns only. Most
mount no Responses surface at all, so the endpoint buys nothing there beyond
the one turn the gateway would otherwise reject (Snowflake Cortex 400s a 5.6
tool turn on chat/completions no matter what we send). A gateway that answers
404/"API disabled" is retried once on chat/completions while pre-commit and
memoized per effective base URL (`gatewayLacksResponses`), so the next turn
skips the probe.

Both rules sit behind `ROUTER_OPENAI_RESPONSES_BROAD` (default on, per-org
overridable via `flags.KeyOpenAIResponsesBroad`). Turning it off restores the
narrow rule for direct OpenAI too — the reasoning tool turn chat/completions
rejects is still promoted, since that one is a correctness fix, not a rollout.

Anthropic and chat/completions ingress re-emit the request through
`PrepareOpenAIResponses` per attempt, so a compaction or handover rewrite is
carried faithfully. A Responses-ingress caller dispatches its ORIGINAL bytes
natively instead, which is why that promotion is skipped when compaction or a
handover rewrote the envelope — those bytes are stale, and the chat projection
is the only faithful representation until item-level emit from a rewritten
envelope lands.

**A chat caller is served chat, whatever the upstream surface.** The three
combinations differ in both request emit and response handling, so the decision
is the typed `openAISurface` (`surfaceChat` / `surfaceResponsesNative` /
`surfaceResponsesTranslated`), not a URL swap: a translated attempt wraps the
client writer in `translate.ResponsesToOpenAIChatWriter`, which renders the
Responses stream as chat.completion.chunk frames (or one chat.completion body
for a non-streaming client). It returns a pre-output upstream failure as
`providers.UpstreamErrorResponse` so the unsupported-endpoint fallback still
works, and switches to an in-stream error frame once output is committed.

## Runtime provider fallback

Multi-binding models (deepseek/qwen/moonshot with Fireworks/Makora/Bedrock primary + OpenRouter fallback in [`catalog.Model.Providers`](../router/catalog/catalog.go)) dispatch through [`dispatchWithFallback`](fallback.go). The helper walks the ordered binding list, retries on `providers.IsRetryable` errors (5xx/408/429 buffered responses, transport errors, `httputil.ErrUpstreamIdleTimeout`), and on exhaustion writes the final upstream error envelope via a format-specific renderer (`flushUpstreamErrorAsAnthropic` for ProxyMessages, `flushBufferedIfPresent` for ProxyOpenAIChatCompletion).

**Model-not-found 404 → cross-binding failover (only).** A buffered upstream 404 (`providers.IsUpstreamModelNotFound`) means the chosen provider doesn't serve the model — a stale/wrong upstream id or a provider with no active endpoints. It is deliberately *not* in `IsRetryable`: re-hitting the same provider is futile (so it never triggers same-binding retry), but a different provider binding may carry the model, so it triggers one cross-binding hop. This rescues a request that would otherwise hard-fail at the client as "selected model may not exist." On the last binding the 404 still flushes.

**Billing-blocked 402 → cross-binding failover (only).** Same shape as the 404 (`providers.IsUpstreamProviderBillingBlocked`): the provider refuses this account — credits exhausted, or the endpoint moved behind a billing plan we're not on, which is how Makora EOL'd DeepSeek-V4-Pro (402 `insufficient_credits` on every turn while Together/Fireworks kept serving it). Same-binding retry would just re-bill the same rejection, so it walks to the next binding and flushes on the last one.

**Same-cluster model failover ([`sibling_failover.go`](sibling_failover.go)).** Provider-level failover assumes the model has somewhere else to run; a single-binding model on an overloaded provider does not (prod's `claude-opus-5` 529 storm: three attempts, one dark Anthropic endpoint, client eats the 529). When every binding is exhausted pre-commit on a retryable/404/402 fault, `ProxyMessages` re-dispatches a *different* model drawn from `Decision.Metadata.CandidateModels` — the pool the policy already scored for this turn, so the rescue stays inside the accepted quality band — preferring a candidate on a provider other than the one that just failed. It runs last in the rescue chain (after baseline and subscription failover), rebuilds the request through `buildAttempt` for the candidate's translation family, re-resolves credentials and bindings, and attributes pricing/telemetry/pin usage to the model that actually served. Gated by `ROUTER_SIBLING_FAILOVER` (default on) and skipped for `/force-model`, shadow mode, and subscription-only balances. BYOK (`shouldFailover`) normally disables it too, with one carve-out: a BYOK-gateway turn may rescue onto a candidate that one of the request's own gateway keys aliases (`gatewaySiblingDecision`) — same credentials, so no cross-provider 401 risk.

**Single-binding same-binding retry.** Most catalog models carry one binding (Anthropic/OpenAI/Google), so cross-binding failover has nowhere to walk — a sole-provider 5xx/timeout would kill the request. For these, `dispatchWithFallback` retries the *same* binding in place up to `maxSameBindingRetries` (2) with exponential backoff (`sameBindingBackoff`: 250ms, 500ms), pre-commit only, abortable on ctx cancel (`sleepWithContext`). Multi-binding models skip in-place retry (`len(bindings) > 1` breaks the inner loop) and fail straight over to the next provider — a different upstream beats re-hitting the flaky one. Tests inject `Service.retrySleep` to keep the backoff instant.

**Retries are bounded twice: count AND wall-clock.** `maxSameBindingRetries` caps how many attempts; `sameBindingRetryBudget` (10s) caps how much time they may consume in total. The count alone bounds attempts but not cost — an upstream that accepts the stream and never answers burns a full `ResponseHeaderTimeout` (30s) per attempt, so three of them spend ~90s on a request that was never going to be served (prod 2026-08-26: a gateway hanging deterministically on tool-result turns, where every retry re-sent the identical payload). A transient blip clears on a *quick* retry by definition, so an attempt series that already outran the budget is not the fault class in-place retry was built for; cheap failures (5xx in milliseconds) still get the full attempt count. The budget stopping a retry logs at WARN with `spent_ms`/`budget_ms` — without it, a hang and a blip are indistinguishable in the logs. Tests inject `Service.now` to simulate a slow attempt without burning real time; express the simulated duration as an absolute value, never as a multiple of `sameBindingRetryBudget`, or the test scales with the constant and can never fail.

`preludeBuffer` wraps the client writer on every request so the eager SSE Prelude doesn't commit the response to the client before the upstream produces its first byte. The buffer absorbs pre-Seal writes (Prelude's status + `message_start`), commits on the first post-Seal write (= first upstream chunk), and `Discard()`s pre-commit state between attempts so a retry begins with a pristine writer. `Committed()` is the retry gate: once it flips, the response is on the wire and no further retry is allowed.

Per-attempt body rebuild: each closure constructs `EmitOptions` with `TargetProvider = d.Provider` so the OpenRouter-only gates in [`emit_openai.go`](../translate/emit_openai.go) (`provider` hint, `reasoning: {enabled:false}`, system reminder for tool turns, tool-temp override) fire on the OpenRouter attempt but not on Fireworks/etc. Otherwise OpenRouter would load-balance to non-DeepSeek-native hosts (no prefix caching) and reasoning would burn the max_tokens budget on hidden thinking.

**Invariants:**
- Unconditional wrap: `preludeBuffer` engages on every request (single- and multi-binding alike). The old single-binding bypass was removed after the v0.58 SWE-bench bake-off traced 46/84 empty-patch failures to it — bypassed requests shipped a marker-only turn to Claude Code on upstream api_errors. TTFB cost is ~200B of buffered SSE released the moment the upstream's first byte arrives. `Committed()` is the retry gate for both cross-binding failover and single-binding in-place retry.
- Retry gated on `preludeBuf.Committed() == false`. Once committed (first upstream byte flushed through the chain), switching providers mid-stream would interleave two model outputs.
- Per-attempt `Prepare*` + translator construction. Translators are stateful; a retry must rebuild the chain from scratch.
- BYOK and inbound-client-credential requests skip failover entirely (`shouldFailover()` returns false) — those keys bind to one provider and would 401 elsewhere.
- Cancel/deadline classified as non-retryable: client disconnect or per-request budget elapse must not waste a second upstream call.
- After dispatch, `actPricing` is re-resolved against the WINNING binding via `catalog.PriceFor(finalProvider, decision.Model)` so debits and OTel `cost.actual_*` reflect the actually-served provider's per-1M rate (the catalog's `PrimaryPriceFor` would otherwise always return the primary's).

## Fast-tier dispatch (`fast_mode_models`)

An installation opts catalog models into the provider's paid fast tier via `PUT /admin/v1/fast-mode-models` (`auth.Installation.FastModeModels`, carried in ctx under `InstallationFastModeModelsContextKey`). [`fastModeForAttempt`](fastmode.go) decides **per attempt** — against the attempt's own ctx, model, and binding — whether `EmitOptions.FastMode` is set: the model must be listed, the `(provider, model)` binding must publish a `FastPrice` (first-party OpenAI → `service_tier:"priority"`, first-party Anthropic → `speed:"fast"` + beta; gateways never), and the resolved credential must not be a subscription OAuth token (Weave does not bill those turns). Raw passthrough is untouched.

**Routing never sees the fast rate.** Scorer, planner, and `Decision` pricing keep using `ProviderBinding.Price`. Only after dispatch does `servedPricing(finalProvider, model, fastServed)` swap in `catalog.FastPriceFor` for debits, cost headers, `cost.actual_*` / `cost.fast_mode` OTel attributes, and the policy-outcome `cost_usd`. Every attempt closure re-evaluates `fastServed` before it dispatches so a failover onto a gateway or subscription is billed at the tier it was actually sent on; an Anthropic-family binding hop that flips the tier goes through [`anthropicTierAttempt`](fastmode.go), which re-emits the body rather than reusing one carrying the other tier's `speed` field — the primary, baseline-failover, and subscription-failover dispatches all wrap their prepared body in it. A fast send that Anthropic refuses for lack of fast-mode allocation (429 naming "fast mode" tokens — `providers.IsAnthropicFastModeQuotaRejection`) is re-sent once at standard speed and billed at list, so an org that opts in without fast access keeps working; an ordinary 429 stays with the failover loop. The OpenAI-chat → Anthropic cross-format path does the same inline.

## Client-facing SSE keepalive

Clients time a stream out on received BYTES, not on semantic events: Claude Code
aborts a first-party stream (which includes any `ANTHROPIC_BASE_URL` override,
so all routed traffic) after **180s** of byte silence. A long reasoning phase
translates to zero client-facing frames, so a healthy turn reads as a dead
connection — prod 2026-08-24, three `gpt-5.6-luna` turns that each spent their
whole 64K output budget reasoning for 320-360s and were killed client-side at
exactly 180s while the router went on to complete them successfully.

None of the upstream watchdogs can cover this. They measure the *upstream* leg:
byte-idle keeps getting reset by Responses bookkeeping frames, and the
output-stall budget (240s) is longer than the client's 180s by design. The gap
is the *client* leg, so `ProxyMessages` wraps the client writer in
[`sse.KeepaliveWriter`](../sse/keepalive.go), which emits `anthropicPingFrame`
after `ROUTER_SSE_KEEPALIVE_INTERVAL_SECONDS` (default 15s, 0 disables) of
silence. This is what direct-to-Anthropic already gets for free — Anthropic's own
API emits `ping` for exactly this reason (see `anthropic_footer.go`, which
forwards them).

**Invariants:**
- **Innermost wrap.** The keepalive sits closest to the socket, below the
  feedback footer, so it observes the bytes that actually reach the client
  rather than what the proxy handed to the next translator.
- **Arms on first byte, never before.** For a `preludeBuffer`-wrapped chain the
  first byte through is the commit point, so a keepalive can never commit a
  response the router still wants to retry on another binding.
- **Record-boundary only.** A frame goes out only when the last write ended on a
  blank line, so a keepalive can never land inside a partially written event.
- **`Close()` before the handler returns** (deferred at the wrap site) and it
  blocks on any in-flight frame, so no keepalive outlives the response.

Only the Anthropic Messages surface is wrapped today — that is where the failure
was observed. `KeepaliveWriter` takes the frame as a parameter, so adding the
OpenAI/Gemini surfaces is a wiring change plus their own frame.

## Upstream response-header observation

Proxy attaches a `providers.UpstreamHeaderObserver` to the request context. Provider adapters call `providers.ObserveUpstreamHeaders` as soon as a response arrives, allowing subscription-limit tracking without coupling the adapters to proxy internals. **Don't add a provider that forgets to report the headers.**

## What NOT to do

- **Don't move provider-call logic into planner.** Planner must remain pure so EV math is provable. Anything network-touching goes in `proxy.Service`.
- **Don't add a handover path that doesn't time out.** `Summarizer` contract says implementations MUST respect the context deadline. On timeout/error the proxy keeps the full prior history unchanged — do NOT reintroduce a silent trim-to-last-N fallback (it lobotomized switched-to models; see the handover-fallback fix).
- **Don't cache streaming responses.** Streaming bypasses cache on purpose — captured bytes would be post-translation SSE frames, and lookup latency budget is hostile to first-token-time. If you think this should change, write a doc first.
