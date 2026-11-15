# Failover, rescue and dispatch errors

How `internal/proxy` recovers a failed turn, what it remembers about the failure, and how the failure reaches the client. Terminology follows [SEMANTICS.md](SEMANTICS.md); subscription funding rules are in [SUBSCRIPTION_ROUTING.md](SUBSCRIPTION_ROUTING.md); stream-commit and cancellation invariants are in [INFERENCE_BOUNDARY.md](INFERENCE_BOUNDARY.md).

## Dispatch plans and the prelude

- `dispatchPlanned` runs a surface's attempt closure through the dispatch executor and keeps the surface-level contract: per-binding headers, credential re-resolution on fallback, managed-subscription leasing and account rotation, prelude discard on retry, and the entrypoint's own error rendering on exhaustion.
- A turn can retry or fail over only while its prelude is buffered (`preludeBuffer.Committed` is false). After commit the only way to report a failure is an in-stream SSE error frame; a JSON envelope appended to a live stream is unparseable to the client. Native Responses bytes pass through verbatim, so no frame is synthesized into that stream.
- Fallback attempts re-resolve credentials against an empty header set: `shouldFailover` already ruled out BYOK and client-credential paths, so the only remaining source is the deployment key.
- A managed-subscription turn never fails over to a paid binding. `dispatchAbort` marks a failure (for example, a credential lease that could not be obtained) that the surface returns as-is, with no failover and no envelope.
- The terminal eligibility gate (`CheckEligibility` against the request's model boundary) runs before the first binding is tried.
- When an upstream error is replayed to the client, hop-by-hop headers, `Content-Length` and `Content-Encoding` are dropped: the body is re-rendered. A context overflow is not replayed raw; it is rendered in the client's native prompt-too-long shape.

## The rescue chain

`rescueChain` is one turn's walk from the routed attempt through its rescues. It owns the served decision, bindings, marker and error while the walk runs. The failed primary's decision and error are captured before any rescue runs, because failover rewrites `decision.Model`; post-turn strikes always target the primary, never the rescuer.

Order on `/v1/messages` (`messagesRescue`): baseline rescue (re-dispatch the requested model on Anthropic) or the subscription retry (suppress the OAuth token and retry the same model once on the Weave or BYOK key after a 429, timeout, 401/403, model-access 404 or third-party-client 400), then the shared sibling rescue last.

Order on Chat and Responses (`chatRescue`): Codex and Claude subscription retries, the cyber-refusal retry, then the sibling rescue.

Invariants:

- The deferred upstream error reaches the client exactly once. Whichever rescue owns the deferred flush writes it when it declines to run; a failed retry keeps the same dark model and holds the error so the sibling rescue can still serve.
- A managed-pool failure is rendered by the handler from the classified sentinel pre-commit; post-commit the proxy writes an in-stream frame but still returns the sentinel so the handler classifies and logs it.
- Telemetry (`crossFormat`, response summary, request stats) reflects the binding that actually served; `baseline_failover` is true only when the baseline actually served.
- Paid recovery is suppressed when credits are depleted. When the plan credential is the only reason the walk is open, the sibling walk waits for the plan's own 5xx; a 429 or timeout on the plan does not buy a paid sibling.

### Sibling rescue candidates

- Roster-backed routes use only their ordered eligible groups; other routes append the candidate pool and paired model and prefer other providers. Peers whose window cannot fit the prompt are rejected.
- Every in-turn rescue (sibling failover and the cyber-refusal retry) resolves candidates under the same availability, exclusion, context-fit and BYOK-gateway rules (`rescueDecisions`). A model the pool already excluded is never resurrected. The walk never returns the model that just failed.
- `rescueExcludedModels`: deployment and session demotions, models leased by another probe, and models the thread's billed usage says the request overflows.
- BYOK disables cross-provider failover (a foreign provider would 401), but gateway-aliased candidates reuse the held credentials and stay eligible (`gatewayRescueDecisions`); candidates behind a different gateway binding rank first. A gateway overflow walks only the other gateways, since same-gateway peers share the window that rejected it.
- A Codex or Claude plan is a funding source, not the only credential, so sibling rescue stays allowed for plan-served turns (`subscriptionSiblingAllowed`).
- Last resorts, in order, once every eligible candidate has failed pre-commit: arms in a rate-limit cooldown (soonest to recover first, ties by name), models whose overload circuit is open on this replica, then session-lifetime strikes. Readmission lifts only the soft exclusion, stays inside the turn's scored pool (`strikesInRescuePool`, `rosterRescueAdmits`), and never applies to the cyber-refusal retry.

### Cyber-refusal retry

OpenAI's cybersecurity classifier declines the turn itself, so the retry must leave the vendor: a target on OpenAI is rejected. The gate is armed before the first upstream byte and withholds the preamble (bounded by `cyberRefusalHoldCap`) until it knows whether the stream is a refusal; while holding, `Flush` does not reach the client. The gate forwards `ArmOutputProgress` to the wrapped writer so the output-progress watchdog stays armed.

## Strikes, demotions and cooldowns

- `maybeDemoteArmAfterCommittedStreamFailure`: one strike after a committed stream died upstream, since such a turn can neither retry nor fail over.
- `maybeDemoteArmAfterRescuedFailure`: strike the primary after a pre-commit failure the sibling rescue handled, whether or not the rescuer served. Under `transient_rate_limit`, a buffered 429 or gateway 5xx gets a time-limited cooldown (`ROUTER_RATE_LIMIT_COOLDOWN_SECONDS`, see [CONFIGURATION.md](CONFIGURATION.md)) instead of a session-lifetime strike.
- No strike when the flag is off, the pin row is unaddressable, the turn was hard-pinned (utility turns and the native web-search passthrough write no session routing state), the pin was user-forced (`ReasonUserForceModel` and its tier-clamp suffix), the failure is not upstream-owned, or the caller's per-cluster allowlists name no other model (`clusterAllowlistsPinModel`; withdrawing it would fall open to another vendor). Strikes are not gated on a sticky hit.
- Strikes are soft (`AutomaticExcludedModels`): every enforcement site keeps the unfiltered pool when the exclusion would empty it, so a strike never widens the models configuration lets automatic routing choose.
- Arm-owned failures only (`isRescuedPrimaryFailure`): a 404 is remembered per gateway endpoint, a 529 belongs to the overload breaker, a managed-pool error belongs to `maybeExpireSubscriptionArmPin`, and a bare cancellation is the client leaving.
- Watchdog sentinels (idle, output stall, slow throughput) are checked before cancellation: the watchdogs cancel the dispatch context with the sentinel as its cause, so a bare context check would misread a stall as a client disconnect.
- Post-commit SSE error renderers return a synthetic status wrapper; `dispatchErrorBehindFrames` peels every such wrapper (the Anthropic path can frame twice) and keeps statuses read from real responses.
- `demoteArmForSession` strikes every row the next turn merges (the sticky-state role, the base pin role and its HMM history row), expiring each in one strategy-guarded write so a late failure cannot reclaim a row another strategy already replaced. Writes use a context detached from cancellation so the strike lands after the client left.
- Pin eviction: a deterministic 4xx dead arm is evicted at once; other failures take `pinEvictionStrikeThreshold` (two) consecutive strikes.

## Provider overload (529)

- `maybeDisableProviderAfterOverload` expires both the active pin and the HMM history row, because `hmmStayPin` treats each as a stay candidate.
- The per-model circuit breaker (`ROUTER_PROVIDER_OVERLOADED`, overridable per organization) opens after `providerOverloadStrikeThreshold` overloads. Organizations see shared circuits they confirmed with their own failures plus their own gateway circuits. A half-open probe readmits the model for one request; a 529 through an organization's own gateway is evidence about that endpoint only.

## Context overflow

- The router never rewrites client history. The client receives its native prompt-too-long error and compacts itself. `contextOverflowMessage` starts with "prompt is too long" because Claude Code and opencode key reactive compaction on that phrase; OpenAI clients get `context_length_exceeded`.
- Upstream overflow is detected from provider error bodies (`upstreamContextOverflowMarkers`); rate limits never qualify.
- A request served on a larger-window model past the requested model's window is answered as an overflow, because the client budgets compaction against the window it asked for.
- A compaction turn that overflows every summarizer reports `CompactionUnservableError`, which does not unwrap to the overflow: answering a compaction turn with prompt-too-long would loop.

## Codex subscription quota

- `usage_limit_reached` and `insufficient_quota` mean the caller's plan cannot serve the turn. The turn falls back to an OpenAI API key (per-request BYOK or `OPENAI_API_KEY`) and the plan is marked spent. Without a fallback key, dropping the token is strictly worse than surfacing the upstream error.
- `codexExhaustionWindow` widens the recorded window to reach the reported reset, so a weekly limit is not forgotten after the default rolling window.

## Dispatch error classification

`ClassifyDispatchError` maps sentinels to client-facing classes; callers handle mid-stream failures and format-specific sentinels first. Order matters where sentinels wrap each other:

- a billing-gated pool refusal is classified before the pool cases;
- `ErrGatewayServesNoDeployedModel` before the generic policy-unavailable case it wraps;
- `ErrAllowlistEmptiesPool` before `ErrNoEligibleProvider`, which it wraps;
- context overflow before the generic upstream-status cases, so every client sees its native shape;
- upstream statuses are preserved so a buffered 429 or 5xx is not rewritten.

`FailureOwner` attributes each failed attempt (customer, router, provider) for telemetry; a committed stream the client abandoned is attributed to the cancellation, not the synthetic 502. Terminal messages name the owner of the failing attempt (a customer gateway by host) without changing status or error type.

## Stream cuts

`streamCutObserver` tees the attempt's writer and records the wire state when a committed stream dies: frame count, last event type and the gap since the last frame. It restarts for every attempt and keeps the first observation. Anthropic-shaped streams also record whether a content block completed, since Claude Code retries a cut turn only before that. Observers forward `ArmOutputProgress`; hiding it would downgrade the stream to byte-idle guarding.

## Translation planning

`planTranslation` restricts candidates to targets the compatibility matrix proves lossless. `ErrTranslationIntrinsicallyIncompatible` means no preserving target exists; `ErrTranslationCompatibleProviderUnavailable` means one exists but is not configured or authorized. Native search tools never cross provider families regardless of the rollout mode. See [TRANSLATION_COMPATIBILITY.md](TRANSLATION_COMPATIBILITY.md).
