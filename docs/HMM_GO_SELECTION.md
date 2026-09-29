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

## Local draft preview and policy tooling

`policyctl preview` reads a local draft roster and invokes the same Go selector
used by serving. It requires no registry, deployed artifact, sidecar, or model
call. Its JSON output preserves the existing draft-preview fields (source,
environment label, roster schema, harness, quality bias, per-cluster order and
scores, WII/WPI, pins, vendor flags, and optional quality-grid winners) and
declares `preview_schema_version: hmm_draft_roster_preview_v1`. Scores come from
the selector's float32 serving score map, rounded to six decimals.
Use `--class-order class-a,class-b` when a nonstandard taxonomy omits
`class_order` from its draft roster.

```bash
go run ./cmd/policyctl preview --roster-file /path/to/draft.json --harness codex
go run ./cmd/policyctl preview --roster-file /path/to/draft.json --quality-bias 0.4 --grid 21
go run ./cmd/policyctl preview --roster-file /path/to/draft.json --alpha low=0.5
```

With no quality bias, preview preserves the roster's fixed order and scores.
An explicit non-neutral bias uses calibrated WII/WPI ranking; `--alpha` tests
an exact per-cluster alpha through a local ranking copy passed to the selector.
`--alpha` and `--grid` cannot be combined; the sweep accepts 2–101 points.
The input must pass the Go roster and catalog validators, so an unknown model
or malformed indices fail before any projection is printed. `--environment`
only echoes the caller's label; it does not resolve a deployment target.

`policyctl compile --source <reviewed-roster> --output <policy>` compiles a
reviewed v7/v7.5c source into canonical `hmm_go_selection_policy_v1` bytes.
`policyctl validate --policy <policy>` checks the resulting schema and catalog
bindings. `policyctl publish` owns immutable legacy policy publication with
an exact classifier identity. Managed generic publication and target activation
use [the serving controller](SERVING_CONTROL.md): `serving publish --dry-run`
validates locally, while `serving apply` activates a target and is outside
draft preview. None of these commands build or publish classifier artifacts;
private rosters, target assignments, ML code/data, and credentials remain
outside this repository.

Forward policy publication starts from reviewed roster JSON and runs through
Go compilation and validation. WorkWeave's historical `build_aa_roster.py`
recreates roster sources from private AA score snapshots and WII/WPI
normalization assets for evaluation reproducibility; it does not publish a
serving policy. Keeping that analysis with its private inputs does not create
a second supported publication path.

## Configuration

On the self-hosted/local roster path, `ROUTER_HMM_ROSTER_PATH` is required whenever
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

On the self-hosted path, roster content can be rolled back by restoring the
previous roster artifact and redeploying; an invalid roster fails boot.

## Still to remove

- The silent-drop mapping contract of `hmm.DeployedModelsForRosterIDs`, once the
  admin roster view reads the declarative roster instead.
