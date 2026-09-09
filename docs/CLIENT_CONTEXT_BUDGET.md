# Client context budgets and compaction recovery

A provider's context capacity is not the caller's effective context budget.
The router resolves client evidence for each Anthropic Messages request, before
model normalization loses the `[1m]` variant and before upstream preparation
adds beta capabilities. Route preview uses the same resolver.

## Evidence, not configuration

`router.ClientBudget` travels with request context and routing requests. It is
not stored in session pins, inferred from an installation, or reused by another
agent. Its evidence categories are:

- `harness_default`: a supported client version and model imply a default window
  under the verified custom-base-URL behavior. The threshold is a **default**,
  not the client's effective configured threshold.
- `ambiguous_long_context`: a long-context beta arrived without the original
  model variant. The numeric defaults are unknown (zero).
- Empty/unknown: unsupported version, missing harness identity, or unknown model.
  Numeric defaults are unknown (zero), never borrowed from the provider catalog.

The supported calculation currently covers Claude Code **2.1.257**. Its ordinary
200K path reserves 20K output tokens and another 13K buffer, yielding a 167K
default threshold. A supported explicit `[1m]` model variant implies a 1M
window and 967K default threshold. The request's `max_tokens` is **not** the
client's compaction reserve. `sdk-ts` identifies an SDK entrypoint, not a
specific orchestrator.

An isolated capture of that installed client established these limits:

| Local setting | Wire model | Inbound 1M beta | Runtime model window | Ordinary threshold |
|---|---|---|---:|---:|
| Bare Fable 5.1, auto-compact window 1M | Bare Fable 5.1 | No | 200K | 167K |
| Explicit `[1m]`, auto-compact window 1M | Bare Fable 5.1 | Yes | 1M | 967K |
| Bare, auto-compact window 100K | Bare Fable 5.1 | No | 200K | 67K |
| Explicit `[1m]`, auto-compact window 100K | Bare Fable 5.1 | Yes | 1M | 67K |
| Bare plus `ANTHROPIC_BETAS` adding the 1M beta | Bare Fable 5.1 | Yes | 200K | 167K |

The client strips its local suffix before sending. Private auto-compaction
settings can produce identical inference requests with different thresholds.
The beta can also be added independently of the client's window. Beta ordering
is not a budget contract. Therefore the router cannot derive an exact effective
threshold from these inputs; no new installation headers conceal that limit.

## Router behavior

Provider-fit filtering still compares the actual request estimate and output
reserve with eligible **provider capacity**. It is not clamped to a client
window. The proactive cascade defers to a supported harness default only when
the request fits the pool and the pool serves that default threshold. Unknown
and ambiguous clients do not gain catalog-based deferral.

For supported client compaction requests, the router appends a concise handoff
instruction to the summary call: retain decisions, changed files, unresolved
errors and the next action; avoid copied file dumps and rereading known evidence.
The text target is 1% of the default client window, capped at 4K tokens. This is
**advisory**: `max_tokens` and reasoning settings remain intact so the handoff is
not silently cut off.

On a small-default-window continuation carrying the client's standard leading
summary marker, the initial recovery period requests one tool call per response.
The period ends after more than three assistant messages appear in the current
history, including a preserved assistant batch. It is stateless and does not
track or conflate sessions. Anthropic receives `disable_parallel_tool_use`;
OpenAI Chat/Responses receive `parallel_tool_calls: false`. Gemini has no
corresponding limit: only the summary's continuation guidance applies there.

Ordinary inference history is not edited. The client still owns restored
instructions, attached files, preserved tool results and its compaction guard.
Semantic-cache replies are bypassed during active recovery so an older parallel
batch cannot bypass the request limit. Shadow/blind experiment paths retain
their existing behavior.

## Verification and remaining limits

Tests cover original-variant preservation, serving/preview parity, beta-only
ambiguity, upstream-added beta isolation, same-session model changes, isolated
request contexts, unknown versions, provider-fit/deferral behavior, summary
prompt preservation, recovery expiry, and native/translated tool limits.

A serialized batch is not a byte bound: one read or shell call may still return
an oversized result. A lower private threshold may be below restored fixed
content. Summary length and bounded retrieval depend on model compliance.
Consequently this is a targeted mitigation, **not a guarantee that compaction
thrashing is impossible**. A synthetic upstream can verify the real client's
retention and guard mechanics, but cannot establish live model compliance or
production recovery. Keep those distinctions when evaluating a rollout.
