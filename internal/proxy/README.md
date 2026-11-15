# internal/proxy

`proxy.Service` (`service.go`) orchestrates request parsing, routing, credential
resolution, dispatch, failover, telemetry and billing for the three inbound
surfaces (`ProxyMessages`, `ProxyOpenAIChatCompletion`, `ProxyOpenAIResponses`).
Feature flags and their descriptions live in `internal/flags`; environment
variables are in [CONFIGURATION.md](../../docs/CONFIGURATION.md).

## Request-path ordering

- Session keys (`bindRequestLogger`, `deriveForceModelSessionKeyForRequest`)
  must be derived from the unstripped opening message. Derivation can fall back
  to prompt text, so the key would differ across turns only if it read text that
  command extraction removed; `StripHistoricalCommands` keeps the original
  opening text. `TestForceModelCommand_PinKeyIsDerivedFromUnstrippedOpeningMessage`
  (#2242) pins this.
- Extract commands before `CoalesceDuplicateToolResults` and
  `SanitizeOrphanedToolCalls`: a skill can encode its command as a plain user
  string after an assistant `tool_use`, and sanitizing first erases the
  provenance and leaves a dangling `tool_use` that some upstreams reject.
  `TestProxyMessages_ForceModelAfterOrphanedToolUseStaysAgentIssued` and its
  chat-completions twin pin this.
- Strip the Claude Code 1M-context model tag (`claude-opus-4-8[1m]`) to the
  canonical model id before parsing; it is a client display convention and a
  native Anthropic upstream 404s on it. The 1M window is enabled separately via
  the context-1m beta.
- Snapshot inbound state (compaction tracker, spiral scan, tool-output
  telemetry) before any envelope rewrite such as the switch handover.
- Skip the compaction-aware handover when a model-switch handover or the
  proactive compaction cascade already rewrote the envelope this turn; a second
  rewrite would double-trim it.
- Every surface wraps the client sink in the prelude buffer, even for a single
  binding, so a pre-first-byte upstream failure can drop the routing marker and
  retry or render a clean error. Skipping it for single-binding requests once
  left Claude Code with only marker text and no `tool_use` after an `api_error`;
  the cost is one round-trip of buffered SSE bytes. The keepalive writer is the innermost wrap and
  only arms once the prelude commits, so a ping can never strand a response the
  router still wants to retry.
- A committed Responses stream that fails upstream is terminated with
  `response.failed` instead of a JSON error envelope.

## Routing and eligibility

- `routeFor`: the `rl` strategy fails closed with `ErrPolicyUnavailable` (HTTP
  503) when the policy router is not wired; there is no silent fallback to the
  cluster scorer.
- `routeWithStrategy` re-checks `catalog.CheckEligibility` on the decision as a
  last product-boundary gate, catching a model that came from a pin, fallback
  table or deployed-set default rather than the filtered pool.
- `modelPermittedByAllowlist` must be used directly for passthrough-only models
  outside `routableUniverse`. A nil allowlist means no restriction; an empty
  effective intersection fails closed. Subscription-state model sets never widen
  the installation allowlist.
- `extendedContextTriggerTokens` (140K estimated) enables the context-1m beta
  well below the 200K window because `FullTokenEstimate` undercounts dense
  Claude Code bodies by roughly 20-30%.
- Recovery-probe leases (`localRecoveryProbeLease`) carry an id so a stale
  release cannot free a newer lease, and an expiry so a request that never
  released cannot deny the model forever. The lease outlives a known request
  deadline by one minute.
- Session-level writes that run after streaming (cooldown clears, re-pins,
  billing) use `context.WithoutCancel`: the request context is already canceled
  once the stream finishes.

## Session pins

- User-forced pins (`translate.ReasonUserForceModel`, including the tier-clamp
  suffix) record `pinNeverExpires` and clear only on `/unforce-model`; every
  other pin uses the sliding `pinSessionTTL`. The sentinel stays inside the
  Postgres timestamp range.
- Hard-pinned turns (probe, compaction, title generation, classifiers) keep a
  zero session key; nothing persists a pin under the zero key.
- A cyber refusal never overwrites a `/force-model` pin.
- A committed stream that died upstream, or a primary handed to a sibling
  pre-commit, is struck so the next turn does not re-pick the same arm. A
  schema/capability rejection evicts the pinned arm even when a rescue served.
  The two-strike provider disable is skipped after a baseline rescue, whose
  provider is not the sticky pin's.

## Credentials

- `PassthroughEligible` is surface-scoped: an Anthropic-surface request can use
  a client Anthropic key but never reach OpenAI with it, and vice versa.
- Claude (`sk-ant-oat-`) and Codex (ChatGPT JWT + account id) OAuth bearers in
  the inbound `Authorization` are honored on router-keyed requests; general API
  keys are not forwarded on the router-key path.
- An observed-exhausted (or overage-drawing) subscription is suppressed per
  provider (`withSuppressedClaudeSubscription`,
  `withSuppressedCodexSubscription`). The suppressed token must not come back as
  the inbound bearer, and `resolveAndInjectCredentials` clears the context
  credential so the provider client falls back to the deployment key.
- Each failover attempt re-resolves credentials for the binding that actually
  served; subscription suppression carries forward so cost attribution and the
  billing key reflect the key that paid.
- Subscription-only mode refuses with 402 when routing did not resolve to the
  caller's own subscription credential, rather than billing a paid model
  against a negative balance. A linked-first turn, whose organization credits
  are intact, continues paid unless its spent plan is the only credential.
  Served-on-subscription turns pin dispatch to the single subscription binding,
  and a dispatch failure there returns the raw upstream envelope.
- The usage-bypass gate falls back to `defaultUsageBypassThreshold` (95%
  utilization) when an installation enabled it without choosing a threshold.

## Telemetry and billing

- `emitBilling` debits once per upstream call, plus a second `_summary` debit
  for a switch turn that invoked the handover summarizer. The summary runs on
  the deployment/BYOK key, never the subscription token; `byokServedForProvider`
  bills it at the fee rate when a BYOK key paid. Unknown summarizer models price
  at zero rather than skipping the ledger row.
- `fireBilling` is synchronous (durable before the handler returns) on a
  detached five-second context, so client cancellation cannot drop the write.
- Eval rows never enter serving telemetry.
- Nullable telemetry stays NULL rather than zero when unknown: cache-token
  pointers for zero counts, pin age without a computed age, tool counts on a
  stream whose end was not observed, and argument validation on native
  passthrough.
- `refusalObserver` scans at most `refusalScanCap` bytes, rescanning a
  `refusalScanOverlap` window so a signal split across chunks is caught, and
  extracts the category from the whole prefix. The category is telemetry only.
  Refusal detection is unconditional; only the re-pin is behind a kill switch.
- `isDegenerateResponse` flags fewer than `degenerateOutputThreshold` output
  tokens with no tool calls and a natural `end_turn`; cross-format stop-reason
  demotion is excluded.
