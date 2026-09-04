# internal/translate — AGENTS

> **Mirror notice.** Generated from [CLAUDE.md](CLAUDE.md). Edit CLAUDE.md, then run `make generate-agent-guides`; CI rejects drift.

Cross-format wire-format conversion. Pure functions, no I/O, no provider knowledge, no domain types. Read [root CLAUDE.md](../../CLAUDE.md) first.

## Scope

Covers all three directions: Anthropic ⇄ OpenAI and Gemini ⇄ {Anthropic, OpenAI} via a `RequestEnvelope` intermediate + per-target `emit_*.go` files.

**Only [`../proxy`](../proxy) calls this package.** Providers stay ignorant of cross-format concerns.

## Adding a wire-format pair

When a new inbound format needs to talk to an existing upstream provider with a different wire format:

1. **Add conversion functions to this package.** Pure functions only — no I/O, no provider knowledge, no domain types.
2. **If response streaming, adapt [`stream.go`](stream.go) / [`gemini_stream.go`](gemini_stream.go)** or add a sibling decorator. Decorators wrap `http.ResponseWriter` and translate on the fly so we never buffer entire responses. Use [`../sse`](../sse) for zero-alloc SSE framing. A decorator that only prepends synthetic content to a stream (like the `*RoutingMarkerWriter` types) should embed `sse.ChunkedWriter` for the shared `Header`/`WriteHeader`/`Flush`/`FlushEvent` + streaming-detection bookkeeping, and add only its format-specific `Write`/`emit*` methods. A full response translator (buffers to translate one wire format into another, e.g. `AnthropicSSETranslator`) has enough divergent `WriteHeader`/streaming logic that it should NOT embed `ChunkedWriter` — reuse only `sse.FlushWriter(bw, flusher)` for its `flushEvent` helper.
3. **Compose the new translation in `proxy.Service.Proxy*`.** Proxy is the only caller of `translate`.

## OpenAI chat/completions ⇄ Responses (same vendor, two wire formats)

Direct OpenAI is served on `/v1/responses` even for a chat/completions caller,
so the pair exists in both directions: `buildResponsesFromOpenAI`
([`emit_openai_responses_from_openai.go`](emit_openai_responses_from_openai.go))
projects `messages` onto Responses `input` items — assistant `tool_calls`
become `function_call`, `role:"tool"` turns become `function_call_output` keyed
by the same `call_id`, `response_format` becomes `text.format` — and
[`responses_to_openai_chat_writer.go`](responses_to_openai_chat_writer.go)
renders the Responses stream back as chat.completion.chunk frames
(`output_text.delta`→`delta.content`, reasoning summary deltas→
`delta.reasoning_content`, `function_call_arguments`→`delta.tool_calls`), with
[`responses_to_openai_chat_response.go`](responses_to_openai_chat_response.go)
serving a non-streaming client one chat.completion body.

Only the **leading** system/developer run is hoisted into `instructions`, for
the prefix-stability reason below; a mid-conversation system message stays in
place as a `developer` input item. A parameter Responses cannot express keeps
the turn on chat/completions instead (`RequiresChatCompletionsParams`) — `n>1`,
penalties, `logprobs`, `logit_bias`, `seed`, stop sequences. Reasoning is
degraded, not smuggled: a chat client cannot round-trip an encrypted reasoning
item, so it sees summary text and loses cross-turn reasoning replay.

## Anthropic-specific stripping (load-bearing)

Anthropic-only fields (`thinking`, `cache_control`, `metadata`, Anthropic beta headers) are stripped at translation time **and again defensively in the OpenAI / openaicompat adapters**. Keep both checks — belt-and-suspenders is intentional because the field set drifts as Anthropic adds beta features.

## Prefix-stable system handling (load-bearing)

Anthropic 400s on `role:"system"` inside `messages`, so `hoistAnthropicSystemMessages` clears them — but only the **leading** run is hoisted into `system`. A mid-conversation system message is demoted to `user` **in place**. Hoisting it instead would move its text in front of the whole history, so a client that emits a system reminder per turn (Claude Code) shifts the cached prefix on every turn and re-writes the entire prompt; prod traffic showed ~890k cache-creation tokens per turn against a flat 17.5k read.

## `<think>` content-channel extraction (gated)

