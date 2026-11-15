# Request envelope, emit overrides and in-band commands

How `internal/translate` reads an inbound request (`translate.RequestEnvelope`), what it rewrites before dispatch, and why. Each rule below exists because an upstream rejected the request or a client misbehaved without it; the tests named in `internal/translate` pin the behavior.

## Envelope

- The parsed body is immutable after `ParseOpenAI`/`ParseAnthropic`/`ParseGemini`. Same-format emit applies byte-level `EmitOverrides` with gjson/sjson and no cloning; cross-format emit reads with gjson and writes with the package's JSON writer. `Clone` copies the body.
- `Stream` is true only for a JSON boolean `"stream": true`; a numeric value is not coerced. Gemini encodes streaming in the URL path, so the Gemini handler injects a synthetic `"stream": true`.
- `ClientSessionID` is the client's own session id (the value a user sees in `/status`), taken from `metadata.user_id` (Anthropic, Gemini) or `user` (OpenAI chat). A UUID-shaped marker is returned bare, anything else is truncated to 64 bytes. It is for log correlation only and differs from the internal session key (sha256 of API key id and user id).
- `SystemBlocks` keeps Anthropic content-block boundaries (Claude Code sends `gitStatus` as its own block); other formats yield one block.
- `ThreadDiscriminatorText` is the opening user text the session key hashes. After `StripHistoricalCommands` it stays the pre-strip text, so removing an opening directive from the body never re-keys the thread. Derive the session key before stripping `/force-model`.
- `ToolValidator` compiles the request's tools (chat function tools are projected to the Anthropic `{name, input_schema}` shape) through toolcheck's LRU cache, because sessions resend byte-identical tools every turn. A nil validator means syntax-check only.
- `LastUserMessageInfo.ToolResultBytes` is the trailing tool_result payload size, a cheap triviality signal (tiny result, likely a trivial continuation).
- `RequestsTitleSchema` detects client title-generation calls from a JSON-schema response format with a string `title` property, without matching prompt text. Codex title turns are also recognized from `client_metadata.x-codex-turn-metadata` (`thread_source` or `turn_trigger` = `thread_title`) or from the title prompt, but only in a fresh Responses conversation (no assistant turn, tool call or tool output). Earlier user items are allowed because Codex prepends harness context as separate user messages; history is not, so quoted title instructions in a normal session never match.

## Emit options

