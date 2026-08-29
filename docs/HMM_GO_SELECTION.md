# HMM deterministic selection in Go

Target architecture and staged rollout for moving the HMM strategy's
deterministic layer — roster ownership and within-cluster arm selection — from
the Python policy sidecar into the Go router. The sidecar keeps ML inference
(complexity classification) only.

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

## Flag ladder

Each stage is independently revertible and defaults to the previous stage's
behavior. Served routing is unchanged until the final flag is enabled.

| Stage | Control | Default | Effect when enabled |
|---|---|---|---|
| 1. Fail-loud observation | *(always on, log-only)* | — | Roster refresh WARN-logs arms that fail catalog validation, once per distinct roster snapshot. `validate-roster` CLI exits non-zero on any invalid arm for explicit CI use. Serving unchanged. |
| 2. Declarative roster | `ROUTER_HMM_ROSTER_PATH` | *(unset — inert)* | Loads and validates the roster JSON at boot; invalid arms fail boot. The loaded roster only feeds stages 3 and 4; it does not serve on its own. |
| 3. Selection shadow | `ROUTER_HMM_SELECTION_SHADOW` | `false` | Recomputes the deterministic pick in Go after each sidecar decision and logs agree/diverge. Log-only. Requires stage 2. |
| 4. Go selection | `ROUTER_HMM_GO_SELECTION` | `false` | Go's deterministic pick serves (reason suffixed `:go_selection`); the sidecar's classifier label/confidence is kept. Explicit force-cluster and per-key cluster overrides take precedence when they actually constrain the pick (a ranked-group pass-through with no configured list for the winning group does not); fails open to the sidecar's pick when no ranked group holds an eligible arm. Because the Go pick is deterministic, pin-sticky eligibility is neutralized on any Go pick (typed field forced `false`, legacy sentinel stripped) so a session pin cannot veto it. Requires stage 2. |

See [CONFIGURATION.md](CONFIGURATION.md) for the full variable reference.

## Rollout

1. Observe stage-1 warnings in production; fix any invalid roster arms.
2. Ship the generated roster file and set `ROUTER_HMM_ROSTER_PATH`. Boot
   failure on an invalid roster is the intended fail-loud gate.
3. Enable `ROUTER_HMM_SELECTION_SHADOW` and watch the shadow-comparison logs
   until agreement is total (or every divergence is understood and accepted).
4. Enable `ROUTER_HMM_GO_SELECTION`. Selection is now Go-authoritative.
5. Once stable, remove the sidecar's deterministic selection layer and the
   legacy code paths marked deprecated in the router (the reason-string
   sentinel match in `internal/proxy` and the silent-drop mapping contract of
   `hmm.DeployedModelsForRosterIDs`).

## Rollback

Every stage rolls back independently by flipping its flag and restarting:

- `ROUTER_HMM_GO_SELECTION=false` returns selection authority to the sidecar
  with no other change (stage 3 shadow may stay on).
- `ROUTER_HMM_SELECTION_SHADOW=false` stops shadow logging only.
- Unsetting `ROUTER_HMM_ROSTER_PATH` makes the declarative roster fully inert
  (stages 3 and 4 then log a warning and stay disabled).
- Stage-1 validation is log-only and needs no rollback lever.

No flag changes the sidecar contract: sidecars that do not emit the optional
typed fields (`predicted_label`, `class_probabilities`,
`pin_sticky_override_eligible`) keep working via the legacy reason-string
sentinel path.
