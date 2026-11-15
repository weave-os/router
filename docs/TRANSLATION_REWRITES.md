# Translation rewrites

Why `internal/translate` rewrites requests and responses the way it does. Each
rule here is pinned by a test in `internal/translate`; this page records the
client or upstream behavior that makes the rule necessary. Cross-format
requirements and rollout modes live in [TRANSLATION_COMPATIBILITY.md](TRANSLATION_COMPATIBILITY.md);
notice lifecycle lives in [ROUTER_NOTICES.md](ROUTER_NOTICES.md).

## Response writers

### Routing marker

The marker writers (`AnthropicRoutingMarkerWriter`, `OpenAIRoutingMarkerWriter`,
`GeminiRoutingMarkerWriter`) commit headers and emit the marker from `Prelude`,
right after the routing decision and before the upstream returns a byte, so
first-byte latency is routing time rather than upstream prefill. An empty marker
passes upstream bytes through unchanged; on Gemini a streaming prelude with an
empty marker still writes an SSE comment so headers are committed.

On Anthropic the router's early `message_start` cannot know real token counts.
The upstream `message_start` is dropped, its input-side usage (input and cache
tokens) is stashed and folded into the next `message_delta`. Fields the upstream
already set on the delta win, and `output_tokens` from `message_start` is a
placeholder that is never copied. Events are buffered across fragmented writes.

`ArmOutputProgress` drives the output-progress watchdog: it fires only on
output-bearing frames (content, reasoning, tool-call fragments, a terminal
finish reason), never on keepalives, pings, or empty/role-only frames.

### Feedback footer

Footers appear only on natural answer turns: Anthropic `end_turn`/`stop_sequence`,
OpenAI `stop`, Gemini `STOP`, and never on a turn that streamed a tool call.

- Anthropic folds the footer into the final text block as one more `text_delta`
  just before that block's `content_block_stop`, which is held until the next
  event shows the block is last. Wrappers around Claude Code render only the
  last content block as the chat bubble, so a separate footer block would hide
  the answer. Pings between the held stop and `message_delta` are forwarded
  without releasing it. A final `thinking` or `tool_use` block gets no footer.
- OpenAI and Gemini inject a footer chunk before the chunk carrying the finish
  reason. A terminal chunk that also carries answer text is split into content,
  footer, then finish. Some OpenAI-compatible upstreams end tool turns with
  `finish_reason: "stop"`, so the tool-call guard, not the finish reason,
  decides; an empty `tool_calls` array or an empty/`null` Gemini `functionCall`
  is not a tool call.
- `FeedbackFooterInHistory` and `FeedbackFooterInResponsesHistory` show the hint
  at most once per conversation. They count only assistant text and must run on
  the original body, before ingress strips the hint.

### Think tags and Seed-style tool calls

`thinkTagSplitter` reroutes only a leading `<think>…</think>` block (after
whitespace) into thinking; mid-prose tags pass through. It streams thinking as
it arrives and holds back at most a close-tag-length tail.

Some OpenAI-compatible upstreams stream a tool call as Seed-style
`<seed:tool_call>` text and end with `finish_reason: "stop"`.
`SeedToolCallChatWriter` holds content only while it could still be that markup
(bounded by a capture cap) and converts it to structured `tool_calls` only when
every call names a declared tool with object or array arguments. Anything else
is released verbatim. `SeedToolCallNames` returns nil when no client tools were
declared, `tool_choice` is `"none"`, or `n > 1`.

### Tool-call identity

`ToolCallLedger` keys stream entries by source index first because providers
omit IDs on early deltas or reuse one ID for parallel calls. Late source IDs are
aliases; an emitted client ID is never replaced.

Opaque provider state rides inside tool-call IDs because the ID survives every
client SDK round trip. Gemini thought signatures and OpenAI encrypted reasoning
are base64-appended after a delimiter, and embedding is idempotent. OpenAI
reasoning envelopes carry the minting account+model scope: the upstream rejects
replay elsewhere with a 400, so out-of-scope or non-OpenAI envelopes are not
replayed. A suffix that does not decode as a router envelope is caller data and
is kept.

## Request rewrites

### Router directives

