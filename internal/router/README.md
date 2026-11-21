# internal/router

Inner-ring value types for routing: `router.Request`, `router.Decision`, the
`router.Router` interface, strategies, routing models, model capabilities,
effort levels, policy pins and the atomic-classifier contract. No I/O; wire
formats are named independently of provider names so the package stays
dependency-free.

## Request semantics

- `ForceModel` is a valid explicit force-model pick. Decorators keep the
  underlying selection (no exploration). `ForceCluster` (`x-weave-force-cluster`)
  instead narrows the pool the policy may draw from; the sidecar is never told,
  so Go re-checks `Result.RankedFallback` after the sidecar answers
  (`policy.ApplyClusterArmOverridesRequireMatch`). It only applies to
  `hmm`/`hmm_embedding`; other strategies reject it with a typed error.
- `Overrides.Alpha` (`x-weave-routing-alpha`) is a raw quality weight applied
  uniformly across clusters, for bake-offs. `Overrides.QualityBias` is the
  user-facing 0..1 dial, mapped through a per-bundle calibration so the
  midrange moves the mix (0 = all-cheapest, 1 = best quality). Header and
  installation knobs are never merged; when both are set, `QualityBias` wins.
- `Overrides.ForceEffort` (`x-weave-effort` or a `:level` suffix) beats effort
  escalation; per-model caps still apply (xhigh clamps to max without
  `CapXhighEffort`).
- `TranslationRequirements` are hard compatibility constraints inferred at
  ingress, separate from the `HasTools`/`HasImages` quality hints. `NativeOnly`
  means the source wire family and endpoint must reach the upstream unchanged.
  An empty compatible pool must never become an incompatible fallback.
- `Unscorable` marks a continuation without user-authored text; policies select
  from the eligible roster without classifying injected command wrappers.
- `HasImages` drops text-only models and evicts a text-only session pin.
- `PolicyTurnContext` is content-free persisted turn state; nil keeps older
  policy clients wire-compatible.
- `ClassifierPrediction` is only set after release-bound durable
  classification; `PreviousPolicyGroup` is used only when a continuation has no
  user text and escalation is active; `SlidingThresholdPinGroup` lets selection
  step up from the pin's group on an authoritative turn.
- `ReasoningConfigurationSHA256` and `ToolConfigurationSHA256` are privacy-safe
  hashes. The request still carries raw text (`PromptText`,
  `ConversationMessages`), so it is not safe to log or persist as a whole.
- `TrainingAllowed` is false unless the org is eligible for policy learning;
  serving continues either way. `ShadowMode` decisions are never served traffic
  nor online-learning input.

### Provider and model gating

| Field | Meaning |
| --- | --- |
| `EnabledProviders` | Per-request provider gate; nil is unrestricted. |
| `CustomBindings` | A key's `model_aliases`: rank after catalog bindings, unless `GatewayProviders` is set. |
| `GatewayProviders` | Non-empty means gateway-exclusive routing: only aliased models are routable. |
| `ExcludedModels` | Hard union of installation `excluded_models`, the desugared allowlist, the product boundary and request-time safety filters. Emptying the pool returns `ErrNoEligibleProvider`. Also rejects `/force-model`. |
| `AllowedModels` | The org allowlist, kept so errors name it (`ErrAllowlistEmptiesPool`, which still matches `ErrNoEligibleProvider`); enforcement is via `ExcludedModels`. |
| `SafetyExcludedModels` | Only physical constraints (context overflow, unsigned Gemini history). The usage-bypass gate consults this, not policy exclusions. |
| `ContextWindowExcludedModels` | Excluded only by the byte-based context estimate; a `/force-model` pin on one is still dispatched so the provider's exact count decides. |
| `ImageLimitExcludedModels`, `UnsignedHistoryExcludedModels` | Subsets excluded for image caps below `ImageCount` and for unsigned tool-call history. |
| `OverflowAdmittedModels` | When the estimate rules out everything, the widest-window models are sent anyway so a real overflow reaches the client natively; later checks must not re-exclude them. |
| `AutomaticExcludedModels` | Withdrawn from automatic selection only: an explicit pin still serves, and routers ignore it for a turn when it would empty the pool. |
| `ProductEligibility` | The plan boundary (not org-editable); restricted boundaries are also desugared into `ExcludedModels`. |
| `PreferredModels` | Rank-decaying score bonus that wins close calls only; entries outside the pool are ignored. |
| `ClusterArmOverrides` | Per-key HMM cluster allowlist (label to ordered model IDs); absent clusters keep the artifact default. |

