# Managed serving controller contract

The `policyctl serving` group is the low-level interface for a trusted deployment
workflow. It does not build images, deploy Cloud Run services, change revision tags,
grant IAM access, or turn on gateway-managed serving. Those preparation steps belong
to the environment's orchestrator. Existing policy commands remain the legacy
interface; they must not be used to manage migrated targets.

## Immutable proposal lifecycle

Publish canonical manifests with `serving publish --kind <kind> --manifest <file>`;
`serving validate` checks the same bytes without opening the registry. Supported
kinds are `classifiers`, `releases`, `bindings`, `profiles`, `selection_sets`, and
`proposals`. All commands accept `--registry gs://<bucket>/<prefix>`.

The workflow composes the full destination selection set and proposal, preserving
registered destination profiles. A proposal binds the source release, target,
scope, complete selection set, previous selection set and expected GCS generation,
operator, reason, request UUID, evidence references, and explicit emergency
withdrawals. Publishing never activates it. The exported Go contracts in
`internal/policyregistry/serving_contract.go` define the exact JSON schemas.

| Command | Required inputs | Effect |
| --- | --- | --- |
| `serving resolve` | `--proposal-sha256 <digest>` | Resolves and validates the immutable proposal; outputs its URI/digest/storage-generation `ObjectRef`. Run before approval. |
| `serving prepare` | `--proposal <ObjectRef.json>` | Checks the bound destination generation, every default/profile tuple, private classifier attestation, and private worker snapshot readiness. No activation or infrastructure writes. |
| `serving activate` | `--proposal <ObjectRef.json>` | Revalidates then CASes the target's single control-state object, atomically superseding its outgoing activation. |
| `serving rollback` | Same inputs as activation | Uses the same CAS path and requires a source previously serving the same target/profile. Normal rollback retains pins; emergency withdrawals must already be in the approved proposal. |
| `serving status` | `--target <target>` **or** `--proposal <ObjectRef.json>` | Authoritative target state or outcome reconciliation for that exact proposal. Never validates against currently reachable workers. |

Targets are `staging`, `prod/stable`, and `prod/weave-internal`; beta is rejected.
Profile registration/version changes use a `profile`-scoped proposal, not another
target or writer. Forward scopes preserve registered profile keys and revisions.
An exact whole-selection rollback uses the immutable proposal scope `rollback`:
its selection-set URI, digest, and storage generation must match an activation
in the same target's history, and its source release must equal that set's default
release. Only this scope can restore the historical profile inventory, including
removing profiles introduced later or restoring older profile revisions.

Both `prepare` and `activate` enforce this scope; `serving rollback` additionally
checks historical source provenance for legacy component/profile rollback callers.
All destination tuples still require artifact and private readiness validation.
The same approval and generation CAS apply. Normal rollback retains existing
session pins; a fresh request for a profile absent from the restored set fails
closed instead of falling back to the default. Historical manifests and revisions
remain retention roots and are not deleted.

## Non-circular configuration and evidence

Configuration artifacts cover effective behavior-affecting settings and exact
secret-version references, including transport endpoints/audiences and timeout
settings. They exclude only bootstrap back-references such as a selection-set
reference and evidence references. Do not exclude behavior-affecting transport
configuration merely to make composition easier. Bootstrap references are
validated separately as exact immutable inputs, not treated as behavior config.

Create physical-revision evidence from the deployment control plane before
private readiness: exact revision, resolved OCI image identity, effective
configuration, secret versions, target and environment. This is what a binding's
`attestation` references; it must not reference the containing binding, selection
set, or later successful smoke. Publish binding and selection-set identities next,
then perform private readiness/smoke against those identities and publish the
proposal's evidence. This order avoids configuration→selection-set→binding→
configuration and binding→post-readiness-evidence→binding digest cycles.

The controller verifies that every proposal evidence artifact, selected/source
build attestation, and selected physical-binding attestation exists at its exact
GCS generation and matches its digest (audit payloads are bounded to 8 MiB).
This is byte integrity, not an invented proof format or signature verifier.
Evidence semantics/provenance are trusted assertions by authorized registry
writers, independently supplemented by actual destination validation. Environment
integration must authorize those writers, verify real image/configuration state,
and bind later smoke/evaluation evidence to the exact tuples tested. Derived
customer or component-only tuples do not inherit a source whole-release evaluation
claim; their own fresh destination validation is mandatory.

Approval is enforced by the orchestrator's protected environment and restricted
registry-writer identity. The activation records the executing identity as
`<GITHUB_ACTOR>@run:<GITHUB_RUN_ID>`, else `$USER`, else the proposal's operator.

**Deprecated flags:** `--approved-proposal`, `--workflow-actor`, and
`--validation-origin` are still parsed but ignored (a one-line warning is written
to stderr) and will be removed.

A CAS conflict requires a fresh preview/confirmation, not a changed generation
inside the old proposal. Reusing a request UUID with different proposal bytes is
a conflict. Retrying an activated proposal returns its original activation, even
after supersession or a worker outage; it never makes it current again. If output
delivery fails after the CAS, the error says `activated; output observation
degraded`: reconcile the same proposal with `status`, rather than creating a new
deployment. A new promotion of identical release bytes intentionally creates a
new activation incarnation without resetting older supersession deadlines.

## Private destination validation

Validation runs from an authorized environment-local runner that can reach both
private revisions. The worker/classifier revision HTTPS origins and IAM service
audiences come from the immutable binding selected by the proposal.
There is no HTTP, redirect, public-readiness, or control-plane-only
bypass. Requests have a two-minute end-to-end deadline so zero-traffic revisions
can cold-start, and responses have a 1 MiB bound.
Google identity tokens travel in `X-Serverless-Authorization`, preserving the
normal routing/subscription `Authorization` header.

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
references must match the proposed bundle, including storage generations.

The legacy classifier `/readyz` response attests only the core identity and does
not satisfy this contract. Environment integration must deploy the full
classifier attestation endpoint before managed preparation/activation can pass;
the controller deliberately fails closed until then. Current default-off worker
and legacy policy operation do not require this new endpoint.

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
dispatched. PR2 deployment configuration must use `/readyz` for traffic-admission
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
manifests, compiled catalog compatibility, and physical worker identity. It reads
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
fixture, immutable digest tampering, generation-CAS conflicts, exact-generation
reads, private TLS endpoint validation/redirect rejection, auxiliary/config
mismatches, CLI approval binding, no-write preparation, emergency rollback,
superseded idempotent retries, and committed-output failure reconciliation.
They do not establish live IAM, image attestation, infrastructure ownership, or
environment-local latency; those remain authorized rollout checks.
