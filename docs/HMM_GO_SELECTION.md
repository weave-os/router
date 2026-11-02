# HMM deterministic selection in Go

Architecture of the HMM strategy's deterministic layer — roster ownership and
within-cluster arm selection — which lives in the Go router. The sidecar keeps
ML inference (complexity classification) only.

The staged rollout (`ROUTER_HMM_SELECTION_SHADOW` → `ROUTER_HMM_GO_SELECTION`)
is complete and both flags are gone: Go selection is the only path. The sidecar
contract followed: `policy_router_v3` carries a classification and no selected
arm at all.

## Why

The roster↔catalog binding (`internal/router/hmm/mapping.go`,
`internal/router/hmm/roster.go`) historically dropped unknown roster IDs
silently. One inert roster arm caused a production incident: 19.8% of
balanced-cluster turns fell through to maximum-tier arms ($821 of $1,022
cluster spend). Moving the roster to declarative data that Go validates
fail-loud, and the deterministic selection walk into Go, removes that failure
class and shrinks the sidecar's authority to what only it can do: ML inference.

## Target state

| Concern | Owner |
|---|---|
| Roster contents (`hmm_router_cluster_roster_v7` JSON) | Declarative data, loaded and fail-loud validated by Go at boot (`internal/router/hmm/rosterdata`) |
| Roster↔catalog validation | Go: `hmm.ValidateRosterIDs` (`internal/router/hmm/validate.go`) plus the `validate-roster` CLI for CI |
| Within-cluster deterministic arm selection (harness policy, preference-adjusted WII/WPI ranking, ranked cluster-fallback walk) | Go: `internal/router/hmm/selection` |
| Complexity classification (ML) | Python sidecar (`policy_router_v3` contract) |
| Ranked cluster fallback (per-group probability, roster arms, eligible arms) | Python sidecar — the only selection input the router accepts |

## Configuration

