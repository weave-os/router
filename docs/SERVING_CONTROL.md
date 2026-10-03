# Managed serving controller contract

The `policyctl serving` group is the low-level interface for a trusted deployment
workflow. It does not build images, deploy Cloud Run services, change revision tags,
grant IAM access, or turn on gateway-managed serving. Those preparation steps belong
to the environment's orchestrator. Existing top-level policy commands (`compile`,
`check-rosters`, `validate`, `publish`, `promote`, `rollback`, `status`) remain the
legacy lane-head interface for the self-hosted HMM path; they must not be used to
manage migrated targets.

The trust model is a single operator team publishing through one workflow identity.
The registry therefore keeps the guarantees that prevent state corruption (immutable
content-addressed objects, generation CAS on target state, strict decode, live
destination validation) and drops the layers that only defended against a hostile
registry writer. The kept list is at the end of this document.

## Command surface

`policyctl serving` exposes exactly four verbs. Every verb accepts
`--registry gs://<bucket>/<prefix>` (default `gs://weave_ml/weave_registry`).

```bash
policyctl serving publish  --kind candidate|selection_set|proposal --manifest <file> [--dry-run]
policyctl serving publish-release --candidate <file> --selection-set <file> --proposal <file> [--dry-run]
policyctl serving apply    --proposal <ObjectRef.json> | --proposal-sha256 <sha256> [--dry-run]
policyctl serving status   --target staging|prod/stable|prod/weave-internal | --proposal <ObjectRef.json>
```

| Command | Required inputs | Effect | Stdout on success |
| --- | --- | --- | --- |
| `serving publish` | `--kind`, `--manifest` | Strict-decodes the file (`DisallowUnknownFields`, no trailing JSON), runs the kind's `Validate`, and publishes the bytes immutably at `artifacts/<sha256>.json` with a `DoesNotExist` precondition plus read-back verification. Never activates anything. | `ObjectRef` — `{"uri","sha256","generation"}` |
| `serving publish --dry-run` | `--kind`, `--manifest` | Every pre-write check a publish performs (artifact kind, strict decode, `Validate`, supported schema) and reports the digest the bytes would publish under. Opens no registry connection and writes nothing. Any JSON encoding of the manifest is accepted — there is no canonical-byte requirement; surrounding whitespace is trimmed before digesting, exactly as `publish` stores it. | `{"kind","sha256"}` |
| `serving publish-release` | `--candidate`, `--selection-set`, `--proposal` | The three `serving publish` calls of one release in one invocation: publishes candidate → selection set → proposal through the same create-only path, binding each manifest to the reference — including the registry-assigned generation — of the object published before it. Never activates anything. | `{"candidate","selection_set","proposal"}`, each an `ObjectRef` |
| `serving publish-release --dry-run` | Same | Every pre-write check on all three manifests, with the references filled in at a placeholder generation, and reports the candidate's digest. Opens no registry connection and writes nothing. | `{"candidate":{"kind","sha256"},"selection_set":{"kind","validated"},"proposal":{"kind","validated"}}` |
| `serving apply` | `--proposal <ObjectRef.json>` **or** `--proposal-sha256 <digest>` | Reads the proposal at its exact generation, checks `sha256(stored bytes) == ref.sha256`, computes the activation transition against the authoritative target state (replay detection first), validates the proposal (evidence, candidate attestation, every lane against its live worker and classifier revision, scope rules), then CASes the target's single state object, superseding the outgoing activation and applying explicit withdrawals in the same write. A proposal with `scope: rollback` — or any proposal listing `withdraw_activations` — additionally has its `source_candidate` checked against the target's retained activation history before the CAS, on the dry run as well as the commit. | `ActivationResult` — `{"snapshot":{"state","generation"},"activation","outcome":"activated"\|"superseded","replayed"}` |
| `serving apply --dry-run` | Same | Everything above except the CAS: no registry write, no infrastructure change. A proposal that was already activated reconciles to its original outcome instead of re-validating destinations, so completed retries never require healthy old revisions. | `PreparationResult` — `{"proposal","prepared",` `"activation"?}`; `prepared:true` means ready to apply, `prepared:false` with `activation` means already applied |
| `serving status --target` | `--target` | Authoritative target state read (`state/<env>/<target>.json`, falling back to the legacy path for targets that have not applied since the layout move). Never validates against reachable workers. | `ServingStateSnapshot` — `{"state","generation"}` |
| `serving status --proposal` | `--proposal <ObjectRef.json>` | Outcome reconciliation for that exact proposal: scans the target's activations for `activation.proposal == ref`. | `ActivationResult` with `replayed:true` if activated; otherwise `{"proposal","activated":false,"generation"}` |

