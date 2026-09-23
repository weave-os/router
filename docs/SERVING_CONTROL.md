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
policyctl serving apply    --proposal <ObjectRef.json> | --proposal-sha256 <sha256> [--dry-run]
policyctl serving rollback --proposal <ObjectRef.json>
policyctl serving status   --target staging|prod/stable|prod/weave-internal | --proposal <ObjectRef.json>
```

| Command | Required inputs | Effect | Stdout on success |
| --- | --- | --- | --- |
| `serving publish` | `--kind`, `--manifest` | Strict-decodes the file (`DisallowUnknownFields`, no trailing JSON), runs the kind's `Validate`, and publishes the bytes immutably at `artifacts/<sha256>.json` with a `DoesNotExist` precondition plus read-back verification. Never activates anything. | `ObjectRef` — `{"uri","sha256","generation"}` |
| `serving publish --dry-run` | `--kind`, `--manifest` | Every pre-write check a publish performs (v2 kind, strict decode, `Validate`, v2 schema) and reports the digest the bytes would publish under. Opens no registry connection and writes nothing. Any JSON encoding of the manifest is accepted — there is no canonical-byte requirement; surrounding whitespace is trimmed before digesting, exactly as `publish` stores it. | `{"kind","sha256"}` |
| `serving apply` | `--proposal <ObjectRef.json>` **or** `--proposal-sha256 <digest>` | Reads the proposal at its exact generation, checks `sha256(stored bytes) == ref.sha256`, computes the activation transition against the authoritative target state (replay detection first), validates the proposal (evidence, candidate attestation, every lane against its live worker and classifier revision, scope rules), then CASes the target's single state object, superseding the outgoing activation and applying explicit withdrawals in the same write. | `ActivationResult` — `{"snapshot":{"state","generation"},"activation","outcome":"activated"\|"superseded","replayed"}` |
| `serving apply --dry-run` | Same | Everything above except the CAS: no registry write, no infrastructure change. A proposal that was already activated reconciles to its original outcome instead of re-validating destinations, so completed retries never require healthy old revisions. | `PreparationResult` — `{"proposal","prepared",` `"activation"?}`; `prepared:true` means ready to apply, `prepared:false` with `activation` means already applied |
| `serving rollback` | `--proposal <ObjectRef.json>` | The `apply` path with rollback-source validation forced: the proposal `scope` must be `rollback`, `previous_selection_set` must name the incumbent, `selection_set` must be a set previously activated on the same target, and `source_candidate` must equal that set's default candidate. Normal rollback retains session pins; emergency withdrawals must be listed in the proposal's `withdraw_activations`. | `ActivationResult` |
| `serving status --target` | `--target` | Authoritative target state read (`state/<env>/<target>.json`, falling back to the legacy path for targets that have not applied since the layout move). Never validates against reachable workers. | `ServingStateSnapshot` — `{"state","generation"}` |
| `serving status --proposal` | `--proposal <ObjectRef.json>` | Outcome reconciliation for that exact proposal: scans the target's activations for `activation.proposal == ref`. | `ActivationResult` with `replayed:true` if activated; otherwise `{"proposal","activated":false,"generation"}` |

Removed verbs `validate`, `resolve`, `prepare`, and `activate` exit non-zero with a
message naming the replacement (`publish --dry-run`, `apply --proposal-sha256`,
`apply --dry-run`, `apply`). Each verb defines only the flags listed above; any
other flag — including the retired `--approved-proposal`, `--workflow-actor`,
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
- `apply`/`rollback` may fail **after** the CAS committed if stdout delivery fails. The
  error then starts with `activated; output observation degraded`. Do not create a new
  proposal: run `status --proposal` (or re-run `apply` with the same proposal) to
  reconcile the outcome without creating another activation.

## Registry layout

```
<root>/artifacts/<sha256>.json               immutable v2 manifests (candidate, selection_set, proposal)
<root>/state/<env>/<target>.json             mutable, generation-CAS'd control state, one per target
<root>/router_serving/v1/<kind>/sha256/<sha256>.json     v1 manifests — read-only history
<root>/runtime_state/router_serving/v1/targets/<env>/<target>/state.json   v1 state — read-only history
```

`<env>` is `staging` or `prod`; `<target>` is `staging`, `prod/stable`, or
`prod/weave-internal`. Beta targets are rejected.

Objects under `artifacts/` are content-addressed by the digest of the exact bytes
published; the object's `schema_version` selects the decoder, and `artifacts/` holds
only v2 schemas. Objects under the legacy `router_serving/v1/...` namespaces decode
forever as v1 objects, because the activation history inside every state object
references them and content-addressed objects can never be rewritten without orphaning
those references. Each v2 kind is a read-side family that also admits the v1 object it
folds (`candidate` ⊇ `releases`, `selection_set` ⊇ `selection_sets`, `proposal` ⊇
`proposals`), so a v2 proposal may name a v1 `previous_selection_set` and
`apply --proposal-sha256` resolves `artifacts/` first, then the legacy namespace. New writes go only
to `artifacts/`: a publish with a v1 kind (`releases`, `classifiers` → `candidate`;
`bindings`, `profiles`, `selection_sets` → `selection_set`; `proposals` → `proposal`)
is rejected before the registry opens, naming the kind it folded into.

Target state moves lazily: readers try `state/<env>/<target>.json` first and fall back
to the legacy `runtime_state/...` object. The first `apply` on a target under this
layout reads whichever exists and CASes the new path with a `DoesNotExist`
precondition; the incumbent binding comes from the proposal's `previous_selection_set`,
so no generation is transcribed between paths. Nothing needs manual migration and
in-flight v1 proposals still apply.

### Fleet-rollout gate

Every `ReadServingState` reader — the gateway, managed workers, and
`scripts/serving_admission_check` — must run a binary that understands the
`artifacts/` layout, the v2 kinds, and the `state/` path **before** the first v2 or
new-path `apply` against a target. An old binary that keeps writing the legacy state
path after a new-path object exists splits the control plane, and an old binary cannot
decode a target whose current selection set is v2, so its admissions fail closed. Gate
the first apply per target on image rollout, not on merge. After the first v2 apply on
a target, reverting the binary is a one-way door for that target; the designed
object-level rollback is a `scope: rollback` proposal, not a code revert.

## Manifest kinds

Three immutable kinds are published; every field is required unless marked optional.
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
`replacement_id`. A v2 proposal may reference a v1 selection set — rollback must name a
previously activated set, and the history may be v1.

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
3. `apply --dry-run --proposal <ref>` for a no-write preview, then `apply` from the
   protected environment. Retry `apply` with the same proposal if the outcome is
   ambiguous; it replays the original activation instead of creating a second one.
4. `status --target` after activation, or `status --proposal` to reconcile one run.

Profile registration and version changes use a `profile`-scoped proposal. Forward
scopes preserve registered profile keys and revisions. An exact whole-selection
rollback uses `scope: rollback` through `serving rollback`; only that scope can restore
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

## Local verification

### Default-off and managed boot

An unset or whitespace-only `ROUTER_SERVING_ASSERTION_KEY` keeps the worker on its
existing managed/self-hosted path. It does not read serving control heads, subject
projections, session bindings or request-attribution tables. A nonempty weak key
fails boot; it does not silently fall back. Apply additive router migrations
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

Tests include a real Cloud Storage client against an ephemeral local JSON API
fixture, immutable publish collisions, generation-CAS conflicts, exact-generation
reads, v1/v2 dual decode and the lazy state-path bootstrap, private TLS endpoint
validation/redirect rejection, auxiliary/config mismatches, no-write dry-run publish
and apply, emergency rollback, rollback-scope rejection, superseded idempotent
retries, removed-verb rejection, and committed-output failure reconciliation. They
do not establish live IAM, image attestation, infrastructure ownership, or
environment-local latency; those remain authorized rollout checks.
