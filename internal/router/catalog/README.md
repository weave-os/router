# internal/router/catalog

The single source of truth for per-model data: tier, per-provider upstream IDs and per-provider pricing. Pure inner ring, no I/O. Adding a model is one struct literal in `Models` (see `.agents/skills/add-model-or-quirk`).

## Rows and bindings

- Every `Model` carries an ordered, non-empty `Providers` list. `ResolveBinding` picks the first binding whose provider is wired in the deploy; `AvailableBindings` returns them all in order for the failover loop (index 0 primary, the rest ordered fallbacks). This lets SOC-2 direct-provider rows append an OpenRouter fallback.
- Tiered rows with a registered provider are automatic routing targets. Strategy artifacts own selection membership: legacy cluster versions use their `model_registry.json`, and policy sidecars such as HMM intersect catalog targets with their own roster. The catalog controls pricing and dispatch for every strategy.
- `Tier` integer ordering is load-bearing (the planner compares `freshTier > pinTier`). `TierUnknown` rows are passthrough-only: never generic routing targets or policy candidates, but still priced so BYOK, direct-model and lingering session-pin requests bill at real cost instead of `PrimaryPriceFor` returning $0. `HMMTarget` offers an untiered row to an HMM sidecar without adding it to the generic cluster set.
- Retiring a model from routing means making its row untiered, not deleting it; the HMM rosters dropping a model is not enough, since a tiered row keeps the legacy strategy selecting it.
- `Source` is required on every row (an unset value fails the catalog tests). Open-source-only products read dispatch eligibility from it; an ID with no catalog row is refused by `PermittedBy`, because a boundary cannot vouch for a model it cannot classify. `Source` is an alias of the eligibility leaf type so model rows are only written here.
- `ModelID` constants exist so call sites that name a known model don't repeat raw strings; lookup itself stays string-keyed because the table is data.

### Model fields (zero value in parentheses)

| Field | Meaning |
| --- | --- |
| `ID` | Public slash-form or bare ID clients send. `ByID` retries after stripping an Anthropic (`-20251001`) or OpenAI (`-2024-08-06`) date suffix. |
| `CodexSubscription` | Served by native ChatGPT/Codex OAuth; OpenAI API models stay false even on the same binding. Unknown models fail closed. |
| `CodexSubscriptionFallback` | Permits trying a Codex subscription outside the native Codex roster; unsupported targets fall back to the model's API credential. |
| `ContextWindow` (`DefaultContextWindow`) | Total input+output budget. Never overstate it: the overflow pre-filter would route a request onto a model that hard-400s upstream with no failover. `ProviderBinding.ContextWindow` overrides it per binding. |
| `MaxOutputTokens` (unknown) | Documented output limit. |
| `ToolUseQuality` (`ToolUseUnknown`) | `ToolUseLow` flags models that hallucinate tool calls, emit malformed `tool_use` or loop; excluded from tool-bearing argmax, falling back to the unfiltered pool only if exclusion would empty it. |
| `AgenticUse` (`AgenticUnknown`) | Stricter than tool use: `AgenticLow` models emit valid tool calls but can't sustain a harness loop (e.g. grepping for a skill instead of invoking it). Excluded alongside `ToolUseLowSet` so a cost demotion lands on a harness-capable model. |
| `ImageInput` (`ImageInputUnknown`, image-capable) | `ImageInputUnsupported` on text-only models that 4xx on image parts. Unknown models default to image-capable so a passthrough or force-model target is never wrongly evicted. |
| `CustomToolInput` (`CustomToolInputUnknown`) | `CustomToolInputUnsupported` when the model cannot fill a projected Responses custom tool's `{"input": string}` wrapper. |
| `ThinkTagReasoning` | Streams inline `<think>…</think>` instead of `reasoning_content`; the Anthropic translator reroutes a leading block into thinking. |
| `AdvisorRank` (no advisor) | Orders Claude models for Anthropic's advisor tool, which rejects an advisor ranked below the request model; mirrors Claude Code's model catalog. |
| `RequiresSignedToolHistory` | Model 400s on function-call history lacking the `thoughtSignature` it issued. |
| `ForceModelOnly` | Serves only when named; never a newer stand-in for an older sibling in `LatestInFamily`. |

### Binding and pricing fields

