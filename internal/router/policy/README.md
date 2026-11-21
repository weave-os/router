# internal/router/policy

Shared, I/O-free building blocks for routers that delegate a decision to an
external policy sidecar, plus the checked-in inference-purpose registry and
plan resolver. The sidecar returns classifier facts; candidate filtering,
eligibility, arm selection, pins and fallbacks stay in Go (see
[Go HMM selection](../../../docs/HMM_GO_SELECTION.md),
[the policy router harness](../../../docs/POLICY_ROUTER_HARNESS.md) and
[the inference boundary](../../../docs/INFERENCE_BOUNDARY.md)).

## Sidecar contract versions

| Constant | Contract |
| --- | --- |
| `SchemaVersionV1` | First stable policy-sidecar wire contract. |
| `SchemaVersionV4` | Classification facts only: candidate identities, roster data, group clamps and selection fields are forbidden. |
| `SchemaVersionV5` | Release-addressed classifier service; every result must name the admitted release, target selection, variant, taxonomy and variant artifact or it fails closed. |

`ArmIdentity`/`MakeArmID` mirror the Python temporal-Q `make_arm_id` so arm IDs
match across languages. `ClassifierTimeoutHeader` carries the router's
remaining classify budget in whole milliseconds (capped at
`MaxClassifierTimeoutMilliseconds`) so the classifier stops work the router
will no longer wait for.

## Harness vocabulary

The router decides the harness value; sidecars use it verbatim and must degrade
a value outside their vocabulary to the pooled default, so adding a harness
never changes what an older sidecar serves. The roster policy accepts
`*`, `claude_code`, `codex`, `pi` and `opencode` as per-harness keys; `cursor`,
`api` and unknown clients stay pooled. `SelectionHarnessForClientApp`
canonicalizes OpenCode aliases but keeps the legacy pi-subagent identity until
its policy rollout is evaluated separately.

## Candidate resolution

`Resolver.Resolve` applies request filters, provider policy, capability soft
filters and roster mapping in a deterministic order. Points that are easy to
break:

- An empty roster ID from `RosterMapper` excludes the model on purpose.
- `ManagedProviderPolicy` never offers OpenRouter.
- The org's positive allowlist is enforced directly (`ExclusionNotAllowlisted`),
  not only through the exclusion desugaring, so strategy-specific candidates
  cannot bypass it.
- A product-ineligible model is reported as `ExclusionProductIneligible` ahead
  of the generic exclusion set it is also desugared into; otherwise a product
  refusal would look like the org's own `excluded_models` entry. It is hard: no
  funding path or allowlist widens it.
- `ExclusionAutomaticDisabled` is soft: a deployment-wide disable withdraws a
  model from automatic choice but is skipped when it would empty the pool, and
  an explicit user pin still reaches the model.
- A model the proxy admitted on total overflow is never dropped for context
  size here; the provider's exact count decides, so a real overflow reaches the
  client as the native prompt-too-long error it compacts on.
- Under gateway-only tenancy a model is served only by gateways whose key
  aliases it; a catalog-declared gateway binding is not enough.
- `BindingForSelection` resolves an arm ID first and falls back to the legacy
  roster ID. Only recognized effort suffixes (`model:xhigh`) are split off and
  set `Binding.Effort`; other `:` suffixes stay part of the model ID.
- An empty candidate set maps to a typed error (`ErrNoRoutableModels` and its
  gateway, credential and image-limit narrowings) so the caller names the
  configuration to change. `ErrContextWindowExceeded` applies only when a model
  was dropped for size, and is answered with the client's native
  prompt-too-long error.

## Cluster overrides and forced clusters

`ApplyClusterArmOverrides` re-selects the served arm under per-key cluster
allowlists by walking the ranked fallback and intersecting each group's
override with the request's eligible candidates (the full resolved set, since
an override may add models the artifact never placed in that cluster), so
global exclusions still win. It fails open: with no ranked fallback, or when
the overrides empty every group, the sidecar's pick stands. A per-key allowlist
is control-plane config already validated against the roster.

`ApplyClusterArmOverridesRequireMatch` is the fail-closed variant for a
caller-forced cluster: an unknown label, an emptied group or a missing ranked
fallback is `ErrForcedClusterUnservable`. `SidecarRouter` returns that error
unwrapped so the dispatch classifier maps it to a 400 rather than a sidecar
outage, and annotates the reason even when the forced cluster was already
selected.

The presence of `ranked_fallback` in a `/route` response is the proof that the
sidecar supports overrides; boot-time capabilities can be stale after an
upgrade. When the router reselects a different arm it drops the sidecar's
`DisplayMarker`, which named the pre-override pick, so the marker renders the
served binding. Selection uses the resolved arm ID because on arm-enumerating
resolvers a roster ID can be shared across providers. Shadow decisions are
never learning events.

`RescueModelOrder` lists rescue models in ranked-fallback order using each
group's effective arms, so a model a key excluded never re-enters through a
rescue.

## Inference registry and plans

`PlanResolver` is the only constructor of a populated `ResolvedPlan`, whose
slice accessors return copies. Invalid explicit inputs never fall through to
another target. Every target-selecting policy declares the core constraints
the candidate resolver already applies, and `verifyHardConstraints` re-checks
them against the bindings a plan emits, so declared constraints describe the
plan. `ResolveRouted` authorizes exactly the binding walk the turn loop
computed and fails closed on unregistered purposes, policies that neither
route nor hard-pin, an empty model or walk, or a missing or undeclared origin.

`UtilityTarget` serves `UtilityHardPinDefaultModel` when the request can,
otherwise the lowest-tier, cheapest servable tiered model. Models weak at tool
use or agentic loops rank last because sub-agent turns run a tool loop, and
text-only models are never utility targets because utility turns may carry
images. `SavingsComparisonModel` is the dashboard savings baseline: router-picked
API turns are repriced at its catalog price, matching hosted reporting.