## Decision semantics

- `Decision.Model` stays a bare catalog ID; `Effort` is applied by the emit
  path. `Decision.ServedIdentity` is `model:effort` (bare without effort), so an
  effort-only change counts as a model switch (it invalidates thinking
  signatures and the prompt cache). Pins written before effort existed compare
  as a switch once; that is conservative, not unsafe.
- `Metadata` is nil for non-content-aware routers.
- Fresh-only fields are never persisted into session pins, or a replayed pin
  re-emits stale data: `ClassifierMargin`, `SidecarTimings`, `SidecarStats`.
  In `SidecarTimings` and `SidecarServingStats`, nil means unmeasured and a
  present 0 is a real measurement.
- `ClusterIDs` is sorted ascending; index 0 is not necessarily the closest.
- `RescueModels` orders in-turn rescue candidates (ranked group fallback with
  per-key allowlists); `RosterFailover` restricts rescue to it.
  `CandidateModels` is the eligible pool in catalog order with no preference.
- `PolicyGroup` is persisted in pins to tell a within-cluster reroute from a
  cluster change. `SelectedRosterArmID` keeps the effort suffix and is the only
  form that keys into `ArmScores`.
- `AuthoritativePerTurnSelection`: downstream may retry providers but must not
  replace the model this turn.
- `CandidateScores`, `CandidateProviders` (resolved per request, so correct
  under BYOK) and `Propensity` (1.0 for argmax) feed off-policy analysis and do
  not affect routing. `PairedModel` is the runner-up frozen into the pin.
- `EffectiveKnobsHash` isolates the response cache by canonical knobs.

## Strategies and routing models

- `Strategy` zero value means the deployment default; `StrategyFromContext`
  returns `StrategyHMMEmbedding` when nothing (or "") was stashed, and
  `WithStrategy` with "" leaves ctx unchanged.
- `StrategyHMM` is opt-in only, never a silent default. `StrategyRL` is the
  retired built-in RL wire name: reserved and fail-closed, never served.
  `StrategyHMMBeta`/`StrategyHMMQwen` are explicit session overrides
  (`IsBetaStrategy`). `StrategyLLMClassifier` requires the release-bound thread
  handshake ([LLM classifier context](../../docs/LLM_CLASSIFIER_CONTEXT.md)).
- `RoutingModel` IDs select an automatic router, not an upstream model.
  Variants with `FallsBackToPrimary` serve `PrimaryRoutingStrategy` when the
  variant is absent, so releases can add and drop experiments.
  `WithRoutingModel` records an explicit pick that legacy preferences cannot
  override.
- `ErrRoutingUnavailable` maps to 503; `ErrNoEligibleProvider` to 4xx.

## Policy pins

`PolicyPin` is `<policy_artifact_sha256>@<roster_sha256>`, both 64 hex
characters, normalised to lowercase. An unauthorized request is recorded with
a zero pin so replay analysis sees it was asked for without it affecting the
response. An honoured pin whose artifact or roster is not loaded fails closed
with `ErrPolicyPinUnavailable` (503 `PolicyPinUnavailableReason`) instead of
falling back to the current policy.

## Atomic classifier contract

