# Translation compatibility

The router derives a `TranslationRequirements` value at ingress before it
scores a model. It is separate from quality hints: `HasTools` and `HasImages`
can still influence model quality, while requirements describe semantics that
must survive the selected wire-format path.

Current requirements cover source format and endpoint, function/custom tools,
reasoning replay and signatures, media, citations/search, structured output,
prompt-cache controls, and detailed usage. The initial hard checks are native
Responses extension unions, native Gemini ingress, and image input on known
text-only models.

Responses-specific projection, passthrough and stream translation behavior is
described in [RESPONSES_TRANSLATION.md](RESPONSES_TRANSLATION.md).

An upstream decrypts a reasoning item only under the account and model that
produced it, and a rejected replay ends the client session. The signature the
router mints therefore carries a scope fingerprint of provider, wire model,
normalized endpoint (case, trailing slash and default port ignored) and the
credential's stable principal, and the next turn replays reasoning only when
the scope matches. The principal is never the per-request bearer: a refreshed
workload-identity token still decrypts the same items. Without a request
credential the provider adapter's deployment principal stands in, and a
principal derived from key material is appended verbatim instead of being
passed through a fast hash.

## Rollout modes

`ROUTER_TRANSLATION_COMPATIBILITY_MODE` is validated at startup:

| Mode | Behavior |
| --- | --- |
| `off` | Restores pre-compatibility broad eligibility. Use only as an emergency rollback. |
| `shadow` | Emits the same compatibility diagnostics without changing broad candidate pools. Native-only safety paths remain constrained. |
| `enforce` | Applies declared semantic requirements as provider/model candidate filters. |

Native Responses requirements select the OpenAI-compatible family and forward
the original client body with only the routed model changed. This preserves
custom, namespace, built-in, and unknown Responses unions instead of reducing
them to function tools. Gemini ingress selects the native Gemini family before
routing; it no longer reaches the late unsupported-cross-format path.

## Observability

`translation.compatibility` OTel log records use stable requirement codes and
include source format, target family, rollout mode, and whether the exclusion
was enforced. `translation.transform` records describe Responses ingress
outcomes. Paths/reasons remain trace/log detail only; prompts, tool schemas,
arguments, media, and credentials are never attached.

The first rollout should remain in `shadow` until observed exclusions have a
declared code and representative conformance coverage. Move back to `shadow`
or `off` before reverting a binary if broad enforcement causes an incident.

## Streaming translation invariants

The cross-format stream translators in `internal/translate` keep these wire
rules; each is pinned by a translate unit test that fails when it is broken.

- A stream succeeds only on an explicit upstream terminal (finish reason or
  `[DONE]`). EOF before any output defers to failover; EOF after output emits
  an error event instead of a clean `end_turn`.
- An upstream error status that arrives after the SSE prelude is committed is
  surfaced as an Anthropic `error` event (429 `rate_limit_error`, 503
  `overloaded_error`, other 4xx `invalid_request_error`, else `api_error`).
  The relayed error body is bounded.
- Output progress for the stall watchdog fires on text (including
  whitespace-only text), streamed reasoning, tool-call deltas and the
  terminal finish, never on role-only, null or usage-only keepalives.
- A turn with a `tool_use` block always ends with `stop_reason: tool_use`; a
  `tool_calls` finish with no surviving tool block is demoted to `end_turn`.
- The text-only-turn recovery nudge fires only for tool-bearing requests whose
  turn opens with leaked tool-call markup, never after a truncated
  (`length`) turn, a suppressed tool call or on Gemini 3.x.
- Gemini 3.x signs only the first function call in a turn; that signature is
  replayed on every later call, carried in the OpenAI tool-call id.
