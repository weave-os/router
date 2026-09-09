# Serving through classifier failures

Automatic HMM routing preserves the original classification evidence before
compaction. The provider receives the effective compacted envelope, with the
current user task and recent complete tool exchanges retained. Input remains
bounded by ingress limits; the classifier projection keeps at most 96 messages,
3,000 bytes per text and 48,000 text bytes total. Original turn counts,
history truncation and permission-gated training deltas retain their source.

Classifier errors never authorize a provider call. The serving layer discards
failed classifier output and independently calls the registered main-inference
policy resolver. Recovery prefers a live, successfully served session identity,
then the configured preference, then the eligible local catalog ordered by the
requested tier and estimated cost. Alternatives prefer another provider. Each
model receives its own immutable plan; the executor remains the dispatch owner.

Allow/exclude lists, request credentials, gateway aliases, translation support,
effective provider context windows and spend constraints apply to every plan.
No sidecar roster or artifact is needed to build the local recovery set. Explicit
model/cluster controls, utility hard pins and evaluations keep their contracts.
Shadow/evaluation classification faults remain failures.

## Bounds and state

- The existing total policy timeout is shared across classification reroutes.
  Attempt timeouts and bounded transport retries stay inside that deadline.
- A client admits at most 64 concurrent decisions. Admission is nonblocking.
  Five endpoint failures open its circuit for 30 seconds; one half-open probe
  tests recovery. Separate stable/beta clients have independent state. A
  request-specific evidence rejection does not open a shared circuit.
- Recovery allows at most eight provider dispatch calls and starts no new call
  after its 30-second retry window. This includes protocol/fast-mode retries
  inside the proxy. Provider adapters retain their bounded HTTP endpoint
  discovery probes. An already serving stream is not interrupted by this bound.
- Client cancellation prevents new dispatch. Synthetic routing preludes may
  survive a retry; committed provider output prohibits any model switch.
- Only a successful recovered turn updates operational serving history. It
  creates no classifier scores, propensity, artifact identity or training
  outcome. Subsequent turns attempt classification again when the circuit allows.

## Configuration and readiness

`ROUTER_POLICY_DEADLINE_FALLBACK` remains the compatibility kill switch and now
defaults to `true`. Its scope is all classifier dependency failures, including
400, authentication errors and malformed responses. Setting it to `false`
restores strict serving failure. `ROUTER_POLICY_DEADLINE_DEFAULT_MODEL` is a
preference, not authority: an excluded or incompatible preference is skipped.
Existing policy timeout environment variables retain their meanings/defaults.

The local recovery policy is initialized before the HMM client/roster. Invalid
HMM configuration may boot in degraded serving mode only when recovery has a
valid local target. `/health` remains process liveness. `/ready` requires the
database and either primary policy health or valid local recovery; its degraded
response explicitly reports `primary_routing: unavailable`. Readiness is a
deployment check; it cannot promise that every tenant has an authorized target
or that an upstream provider will accept its next call.

`router.policy.recovery` records bounded failure reason and recovery outcome,
including authorization failures before provider dispatch. Correlated request
logs retain the failure, selected model/provider, candidate count and policy
revision. Do not put prompts, keys, tenant IDs or session IDs in metric labels.
`router.policy.decision.duration` includes admission rejection and records the
failure reason and circuit state. Debug logs report source message/text counts,
dispatched message count and whether a real textual boundary was present.

## Regression and release checklist

Run the Go suite, the bundled sidecar suite, generated policy/agent-guide checks
and inference-boundary checks. The main required Test job depends on the bundled
sidecar gate, which also runs concurrent classifier outage recovery.

The incident probe uses a cold session, a long synthetic tool loop, real
compaction, the observed classifier rejection and a local fake provider. It
checks the retained task, actual classifier wire history and completed answer:

```sh
go test ./internal/proxy -run 'TestCompactedToolLoopRetainsOriginalClassifierEvidence|TestPolicyFaultRecoveryAcrossServingSurfaces' -count=1
ROUTER_RECOVERY_LOAD=1 go test -race ./internal/proxy -run TestRecoveryLoadAndClassifierRestoration -count=1
```

These probes make no paid inference calls. The load probe serves 10,000 requests
at each of concurrency 32 and 128 and verifies primary classification resumes
after the injected outage heals; it is not a production capacity benchmark.

The managed raw-V5 feature builder is owned by the consuming deployment. Before
its release, validate synthetic classifier requests with that exact revision's input
validator and feature builder; record the router commit, managed sidecar commit,
classifier/roster artifacts, effective configuration and deployment revision.
Replay classifier rejection, timeout, malformed output and unavailable-first-
provider cases against fake inference endpoints, including streaming and a
session with existing history. Do not infer managed compatibility solely from
the bundled feature builder passing.

Canary the separately approved release while observing classifier failures,
recovery success/exhaustion, latency and provider spend. Confirm a healed
classifier resumes selection. Roll back the router pin or disable recovery only
as an explicit operational decision: the kill switch brings dependency-driven
503s back. Router PR publication does not deploy the managed service or update
its artifact pins.
