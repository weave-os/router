---
name: debug-codex-session
description: Investigate a specific Codex CLI session by session ID — correlate the local rollout transcript (`~/.codex/sessions/YYYY/MM/DD/rollout-*-<SESSION_ID>.jsonl`) with the router's production logs to understand what Codex rendered vs. what the upstream served on the /v1/responses path. Use when given a session ID and asked "why did X render?" (missing thinking, missing routing marker, wrong model, tool-call weirdness) for a Codex conversation routed through the router.
---

# Debugging a Codex session

Given a Codex **session ID**, pull the local rollout transcript (what the client saw) and the corresponding production cloud logs (which model/provider served it), then correlate them. The rollout `.jsonl` is ground truth for *what rendered*; the cloud logs confirm *what the router decided and what the upstream sent*; `internal/translate/responses*.go` + `internal/proxy/service.go` explain *why the wire shape looks that way*.

The sibling skill [debug-claude-session](../debug-claude-session/SKILL.md) is the Claude Code counterpart. The workflow is the same shape; the transcript format, the id conventions, and the correlation key are all different — do not carry Claude assumptions over.

## Setup: Cloud deployment config

Before starting, create a gitignored config file with your deployment's cloud logging details:

```bash
cat > .claude/skills/debug-codex-session/.deployment.json <<'EOF'
{
  "cloud_provider": "gcp",
  "project_id": "your-project-id",
  "region": "us-central1",
  "service_name": "router",
  "log_command_template": "gcloud logging read ... --project {project_id} --format=json"
}
EOF
```

If `.deployment.json` is missing, prompt the user for these details and walk them through creating it. The file is gitignored and contains no secrets — just the service/project/region names needed to construct cloud log queries.

## Critical gotchas (read first)

- **Correlate by `client_session_id`, not by time.** Codex 0.149+ sends the session ID as the `Session-Id` header (and `Thread-Id` with the same value on the main thread); `sessionIDFromHeaders` ([internal/proxy/client_identity.go](../../../internal/proxy/client_identity.go)) picks it up and `bindRequestLogger` binds it as `client_session_id` on every log line. So the Codex session ID from the transcript filename is a **direct** log filter. This is the big difference from the Claude skill, which has to correlate by time + model.
- **The transcript does NOT record the served model.** `turn_context.model` and `thread_settings_applied.model` are the model Codex *requested* (e.g. `gpt-5.6-sol`). The router ignores the request's `model` field for routing. The **served** model appears only in (a) the injected Weave routing marker, when one was emitted, and (b) `decision_model` in cloud logs. Never report `turn_context.model` as the model that served the turn.
- **Empty `summary: []` on a reasoning item is not lost thinking.** Native Codex reasoning items carry opaque `encrypted_content` (replay state for the next Responses request) and a *public* `summary` array. With `summary: "auto"` the upstream frequently returns zero summary text while still billing `reasoning_output_tokens`. Check `event_msg/token_count → info.total_token_usage.reasoning_output_tokens` before concluding thinking was dropped — nonzero tokens with empty summaries means the model reasoned and the upstream chose not to surface a summary.
- **Codex subscription turns take the native passthrough path — the router does not translate them.** `codexResponsesRequest` ([internal/proxy/service.go](../../../internal/proxy/service.go)) detects a ChatGPT JWT + `ChatGPT-Account-ID`; the original Responses body is preserved and `ResponsesWriter.SetPassthrough()` / `SetPassthroughBadge()` ([internal/translate/responses.go](../../../internal/translate/responses.go)) forward upstream bytes verbatim. On that path, "the router dropped it" is almost never the answer — verify passthrough before blaming translation.
- **`ProxyOpenAIChatCompletion` in the logs does not mean Codex used chat completions.** `ProxyOpenAIResponses` delegates into the shared chat proxy for routing/billing/telemetry, so the completion log line is named `ProxyOpenAIChatCompletion complete` and `ingress="openai_chat_completions"` even for `/v1/responses` traffic. Check `path` on the access-log line to see the real surface.
- **Short base36 item ids are router-minted; long hex ids are upstream.** `newResponsesID` produces `msg_23aljo`-style ids, so a synthetic routing-badge assistant item looks like `msg_s6w20w`, while a genuine upstream assistant message looks like `msg_09cf01fda402...`. Same for `rs_`, `ctc_`, `fc_` prefixes. This is the fastest way to tell injected content from served content.
- **The routing marker is a separate assistant item, and it is stripped on the way back up.** `StripRoutingBadgeFromResponsesInput` removes the badge from replayed input so router text never reaches the model. A missing marker on a turn is often expected (sticky same-model turns emit nothing) — see [test-codex-locally](../test-codex-locally/SKILL.md) for the `PriorServedModel == ServedIdentity()` rule.
- **The rollout file mixes client-side and upstream events.** `response_item` entries are conversation items (both directions); `event_msg` entries are client-side UI/telemetry events. `developer`/`user` role messages with UUID-shaped ids are locally constructed, not served.