- `UpstreamID` empty means "same as `Model.ID`"; non-empty is rewritten at proxy time.
- `Pricing` is USD per 1M tokens. `CacheReadMultiplier` and `CacheWriteMultiplier` are relative to the base input price; zero means `DefaultCacheReadMultiplier` (0.5: high enough not to treat unknown providers as free caching, low enough not to block switches) and `DefaultCacheWriteMultiplier` (the legacy production rate, kept until per-provider data is verified).
- `USInferenceGeoMultiplier` scales every token category when the provider reports US-only inference; zero means no geography premium. Only first-party Anthropic charges it, so synthesized and borrowed prices go through `WithoutInferenceGeoPremium`.
- `LongContext` applies alternate rates above a prompt-size threshold (`ForInputTokens`; the threshold is exclusive).
- `FastPrice` is the published rate on the provider's fast tier (OpenAI `service_tier=priority`, Anthropic `speed=fast`); zero means no fast tier and unset cache/geo multipliers inherit `Price`'s. Routing always scores on `Price`; only post-dispatch billing sees the fast rate. `SupportsFastMode` drives the dashboard's fast-mode toggle.
- `MaxImagesPerRequest` caps image parts per request; zero means unknown (no limit).
- `SubscriptionOverflowOnly` hides a binding from routing and failover; only an installation's exhausted-subscription OpenRouter lane dispatches it. `BindingFor` still returns it.
- Gateway bindings (`anthropic_gateway`, `openai_gateway`) carry indicative list prices, not invoiced ones, and trail the direct vendor binding. Anthropic-spec gateways rank above `openai_gateway` because thinking blocks and `cache_control` survive natively. Gateway-specific model IDs map via the key's `model_aliases`. Measured per-tenant gateway caps live in `gateway_context_window.go`; other gateways of the same type keep the catalog window.
- Configuration-declared (custom) bindings rank after catalog bindings, never duplicate a catalog provider, and inherit primary pricing without the geo premium: the custom endpoint bills on its own contract and list price is the only rate we have.

## Cost math

`EffectiveInputCost` is the single source of truth for the OTel emitter, the telemetry write path and the billing debit hook: fresh tokens at base rate, 5-minute cache writes at the binding's write multiplier, 1-hour writes (`UsageModifiers.CacheCreation1h`, a clamped subset) at `EffectiveCacheWrite1hMultiplier` (Anthropic-spec only; other families use the 5-minute rate), cache reads at the read multiplier, all scaled by the geo multiplier. Anthropic-spec `input_tokens` is fresh-only, while OpenAI/Gemini `prompt_tokens` include cached tokens and are subtracted. `PromptTokens` adds cached tokens back for Anthropic so the `LongContext` tier sees the full prompt. Output has no cache multipliers.

`CounterfactualInputCost` prices the savings baseline: a baseline that never switched models would have read the warm prefix from cache, so `warmPrefill` of the cache-creation tokens is priced as reads and the remaining writes keep the observed 5-minute/1-hour split.

`USDToMicros` rounds to BIGINT micros and collapses NaN, Inf and negatives to 0 (never write nonsense or debit a negative amount); `SignedUSDToMicros` keeps negatives for planner EV terms.

## Token scalars

`routerTokenScalarTable` maps served model -> baseline model -> `RouterTokenScalars`, each ratio `tokens(baseline) / tokens(served)`, so it multiplies served counts into the baseline's estimate. Above 1.0 means the baseline is more verbose, making the counterfactual dearer and reported savings larger. Inverting the direction breaks no test (reciprocal cells stay self-consistent), so check it by hand. An absent pair means unit scalars. Nothing in the router reads the table: the Weave dashboard's `scripts/sync_router_pricing.py` copies `token_scalars.go` verbatim, rewriting only the package clause, so the file must stay free of imports and functions and keyed by raw IDs.

## Model row notes

Why individual rows look the way they do. Re-confirm a served context window before raising it.

