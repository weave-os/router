# HMM deterministic selection in Go

Architecture of the HMM strategy's deterministic layer — roster ownership and
within-cluster arm selection — which lives in the Go router. The sidecar keeps
ML inference (complexity classification) only.

The staged rollout (`ROUTER_HMM_SELECTION_SHADOW` → `ROUTER_HMM_GO_SELECTION`)
is complete and both flags are gone: Go selection is the only path. Managed
serving uses `policy_router_v4`: the sidecar returns classifier facts without a
selected arm, and Go selects from the admitted policy.

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
| Complexity classification (ML) | Private Python sidecar (`policy_router_v4` contract) |
| Ranked cluster fallback (per-group probability, roster arms, eligible arms) | Go selector, using classifier probabilities and the admitted roster |

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
scores used by effort hysteresis. Distribution applies model exclusions and
retains a model when any policy-allowed provider binding remains; its cost uses
the first surviving binding. WPI is a normalized evaluation-workload axis,
not a billing rate; preview dollars come from catalog serving prices.

`mode_policies` and `membership_by_harness` in older roster files are historical
metadata. Current v4 requests have no router-mode input, and Go selection does
not use per-harness membership. New policy authors should omit both fields;
`arms_by_harness` remains the serving order override.

Selection is **fail-closed**. A v4 classifier response with an incomplete class
contract or no eligible arm returns an error; Go does not accept a sidecar-picked
model or provider as a fallback.

See [CONFIGURATION.md](CONFIGURATION.md) for the full variable reference.

## Deployment and rollback

Managed rollback pins the router image, classifier image, and policy selection
set together through the serving registry. A v4 worker must use a compatible
classifier package and admitted policy; replacing just one image is unsafe.

For legacy/self-hosted configuration, roll back roster content by republishing the
previous roster artifact and redeploying; an invalid roster fails boot. Managed
serving instead promotes the prior immutable policy reference on the current code
binding, preserving existing conversations without rebuilding or creating a
customer-specific revision.

## Still to remove

- The silent-drop mapping contract of `hmm.DeployedModelsForRosterIDs`, once the
  admin roster view reads the declarative roster instead.
