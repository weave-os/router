# providers

The upstream client interface (`providers.Client`), the provider registry, upstream error classification and shared wire helpers. Concrete adapters live in the subpackages; `httputil` holds the shared transport and stream watchdogs.

## Adding a provider

A provider is three entries that must move together: its `Provider*` constant, its `providers.APIKeyEnvVars` entry and its `providers.ProviderFamilies` entry. A provider without a family falls through to `ErrProviderNotConfigured` at dispatch, a 502 for a provider that looked enabled at boot. `providers.ValidateDispatchable` runs at boot (the composition root panics on error) and `families_test.go` fails when any of the three maps disagree.

`providers.TranslationFamily` is what the proxy dispatches cross-format translation on, so an OpenAI-compatible provider routes correctly as soon as it has a family entry. Speaking the Anthropic wire format does not imply executing Anthropic server tools: gateways relay to function-tool-only backends (`providers.SupportsAnthropicServerTools`).

Gateways (`providers.ProviderAnthropicGateway`, `providers.ProviderOpenAIGateway`) are customer-hosted, per-tenant endpoints with no vendor default. A gateway serves only the models its key's aliases name, so routing treats it as the installation's exclusive upstream (`providers.IsGateway`), and a credential without a base URL is undispatchable (`providers.RequiresBaseURL`). Their keys pair with `ANTHROPIC_GATEWAY_BASE_URL` / `OPENAI_GATEWAY_BASE_URL`.

## Usage and caching

xAI reports `completion_tokens_details.reasoning_tokens` on top of `completion_tokens` while billing reasoning at the output rate, so billed output is output + reasoning (`providers.OutputExcludesReasoning`). Every other OpenAI-compatible upstream, including OpenRouter's xAI route, folds reasoning into `completion_tokens`.

`providers.CacheTTLFor` is a best-effort prompt-cache lifetime. Anthropic's 1h extended cache is what session pins are sized to; OpenAI-compatible providers cache on an undocumented minutes-scale window, so the planner stops crediting a pin with a cache-read discount once it outlives the provider's cache. Gateways publish no cache lifetime and keep the conservative `providers.DefaultCacheTTL`.

## Error classification

Upstream failures reach the proxy as one of two types:

- `providers.UpstreamErrorResponse`: a buffered non-2xx (body capped at `providers.MaxBufferedErrorBytes`, the rest drained without retention). Nothing reached the client, so the proxy may fail over or flush it.
- `providers.UpstreamStatusError`: the non-2xx body was already written to the client. Handlers must not write their own envelope.

`providers.IsRetryable` means "no bytes reached the client, a different provider may succeed". The stall sentinels (`ErrUpstreamIdleTimeout`, `ErrUpstreamOutputStall`, `ErrUpstreamSlowThroughput`, `ErrUpstreamEmptyCompletion`) are upstream-owned even though the watchdogs surface them by cancelling the request context, so they are classified before the caller-cancellation guard. They are defined here rather than in `httputil` (which owns the watchdogs and re-exports them) to avoid an import cycle. Retryable statuses are 5xx, 408 and 429; other 4xx are the client's fault.

Other classifiers gate narrower decisions and are deliberately not `IsRetryable`:

| Signal | Meaning | Licenses |
| --- | --- | --- |
| `IsUpstreamModelNotFound` (404), `IsUpstreamProviderBillingBlocked` (402) | fatal for the binding, not the model | cross-binding failover |
| `IsUpstreamSchemaRejection`, `IsUpstreamThoughtSignatureRejection` | a provider's grammar compiler, or Gemini's opaque thought signature, rejects what another binding accepts | cross-binding rescue |
| `IsUpstreamCapabilityRejection` | the model cannot serve a modality the request carries; every binding rejects it identically | nothing |
| `IsUpstreamOutputConfigFormatRejection`, `IsUpstreamPromptCacheKeyRejection` | the upstream's schema has no such field | one stripped retry |
| `IsAnthropicFastModeQuotaRejection` | no fast-mode allocation, not an ordinary rate limit | one retry at standard speed |
| `IsUpstreamResponsesUnsupported` | no usable `/v1/responses` (404, or 400/403 prose from per-account gating) | re-emit onto chat/completions |
| `IsUpstreamCyberPolicyRefusal` | OpenAI's cybersecurity classifier declined; matched by code or message because streamed error events carry only the message | rescue elsewhere |

Prose classifiers substring-match upstream bodies; keep their phrases narrow, because a loose match either rescues a malformed request onto another provider (masking the bug) or fires on ordinary validation errors. `ContainsCyberPolicyRefusal` does not match the broader `invalid_prompt`/content-filter codes, since a different model would decline those too.

`IsHTTP2ConnectionLoss` matches stream resets, GOAWAY, connection errors and unanswered keepalive pings. net/http's bundled HTTP/2 client returns unexported copies of the x/net error types, so all but `StreamError` are matched by message text.

## Credentials and identity

`providers.CredentialPrincipal` names the upstream account behind a bare API key. The token travels to the client inside the minted reasoning envelope, so it is derived with a password-grade KDF (a plain hash of a key is an offline oracle for it), salted by endpoint so one key on two endpoints is two accounts, and memoized per key. `providers.DeploymentPrincipal` is the same idea for an adapter's own boot-time key, for requests that carry no credential.

`providers.UpstreamHeaderObserver` records subscription rate-limit headroom right after an upstream responds. It checks the resolved credential so only the caller's own subscription is recorded, and must be cheap and non-blocking.

## Gateway URLs and model discovery

- Azure OpenAI and Foundry resources serve the OpenAI surface under `/openai/v1`; root and bare `/v1` answer 404. `NormalizeAzureOpenAIBaseURL` rewrites only those two forms so an explicit deployment or gateway path is preserved.
- Cortex REST (`/api/v2/cortex`) and named AI Gateway (`/api/v2/aigateways/<name>`) roots mount the OpenAI and Anthropic surfaces one `/v1` below. `NormalizeCortexOpenAIBaseURL` appends it; `CortexRESTRoot` maps any of these back to the account's REST root without checking the host.
- `GatewayVersionMemo` probes the alternate URL when a gateway base URL and an adapter's canonical suffix disagree on `/v1`, then memoizes whichever answered.
- `ParseModelIDs` accepts the OpenAI/Anthropic `{"data":[{"id":...}]}` list and the bare `{"models":[...]}` array some gateways publish, whose entries may be strings or objects keyed by `id`, `name` or `model`. Some gateways reject a bodyless catalog GET with 415 or 400 and serve it once a JSON entity is sent (`ModelListNeedsEntity`, `EmptyJSONEntity`).
- Model discovery prefers a request-context BYOK credential over the deployment key, as inference does, and never returns upstream response content in its errors because it may carry credentials or private endpoint details.

## Request mutation stats

`providers.RequestMutationStats` counts translation-time mitigations (Claude-Code-only tools and task reminders stripped, server tools stripped, the Gemini tool-use reminder, Gemini `VALIDATED` tool mode). They are logged on request completion so a mitigation's production impact is measurable; `GeminiValidatedToolMode` also tells the proxy whether an `AUTO`-mode retry is worth attempting after a generic `INVALID_ARGUMENT`.