| Row | Note |
| --- | --- |
| `claude-haiku-4-5` | $1/$5 per the published table; $0.80/$4 is Haiku 3.5's rate. |
| `claude-haiku-5-5` | $0.10/$0.50 up to 100K prompt tokens, $0.50/$2.50 above; 0.1x cache reads in both tiers. Native 1M, no fast tier. Unlike Sonnet/Opus 5.5 it accepts disabled thinking and forced `tool_choice`. |
| `claude-sonnet-5` | $2/$10, 0.1x reads: the launch rate became standard and the planned $3/$15 increase was cancelled. 1M only behind the context-1m beta, so the catalog carries 200K. |
| `claude-sonnet-5-5` | $2/$10, 0.05x reads, no fast tier, native 1M. Thinking cannot be disabled (`between_tools` is the floor) and forced `tool_choice` is rejected. |
| Legacy Opus (4.1 and earlier, 4.5) | Untiered, priced passthrough: $15/$75 through 4.1, $5/$25 from 4.5. |
| `claude-opus-4-6`/`-4-7` | $5/$25; 1M via the context-1m beta, so the catalog reports 200K and the proxy expands to 1M when the beta header is present. The 1M expansion is `contextWindowForRequest` in `internal/proxy`. `claude-opus-4-8` is retired to priced passthrough. `claude-opus-5` serves 1M natively (no beta header) at the same $5/$25. |
| `claude-opus-5-5` | $4/$20, 0.05x reads, fast $8/$40. Native 1M, adaptive thinking always on, forced `tool_choice` rejected. |
| Fable 5 / 5.1 / 5.2 | Fable 5 retired to priced passthrough (BYOK pins, compaction summarizer). Safety classifiers can return `stop_reason: "refusal"` with HTTP 200 (see translate's `mapStopReason`). 5.1: $10/$50, 0.025x reads; 5.2: $8/$40, 0.025x reads, fast $16/$80. Not cluster roster members until they have quality labels. |
| `gpt-4o`, `gpt-4o-mini` | Priced for passthrough, not routing targets. |
| `gpt-5.4-nano` | 400K window (OpenRouter listing and direct OpenAI), not 1M; `gpt-5.4-mini` shares it. |
| `gpt-5.5` | Retired to priced passthrough (untiered, see above). |
| `gpt-5.6-*` | Sol/Terra/Luna family, GA 2026-07-09. `-pro` rows are Pi roster aliases that dispatch to the native OpenAI IDs. |
| `gpt-6-sol`/`gpt-6-luna` | Same >272K schedule (2x input, 1.5x output) and 2x fast schedule as Astra. |
| `grok-*` | Native xAI only. Standard (<200K) rate; long-context repricing not modelled yet. |
| Muse Spark / Ember / Wonder | Meta's hosted Model API at the standard (non-contributor) rate. `SourceUnknown`: no confirmed weight release, so open-source-only products must not dispatch them until the license is verified. Ember $0.10/$0.50 (reads $0.004), Wonder $4/$20 (reads $0.20). |
| `meta/muse-spark-open` | Open-weight 816B-A42B release; every host lists $1.25/$4.25, Together is the one supported. |
| `gemini-3.7-flash`/`3.8-flash` | Priced at the post-promo $1.50/$7.50, not the $0.75/$3.75 introductory rate that ends 2026-12-31, so the compiled price doesn't go stale. |
| OSS pool | Managed prod ships only the SOC-2 primary keys (Fireworks/Makora/Bedrock/OpenAI/Anthropic/Google) and drops the trailing OpenRouter binding; self-hosters with only an OpenRouter key route every OSS model through it. Fireworks-dedicated rows carry a trailing OpenRouter binding for deploys without a Fireworks key. Wafer bindings trail and resolve only when the bindings ahead of them are unwired; `wafer_anthropic` (Messages surface) trails `wafer` (OpenAI-compat). |
| `qwen/qwen3-235b-a22b-2507` | Bedrock primary after OpenRouter routed Qwen through a host returning non-SSE responses (silent Claude Code stalls). `ToolUseLow`: the non-thinking Instruct variant returned narrative text with no tool calls in production. |
| DeepSeek V4 / V4.1 | V4 serves 1,048,576 tokens natively (the old 131,072 filtered requests over ~128K). Makora retired V4 Flash; V4.1-Flash (natively multimodal, 1M) replaces it there, with Fireworks and OpenRouter as fallbacks. Bare `deepseek/deepseek-v4-pro` is the old 0423 build, untiered after Makora's EOL but bound for pins and `/force-model`; the dated `-0813` row is the release Artificial Analysis scores, so rosters target it. |
| `inclusionai/ling-3.0-flash` | DeepInfra primary; OpenRouter is a trailing self-hosted fallback and never the first binding. |
| MiMo V2.6 Flash / Pro | Flash: Makora's official `-RL` checkpoint first, DeepInfra (no suffix) as fallback. Pro: DeepInfra-first, top tier reserved for maximum-complexity Max requests; DeepInfra rejects more than 4 images. `mimo-v2.5` (base) was removed after sustained tool-calling failures; the pro variant is stable. |
| Kimi | k2.7 "Code": same rates as k2.6, ~30% fewer thinking tokens. k3 is a separate price class ($3/$15), not k2.7's successor; first multimodal Kimi. K3.1 Pro: post-trained K3 at $2/$10. OpenRouter is the same-price outage fallback. |
| `qwen/qwen3.6-35b-a3b` | `TierLow`: active-parameter budget and AA Coding Index put it below v4-flash. |
| MiniMax | m2.7 serves 204,800 despite "1M" marketing (requests over ~205K hard-400); Together leads (~4x Fireworks' throughput at the same price). m3.1-flash: 1,000,000 is the smallest served window, and disabling thinking is a 400. m3: the direct endpoint serves 1M but fallbacks can be limited to 512K, so the window stays conservative; it accepts images. |
| GLM | 5.1 ships the streaming tool-call fix 5 lacks; `emit_openai` injects `tool_stream` and disables thinking for it. 5.2 confirmed at 1,048,576; Together leads on throughput at the same price. 5.3 is text-only, Fireworks-only. 5.3-Flash is natively multimodal, thinking cannot be disabled (keep it out of `openRouterReasoningHint`), Makora leads, priced post-promo. 1,310,720 is Cloudflare-only; served max is 1,048,576. |
| `qwen/qwen3.7-plus` | Retired to priced passthrough, superseded by `qwen3.8-max`. |
| `qwen/qwen3.8-max` | Fireworks-only for SOC-2. Fireworks caps at 131,069 tokens ("model maximum context length: 131069"), not the documented 1M. |
