---
name: test-harness-integration
description: Verify a new or changed coding harness against the local router with real inference, UI, telemetry, model switching, provider formats, and caching before committing or pushing harness support.
---

# Test harness integration

Run this before committing or pushing a new harness integration or a change to
harness identity, authentication, request rewriting, session affinity, caching,
streaming, tool handling, UI, telemetry, or model switching. Exercise the changed
router source with a real provider and the actual client whenever possible. Unit tests, replay cassettes,
a health check, and a single successful completion do not establish live harness
compatibility. Documentation-only changes do not require paid inference.

## Set up the smallest real test

1. Read the affected package guide and [smoke guide](../../../docs/SMOKE.md).
   Run `python3 scripts/agent_checks.py doctor` and
   `python3 scripts/agent_checks.py plan --base origin/main` from the router root;
   run the selected checks and relevant regressions. For a bug fix, establish
   a synthetic fail-before/pass-after regression as the root guide requires.
2. Record the router revision plus local changes, client name/version and surface
   (CLI, desktop, IDE, SDK), authentication mode, ingress protocol, intended
   provider/model, and the behavior being verified. Test each supported auth
   path affected by the change, especially subscription and API-key paths
   separately. A router access key alone does not prove subscription auth.
3. Start an isolated local router built from the changed source. Use an existing
   supported launcher and capture facility; check its current help first. In a
   Weave checkout, `wv router local --help` describes `--router-source PATH`,
   `--inspect`, and `--harness`; pass the changed router checkout explicitly so
   the default production revision is not tested by mistake. In a standalone
   checkout use the repository's local client skills and isolated smoke setup.
   Confirm the actual endpoint in a captured inbound request before trusting a
   client response: inherited client settings can override shell environment.
4. Use an empty scratch workspace, authored synthetic content, existing test
   credentials, short outputs, and a bounded request/token budget. Default to
   one three-turn conversation and one tool round trip per representative
   supported provider family, reusing these turns for UI, telemetry, and switch
   checks. Add cases for distinct affected auth/client paths without requiring
   every model or a full cross-product. Set the total budget before starting;
   stop on unexpected spend, repeated errors, or exhausted budget.
   Carry out live testing within the task's authorization and explicit limits;
   do not deploy, change shared policies, or edit global client routing settings.
   If credentials, budget authorization, client access, or dependencies are
   missing, run the available checks and report the exact blocked coverage.

## Define the coverage matrix

For new harness support, cover each advertised model family with a real model:
Anthropic, OpenAI, and Google. For an existing integration, cover every family
and boundary affected by the change. Provider brand and ingress format are
separate dimensions: an Anthropic-format harness routed to OpenAI must exercise
that translation, not just OpenAI's native endpoint.

| Boundary | Required evidence when supported |
| --- | --- |
| Anthropic Messages | Text and tool-result continuation; thinking/signature replay when enabled; complete streamed usage and terminal message |
| OpenAI Responses | Output items, tool call IDs/results, reasoning replay when enabled, complete terminal snapshot and usage |
| OpenAI Chat Completions | Text/tool deltas, finish reasons, terminal stream marker and final usage; test separately if the harness uses it as well as Responses |
| Google Gemini | Native `generateContent`/`streamGenerateContent` where used, text and function call/response parts, finish reason and usage metadata |

Select only ingress-to-upstream combinations the router supports; consult
[translation compatibility](../../../docs/TRANSLATION_COMPATIBILITY.md). Native
Gemini ingress currently stays within the Gemini family. Confirm unsupported
combinations fail clearly instead of silently losing semantics. The existing
smoke suite's Anthropic/OpenAI replay coverage does not establish Google live
coverage. Mark each row passed, failed, blocked, or inapplicable with its reason.

Exercise images, structured output, parallel/custom tools, reasoning/effort,
compaction, resume, and child agents when advertised by the new integration or
affected by the change. Verify semantic preservation through translation and
history replay; unsupported features need a clear rejection or documented limit,
not a silent downgrade. Do not require every optional feature for every model.

## Drive the actual user flow

- Launch the actual harness against that router. For a desktop integration,
  exercise its desktop surface; a CLI with the same vendor name is not coverage
  of the desktop request envelope. If the client cannot be driven here, send an
  authored request matching its documented wire shape as a partial check and
  explicitly leave actual-client coverage unverified.
