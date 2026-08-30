# HMM deterministic selection in Go

Architecture of the HMM strategy's deterministic layer — roster ownership and
within-cluster arm selection — which lives in the Go router. The sidecar keeps
ML inference (complexity classification) only.

The staged rollout (`ROUTER_HMM_SELECTION_SHADOW` → `ROUTER_HMM_GO_SELECTION`)
is complete and both flags are gone: Go selection is the only path.

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
| Complexity classification (ML) | Python sidecar (`policy_router_v1` contract) |
| Pin-sticky eligibility signal | Typed contract field `pin_sticky_override_eligible` (successor of the `[pin_sticky_override_eligible]` reason-string sentinel) |

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
does not); selection fails open to the sidecar's pick when no ranked group holds
an eligible arm. Because the Go pick is deterministic, pin-sticky eligibility is
neutralized on any Go pick (typed field forced `false`, legacy sentinel
stripped) so a session pin cannot veto it.

See [CONFIGURATION.md](CONFIGURATION.md) for the full variable reference.

## Rollback

There is no runtime flag to flip. Rolling selection back to the sidecar means
reverting the router image pin (in WorkWeave) to a build that still carries
`ROUTER_HMM_GO_SELECTION`, and setting that flag to `false`.

Roster content is still rolled back on its own: republish the previous roster
artifact and redeploy — an invalid roster fails boot rather than serving.

No change here touches the sidecar contract: sidecars that do not emit the
optional typed fields (`predicted_label`, `class_probabilities`,
`pin_sticky_override_eligible`) keep working via the legacy reason-string
sentinel path.

## Still to remove

- The reason-string sentinel match in `internal/proxy`
  (`hmmArmSelectorUnavailableSentinel`), once every sidecar emits the typed
  `pin_sticky_override_eligible` field.
- The silent-drop mapping contract of `hmm.DeployedModelsForRosterIDs`, once the
  admin roster view reads the declarative roster instead.