See [LLM classifier context](../../docs/LLM_CLASSIFIER_CONTEXT.md). In this
package: `ClassifierFeatures` are whole-prefix counts (unknown tool outcomes
are not successes); `ClassifierContext.TurnDigest` is the full API-call prefix
digest (name kept for persisted compatibility); provenance indices are
validated but not rendered into the prompt; historical predictions are joined
only within the same tenant, thread and release and are never defaulted;
`ClassifierPrediction` stores facts, never prompt content; a
`ClassifierSessionStore` callback commits before provider dispatch and rolls
back on error; `ErrClassifierUnavailable` never authorizes another classifier;
`ErrClassifierInputTooLong` rejects instead of truncating.

`ClientBudget` is recomputed at ingress and never inherited from a pin; harness
defaults are not effective limits.

## Model capabilities

`router.Lookup` falls back from a dated ID (`-20251001`, `-2024-08-06`) to its
base; unknown models get the zero `ModelSpec`, and adapters strip every
capability-gated field when `Supports` is false. Reasoning levels are ordered
cheapest first for deterministic clamping. `model_test.go` pins the quirks:

| Capability | Upstream behavior |
| --- | --- |
| `CapAdaptiveThinking` | Anthropic 4.6+ accept only `thinking.type=adaptive`; Haiku 4.5 400s on it. |
| `CapExtendedContext` | 1M context via the `context-1m-2025-08-07` beta (native on fable-5). |
| `CapXhighEffort` | Effort `xhigh` (Opus 4.7+, Sonnet 5.5, GPT-5.6+, Grok 4.6+). Sonnet 4.6/5 top out at `max`, so emit clamps rather than 400s. |
| `CapServerSideFallback` | Top-level `fallbacks` field (models whose safety classifier can return `refusal`); other Claude models 400. |
| `CapAutoToolChoiceOnly` | Forced `tool_choice` 400s (Fable 5.1+, Opus/Sonnet 5.5); emit downgrades to auto. |
| `CapSamplingParamsUnsupported` | `temperature`/`top_p`/`top_k` rejected (Fable 5.1+). |
| `CapAssistantPrefill` | A trailing assistant turn is accepted; everyone else, unknown models included, has it dropped. |

`AlwaysOn` reasoning maps a disable request to the floor level (Muse, MiniMax
M3.1 Flash, GPT-6 Astra); GPT-6 Sol/Luna accept `none`. GPT-5.6 keeps `max` off
its menu because only the Codex backend accepts it. Gemini's OpenAI-compatible
endpoint ignores reasoning and thinking fields.

## Continuity helpers

`internal/router/continuity` holds the router-layer session-continuity rules
that the serving layer applies to non-HMM strategy decisions (`rl`,
`decision_model`, `llm_classifier`, HTTP policy adapters). They read only
`router.SessionView` (its `Pin` is the generic session-pin view) plus turn
facts and flag values, and never perform I/O:

- `ToolResultSticky`: keep the eligible pin on a tool-result turn.
- `CanReusePin`: keep the eligible pin when the planner is disabled.
- `Plan`: run the pure `planner.Decide` against the pin view; the EV planner
  still governs both switch directions.

Strategies that set `AuthoritativePerTurnSelection` bypass both pin helpers.
HMM-only pin state stays behind `internal/router/hmm` accessors.

## Package layers

| Package | Layer | Owner | Role |
| --- | --- | --- | --- |
| `switchpolicy` | Router | `continuity` (cache economics) | Versioned switch-policy rules replayed by the agreement shadow. |
| `bandswap` | Router | `continuity` (cache economics) | Classifier that picks the served band when the planner stays on a pin. |
| `taskarm` | Router | `policy` (classifier inputs) | Task-arm labels and assignment read by the serving-layer experiment. |
| `taskdomain` | Router | `policy` (classifier inputs) | Task-domain contracts consumed by HMM selection and policy inference. |

The serving layer (`internal/proxy`) calls these packages; none of them
import serving-layer code.
