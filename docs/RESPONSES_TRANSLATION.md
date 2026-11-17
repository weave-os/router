# Responses API translation

How the router accepts OpenAI Responses requests (`POST /v1/responses`, the
Codex wire format) and translates Responses streams for other clients. The
cross-format rollout and requirement gates are in
[TRANSLATION_COMPATIBILITY.md](TRANSLATION_COMPATIBILITY.md); routing notices
are in [ROUTER_NOTICES.md](ROUTER_NOTICES.md). The behavior below is pinned by
the tests in `internal/translate/responses*_test.go`.

## Ingress: Responses to Chat Completions

`translate.ResponsesToChatCompletions` projects the subset of the Responses
spec Codex emits: `instructions`, input items (`message`, `function_call`,
`function_call_output`), `tools`, `tool_choice`, `max_output_tokens`,
`temperature`, `top_p`, `parallel_tool_calls` and `metadata`.

- `reasoning` is dropped. Conversion runs before routing, so the served
  provider is unknown; forwarding it as `reasoning_effort` broke non-Gemini
  models (Codex sends values like `"none"`, and some OpenAI models reject
  `reasoning_effort` alongside tools). Per-provider reasoning is derived
  downstream from the request's own signals.
- An input item with a `role` and no `type` is a message.
- Router badges are stripped from history so they are never fed back to a
  model: only from the first text part of an assistant message, never from
  user text. An assistant item left with no text and no non-text part is
  dropped rather than sent as an empty shell.

### Portable Codex projection

`translate.ConvertResponsesToChatCompletionsWithOptions` with
`PortableCodex` projects Codex history so any Chat provider can serve it.
Anything without a portable equivalent marks the turn native-only
(`Requirements.NativeOnly`), and native OpenAI dispatch forwards the original
bytes.

- `/model` switches: Codex records a switch as a retained developer message in
  `<model_switch>` markers. A session whose catalog came from
  `model_catalog_url` records it as `<model_instructions>`, which other
  base-instruction changes reuse, so it only counts as a switch for clients
  that loaded that catalog.
- Developer messages (Responses Lite base instructions) become `system`.
- Hosted web search runs on OpenAI; the projection drops it, the
  `CitationsOrSearch` requirement keeps turns that actually searched on
  OpenAI, and a tool choice forcing hosted search stays native.
- Tool-output images: Chat `role:tool` content is text-only on most targets,
  so images move to one user message after the whole run of tool results,
  labelled per call ID. Outputs that repeat a call ID share one label.
- Only a user's inline image projects; a `file_id` reference or an
  assistant-authored image stays native.
- A reasoning summary is a display digest of the encrypted reasoning beside
  it and is dropped with that reasoning rather than pinning the session to
  OpenAI. Encrypted agent-to-agent messages stay native: their plaintext can
  be only the delivery header, while the task is in the encrypted block.
- `phase` is dropped (Chat has no field; it only classifies visible text for
  Codex's presentation layer). A `null` optional content field is empty.
- Function schemas are inlined (`$defs`/`definitions`); unresolvable `$ref`s
  stay native. Keys inside `properties` are field names, not schema keywords.
- Tool names are aliased by hashing the original, and names already shaped
  like generated aliases are rewritten too, so aliases stay distinct and
  reversible.

## Egress: Chat Completions to Responses

`translate.ResponsesWriter` renders a Chat Completions upstream as a Responses
stream, or in passthrough mode forwards a native Responses upstream.

- The badge is prepended to the first non-empty assistant text delta; a
  text-free passthrough turn gets a synthetic assistant item to carry it.
- Passthrough drops upstream `Content-Length` and `Content-Encoding`. The Codex
  subscription backend omits `Content-Type` on SSE, so the first event is
  inspected and the stream committed as `text/event-stream` exactly once.
- `ForgetWithheldHeaders` drops a header write the caller's retry buffer held
  back, so a native attempt that failed before any output can still be rescued
  on another format via `ClearPassthrough`.
- Native output is inspected before badges or footers are added, so an empty
  upstream turn is still rejected. An empty terminal `response.output` (the
  Codex backend sends one) is rebuilt from the streamed `output_item.done`
  items in `output_index` order; a populated one is left alone.
- Synthesized native events continue after the highest upstream
  `sequence_number`, even when held footer events are written out of order.
- A Chat tool-call id becomes the `call_id` with its base clamped to 64
  characters and any Gemini thought-signature carrier kept, so the client
  echoes the signature back and the next turn can still be served by Gemini
  3.x. The carrier can make the `call_id` much longer than 64 characters.
- `translate.ClampResponsesInputCallIDs` deterministically shortens `call_id`s
  minted cross-format (they carry a Gemini thought signature) to OpenAI's
  64-character limit, so a call and its output keep matching.

## Responses upstream to Anthropic or Chat clients

`translate.ResponsesToAnthropicWriter` and `translate.ResponsesToOpenAIChatWriter`
translate a streaming Responses upstream for Messages and Chat clients;
non-streaming clients get one body built at `Finalize` from the terminal
response.

- Reasoning maps to Anthropic thinking; Chat clients get only the summary as
  `reasoning_content`, since encrypted reasoning has no Chat field.
- Items that arrive only as `output_item.done`, and tool arguments that arrive
  only on `function_call_arguments.done`, are still emitted.
- The stop reason follows what streamed: `tool_use`/`tool_calls` only when a
  tool block reached the client, otherwise `end_turn`/`stop`, whatever the
  terminal payload claims.
- An over-window prompt fails inside an HTTP 200 stream with
  `context_length_exceeded`; it reports 400 so dispatch classifies it as a
  context overflow. Other failures, including nested `error` events and
  malformed frames, are upstream faults (retryable before any output).
- Custom-tool argument errors name only short identifier keys, never values.
  An unwrap failure on a length-finished turn is a truncation
  (`translate.ErrToolCallTruncated`): Anthropic sends no argument deltas for a
  `tool_use` block that `max_tokens` cuts off.
