# Translation compatibility

The router derives a `TranslationRequirements` value at ingress before it
scores a model. It is separate from quality hints: `HasTools` and `HasImages`
can still influence model quality, while requirements describe semantics that
must survive the selected wire-format path.

Current requirements cover source format and endpoint, function/custom tools,
reasoning replay and signatures, media, citations/search, structured output,
prompt-cache controls, detailed usage, and an explicit parallel tool-call
limit. The initial hard checks are native
Responses extension unions, native Gemini ingress, and image input on known
text-only models.

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

A parallel tool-call limit (`parallel_tool_calls: false` on OpenAI Chat
ingress, or `tool_choice.disable_parallel_tool_use: true` on Anthropic
Messages ingress, on a request that declares tools) survives translation to
the Anthropic and OpenAI-compatible families. Gemini has no equivalent, so this
requirement excludes only the Gemini family, under code
`parallel_tool_limit_unsupported`. Responses ingress does not set it yet.

## Observability

`translation.compatibility` OTel log records use stable requirement codes and
include source format, target family, rollout mode, and whether the exclusion
was enforced. `translation.transform` records describe Responses ingress
outcomes. Paths/reasons remain trace/log detail only; prompts, tool schemas,
arguments, media, and credentials are never attached.

The first rollout should remain in `shadow` until observed exclusions have a
declared code and representative conformance coverage. Move back to `shadow`
or `off` before reverting a binary if broad enforcement causes an incident.