Some OpenAI-compat upstreams (today `xiaomi/mimo-v2.5-pro`) stream chain-of-thought as inline `<think>…</think>` in the **`content`** channel rather than `reasoning_content`/`reasoning`. Left alone, Claude Code renders the raw tags as prose. When the catalog model carries `ThinkTagReasoning: true` (plumbed to the translator via `WithThinkTagReasoning`), [`think_tag.go`](think_tag.go)'s `thinkTagSplitter` reroutes a **leading** `<think>` block into an Anthropic thinking block; everything else passes through as text. Anchored to the start (after leading whitespace) — a mid-prose `<think>` mention stays text, mirroring `leadsWithToolishMarkup`. The splitter is streaming-safe: it buffers at most `len("</think>")-1` bytes (no whole-response buffering), so a tag split across SSE deltas is still caught. Off by default; only `xiaomi/mimo-v2.5-pro` enables it, and only on the OpenAI-compat chat-completions chain (the Gemini chain stays off).

## Gemini 3.x `thoughtSignature` (load-bearing)

The router translator must **round-trip `thoughtSignature` on text / thinking blocks as well as `functionCall` blocks**. Dropping it on text parts breaks the next turn against Gemini 3.x preview models with a 400. The native Generative Language REST client in [`../providers/google`](../providers/google) is mandatory for those flows; the OpenAI-compat surface at `/v1beta/openai` does **not** preserve `thoughtSignature`.

**Carrier: the tool id, not an off-spec field.** For `tool_use` / `functionCall` blocks the signature is the **single** carrier smuggled into the block's id ([`thought_signature_id.go`](thought_signature_id.go), `__thought__<base64>`) — a typed string every client SDK round-trips. Do **not** also emit a raw `thought_signature` block field for tool calls: typed SDKs drop it, and any client that *does* echo it back (Claude Code) makes the next turn 400 if it re-routes to Anthropic (`tool_use.thought_signature: Extra inputs are not permitted`). Text blocks have no id, so they keep the raw field as their only carrier. Targeting Anthropic, `resolveAnthropicOverrides` strips the raw field from **all** blocks (`StripThoughtSignature`) — lossless for tool calls (id still carries it), and the only safe option for text (Anthropic can't use a Gemini signature). The OpenAI emit paths clamp the now-oversized id back under OpenAI's 64-char `call_id`/`tool_calls[].id` limit (`clampOpenAIToolCallID`).

## Cross-format reasoning signatures on Anthropic `thinking` blocks (load-bearing)

The Responses→Anthropic writers smuggle an OpenAI reasoning item (`id` + `encrypted_content`) into the Anthropic `signature` field ([`openai_reasoning_signature.go`](openai_reasoning_signature.go)) so the reasoning can be replayed to OpenAI next turn. Anthropic validates that opaque field and answers `Invalid signature in thinking block`, so `resolveAnthropicOverrides` drops those blocks unconditionally (`StripForeignSignedThinkingBlocks`) alongside unsigned ones — not only when `ModelSwitched` is set. The switch guard is not sufficient: a client-side compaction rewrites the first user message, which re-keys the session, so the pin (and with it the prior served model) is gone on exactly the turn that re-routes an OpenAI-served history to Anthropic.

## Tool-call validation + strict decoding (load-bearing)

Model-emitted tool_use arguments are validated against the inbound request's `tools[].input_schema` by [`toolcheck`](toolcheck/) at every response-translation point (OpenAI-compat and Gemini chains, streaming + non-streaming, and both Responses paths). The pipeline: normalize (drop empty-string/null OPTIONAL params) -> minimal JSON repair -> Draft-7 validation -> safe deterministic repair (drop unknown keys, lossless coercions), re-validated. Unrepairable schema mismatches forward as-emitted (the client's own tool error is the feedback loop); only unparseable JSON degrades to `{}`. Every finding surfaces via `ResponseSummary.ToolCallIssues`, which the proxy logs as `router.tool_call_invalid`. **Everything is fail-open** — a schema that won't compile must never fail a request.

On the emit side the failure class is prevented at decode time where the upstream exposes a knob: OpenAI Responses tools go out with `strict:true` + a strictified schema ([`strictify_openai.go`](strictify_openai.go) — additionalProperties:false, all-required, optionals as null unions; non-strictifiable schemas fall back to non-strict). Gemini 3.x gets `functionCallingConfig.mode=VALIDATED` when the client didn't force a tool_choice. Proxy-side validation always checks against the ORIGINAL schema — the explicit nulls strict mode induces are dropped by toolcheck's normalize pass.

## Invariants

- **No I/O.** No HTTP, no DB, no filesystem.
- **No domain types.** Don't import `auth`, `proxy`, or anything from `internal/router/*`.
- **No provider package imports.** Translation must be addressable without pulling in `internal/providers/<name>`.
