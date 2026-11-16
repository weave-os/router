# internal/requestcontext

Request-scoped, provider-neutral values a provider adapter needs to shape one
upstream call: resolved credentials, effective base URL and model alias, caller
identity and forwarded correlation headers. Inner-ring and I/O-free, so
adapters depend on it without importing the dispatch orchestrator.

## Credentials

`ExtractClientCredentials` reads client-supplied keys and never forwards a
router-issued key (`auth.APIKeyPrefix`, which clients send on the same headers):

| Provider family | Accepted | Rejected |
| --- | --- | --- |
| Anthropic | `x-api-key` starting `sk-ant-`; `Authorization: Bearer sk-ant-api-…`; a Claude subscription bearer (`SubscriptionTokenPrefix`, OAuth) | any other `x-api-key` (e.g. an OpenAI key) |
| OpenAI-compatible, Gemini | `Authorization: Bearer` | `sk-ant-` keys and OAuth bearers; unknown families get nil |
| OpenAI only | `CodexSubscriptionCreds` turns a ChatGPT JWT plus `ChatGPTAccountIDHeader` into Codex subscription credentials (principal `chatgpt-account:<id>`, stable across token refresh) | not a rejection: `sk-` keys and a JWT without an account id (which the Codex backend would 401/403) fail only the subscription check and are still returned as ordinary `SourceClient` bearer credentials |

`Credentials` notes:

- `APIKey` and `AccountID` are never logged; log only `Source`.
- `SubscriptionPlan` comes only from an authenticated provider profile.
- `OAuth` bearers authenticate with `Authorization: Bearer`, never `x-api-key`.
- `BaseURL`, `ModelAliases` and `FundingPriority` are set only on BYOK
  credentials. A model alias changes only the outbound wire name; routing,
  pricing and telemetry keep the catalog ID. `ApplyModelAlias` rewrites when an
  alias is present, even one equal to the catalog ID, so it overrides an
  adapter's catalog upstream ID.
- `CredentialError` stops an unavailable BYOK key from falling back to the
  deployment credential for that provider.
- `PrincipalID` names the upstream account independently of the key bytes
  (workload-identity and keypair-JWT credentials mint a bearer per request);
  `UpstreamPrincipal` falls back to the key and reports it as secret.
- `WithCredentials(ctx, nil)` is an explicit "none": the adapter uses its
  deployment key.
- `CodexSubscriptionCoversModel` is the curated automatic roster;
  `CodexSubscriptionCanAttemptModel` also allows approved catalog fallbacks
  with an OpenAI binding. Unknown models fail closed.
- `ApplyWIFTokenType`, `ApplyIdentityHeader` and `ApplyForwardedClientHeaders`
  run after the prepared headers are copied, so a client value cannot override
  them.

## Forwarded headers and identity

- `WithForwardedHeaderSnapshot` captures inbound values of every header the
  installation's external keys forward, so router-built requests (compaction
  summaries, web search) stay correlated. `forwardedValue` prefers the inbound
  header, then the snapshot, then (for `ClaudeCodeSessionHeader` only) the
  resolved session id.
- The baggage header is a raw JSON object (no percent-encoding). The router
  overwrites `on-behalf-of` and `passthrough` so forged attribution cannot
  survive; non-JSON and `null` bags pass through unchanged.
- `IdentityHeaderValue` sends the bare email or a percent-encoded JSON bag
  using `%20`, not `+`, because `decodeURIComponent` reads `+` literally.
- `ClientIdentity.ClientApp` is the canonical harness every harness-keyed
  behavior reads, so eval traffic is served like the client it imitates;
  `Eval` only changes `TelemetryClientApp`. `OpenCodeSubagent` is routing
  metadata, never auth, billing or eligibility.
- `WithInternalTestIdentity` is set only after gateway assertion verification;
  `WithClientIdentity` then replaces caller-asserted attribution with it.

## Conversation identity

`CanonicalConversationID` applies header precedence and the Anthropic body
overlay, then OpenCode's native session headers (a subagent resolves to its
immediate parent), then envelope metadata, never mutable message content.
`ConversationFingerprint`, for clients without a session id, uses
`prompt_cache_key`, then a hash of the first user message; callers scope it by
credential. `MetadataSessionID` bounds intentionally differ from the header
overlay: changing them would break stored pins. `ConversationKey` byte
namespaces are golden-pinned for the same reason.

## Other context values

- `ContentLoggingAllowed` defaults to true when unset (admin, passthrough,
  tests); the serving path clears it for zero-retention installations so
  adapters and httputil never log their bodies.
- `ServingIdentity` is verified admission metadata, never client headers.
  `ServingStateKey` returns the key unchanged for legacy requests and a
  namespace-scoped digest for managed ones, so late writes cannot mutate a
  rebound conversation.
- `WithSubscriptionExtraUsage` allows the caller's own subscription overage
  (Codex credits, Claude Enterprise overage) without lifting cooldowns.
- `ClientBudgetFrom` absence means unknown, not a catalog window.