Directives (`/fm`, `/rf`, `/beta`, router-models, router-session, list-models)
are recognized only on the first non-empty line of the trailing user message,
including inside client wrappers such as a pasted-file header, and are stripped
so no upstream sees them. Listing directives accept only the bare form: the
router answers a read from the request it holds, while mutations need the
argument-taking commands. Earlier-turn directives come back on replay as plain
user text; `stripHistoricalDirective` removes them while keeping any text the
user wrote alongside. Beta acks, and empty assistant turns that directly follow
a removed command, are dropped; other empty assistant turns are kept.

### Reasoning, effort and tier

- A target that spends output on hidden reasoning gets a floor on its output
  budget, because tiny client budgets (quota probes, titles, verdicts) would be
  spent before a visible token. The budget is a ceiling, so the floor costs
  nothing when unused. Always-on adaptive Anthropic targets default to a larger
  `max_tokens` when the client omits it, since Codex never sends one.
- `ApplyOpenAIResponsesEffort` overrides `reasoning.effort` on verbatim native
  Responses bodies so an effort-qualified arm is served at the effort it was
  selected for; every other native field is kept.
- `ApplyOpenAIFastMode` lets the installation's fast-mode setting own
  `service_tier` on first-party OpenAI: a caller-sent priority tier is stripped
  when fast mode is off, because it would be billed at standard. Gateways relay
  the caller's tier.

### System text

- The autonomy and workspace texts are appended as the last, uncached system
  segment so every byte the client marked cacheable stays in place. Only
  Anthropic-source requests get them; Gemini-served attempts skip the autonomy
  text because it pushed Gemini 3.x into explore loops.
- `openRouterSystemReminder` adds reminders for upstream families that mishandle
  agentic edits (whitespace or punctuation drift in `old_string`).
- `placeAnthropicSystemMessages` moves mid-conversation system messages to the
  nearest position Anthropic accepts: after a user turn (or a server tool
  result) and before an assistant turn.
- Anthropic treats a final assistant message as prefill and rejects trailing
  whitespace; models that reject prefill have the trailing assistant turn
  dropped, including an unanswered `tool_use`, which Codex resends after a
  stream cut.

### Cache control

`dropUnsupportedCacheControlFields` removes cache-control fields the target
rejects: all of them on gateways, and on Anthropic any field whose beta the
client did not send. Longer TTLs must precede shorter ones and the breakpoint
count is capped. `AnthropicRequestCacheTTL1h` is true only when every breakpoint
asks for 1h; it prices cache writes when the upstream omits the breakdown.

### Tool history

- Gemini 3.x rejects history whose function calls lack a thought signature, so
  `HasUnsignedToolCallHistory` marks such requests intrinsically incompatible
  rather than transient (`IsIntrinsicallyIncompatible`).
- `tool_choice` distinguishes absent from unrecognized: Gemini upgrades absent or
  `auto` to VALIDATED mode, but a malformed value must not get that upgrade.
- Claude Code `Read` results number lines as `N<TAB>content`. Models not trained
  on that format copy the separator tab into `Edit`'s `old_string`, so for
  non-Anthropic targets the tab becomes `→` (Claude Code's older separator). The
  rewrite touches only `Read` results and, best effort, the `Edit` and
  `MultiEdit` tool descriptions.
- `normalizeEditEscapes` (opt-in) repairs double-escaped `\n`/`\t`/`\r` only in
  known file-edit tool arguments.
- Duplicate tool results for one call are coalesced: string contents join with a
  blank line, mixed contents become a part array, empties are skipped.

## Loop and spiral signals

Tool-call signatures hash canonical JSON so key order does not matter. Empty
`input:{}` calls are skipped: cross-format translation of a cut-off tool call
emits them and clients echo them back, which would fake a loop. Windows reset
only on genuine human turns; tool results, injected client wrappers and Gemini
function responses are not prompts. Retried tool results are observations, not
new executions.

## Usage projection

A cross-tokenizer serve reports input in the requested model's tokens (input
categories scaled), so a client budgeting compaction against that model's window
sees what it would have to fit. Output stays in served tokens. Usage facts come
from the bound provider source.