- Start a fresh conversation with a stable synthetic context large enough to
  exceed the selected model's documented cache minimum. Ask for a short answer,
  then send two follow-ups in the **same session**, preserving history, tools,
  and system content, completing them within the provider's cache TTL. Separate
  stateless CLI invocations are not a multi-turn session; use supported session
  resume or a single harness process. Let normal per-request metadata change.
- Ask the agent to read a harmless scratch file with its normal tool, then
  summarize it. Verify the tool call, arguments, tool result, and subsequent
  assistant response round-trip without schema errors or lost history. Exercise
  streaming completion, cancellation/recovery, or session resume when those
  behaviors are affected. Check non-streaming too if the changed path supports it.
- Open a second fresh conversation and confirm the router recognizes a separate
  session and does not inherit the first session's history or forced routing.
  Provider caches may legitimately share identical prefixes across sessions;
  a fresh session need not have zero cache reads.

## Verify model switching and session state

Use the actual client's supported picker, command, or installed skill. Read the
[pin/switch contract](../../../docs/CONFIGURATION.md#forcing-a-model-or-a-routing-cluster)
and [session semantics](../../../docs/SEMANTICS.md) first; some clients consume
`/model` locally rather than sending it to the router.

1. Send a turn under automatic routing and record the actual served model.
2. Pin model A, send a follow-up, and verify the upstream request used A. Switch
   to model B in a different supported provider family and verify the next
   request used B. Preserve history and complete a tool continuation across the
   switch; reasoning signatures and provider-specific items must remain valid.
3. Clear the pin through the client and verify automatic selection resumes from
   router evidence. Automatic routing may choose the same model as the pin, so
   model name alone does not prove the pin was cleared. If automatic switching,
   profile/effort selection, or subscription fallback is part of the change,
   exercise that transition too and verify its effective policy/credential path.
4. Check persistence on another turn and session resume where supported. Verify
   fresh-session isolation and the documented scope for parent/child threads;
   conversation-wide pins intentionally shared with children are not leakage.
5. Try an invalid or disallowed selection in the isolated test setup. Confirm a
   clear error and accurate visible state; no false success, silent substitute,
   or unexpected paid fallback. Keep failure injection local and bounded.

## Verify the visible client and UI

Inspect the actual desktop, IDE, or terminal UI used by the harness. Protocol
output and screenshots of synthetic mock UI do not establish client rendering.

- Run the supported setup/connection flow in an isolated client profile. Check
  that supported models/commands appear and routing is enabled. If installation
  or configuration changed, verify disable/re-enable or rollback restores the
  previous provider settings without disrupting other clients or sessions.
- Check streamed text, Markdown/code blocks, tool progress/results, thinking
  presentation where supported, and final answers. No raw SSE/JSON, duplicated
  output, stuck spinner, missing final content, or router notices in tool JSON.
- Verify model labels, selection notices, statusline/title/plugin state, and
  pin/clear acknowledgments match the effective selection. Distinguish the
  requested/forced model from the model that actually served a fallback. Check
  updates after a switch, tool turn, new session, and resume when supported.
- Where displayed, verify tokens, cost/savings, and context information against
  their documented sources. Missing values must remain unavailable, not zero;
  stale values from the previous session/model must not persist. The router's
  context estimate is approximate and is not the client's compaction percentage.
- Exercise one bounded error and cancellation/retry path. Ensure the UI exposes
  the failure, settles its loading state, and allows a subsequent successful
  turn without duplicated content or corrupted history. Synthetic local failure
  injection is acceptable; distinguish it from real-provider success evidence.

Record which screens/states were inspected and keep screenshots privately when
useful. If the UI is inaccessible, explicitly leave visual coverage blocked;
a passing CLI or parser test does not fill that gap.

## Verify telemetry through delivery

Define the expected event/field inventory from the active integration contract:
client/harness identity, installation/session/thread/request correlation,
requested and served provider/model, selection/pin/switch reason, auth/funding
path, attempt status/errors, latency, input/output/cache/reasoning usage when
reported, and cost/accounting. Include client lifecycle hooks, outcome/feedback,
and child-agent linkage where the integration emits them. Never equate absent
provider fields with measured zeros or require events the deployment disables.

- Correlate each test turn to its inbound request, routing decision, all provider
  attempts, and final outcome. Keep retries, tool continuations, auxiliary calls,
  and user turns distinct using the documented action/turn semantics; verify no
  unexplained missing events or duplicate charges, including across switches,
  errors and cancellation. Provider work can legitimately incur cost on failure.
- Confirm delivery to the configured collector/storage or consuming dashboard,
  not merely an in-process log or capture. With OTel enabled, inspect the
  documented decision/upstream spans and applicable logs; check identity,
  timing, usage and errors survive export. Exercise required client hooks and
  callbacks and verify receipt, not just emission. See
  [telemetry configuration](../../../docs/CONFIGURATION.md#telemetry-opentelemetry).
- For persisted session totals, use the read-only
  [session cost endpoint](../../../docs/SESSION_COST.md) and, when available,
  [routing decision export](../../../docs/ANALYTICS_EXPORT.md). Match request
  counts, tokens and cost to the captured actions and displayed UI using the
  documented aggregation rules. Allow bounded polling for asynchronous commits;
  an early 404 or a UI response alone does not prove telemetry loss or success.
- If the integration promises a Weave dashboard view, follow these same test
  IDs through ingestion to that view and inspect attribution, model switches,
  usage, and errors there. A local collector proves export only. If downstream
  ingestion/UI is unavailable, report that boundary as unverified.
- Respect configured privacy modes. Full prompt/content capture is opt-in, not
  a prerequisite for complete metadata telemetry. Check that off/hashed modes
  and redaction behave as promised when affected; never enable customer content
  capture or change shared collector settings to complete this checklist.

## Inspect requests and prove caching

Correlate inbound request/session IDs with every provider attempt, including
retries and fallbacks. Inspect the actual upstream body, provider/model and
credential path, response status, complete stream termination, and usage. An
exit code or UI answer without a matching real upstream exchange is insufficient.

For each of the three conversation turns, record input, output, cache creation,
and cache read tokens from provider usage and the usage returned to the client
(and router accounting when exposed). Anthropic exposes
`cache_creation_input_tokens` and `cache_read_input_tokens`; OpenAI APIs may use
`cached_tokens` in input/prompt token details. Google Gemini reports
`usageMetadata.cachedContentTokenCount`, alongside prompt/candidate token counts
in `usageMetadata`. Read streamed final usage too. Record documented model/API
limitations when a usage field is unsupported; an unexpectedly missing field
remains unverified.
Missing cache usage is **unverified**, not a measured zero or a cache hit.

- Verify cache controls, TTL ordering and breakpoint limits on the upstream
  request. Compare the reusable prefix across turns, including system-block
  ordering, tools, identity injection, and volatile metadata placement.
- On cache-eligible warm follow-ups, require positive provider-reported cache
  reads and check the reused amount is consistent with the stable prefix. A tiny
  incidental hit does not prove the large prefix is being reused. Confirm usage
  survives translation to the harness and accounting without double counting.
- Repeated zero reads within TTL require investigation of cache eligibility,
  prefix size, provider/model/credential affinity, breakpoint placement, and
  changing prefix bytes. Do not retry indefinitely or call this a pass because
  responses arrived. If caching is unsupported, explain why it is inapplicable;
  otherwise unresolved zeros fail the cache check.
- **Claude Desktop regression (#2148):** its leading billing line changes
  `cch`/`cc_prompt_id` per request. On subscription traffic, verify any required
  Claude Code identity is inserted after that leading line, preserving the
  cacheable prefix. Existing Claude Code identity, including Desktop's Agent SDK
  variant, must not be duplicated or reordered. Verify API-key traffic receives
  no subscription identity rewrite. Exercise real changing metadata across turns;
  a fixed hand-written system prompt alone misses this failure.

## Preserve the evidence and report

Add or extend an authored synthetic regression at the smallest useful boundary;
use the smoke suite for missing stack-level coverage. Replay protects a recorded
contract and does not prove that today's provider cache works. Record live smoke
cassettes only when needed and authorized, following the smoke privacy rules.

Keep raw client/provider captures in ignored private `.context/`, never public
fixtures. Restore temporary client settings and stop only resources owned by this
run. Report the exact commands and source/client/auth matrix, per-turn cache
counts, stream/tool results, UI states, telemetry delivery/reconciliation,
switch transitions, provider-format coverage, actual served provider/model,
private evidence paths,
and any failed, skipped, or blocked checks in the commit/PR validation summary.
Distinguish replay, synthetic live requests, and actual-client evidence. Do not
claim full integration validation while required live checks remain unverified;
if a push proceeds, disclose those gaps explicitly.