## Workflow

```
- [ ] 1. Locate the local rollout transcript
- [ ] 2. Get the session shape (event histogram + session_meta)
- [ ] 3. Read turn_context: requested model, effort, summary mode
- [ ] 4. Extract the items showing the symptom
- [ ] 5. Check token accounting before calling anything "missing"
- [ ] 6. Fetch cloud logs filtered by client_session_id
- [ ] 7. Correlate transcript + cloud logs
- [ ] 8. Trace to the responses code path
```

### 1. Locate the local rollout transcript

Codex writes one rollout file per session, date-partitioned, with the session ID as the filename suffix:

```bash
find ~/.codex/sessions -name "rollout-*<SESSION_ID>.jsonl" -type f
```

You get `~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-<ISO8601>-<SESSION_ID>.jsonl` — one JSON object per line, no sibling spillover directory (unlike Claude Code). `wc -l` it; a working session is typically hundreds of lines.

Note the ISO timestamp in the filename is **local time**; the `timestamp` field inside each line is UTC (`Z`-suffixed). Use the in-file timestamps for any time math.

### 2. Get the session shape

Every line is `{"timestamp", "type", "payload"}`. The `type` is the outer kind; `payload.type` is the specific event. Histogram first — it tells you at a glance whether the session compacted, aborted, or ran tools:

```bash
python3 - <<'EOF'
import json, collections
F = "<path>/rollout-...-<SESSION_ID>.jsonl"
c = collections.Counter()
for line in open(F):
    try:
        o = json.loads(line)
    except Exception:
        continue
    c[(o.get("type"), (o.get("payload") or {}).get("type"))] += 1
for k, v in c.most_common():
    print(v, k)
EOF
```

The kinds you'll see:

| `type` | `payload.type` | meaning |
|---|---|---|
| `session_meta` | — | one per file: `session_id`, `cwd`, `originator`, `cli_version`, `source`, `model_provider`, `git`, `context_window` |
| `turn_context` | — | per-turn settings: `model`, `effort`, `summary`, `approval_policy`, `collaboration_mode` |
| `response_item` | `message` | a conversation message (`role`: `developer` / `user` / `assistant`) |
| `response_item` | `reasoning` | `id` (`rs_*`), `summary[]`, `encrypted_content` |
| `response_item` | `custom_tool_call` / `function_call` | tool invocations (`ctc_*` / `fc_*` ids, `call_id`) |
| `response_item` | `custom_tool_call_output` / `function_call_output` | results, keyed by `call_id` |
| `event_msg` | `token_count` | cumulative + last-turn usage, plus `rate_limits` |
| `event_msg` | `agent_message` | what the TUI rendered as assistant text — **routing markers show up here** |
| `event_msg` | `task_started` / `task_complete` / `turn_aborted` | turn lifecycle (`turn_id`, timings, `reason`) |
| `event_msg` | `context_compacted` | paired with a top-level `compacted` line holding the summary |
| `world_state` | — | workspace snapshot (AGENTS.md text, etc.) |

`model_provider` on `session_meta` tells you whether the session went through the router at all — it's the provider id from `~/.codex/config.toml` (e.g. `weave`). If it isn't the router's provider id, stop: this session never hit the router.

### 3. Read turn_context

```bash
python3 - <<'EOF'
import json
F = "<path>/rollout-...-<SESSION_ID>.jsonl"
for i, line in enumerate(open(F), 1):
    o = json.loads(line)
    if o.get("type") != "turn_context":
        continue
    p = o["payload"]
    print(f"line {i}: turn_id={p.get('turn_id')} requested_model={p.get('model')} "
          f"effort={p.get('effort')} summary={p.get('summary')}")
EOF
```

`effort` and `summary` are the two knobs that decide how much reasoning is produced and how much of it is *displayable*. `summary: "auto"` is the usual cause of a session with lots of reasoning tokens and no visible thinking. Again: `model` here is the request, not the route.