Managed gateway admission loads the conversation's immutable selection from the
serving registry, validates it in Go, and supplies a request-bound roster. Selection
v3 shares one code candidate per internal/stable target and assigns default/customer
rosters as configuration. Existing conversations retain their admitted roster; new
conversations receive promotions or roster rollback. See
[SERVING_CONTROL.md](SERVING_CONTROL.md#shared-code-cohorts-and-roster-configuration-selection-v3)
for schema, retention and reader-rollout requirements.

The following boot-file configuration describes the legacy/self-hosted path.

`ROUTER_HMM_ROSTER_PATH` is the only lever, and it is **required** whenever
`ROUTER_HMM_SIDECAR_URL` is set: the roster is loaded and validated against the
model catalog at boot (any invalid arm fails boot) and then serves every HMM /
`hmm_embedding` decision. Booting an HMM sidecar without a roster is a
misconfiguration and panics at startup rather than silently handing selection
back to the sidecar.

Per decision: Go's deterministic pick serves (reason suffixed `:go_selection`)
and the sidecar's classifier label/confidence is kept. Explicit force-cluster
and per-key cluster overrides take precedence when they actually constrain the
pick (a ranked-group pass-through with no configured list for the winning group
does not).

The organization quality/price dial is applied only after the classifier band,
candidate set, sidecar allowlist, and harness membership are fixed. Within that
eligible pool, v7 rosters score each arm as `alpha*WII - (1-alpha)*WPI`.
The user-facing neutral value (`0.70`) maps to the independently tuned alpha for
each band; an unset or exactly neutral preference uses the generated fixed order
and score map byte-for-byte. Manual pins and harness vendor priority remain
stronger tiers than the dynamic score, and original roster position breaks ties.

The same Go scorer produces `/v1/router/routing-distribution` and the per-arm
scores used by effort hysteresis. WPI is a normalized evaluation-workload axis,
not a billing rate; preview dollars always come from catalog serving prices.

Selection is **fail-closed**. A `/route` response with no ranked fallback, a
ranked fallback holding no eligible arm, or a `policy_router_v1`/`v2` schema is
rejected as a sidecar outage and the turn returns HTTP 503. There is no
sidecar-picked arm to fall back to: `selected_roster_id`, `selected_provider`
and `model` are null on the wire.

See [CONFIGURATION.md](CONFIGURATION.md) for the full variable reference.

## Deployment and rollback

`policy_router_v3` is a hard break with no compatibility window: a v3 router
rejects a v1/v2 sidecar and a v3 sidecar names no arm for a v1/v2 router. Router
and sidecar deploy together, and roll back together — revert both image pins.
There is no runtime flag to flip.

For legacy/self-hosted configuration, roll back roster content by republishing the
previous roster artifact and redeploying; an invalid roster fails boot. Managed
serving instead promotes the prior immutable policy reference on the current code
binding, preserving existing conversations without rebuilding or creating a
customer-specific revision.

## Still to remove

- The silent-drop mapping contract of `hmm.DeployedModelsForRosterIDs`, once the
  admin roster view reads the declarative roster instead.

## Offline model-agreement candidate

`internal/router/switchpolicy` is an experimental replacement for the competing
ordinary upgrade/downgrade gates. It is consumed by `cmd/replay-switch-policy`
and the opt-in internal shadow comparison described below. Neither consumer
controls dispatch. Existing serving gates and organization overrides remain
unchanged; authoritative adoption and override migration are separate work.

The candidate uses one minimum qualifying probability and consecutive votes for
the exact provider, model, and quality group. Higher-group changes use the
upgrade requirement; lower-group and lateral changes use the downgrade
requirement. The requirements count qualifying observations, not independent
statistical evidence. A different destination, disqualifying probability,
ineligible proposal, or incumbent confirmation resets the run. Changing the
configuration also resets pending votes. Ordinary same-model labels do not
rewrite the incumbent's accepted group or change reasoning effort.

Cache warmth and history trimming cannot authorize a switch. Explicit manual,
incumbent-ineligible, recovery, and active-quality-floor reasons are the only
overrides. The caller must establish compatibility, funding, and recovery
eligibility before supplying an override; a floor cannot lower an already higher
quality group. This evaluator does not decide when a stream may be retried.

Every decision carries its base state revision. `Commit` checks against a latest
in-memory value for offline replay and sequence tests. A future database adapter
must enforce the comparison atomically in its update; reading a row and then
calling this function does not provide concurrency protection.

The replay command accepts content-free JSONL, for example:

```json
{"id":"synthetic-1","session_key":"synthetic-thread","role":"default","timestamp":"2026-01-01T00:00:00Z","proposal":{"target":{"provider":"openai","model":"gpt-6.1-sol"},"group":"high"},"confidence":0.9,"eligible":true,"observed":{"provider":"openai","model":"gpt-6.1-sol"}}
```

```sh
go run ./cmd/replay-switch-policy -input /private/proposals.jsonl \
  -upgrade-votes 3 -downgrade-votes 5 -minimum-confidence 0.5 \
  -trace /private/new-replay-trace.jsonl
```

Three-up/five-down is a comparison default, not an approved fleet calibration.
The input must supply canonical quality groups, eligible candidate identities,
and known overrides. Unknown fields are rejected to catch accidental inclusion
of conversation content. Observations must be unique and ordered by timestamp
within each full pin key and role. Traces are private files created without
overwriting; the summary includes the input hash and coverage counts.

Every pin bucket starts with empty state: no incumbent, pending votes, or prior
revision is loaded. Its first eligible proposal becomes the initial model
immediately, without waiting for the upgrade/downgrade vote count. Continuation
analysis therefore requires history from pin creation. A mid-session suffix is
treated as a new bucket, not as a continuation of the existing model; its choices
and transition counts can differ. An explicit starting-state input is not
supported, and every emitted summary discloses this cold-start boundary.

The command serializes state and holds proposals and request timing fixed. It
does not generate alternative model outputs, reclassify future requests, replay
distributed database scheduling, or estimate quality, billing, or latency.
Prepare the proposal stream from classifier/ranker evidence and explicitly
account for excluded or unsupported branches; do not silently remove individual
requests from a vote sequence.

## Internal agreement shadow

The organization-only integer `agreement_shadow_epoch` defaults to 0 (off). A
fresh positive value starts a run; use a new value when resuming after disabling
and never reuse an old epoch. The epoch is part of the durable state key, so old
votes and late callbacks cannot enter a newly activated run. The setting
is honored only for admitted `prod/weave-internal` requests with a complete
release/binding/state namespace. Stable and self-hosted requests ignore it. It
compares fixed 1-up/5-down and 2-up/5-down policies, both at confidence 0.50,
without changing any serving decision or making a classifier/provider call.
Merging or deploying this code does not enable the experiment.

The proposal is reconstructed from the returned classifier's raw group and
effective arm order, intersected with the request's eligible roster IDs. Canonical
effort suffixes are stripped for membership, and the actual request-resolved
provider is used. Serving sliding thresholds cannot shape the shadow proposal.
Missing, overridden, or inconsistent selection evidence produces a gap instead
of a guessed candidate. No prompt or response content enters shadow state or logs.

Each full admission-scoped pin key and role has a separate row in
`router.agreement_shadow_states`; the 1-up and 2-up histories are distinct values
committed atomically together with a compare-and-swap revision. They never read
or write each other's votes, and the table is separate from live session pins.
An initial segment starts from the eligible live incumbent when its quality
group is known; otherwise it cold-starts and records that initialization. A
changed classifier/selection identity starts a new segment. A hypothetical
incumbent absent from the current eligible targets is replaced immediately with
the typed incumbent-ineligible exception.

Processing runs asynchronously after terminal response telemetry, which gives
the actual served model and failover outcome. Transport failures/rescues, manual/compaction
interventions, unavailable classification evidence, and unsupported paths break
the segment. Request-start timestamps detect out-of-order completions across
workers: these invalidate the old segment rather than manufacturing consecutive
votes. Such overlaps can reduce comparison coverage substantially. Shadow-store
errors log diagnostics and never change a response or prevent baseline telemetry
from being attempted. Idle state is removed by the hourly sweep after seven days.

Completed HTTP 200 turns retain their classifier evidence even when telemetry
labels their output `max_tokens`, refusal, or invalid tool arguments. Those quality
labels remain on the comparison log; they are not transport failures. Successful
force/unforce commands record an unsupported-turn boundary for the affected
thread's pin roles after pin persistence and before acknowledgement, under a
shared two-second best-effort budget. This prevents an immediate tool-result
continuation from joining pre-command votes. Shadow errors do not reject the
command, and failed or rejected commands do not record a successful boundary.

Activation is a separate operational action: apply the table migration before
deploying the binary, then explicitly select a fresh positive epoch for an internal installation
through the existing admin flag controls. No operational change is performed by
this PR. Monitor `router.agreement_shadow` and `router.agreement_shadow.gap` logs,
joined by request ID to ordinary telemetry. Comparison rows include both policy
parameters, proposed/selected identities, votes, direction, reason, simulated
switch, source policy/release identities, segment and initialization metadata.

Before comparing switch counts, reconcile opted-in baseline requests with
comparison/gap/error events. Exclude invalidated segments and any stream with
dropped observations or shadow-update errors; do not silently splice the remaining
rows. Report coverage by turn type, installation and overlap, model usage, and
qualified upgrade holds. Missing or intervention segments are not evidence that
recovery or forcing has been counterfactually validated. The shadow holds live
classifier evidence and tool history fixed, so it still does not measure alternative
answer quality, cache behavior, billing, or the result of a fleet-wide rollout.