Removed verbs `validate`, `resolve`, `prepare`, `activate`, and `rollback` exit
non-zero with a message naming the replacement (`publish --dry-run`,
`apply --proposal-sha256`, `apply --dry-run`, `apply`, and
`apply --proposal <ref>` with a proposal whose scope is `rollback`). Each verb
defines only the flags listed above; any other flag — including the retired `--approved-proposal`, `--workflow-actor`,
`--validation-origin`, and `--stored` — is a usage error before the registry is
opened. Approval is the protected environment the command runs in, the execution
identity recorded on the activation is derived from the environment
(`GITHUB_ACTOR@run:GITHUB_RUN_ID`, else `USER`, else the proposal's `actor`), and
destination validation calls the HTTPS origins the selection set's lanes declare.

### Exit codes and output conventions

- Exit `0`: the command succeeded and stdout holds exactly one indented JSON
  document of the shape listed above. Nothing else is written to stdout.
- Exit `1`: any failure. The error is printed to stderr as plain text and also as a
  structured `slog` JSON record (`"msg":"Serving command failed"`). Stdout stays empty,
  so callers may parse stdout unconditionally when the exit code is `0`.
- A concurrent writer, a `previous_selection_set` that no longer matches the incumbent,
  or a moved target generation fail with an error wrapping `policyregistry.ErrConflict`
  (message contains `conflict`). Compose a fresh proposal against the current state.
- `apply` may fail **after** the CAS committed if stdout delivery fails. The
  error then starts with `activated; output observation degraded`. Do not create a new
  proposal: run `status --proposal` (or re-run `apply` with the same proposal) to
  reconcile the outcome without creating another activation.

## Registry layout

```
<root>/artifacts/<sha256>.json               immutable manifests (candidate, selection_set, proposal)
<root>/state/<env>/<target>.json             mutable, generation-CAS'd control state, one per target
<root>/router_serving/v1/<kind>/sha256/<sha256>.json     v1 manifests — read-only history
<root>/runtime_state/router_serving/v1/targets/<env>/<target>/state.json   v1 state — read-only history
```

`<env>` is `staging` or `prod`; `<target>` is `staging`, `prod/stable`, or
`prod/weave-internal`. Beta targets are rejected.

Objects under `artifacts/` are content-addressed by the digest of the exact bytes
published; the object's `schema_version` selects the decoder, and `artifacts/` holds
v2 schemas plus the opt-in v3 selection schema described below. Objects under the legacy `router_serving/v1/...` namespaces decode
forever as v1 objects, because the activation history inside every state object
references them and content-addressed objects can never be rewritten without orphaning
those references. Each v2 kind is a read-side family that also admits the v1 object it
folds (`candidate` ⊇ `releases`, `selection_set` ⊇ `selection_sets`, `proposal` ⊇
`proposals`), so a v2 proposal may name a v1 `previous_selection_set` — the incumbent binding is
exact whatever layout the incumbent was published under — and
`apply --proposal-sha256` resolves `artifacts/` first, then the legacy namespace.
The `selection_set` a new activation would serve is floored: a proposal naming a set
outside `artifacts/` is rejected before the CAS with an error wrapping
`policyregistry.ErrSelectionSetLayoutFloor`, on every activation path including
rollback. Historical v1 activations stay readable, admissible and withdrawable; only
the set a target would newly serve must be v2. New writes go only
to `artifacts/`: a publish with a v1 kind (`releases`, `classifiers` → `candidate`;
`bindings`, `profiles`, `selection_sets` → `selection_set`; `proposals` → `proposal`)
is rejected before the registry opens, naming the kind it folded into.

Target state moves lazily: readers try `state/<env>/<target>.json` first and fall back
to the legacy `runtime_state/...` object. The first `apply` on a target under this
layout reads whichever exists and CASes the new path with a `DoesNotExist`
precondition; the incumbent binding comes from the proposal's `previous_selection_set`,
so no generation is transcribed between paths. Nothing needs manual migration, and the
state-path move is independent of the selection-set floor: a target still on the legacy
state path bootstraps onto `state/<env>/<target>.json` on its next apply, but that apply
must name an `artifacts/` selection set. In-flight proposals whose `selection_set` is
v1 no longer apply — republish the set as a v2 `selection_set` and compose a new
proposal over it.

### Fleet-rollout gate

Every `ReadServingState` reader — the gateway (`cmd/router-gateway`, on `/readyz`,
`/startupz` and every admission) and `policyctl serving` itself (`status`, `apply`,
`rollback`, and the controller behind them) — must run a binary that understands the
`artifacts/` layout, the v2 kinds, and the `state/` path **before** the first v2 or
new-path `apply` against a target. Managed workers (`cmd/router`) never read target
state: they resolve the immutable artifacts their boot refs and the gateway's
assertion name, so they are in the same gate for the v2 kinds and the `artifacts/`
layout, but not for the `state/` path. An old binary that keeps writing the legacy
state path after a new-path object exists splits the control plane, and an old binary
cannot decode a target whose current selection set is v2, so its admissions fail
closed. Gate the first apply per target on image rollout, not on merge. After the
first v2 apply on a target, reverting the binary is a one-way door for that target;
the designed object-level rollback is a `scope: rollback` proposal, not a code revert.

`scripts/serving_admission_check` is not part of this gate and not part of the fleet.
It is a loopback-only integration harness: it requires a loopback host in
`ROUTER_TEST_DATABASE_URL` and enforces nothing else about the database, so use a
disposable local Postgres fixture. It leaves no residue: on success and on failure it
deletes the installation it created (whose cascades carry the keys, subject access,
profile assignment, session bindings and request attribution) plus the credential
subject those cascades leave behind, then re-counts those tables and fails if any
fixture row survived. It exercises
`internal/postgres/serving.NewServingAdmissionRepo(...).Admit` — credential-subject
enrollment and rotation fences, concurrent first-admission collapse onto one binding,
profile assignment, and the database admission clock — against a fixture decision
closure. It opens no registry, reads no serving state, and
therefore proves nothing about registry layout, v2 decoding, or a deployed
revision's readiness.

## Manifest kinds

Three immutable kinds are published; every field is required unless marked optional.
The v2 shapes below remain supported; [selection v3](#shared-code-cohorts-and-roster-configuration-selection-v3) separates code from policy configuration.
The exported Go contracts in `internal/policyregistry/` define the exact JSON. Digests
are `sha256` hex of the stored bytes; `ObjectRef` is `{uri, sha256, generation}` and
must point inside the registry root.

**`candidate`** (`schema_version: router_serving_candidate_v2`) — one per build, shared
across targets. Merges the v1 release and classifier bundle: `router_image_digest`,
`selection_policy` (`PolicyObject` into `router_policy/v1/policies/sha256/...`),
`classifier` (identity, `package`, `auxiliary_models` — explicit even when `{}` —
and `configuration`), `requirements` (`runtime_contract`, `policy_schema`,
`classifier_wire_schema`, `taxonomy_sha256`), and `provenance` (`router_revision`,
`weave_revision`, `build_attestation`).

**`selection_set`** (`schema_version: router_serving_selection_set_v2`) — one per
target per change: `target`, `default` lane, `profiles` (map of profile key → lane;
explicit even when `{}`). A **lane** is `candidate` (`ObjectRef`), `project`, `region`, `router` and
`classifier` (`RevisionBinding`: `name`, `url`, `audience`, `image_digest`,
`configuration`), `attestation` (`ObjectRef` to physical-revision evidence), and for
profile lanes `profile_key`, `profile_policy`, `profile_requirements`. Deployment
bindings and routing profiles embed inline; the candidate ref is what "same composition
across targets" and rollback-source checks compare by identity.

**`proposal`** (`schema_version: router_serving_proposal_v2`) — one per target per
change: `target`, `previous_selection_set` (optional; omitted only when bootstrapping a
target), `selection_set`, `source_candidate`, `scope`
(`full|router|roster|classifier|profile|custom|rollback`), `profile_key` (profile scope
only), `actor`, `reason`, `request_id` (any non-empty string ≤ 128 chars, e.g.
`run_id:lane_index`), `created_at`, `evidence[]`, `withdraw_activations[]` (explicit
even when `[]`). There is no `expected_generation`: `previous_selection_set` binds the
incumbent's content, the CAS binds its generation.

**Control state** keeps its v1 shape (`router_serving_control_state_v1`): `target`,
`current_activation_id`, `sequence`, `activations` keyed by activation UUID. Each
activation records `selection_set`, `proposal`, `request_id`, `actor`,
`workflow_actor`, `activated_at`, and optional `superseded_at`, `withdrawn_at`,
`replacement_id`. `previous_selection_set` and the recorded history may reference v1
selection sets; `selection_set` may not. An exact rollback must therefore name a
previously activated set that is itself stored under `artifacts/`: rolling back to a
set a target only ever served as a v1 object fails closed on the layout floor.
Republishing that composition under `artifacts/` does **not** make it rollbackable —
the republished object is a new reference the target never activated, so exact
rollback rejects it too. A target whose only good history is v1 recovers by rolling
forward onto a v2 selection set carrying that composition, which restores routing but
cannot remove profile keys registered since (forward scopes preserve the registered
profile inventory). Every target gets one v2 activation and regains exact rollback
from then on.

Evidence and attestation objects (`build_attestation`, lane `attestation`,
`evidence[]`) are arbitrary `gs://` objects inside the registry, verified at apply
time by exact generation and digest.

## Workflow contract

A change costs **three publishes**: the candidate once per build (shared by every
target), then a selection set and a proposal per target. Evidence and attestation
objects are published by the deployment control plane as before.

1. `publish --kind candidate --dry-run` in CI, then `publish --kind candidate` once the
   image is built. The `ObjectRef` output is the lane input for every target.
2. Deploy the revisions, publish revision evidence, then
   `publish --kind selection_set` and `publish --kind proposal` for the target.
   `publish-release --candidate <file> --selection-set <file> --proposal <file>` does
   steps 1 and 2 in one invocation once all three manifests are composed, filling the
   candidate and selection-set references in as it goes.
3. `apply --dry-run --proposal <ref>` for a no-write preview, then `apply` from the
   protected environment. Retry `apply` with the same proposal if the outcome is
   ambiguous; it replays the original activation instead of creating a second one.
4. `status --target` after activation, or `status --proposal` to reconcile one run.
5. To roll back, publish a proposal naming the `artifacts/` selection set the target
   previously served, its `source_candidate`, the current incumbent as
   `previous_selection_set`, and `scope: rollback`; then apply it like any other
   proposal:

   ```bash
   policyctl serving apply --dry-run --proposal rollback-proposal.json
   policyctl serving apply --proposal rollback-proposal.json
   ```

Profile registration and version changes use a `profile`-scoped proposal. Forward
scopes preserve registered profile keys and revisions. An exact whole-selection
rollback uses `scope: rollback` through `apply` — the verb no longer distinguishes
rollbacks, the proposal's scope does, and `apply` runs rollback-source validation for
it; only that scope can restore
the historical profile inventory, including removing profiles introduced later or
restoring older profile revisions. All lanes still require artifact and live readiness
validation, and the same CAS applies. A fresh request for a profile absent from the
restored set fails closed instead of falling back to the default. Historical manifests
and revisions remain retention roots and are not deleted.

Same `request_id` with the same proposal returns the original activation (`replayed:
true`) even after supersession or a worker outage; it never makes it current again. A
second proposal that reuses a `request_id` is simply a second activation — the id is
an audit field, reconciliation keys on the proposal ref. A new promotion of identical
candidate bytes intentionally creates a new activation incarnation without resetting
older supersession deadlines.

### `publish-release`

An `ObjectRef` carries the generation the registry assigns when the object is created,
which nobody can know for an object that does not exist yet. `publish-release`
therefore **fills in** the references it creates rather than asking the caller to
transcribe them: after the candidate is published, every selection-set lane that names
the candidate — by digest, or by leaving the reference blank — is bound to its exact
reference, and after the selection set is published the proposal's `selection_set` and
`source_candidate` are bound the same way. A lane pinned to a different,
already-published candidate keeps it; a selection set that names the candidate nowhere,
or a proposal that names a different `sha256`, belongs to another release and is
rejected before the manifest that names it is written. The candidate file itself is
published byte-for-byte as written.

The generation is the only field filled in silently. A reference the caller did write
is strict-decoded like any manifest field — a misspelled or mistyped field is an error,
not something the fill discards — and a `uri` or `sha256` naming an object other than
the one being published fails closed instead of being overwritten.

Filling is deterministic — the filled manifest is re-encoded from the same document
with the same references — so re-running a release derives the same bytes, and the
create-only path of `publish` reports the objects that already exist with their
existing references and no new write, whether they were published by an earlier full
run or by a run that failed part-way through. `publish-release` never reads or writes
target state and never applies, activates or withdraws anything; `apply` remains a
separate, approved step.

`--dry-run` runs every check the release performs, filling the references with a
placeholder generation, and reports only the candidate's digest: the selection set's
and the proposal's digests depend on references that do not exist until the candidate
and the selection set are published.

## Non-circular configuration and evidence

Configuration artifacts cover effective behavior-affecting settings and exact
secret-version references, including transport endpoints/audiences and timeout
settings. They exclude only bootstrap back-references such as a selection-set
reference and evidence references. Do not exclude behavior-affecting transport
configuration merely to make composition easier. Bootstrap references are
validated separately as exact immutable inputs, not treated as behavior config.

Create physical-revision evidence from the deployment control plane before
private readiness: exact revision, resolved OCI image identity, effective
configuration, secret versions, target and environment. This is what a lane's
`attestation` references; it must not reference the containing lane, selection
set, or later successful smoke. Publish the selection set next, then perform
private readiness/smoke against those identities and publish the proposal's
evidence. This order avoids configuration→selection-set→lane→configuration and
lane→post-readiness-evidence→lane digest cycles.

The controller verifies that every proposal evidence artifact, the source candidate's
build attestation, and every lane attestation exists at its exact GCS generation and
matches its digest (audit payloads are bounded to 8 MiB). This is byte integrity,
not an invented proof format or signature verifier. Evidence semantics/provenance
are trusted assertions by authorized registry writers, independently supplemented
by actual destination validation. Environment integration must authorize those
writers, verify real image/configuration state, and bind later smoke/evaluation
evidence to the exact tuples tested. Derived customer or component-only tuples do
not inherit a source whole-release evaluation claim; their own fresh destination
validation is mandatory.

Approval is enforced by the orchestrator's protected environment and restricted
registry-writer identity. The workflow that passes that gate chose the proposal
digest; the activation records the execution identity derived from that
environment separately from the operator frozen in the proposal's `actor`. Neither
identity should come from untrusted request text.

## Private destination validation

Validation runs from an authorized environment-local runner that can reach both
private revisions. `apply` calls each lane's declared worker and classifier `url` with
an identity token minted for the lane's `audience`. There is no HTTP, redirect,
public-readiness, or control-plane-only bypass. Requests have a two-minute
end-to-end deadline so zero-traffic revisions can cold-start, and responses have a
1 MiB bound. Google identity tokens travel in `X-Serverless-Authorization`,
preserving the normal routing/subscription `Authorization` header.

The worker implements `POST /internal/serving/validate`, only in private managed
serving mode. It accepts `WorkerValidationRequest` (target, optional profile key,
exact selection), loads that snapshot, verifies local boot identity and its own
catalog, and returns `WorkerAttestation`. It does not admit a conversation, invoke
an inference provider, bill a request, poll a mutable head, or check unrelated
targets. Cloud Run IAM and private ingress must restrict this endpoint to the
gateway and explicitly approved validation principals before enabling the mode.

The classifier must implement `GET /internal/serving/attestation`, returning
`ClassifierAttestation`: readiness, exact revision, complete core identity,
loaded package reference, explicit auxiliary-model inventory (including `{}`),
and loaded behavior-affecting configuration reference. Identity must describe
verified loaded content, not merely echo expected environment variables. All
references must match the proposed candidate, including storage generations.

Destination attestations are memoized for the duration of one activation and are
keyed by the complete `RevisionBinding` they were obtained from (name, url,
audience, image digest, configuration reference), so the lanes of one proposal that
share a classifier revision attest it once instead of once per lane. Worker
attestations additionally key on the full `WorkerValidationRequest` (target,
profile key, exact selection), because `/internal/serving/validate` derives the
attested requirements, catalog arms, and echoed snapshot from the request; lanes
with distinct profiles therefore still issue their own worker validation.
Attestation failures are memoized with the same keys, so a rejecting or unreachable
destination keeps failing every lane. The memo holds raw attestation responses only:
every lane still runs the full comparison against its own candidate composition,
profile policy, requirements, and policy arms. Each activation logs
`destination_validation_outcome`, `destination_validation_lanes`,
`destination_validation_http_calls`, and `destination_validation_cache_hits`. The
outcome is `blocked` when a destination refused a lane — validation stops there,
so those counts are partial — and `attested` when every lane the activation
reached was attested, including when a later proposal check rejects the change.

The legacy classifier `/readyz` response attests only the core identity and does
not satisfy this contract. Environment integration must deploy the full
classifier attestation endpoint before managed apply can pass; the controller
deliberately fails closed until then. Current default-off worker and legacy policy
operation do not require this new endpoint.

## Kept guarantees

| Guarantee | Why it stays |
| --- | --- |
| Generation CAS on the target state object | The only lost-update guard between concurrent applies |
| Immutable publish (`DoesNotExist` + read-back verify) | Approvals, history refs, and evidence point at bytes that can never be repointed |
| Strict decode (`DisallowUnknownFields` + trailing-data check) + `Validate` | Fails closed on real corruption or shape drift; byte-format drift is tolerated |
| `sha256(stored bytes) == ref.sha256` on the proposal object at apply/status | Reconciliation keys on the recorded proposal ref; the recorded digest must equal the activated bytes |
| Same-proposal `request_id` replay | Ambiguous CAS or output failures reconcile without a second activation |
| Evidence, build, and lane attestation verification at apply | The only integrity check on externally produced audit payloads |
| Live destination validation (worker `/internal/serving/validate`, classifier `/internal/serving/attestation`) | Registry bytes cannot prove a revision is live |
| Authoritative `ReadServingState` on every admission | The control-plane read must stay live; no stale fallback |
| Serving assertion HMAC over deterministic bytes | The gateway→worker signature boundary needs deterministic signed bytes |
| Registry containment of every `ObjectRef` | Refs may not address objects outside the registry root |
| `previous_selection_set` binding + `withdraw_activations` chain | Proposals must name the exact incumbent; emergency pin withdrawal depends on the chain |

Dropped: canonical-JSON byte equality on manifests, digest re-verification on every
traversal read, the `request_id` uniqueness/conflict arm, `expected_generation` in
proposals, the `--approved-proposal`/`--workflow-actor` binding flags, the
`--validation-origin` allowlist, and the per-component `releases`/`classifiers`/
`bindings`/`profiles` publishes.

## Admission timing measurement

Every gateway admission emits exactly one `Serving admission timing` record at `Info`
from `ServingAdmissionRepo.Admit`, including denied and failed attempts. Durations are
monotonic (`time.Since`, never the database clock) and carry no request content — no
installation, key, subject, conversation or session identifier, no binding JSON and no
error text.

| Attribute | Meaning |
| --- | --- |
| `admission_total_ms` | Whole `Admit` call |
| `admission_tx_ms` | `BeginTxFunc` enter to return |
| `admission_lock_wait_ms` | Time in `GetServingConversationLock`; `0` when not persistent |
| `admission_gcs_state_read_ms` | Authoritative `ReadServingState` |
| `admission_gcs_selection_set_read_ms` | Sum of the selection-set reads |
| `admission_gcs_selection_set_reads` | Number of distinct selection sets read (2 when a previous binding names another activation) |
| `admission_decide_ms` | `ServingAdmission.Decide` |
| `admission_projection_queries_ms` | Identity, lane-enrollment, plan and assignment projection SQL, measured up to the conversation lock so neither the lock wait nor the session-binding read is attributed to it |
| `admission_persistent` | Whether the request carried a conversation |
| `admission_outcome` | `admitted`, `denied` or `error` |
| `target`, `activation_id` | Serving target and admitted activation (empty when nothing was admitted) |

The span rule is positional: every projection query the transaction runs before the
conversation lock — including the installation lane-enrollment lookup that selects
`prod/weave-internal` — counts in `admission_projection_queries_ms`; the lock and the
session-binding read that follows it belong to `admission_lock_wait_ms` and the
remainder of `admission_tx_ms`. The lane lookup's inputs and result are not logged:
only the resulting `target` appears, never the installation id or the enrollment flag
and generation. `activation_id` is stamped only after the transaction commits, so a
stale-generation denial reports no activation.

The record is measurement only. It exists to size the deferred fail-closed target-state
cache in gateway admission after seven days of production data; **no admission cache
exists**, and every admission still performs the authoritative registry reads in the
order `ReadServingState` → selection sets → release selection.

## Local verification

### Default-off and managed boot

In `ROUTER_DEPLOYMENT_MODE=managed` the worker refuses to boot when any other
`ROUTER_SERVING_*` variable is set while `ROUTER_SERVING_ASSERTION_KEY` is empty,
so a serving-stamped revision with dropped key injection can never mount
inference routes without admission. An unset or whitespace-only
`ROUTER_SERVING_ASSERTION_KEY` on a revision with no serving stamping keeps the
worker on its existing managed/self-hosted path. It does not read serving control heads, subject
projections, session bindings or request-attribution tables. A nonempty weak key
fails boot; it does not silently fall back. A serving worker also refuses to
boot unless `ROUTER_DEFAULT_STRATEGY` is one of the policy strategies it
registers (`hmm`, `hmm_embedding`): an unset or `cluster` default would score
every installation without a persisted strategy, and every retired `hmm_beta`
installation, on the legacy cluster embedder. On the request path a managed
worker answers 503 `routing_strategy_unavailable` when the persisted or remapped
strategy is not selectable instead of rerouting to `cluster`; only the legacy
(non-managed) path keeps the cluster fallback. Apply additive router migrations
`0095` through the coordinated migration path before enabling a gateway.

The gateway exposes `/health` for process liveness, `/startupz` for boot
readiness and `/readyz` for admission readiness. Readiness has a five-second
total budget to ping PostgreSQL, resolve the environment's active default
binding from the registry, and acquire a worker IAM token. Missing activation or
unavailable dependencies return 503; no session is admitted and no inference is
dispatched. Deployment configuration must use `/readyz` for traffic-admission
checks, not `/health`. Token acquisition does not prove the destination's
`run.invoker` grant; private deployment smoke still must.

`/startupz` runs the same checks except that a target with no activation yet is
boot-ready, because a container gated on an activation can never be the one that
deploys the first activation. Deployment configuration must use `/startupz` for
the container startup probe and `/readyz` after activation. Requests remain
fail-closed on an unactivated target: forwarding resolves the binding per
request and has nothing to resolve.

Managed workers require `ROUTER_SERVING_TARGET`, `ROUTER_SERVING_PROJECT`,
`ROUTER_SERVING_REGION`, `ROUTER_SERVING_IMAGE_DIGEST`, and
`ROUTER_SERVING_REVISION` (or Cloud Run's `K_REVISION`). Both
`ROUTER_SERVING_CONFIGURATION` and `ROUTER_SERVING_SELECTION_SET` take the suffixes
`_URI`, `_SHA256`, `_GENERATION` for exact immutable references. Set
`ROUTER_SERVING_REGISTRY_URI` explicitly. Keep the signing secret consistent across
gateway and worker, restricted to those identities; validation callers never
receive it. Existing provider, database and HMM timeout/auth configuration still
applies. The gateway separately requires `ROUTER_SERVING_ENVIRONMENT` (`prod` or
`staging`), the registry URI, signing key, and normal router-primary DB settings.

Worker startup validates the bootstrap selection set's default and all profile
lanes, compiled catalog compatibility, and physical worker identity. It reads
no mutable lane head and contacts no classifier. This static readiness closure
must remain available for the lifetime of the worker revision; bootstrap artifacts
are retention roots. It deliberately does not cache an inference runtime or claim
live classifier readiness. Private proposal validation and every uncached admitted
tuple load exercise the actual classifier/runtime. Thus classifier-only releases
can reuse a worker and cold-start it after its original classifier retires.
`/readyz` tests local bootstrap/database/strategy readiness; it does not replace
the exact-tuple private validation required before activation.

### Product compatibility and retention

The gateway preserves inference, authenticated catalog/roster/preview, subscription,
analytics-key export, version, and signed-feedback surfaces. Historical feedback
looks up the original request's installation-scoped immutable attribution and
forwards to that worker; it never substitutes the current release. Configure the
same `ROUTER_FEEDBACK_LINK_SECRET` on gateway and workers when feedback is enabled.
Pre-cutover links without attribution fail explicitly, so environment cutover must
either drain those links or provide an independently reviewed migration path.

Feedback links can outlive the seven-day session retirement window (default token
lifetime is 30 days, and tokens may be non-expiring). Keep their attribution and
referenced workers/artifacts as retention roots in addition to bootstrap references,
retained sessions and in-flight drain. This implementation deletes none of them;
retention remains report-only. Secret rotation and legacy-session/link cutover need
an operator-approved plan before production ingress changes.

### Automated checks

```bash
go test ./internal/policyregistry ./internal/servingvalidate ./cmd/policyctl
go test -race ./internal/policyregistry ./internal/servingvalidate ./cmd/policyctl
go vet ./internal/policyregistry ./internal/servingvalidate ./cmd/policyctl
```

In CI these run inside the `Go checks` job, which is path-gated: the `Test`
workflow classifies the pull request's base..head diff with
[`scripts/go_ci_relevance.sh`](../scripts/go_ci_relevance.sh) and skips
`Go checks` only when no changed file can reach Go compilation (no `*.go`,
`go.mod`/`go.sum`, `db/**`, `scripts/**`, `Makefile`, `Dockerfile*`, sqlc or
lint config, nothing inside a Go package directory, and not the workflow
itself). Every serving-control change touches Go, so the commands above always
run for them. `Inference boundary` and the aggregate `Test` check stay
unconditional, and push, merge and `workflow_dispatch` runs gate everything ON.

Within `Go checks`, the standalone gateway build
(`CGO_ENABLED=0 go build ./cmd/router-gateway`, which guards the gateway
against worker-only cgo dependencies) runs whenever the diff touches
`go.mod`/`go.sum` or any package directory in
`CGO_ENABLED=0 go list -deps ./cmd/router-gateway` — `internal/policyregistry`,
`internal/postgres/serving` and `internal/gateway` among them — and whenever
that closure cannot be computed. Only diffs provably outside the gateway's
dependency closure skip it.

Tests include a real Cloud Storage client against an ephemeral local JSON API
fixture, immutable publish collisions, generation-CAS conflicts, exact-generation
reads, v1/v2 dual decode and the lazy state-path bootstrap, private TLS endpoint
validation/redirect rejection, auxiliary/config mismatches, no-write dry-run publish
and apply, emergency rollback, rollback through `apply`, selection-set layout-floor
rejection, superseded idempotent
retries, removed-verb rejection, and committed-output failure reconciliation. They
do not establish live IAM, image attestation, infrastructure ownership, or
environment-local latency; those remain authorized rollout checks.

## Shared code cohorts and roster configuration (selection v3)

The ordinary production code cohorts are `prod/weave-internal` and `prod/stable`.
Staging retains its existing target. Customers and subscription profiles are roster
configuration within a cohort. Internal and stable may run different candidates;
all customers on a target share one worker image and physical worker/classifier
binding. Historical revisions still serve retained conversations.

`router_serving_selection_set_v3` is an opt-in schema in the existing
`selection_set` artifact family:

| Field | Meaning |
| --- | --- |
| `schema_version` | `router_serving_selection_set_v3` |
| `target` | Existing managed target |
| `code` | `candidate` ObjectRef plus the existing LaneBinding fields: `project`, `region`, `router`, `classifier`, `attestation` |
| `default_policy` | Immutable PolicyObject: `uri`, `sha256`, `generation`, `schema_version` |
| `profiles` | Explicit map of authenticated profile UUID to PolicyObject; `{}` is valid |

The candidate stays `router_serving_candidate_v2`. Its `selection_policy` remains
its immutable bootstrap/build policy. The v3 set's `default_policy` or selected
profile policy is the **effective** roster. All policies must satisfy that
candidate's requirements and classifier taxonomy, and every arm must exist in the
selected worker's Go catalog. A profile cannot specify a candidate, revision or
classifier. Invalid, unavailable or incompatible assignments fail closed; there
is no fallback to another profile or the default roster.

Admission still reads the installation/plan assignment from authenticated primary
storage. Clients cannot select a profile through headers. The existing session and
assertion tuple represents v3 without a database or wire migration: `release` is
`code.candidate`, `binding` is the selection-set ObjectRef, and a named profile's
`profile` is that same set reference. The signed profile key selects the immutable
policy within it. Worker credential and target validation precede snapshot loading.

The runtime cache keys include target, profile key and full immutable references
(including generations). Concurrent customers cannot replace each other's policy.
Eviction reloads the admitted reference, rather than a newer head. Managed roster
discovery uses the request snapshot; the legacy global TTL source is not used.
Policy SHA/schema/roster attribution comes from the effective policy, while release
attribution continues to name the shared candidate.

### Promotion and independent roster rollback

Candidate and proposal v2, control state v1, assertion v1, worker validation and
classifier wire contracts remain unchanged. `publish-release` fills `code.candidate`
for a v3 set, leaving the policy references untouched. For a roster-only update,
reuse the already-published candidate and publish only the set and proposal.

| Proposal scope | V3 contract |
| --- | --- |
| `roster` | Change `default_policy`; preserve profiles and the exact code candidate/binding. `source_candidate` names that shared code candidate. |
| `profile` | Register or change only `profile_key`'s policy; preserve default, all other profiles and exact code candidate/binding. `source_candidate` names shared code. |
| `router` | Move all customers together to the source image/provenance and one new worker revision; retain effective policies, requirements and classifier. |
| `classifier` | Reuse the worker and effective policies; validate the new classifier against every roster. |
| `full` / `custom` | Existing forward profile-preservation rules apply; full promotion reuses the exact source candidate. |
| `rollback` | Restore the exact previously activated selection set on the same target. This may restore code as well as configuration. |

To roll back only a roster while retaining current code, publish a `roster` or
`profile` proposal selecting the prior immutable policy. No image build or customer
revision is required. Existing eligible conversations retain their original policy,
classifier and worker selection across either forward promotion or rollback. New
conversations use the current assignment. Preserve assignment generations during
roster content updates: changing enrollment, authorization, entitlement or profile
assignment generations deliberately triggers the existing rebind behavior.

Retention remains bounded by the existing session lifecycle: 24 hours idle, seven
days from activation supersession, then at least 15 minutes drain grace. Explicit
withdrawals still force rebind. Preserve every policy, selection set, candidate,
classifier artifact, physical revision and bootstrap closure referenced by eligible
sessions, feedback attribution or the supported rollback history. Cache eviction
is not permission to delete an artifact. This runtime performs no history garbage
collection.

### Reader gate and deployment integration

V1/v2 decoding and v2 profile/candidate equality remain unchanged. New binaries can
serve old sets. Old binaries cannot decode v3: keep writing v2 while they coexist
on a target. Before the first v3 activation, upgrade **all** target-state readers
(gateway and controller/policyctl), destination workers and private deployment
normalizers/verifiers. An old stable worker may continue on v2 while internal uses
v3, provided shared readers understand both. Keep old workers available only for
their compatible retained selections.

Returning the current selection to v2 does not permit an old binary rollback:
retained v3 sessions, feedback, bootstrap refs and rollback history still need v3
readers. Roll back configuration or code using compatible binaries and exact
historical references. Never rewrite content-addressed artifacts or discard history
to make an old reader appear compatible.

The private integration PR must:

1. Pin this public runtime and upgrade the gateway, workers, controller/policyctl
   before enabling v3 writes. Preserve candidate build attestations, invocation-time
   verified internal candidate selection and manual stable promotion without rebuild.
2. Extend selection normalization, candidate composition, roster promotion and
   publication to v3. In v3 roster/profile proposals, use the existing shared code
   candidate as `source_candidate`; read effective policies from the set.
3. Bind app capabilities to immutable metadata for the actual shared code candidate.
   Roster changes reuse image capabilities. Verify every effective policy, classifier
   compatibility, revision readiness/identity and gateway traffic. Retain the Cloud
   Run controller execution evidence; no new executor receipt is assumed.
4. Configure the internal/stable shared worker topology and retain historical
   revisions and artifacts through session and rollback windows. Validate customer
   isolation, retained/new conversations and independent roster rollback during
   controlled rollout acceptance.
5. Retire legacy resources only after proving caller migration and retention/rollback
   safety. This public runtime merge does not activate the private deployment.

### Why the selection pointer and controller remain

Cloud Run traffic cutover does not provide authenticated roster assignment,
per-conversation policy/classifier retention, exact serving attribution or atomic
configuration rollback. Keep the selection-set CAS, immutable activation history,
signed admission, exact revision forwarding and private destination validation.
V3 removes per-profile candidate/binding duplication from new configuration; it
retains legacy readers and historical profile bindings for sessions and rollback.
No app deployment-time prerequisite gate or routine production live-request test
is introduced. Implementation tests are separate from live rollout acceptance.
## Internal production plan tests

`ROUTER_TEST_PLANS_ENABLED` defaults to false. It enables internal launch
preparation on managed workers with billing and test-grant admission on production
gateways. Roll out assertion v2 readers to all retained stable workers before
enabling either writer. Ordinary admissions continue using v1 with no test scope;
old strict readers reject v2. Enabling this setting is an operational action,
separate from implementing or validating the feature.

Internal tools require the existing `/internal/v1` shared-token authentication.
They list eligible personal subjects, preview an exact selection, prepare a
confirmed one-hour grant, and revoke a grant. Eligibility requires a complete,
active, internally enrolled credential subject, installation access, an existing
personal routing key, and an enabled positive isolated test budget. Budget
provisioning/funding is an explicitly authorized administrative action; preparation
creates only the short-lived grant and never changes entitlements or issues keys.

Stable, Max and Boost are typed test selectors, separate from subscription plans.
Every test targets `prod/stable`; enrollment authorizes access without projecting
the ordinary internal lane. Max/Boost reuse their server-owned profile keys.
Preparation re-resolves the preview, and each admission checks the grant digest,
current eligibility, single bound session, expiry/revocation and exact retained
activation/profile/policy. There is no fallback to a current head.

`X-Weave-Test-Grant` and `X-Weave-Test-Session` are consumed at the gateway and
removed from the worker hop. The gateway also removes provider credentials and
client email attribution; only the signed v2 test scope selects identity, plan,
session and billing subject. Workers verify it before email/subscriber resolution
and authenticate without provider-secret lookup. `/v1/test-plan/validate` loads the
exact worker snapshot without inference and returns the admitted scope and tuple.

Test inference is prepaid-only, uses `internal_test_budgets` and
`internal_test_credit_ledger`, and meters the authenticating key's spend atomically.
Missing or disabled test funding fails closed. Customer balances, allowance,
linked subscriptions, BYOK, overrides and autopay cannot fund tests. Settlement
of already admitted work remains possible after disabling the budget. This path
tests routing and model eligibility; subscriber included-allowance enforcement
remains owned by the ordinary subscriber tests.

The synthetic database check lives in `scripts/internal_test_plan_check` and
requires `ROUTER_TEST_DATABASE_URL` naming a disposable loopback database.