### 4. Extract the items showing the symptom

Adapt this to your symptom. The template below covers the common "no thinking rendered" case plus assistant-message provenance:

```bash
python3 - <<'EOF'
import json
F = "<path>/rollout-...-<SESSION_ID>.jsonl"
reasoning = empty_summary = 0
for i, line in enumerate(open(F), 1):
    o = json.loads(line)
    p = o.get("payload") or {}
    t = p.get("type")
    if t == "reasoning":
        reasoning += 1
        summary = p.get("summary") or []
        enc = p.get("encrypted_content") or ""
        if not summary:
            empty_summary += 1
        if reasoning <= 3:  # sample the first few
            print(f"line {i}: rs id={p.get('id')} summary_parts={len(summary)} "
                  f"encrypted_len={len(enc)}")
    elif t == "message" and p.get("role") == "assistant":
        mid = p.get("id") or ""
        # short base36 tail => router-minted (badge); long hex => upstream
        origin = "router" if len(mid) < 20 else "upstream"
        text = "".join(b.get("text", "") for b in (p.get("content") or []))
        print(f"line {i}: assistant id={mid} origin={origin} {text[:80]!r}")
print(f"\nreasoning items: {reasoning}, with empty summary: {empty_summary}")
EOF
```

To see exactly what the TUI rendered (including markers), read the `agent_message` events instead — those are the client-side render records:

```bash
python3 - <<'EOF'
import json
F = "<path>/rollout-...-<SESSION_ID>.jsonl"
for i, line in enumerate(open(F), 1):
    o = json.loads(line)
    p = o.get("payload") or {}
    if p.get("type") == "agent_message":
        print(f"line {i}: {(p.get('message') or '')[:120]!r}")
EOF
```

The routing marker renders with invisible provenance characters (`⁣⁠`) prefixed by `EnableCodexBadgeProvenance` — that prefix is how the router recognizes and strips its own badge on replay. Seeing those codepoints confirms the marker came from the router, not the model.

### 5. Check token accounting before calling anything "missing"

```bash
python3 - <<'EOF'
import json
F = "<path>/rollout-...-<SESSION_ID>.jsonl"
last = None
for line in open(F):
    o = json.loads(line)
    p = o.get("payload") or {}
    if p.get("type") == "token_count":
        last = p
if last:
    print(json.dumps(last.get("info", {}).get("total_token_usage", {}), indent=2))
    print("rate_limits:", json.dumps(last.get("rate_limits", {}))[:300])
EOF
```

`reasoning_output_tokens > 0` with zero rendered summaries = the model reasoned and the summary was withheld upstream, **not** a router bug. `output_tokens` vs `reasoning_output_tokens` also tells you whether an apparently empty turn produced anything at all.

### 6. Fetch cloud logs filtered by client_session_id

Unlike the Claude path, filter directly on the session ID:

```bash
gcloud logging read \
  'resource.type="cloud_run_revision"
   AND resource.labels.service_name="router"
   AND jsonPayload.client_session_id="<SESSION_ID>"' \
  --project <project_id> --limit 100 --format=json \
  > /tmp/codex_logs.json
```

If that returns nothing, widen in this order before assuming the session bypassed the router:

1. Add a UTC time window from the transcript (`timestamp` of first and last line) — log retention and index lag both bite.
2. Try a free-text search for the id: `'"<SESSION_ID>"'` — it may appear on lines emitted before `bindRequestLogger` runs.
3. Check `session_meta.model_provider` (step 2) — a non-router provider means there is nothing to find.

Then summarize the routing decisions:

```bash
python3 - <<'EOF'
import json
for entry in json.load(open("/tmp/codex_logs.json")):
    p = entry.get("jsonPayload", {})
    if not p.get("decision_model"):
        continue
    print(json.dumps({
        "timestamp": entry.get("timestamp"),
        "message": p.get("message"),
        "requested_model": p.get("requested_model"),
        "decision_model": p.get("decision_model"),
        "decision_provider": p.get("decision_provider"),
        "decision_reason": p.get("decision_reason"),
        "upstream_status": p.get("upstream_status"),
        "sticky_hit": p.get("sticky_hit"),
    }))
EOF
```

