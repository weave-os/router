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
| Roster contents (`hmm_router_cluster_roster_v6` JSON) | Declarative data, loaded and fail-loud validated by Go at boot (`internal/router/hmm/rosterdata`) |
| Roster↔catalog validation | Go: `hmm.ValidateRosterIDs` (`internal/router/hmm/validate.go`) plus the `validate-roster` CLI for CI |
| Within-cluster deterministic arm selection (harness-specific order, rank-1 pick, ranked cluster-fallback walk) | Go: `internal/router/hmm/selection` |
| Complexity classification (ML) | Python sidecar (`policy_router_v3` contract) |
| Ranked cluster fallback (per-group probability, roster arms, eligible arms) | Python sidecar — the only selection input the router accepts |

## Configuration

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

Roster content is still rolled back on its own: republish the previous roster
artifact and redeploy — an invalid roster fails boot rather than serving.

## Still to remove

- The silent-drop mapping contract of `hmm.DeployedModelsForRosterIDs`, once the
  admin roster view reads the declarative roster instead.