| `EmitOptions` field | Meaning |
|---|---|
| `TargetProvider` | Resolved upstream provider. Gates OpenRouter-only fields (provider and reasoning hints, tool-turn temperature) so a slug rebound to another upstream does not send fields that upstream rejects. Empty falls back to model-slug behavior. |
| `SessionAffinity` | Per-conversation id sent as a prompt-cache stickiness hint so a session lands on the same warm replica; the knob differs per upstream. |
| `StripPromptCacheKey` | Drops `prompt_cache_key` instead of injecting the affinity hint, after a gateway whose schema trails the spec rejected it as unknown. |
| `ModelSwitched` | The serving model changed since the last turn. Thinking signatures are valid only for the model that produced them (Anthropic 400s "Invalid signature in thinking block"), so thinking blocks are stripped. |
| `ForceReasoningEffort` | Escalate-on-failure effort for gpt-5.x (Responses) and gemini-3.x (`thinkingConfig`): gpt-5.x starts low and goes high after a failed or no-progress turn; gemini stays low. |
| `ForceEffort` | User-requested effort (`x-weave-effort` header or `:level` suffix). Wins over `ForceReasoningEffort`; per-model caps come from `ResolveForceEffort`. |
| `EnableExtendedContext` | Targets with the extended-context capability get the 1M window instead of 200K, avoiding 400 "prompt is too long". No-op below 200K input. |
| `EnableServerSideFallback` | Lets first-party Anthropic re-serve a safety-refused turn on a fallback model; ignored for other targets. |
| `KeepCrossVendorOrchestrationTools`, `KeepCrossVendorTaskTools` | Set from `ROUTER_CC_ORCH_TOOLS_CROSSVENDOR` and `ROUTER_CC_TASK_TOOLS_CROSSVENDOR`; see [Claude Code tools on other vendors](#claude-code-tools-on-other-vendors). |
| `AppendAutonomySystem` | Appends `AutonomySystemText` as the last, uncached system element after any client cache_control block; cross-format emitters carry it into the OpenAI system message, Responses instructions or Gemini `systemInstruction`. The proxy decides eligibility. |
| `AppendWorkspaceSystem` | Same, for `WorkspaceSystemText`, on cross-format emitters only. |
| `StripOutputConfigFormat` | Drops `output_config.format` (and an emptied `output_config`) on a one-shot retry after an Anthropic-spec gateway 400s on it. |
| `DowngradeGeminiValidatedToAuto` | Emits `functionCallingConfig.mode=AUTO` instead of `VALIDATED` for Gemini 3.x on a one-shot retry: `VALIDATED` compiles tool schemas into a decode grammar and 400s `INVALID_ARGUMENT` when one does not compile. |
| `ReasoningReplayScope` | Upstream account and model, so encrypted reasoning in history is replayed only where it was minted. Empty replays nothing. |
| `FastMode` | Paid fast tier (OpenAI `service_tier: "priority"`, Anthropic `speed: "fast"` plus beta), first-party targets only. |

## Anthropic same-format overrides

- Unsigned `thinking` blocks (cross-format artifacts) and blocks signed with a router-minted reasoning envelope are always stripped: Anthropic rejects both regardless of pin state, and `ModelSwitched` misses them when client-side compaction re-keys the session. `redacted_thinking` is exempt from the unsigned rule.
- `tool_use.id`/`tool_use_id` values outside `^[a-zA-Z0-9_-]+$` are rewritten (other upstreams mint ids like `functions.Read:0` that Anthropic rejects on replay). The router-minted OpenAI reasoning carrier is dropped from ids first (up to ~8KB of prompt per echoed id); the Gemini thought carrier stays. Both ends of a pair reduce to the same id.
- Gemini-only `thought_signature` is removed from blocks; tool signatures survive through the id carrier.
- A message whose content is emptied by a block rewrite is dropped, because Anthropic rejects `content: []`.
- Thinking shape by target capability:

| Inbound `thinking.type` | Extended thinking | Adaptive, not always-on | Adaptive, always-on | Neither |
|---|---|---|---|---|
| `adaptive` | deleted | kept | kept | deleted |
| `enabled` | kept | rewritten to adaptive, effort from budget | same | deleted |
| `disabled` | kept | kept | rewritten to adaptive `low`, `max_tokens` floored to `minReasoningOutputTokens`, `temperature` removed | deleted |

  The always-on `disabled` case exists because those models 400 on `thinking.type=disabled`; the floor stops hidden reasoning from exhausting a budget sized for a visible-only answer (Claude Code's 64-token auto-mode verdict).
- `xhigh` effort (top-level and `output_config.effort`) is clamped to `max` on adaptive targets without the xhigh capability, so a mid-session re-route cannot forward a level Anthropic rejects with a session-ending 400.
- A forced effort sets `output_config.effort` and deletes a top-level `effort`: Anthropic Messages accepts only the nested form.
- `tool_choice` `any`/`tool` becomes `auto` (keeping `disable_parallel_tool_use`) on auto-only targets.
- Metadata passthrough (count_tokens, model list) deletes `effort`, `thinking`, `context_management` and `output_config`, which those endpoints reject.

## OpenAI same-format overrides

- gpt-5.x chat/completions rejects `reasoning_effort` when tools are present; effort belongs on the Responses API there. Reasoning models also reject `stop`, `presence_penalty` and `frequency_penalty`, and take `max_completion_tokens` instead of `max_tokens`.
- Output caps come from `modelMaxOutputTokens`, keyed by the full catalog id (decision models keep the vendor prefix). Unlisted models fall back to 8192; OSS models are listed because that fallback clamped Claude Code's 64K output request and truncated long turns. Anthropic always-on adaptive targets default to `anthropicAdaptiveDefaultOutputTokens` (capped by the listed model maximum) to leave room for hidden reasoning; an explicit caller `max_tokens` is never raised by the default.
- `ResolveForceEffort` maps `xhigh` to `max` without the xhigh capability, and `max` to `xhigh` on a menu that tops out at `xhigh` (GPT-5.6 on the public Responses API), where rank-clamping would fall a rung lower than the target can serve.

## Router text echoed back by clients

The routing marker and the feedback footer are injected as assistant text on egress, and clients echo them back next turn. `StripRoutingMarkerFromMessages`, `StripFeedbackFooterFromMessages` and the Gemini variants remove them on ingress so they never reach upstream context and assistant prefixes stay prompt-cache stable. Rewritten strings are encoded without HTML escaping to preserve the client's bytes; text blocks left empty are dropped. The footer pattern must match `proxy.feedbackFooterText`.

## Model ids

- `[1m]` is Claude Code's display suffix for the 1M-context variant, not a real model id; native Anthropic returns 404 for it. `CanonicalizeModelInBody` is the single ingress seam that strips it so routing, pins and telemetry key off the canonical id; the 1M window is enabled by the extended-context beta header, never by name.
- Provider-qualified ids (`anthropic/claude-opus-4-8`, sent by eval harnesses and gateway clients) are accepted and resolved by routing. `StripProviderPrefixInBody` rewrites only the outbound metadata passthrough body, which has no routing decision: without it count_tokens 404s for a whole run and Claude Code cannot measure its context.

## OpenRouter and OSS model hints

- `openRouterProviderHint` pins DeepSeek and Kimi to their first-party hosts, which cache prefixes (price load-balancing lands on hosts without prefix caching and breaks agentic sessions). Fallbacks stay on because the first-party host does not serve every slug in the family. Qwen and Google slugs sort by throughput.
- `openRouterReasoningHint` disables reasoning for DeepSeek (ignores `effort=minimal`, defaults on), Kimi and MiMo (tool-call tokens emitted inside unbounded reasoning are missed by OpenRouter's parser, leaving `tool_calls` empty) and GLM-5.1.
- DeepSeek tool turns default to temperature 0 unless the client set one: sampling jitter degrades tool-argument fidelity.
- GLM-5.1 and GLM-5.3-flash need `tool_stream=true`, or tool calls stream with empty arguments. GLM-5.1 also has thinking disabled on vLLM hosts; GLM-5.3-flash cannot disable thinking.
- Qwen3 gets the model card's sampling defaults (temperature 0.7, top_p 0.8, presence_penalty 1.5, repetition_penalty 1.05) when unset; presence_penalty breaks the repeated identical tool-call loop. The repetition penalty is skipped on Fireworks.

## Claude Code tools on other vendors

`claudeCodeOnlyToolNames` lists tools Claude Code implements itself (subagent dispatch, plan mode, skills, deferred tool loading, notifications, worktrees, MCP resource listing). On Anthropic-to-other-vendor emit they are dropped: other models hallucinate calls to them (an eval saw 224 phantom calls across 150 shards, 96% in the Task family). Anthropic-to-Anthropic keeps them.

- Scheduling (`ScheduleWakeup`, `Cron*`, `Monitor`), shell-session (`BashOutput`, `KillShell`) and `NotebookEdit` are ordinary client-executed tools and are not on the list; stripping them broke `/loop` and background shells.
- `ToolSearch` is always kept: it is the only way to load a deferred MCP schema.
- `KeepCrossVendorOrchestrationTools` keeps Task/Agent, TaskOutput/TaskStop, SendMessage, Workflow, Skill and plan mode. Task-list tools (`TaskCreate`/`TaskUpdate`/`TaskGet`/`TaskList`) additionally need `KeepCrossVendorTaskTools`: other models obey the "task tools haven't been used recently" reminder literally and burn turns on them. When they are dropped, that reminder is cut from user, tool_result and system messages too, deterministically so the prefix stays cache-stable.
- `tool_choice` and past tool_use/tool_result blocks are not rewritten: changing history would break prompt caches and leave dangling ids.

## Handover and history repair

- `RewriteForHandover` replaces non-system messages with `[summary, latest user]` (summary prefixed `HandoverSummaryTag`) to bound input cost on mid-session switches; tool_result blocks on the kept user message are removed because the summary has no tool_use.
- `TrimLastNMessages` keeps n non-system messages (3 when n <= 0). If the tail has no text-bearing user turn it pulls back the newest earlier one and resumes on an assistant turn, so no two user roles are adjacent. Gemini drops functionResponse parts at the head of the kept tail, since their functionCall turn was elided.
- `SanitizeOrphanedToolCalls` runs every turn so one mid-stream failure never bricks a session: some providers 400 on unpaired tool-call ids. Unanswered tool calls are stripped unconditionally (a healthy client history never has one in flight), and orphaned Anthropic tool_results are stripped even without an orphaned tool_use (a resumed session whose assistant turn was never persisted), since Anthropic rejects them anywhere. Gemini does not validate pairing.

## In-band commands

`ExtractForceModelCommand` reads `/force-model <model>` (aliases `/model`, `/fm`), `/force-parent <model>` and `/unforce-model` (`/ufm`), plus `$` forms for clients whose skill namespace is `$` (Codex). Pins it creates carry `ReasonUserForceModel`, an immutable sticky that bypasses routing until cleared; `ReasonLoopEscalation` is the same kind of pin for tool-call loop escalation.

- Only the trailing user or tool-result turn counts; a later conversation turn cancels it, but interleaved `role: "system"` notices do not. Codex's separate `<skill>…</skill>` user message right after the typed directive is an attachment, not a newer turn, but only when it follows a user message and holds nothing else.
- Only the first non-empty line is read, after skipping leading attribute-free `<tag>…</tag>` blocks that clients inject (kept in the stripped output). Pasted content starting with `/` therefore cannot change routing.
- The rest of the line is the model name, whitespace collapsed, so `/fm qwen 3.8` never silently pins `qwen`. Put the prompt on the next line.
- From tool results, `/model` is not accepted (tool output may contain model documentation); Codex exec output is read from the first line after `Output:`.
- Cursor wraps the typed message in `<user_query>`; commands are parsed inside it.
- An OpenAI `role: "tool"` message is kept when its command is stripped so tool_calls stay paired; stripping never leaves an empty history.
- Directives that short-circuit a turn only discard text made of known client wrappers (`claudeCodeInjectedBlockPrefixes`), never arbitrary tagged text the user wrote.

## Routing features and token estimates

- `FullTokenEstimate` divides bytes by 6 (base64 thought signatures would otherwise evict extended-context models); `ContextOverflowTokenEstimate` divides by 4 after subtracting signatures, because ÷6 undercounted a signature-light ~263K-token body to ~175K and it 400'd on a 256K model. Base64 images count 1600 tokens each instead of their bytes. `SignatureTokenSavings` is what signature-stripping targets save; it is zero for non-Anthropic formats. Router-minted reasoning echoed in signatures and tool ids is excluded only when it decodes as a router envelope.
- A user message with any tool_result (or Gemini functionResponse) classifies as `tool_result` even with other text, so tool flows stay on the session's model.
- `RoutingFeatures.MaxTokens` drives probe detection (SDK quota probes send `max_tokens=1`). `TitleGenHint` is set only for Codex.

## OpenAI strict tool schemas

`strictifyOpenAISchema` converts a tool schema to OpenAI strict mode or reports it cannot: every object closed with all properties required, optional properties made nullable with the note "(Optional; pass null to omit.)" (gpt-5.x otherwise fills optionals), null added to enums of nullable fields, and constraint keywords strict mode rejects (`format`, `pattern`, `propertyNames`, …) moved into descriptions. It bails (tool stays non-strict) on composition keywords, `$ref`, freeform dictionaries, tuple or untyped array items, typeless anyOf branches, and the depth and size limits.