`ProxyOpenAIChatCompletion complete` is the per-turn summary line for Codex traffic (see gotchas — the name is an artifact of `ProxyOpenAIResponses` delegating into the shared path). Its fields answer most questions on their own: `decision_model`, `decision_provider`, `decision_reason`, `failover_used`, `upstream_status`, `route_ms`, `proxy_ms`.

### 7. Correlate transcript + cloud logs

Join on:

- **`client_session_id`** (exact — the primary key for Codex).
- **`turn_id`** from the transcript ↔ turn boundaries in the logs, by timestamp within the session.
- **Served model**: cloud `decision_model` ↔ the model named in the transcript's routing marker. If they disagree, the marker was stale or stripped — trust the log.

What the logs then tell you: which model/provider actually served, whether a fallback or failover fired, whether the pin was sticky, whether the upstream returned a non-200, and how usage was accounted.

### 8. Trace to the responses code path

Codex traffic enters at [internal/api/openai/responses.go](../../../internal/api/openai/responses.go) → `proxy.Service.ProxyOpenAIResponses` ([internal/proxy/service.go](../../../internal/proxy/service.go)). From there the path forks:

| Situation | Path | Code |
|---|---|---|
| ChatGPT subscription (JWT + `ChatGPT-Account-ID`) | native Responses passthrough, bytes forwarded verbatim | `codexResponsesRequest`, `ResponsesWriter.SetPassthrough` / `SetPassthroughBadge` in [internal/translate/responses.go](../../../internal/translate/responses.go) |
| Prepaid / BYOK, OpenAI decision | Responses → chat completions → Responses re-emit | `ConvertResponsesToChatCompletionsWithOptions`, [emit_openai_responses.go](../../../internal/translate/emit_openai_responses.go) |
| Non-OpenAI decision (Anthropic/Gemini/compat) | Responses → chat → provider format → Responses re-emit | [responses_to_openai_chat_writer.go](../../../internal/translate/responses_to_openai_chat_writer.go), [emit_openai_responses_from_openai.go](../../../internal/translate/emit_openai_responses_from_openai.go) |
| Badge / marker injection + strip | synthetic assistant item, provenance-prefixed | `emitNativeBadgeBeforeOutput`, `StripRoutingBadgeFromResponsesInput` in [responses.go](../../../internal/translate/responses.go) |
| Codex-specific item shapes | `custom_tool_call` vs `function_call` id prefixes | [responses_codex.go](../../../internal/translate/responses_codex.go), `toolCallItemIDPrefix` |

Read backward from the emitter to the upstream event that triggered it. On the passthrough path there is no emitter — the bytes are the upstream's, so the answer lives upstream or in the request Codex built.

## Example: "zero thinking is showing up"

1. Transcript located at `~/.codex/sessions/2026/08/28/rollout-2026-08-28T09-10-06-<id>.jsonl` (step 1).
2. Histogram: 101 `reasoning` items, 99 `token_count` events, 93 `custom_tool_call` pairs (step 2).
3. `turn_context`: requested `gpt-5.6-sol`, `effort: "high"`, `summary: "auto"` (step 3).
4. Every reasoning item has `summary: []` and 1,400–4,600 chars of `encrypted_content` (step 4).
5. Final `token_count`: ~8,300 `reasoning_output_tokens` — the model definitely reasoned (step 5).
6. Cloud logs filtered by `client_session_id` show `decision_model=gpt-5.6-luna`, `decision_provider=openai`, HTTP 200s, no upstream errors (step 6).
7. Path is native Codex passthrough: `SetPassthrough()` forwards upstream bytes unchanged (step 8).
8. Verdict: not an inference bug (reasoning tokens produced), not a translation-loss bug (encrypted reasoning preserved verbatim). The upstream returned encrypted reasoning with an empty public `summary` under `summary: "auto"`, so Codex has no text to render. Synthesizing text from `encrypted_content` is impossible and would be wrong.

## Notes

- On the passthrough path the transcript records the **native Responses** shape — the same bytes the upstream sent, minus what Codex chose not to persist. On the translated path it records the router's re-emitted Responses shape, which is where translation artifacts live.
- `compacted` + `context_compacted` mark a mid-session context handover; items before it may not reflect what the model actually saw afterward. Check for them before reasoning about "the model ignored X".
- `turn_aborted` with `reason: "interrupted"` is a user cancel, not a failure — don't chase it in the logs.
- To reproduce a Codex-path artifact locally (without prod), use the sibling [test-codex-locally](../test-codex-locally/SKILL.md) skill.
