# Atomic classifier sessions (opt-in)

`llm_classifier` is an explicitly enrolled, release-bound strategy. It is off
unless configured at startup; neither a strategy header, an installation default,
nor a deployment default can enroll a conversation. This implementation is not
production activation. Managed gateway admission remains blocked until the
release controller can attest the classifier's deployment and authentication.

## V3 boundary

The trained target is a **user turn**, not an independently labeled API call.
Before each human message is answered, the input contains:

- the current user message;
- the last ten completed assistant text-response blocks, oldest first,
  including explicitly empty text blocks;
- counts of human messages (including the current one), tool invocations,
  and explicitly failed tool outcomes across the entire preceding prefix.

Reasoning, planning, tool arguments and result payloads are not response history.
Tool-only messages are not invented empty text responses. Tool continuations
reuse the current user boundary; their new responses and counts enter the next
user boundary, matching the training example construction. Reclassifying every
tool continuation with these new events would be a different input contract.

`internal/proxy/classifier_context.go` builds that boundary from the existing
unclipped `translate.EscalationObservation`, independently of the clipped generic
policy wire. Native Responses must be parsed from the original request, never
the chat-completions projection. Text is not trimmed or truncated. Token limits
remain the classifier tokenizer's responsibility.

`router.ClassifierContext.WithHistoricalPredictions` constructs the V3
`/classify` JSON. Each response refers to its owning turn's causal digest;
all blocks from that turn receive its recorded prediction. Missing predictions,
gapped suffixes and invalid classes fail closed. Selected model tiers, offline
labels and retrospective classifications are not historical predictions.

## Identity, persistence and recovery

A turn digest is **not** a session or authorization key. The authenticated
`POST /v1/router/threads` handshake takes a fresh `new_chat_id` UUID and returns
a `thread_token`. Clients persist the UUID before the call, retry it unchanged,
and create a distinct UUID for each genuinely new child conversation. The
server binds the ticket to the installation, credential, classifier release,
Go selection-policy hash and expiry.
Tickets expire after 30 days without renewal or release rebinding.

Send the ticket as `X-Weave-Classifier-Thread` or the top-level JSON field
`weave_classifier_thread`. Middleware consumes both before request capture and
provider dispatch. Conflicting/empty tokens fail closed. Tickets apply to
Messages, Chat Completions, Responses and Gemini inference; dry-run routing,
handoff, router commands, force-model/cluster, shadow evaluation and policy-pin
overrides are not supported for admitted threads.
Title-generation, quota probes, short-form classification and compaction requests
carrying a thread ticket return 409 before creating classifier facts or session
pins; they cannot establish or replace the conversation root.
An OpenCode client whose enrollment fails sends the reserved
`weave-classifier-unavailable` header value. Middleware returns a non-retryable
400 without dispatch; other invalid tickets still return 409. This is necessary
because OpenCode catches plugin hook exceptions and retries 409 responses.

Postgres `classifier_threads` and `classifier_predictions` store hashes,
counters and classification facts, not prompts. A primary-database row lock
serializes inference and prediction commit before provider dispatch. Retries
and tool loops reuse the committed facts; another replica reads the same row.
Different histories at an already committed user ordinal are rejected. Ordinary
unticketed traffic retains its current strategy.

Each replica admits at most two concurrent classifier transactions, including
row-lock waiters. Admission happens before acquiring a shared database connection;
saturation returns 503 immediately without inference or provider dispatch. This
leaves four of the router's six connections available to other traffic. Capacity
is released after commit, rollback or cancellation; no additional pool is opened.

System/developer instructions participate in the causal digest without entering
the classifier prompt or feature counts. An instruction change within a user
turn requires a new human boundary; it cannot reuse that turn's old prediction.
Responses `instructions` must be a string or null; unsupported shapes are
rejected instead of silently disappearing from that identity.

The pure builder rejects partial Responses continuations/item references,
unmatched or repeated tool IDs/results, open calls at a user boundary or at the
end of history, and mixed human/tool-result messages
whose boundary cannot be determined. ID-less legacy/Gemini tool calls require
an explicit pairing contract before admission. Unknown outcomes do not increment
the error count; text heuristics from spiral telemetry are deliberately not used.

Media-bearing observations (including text mixed with images, audio or files)
are rejected because their omitted payloads cannot establish classifier identity.
Translation retains an in-memory omission flag, not media bytes, without changing
the escalation wire. The builder must consume the original parsed observation,
not a JSON round-trip that loses that flag. User messages with no retained text
blocks are also rejected; explicit empty text remains distinguishable and valid.

The persisted root digest and historical prediction join reject prefix rewrites
and missing earlier predictions. A ticket establishes identity, not lost history:
compaction cannot recover exact counters or missing response blocks. Router
compaction is disabled for admitted threads. Unrecoverable history returns 409,
the transport byte cap returns 413, and classifier/storage failure returns 503,
without switching to another classifier. Start a new conversation when a
thread can no longer supply its complete causal prefix.

## Pi client admission

Set `WEAVE_PI_LLM_CLASSIFIER=1` on Pi 0.83+ with this bundled extension. Only an
explicit new session or empty fresh startup using the Weave provider enrolls.
Existing/resumed, seeded, forked and unsupported unticketed sessions stay on
their existing strategy. New child processes enroll independently; they must
not copy their parent's ticket. Other clients may implement the handshake only
at a verified new-thread lifecycle event, never inferred from message count.

Enrollment is retained in session entries across reload/resume/tree navigation.
Changing router URL/provider, losing enrollment, or a failed pending handshake
aborts the provider request instead of sending it unticketed. Turning the opt-in
off affects only future enrollments. Admitted sessions disable Pi compaction,
extension auto-compaction and legacy handoff/escalation.

## OpenCode client admission

Set `WEAVE_OPENCODE_LLM_CLASSIFIER=1` with the bundled plugin. A
`session.created` event persists a distinct UUID for each parent and child;
`chat.headers` then authenticates the handshake and sends the resulting ticket
on every inference request. The auth-loader fetch hook is not a reliable place
to inject tickets: the pinned CLI can bypass it while still running
`chat.headers`. OpenCode also swallows a hook exception, so the header hook
sets the reserved fail-closed marker before enrollment and replaces it only
on success. Existing sessions without a creation record cannot enroll while
opted in; reopen them with the opt-in unset or start a new session. Title
requests remain unticketed, and compaction or a changed router origin blocks
further classifier dispatch.

## Claude Code and Codex local proxy admission

The `capture/` proxy in WorkWeave has a separate, explicit
`WEAVE_CAPTURE_LLM_CLASSIFIER=1` mode. It is off by default and requires a
private durable state file, a shared hook token and a router upstream. Its
loopback-only authenticated hook endpoint accepts new parent lifecycle events,
Claude `SubagentStart` IDs, and Codex `PreToolUse spawn_agent` permits. The
first Codex child request must carry both a distinct `X-Codex-Window-Id` UUID
and a matching `X-Codex-Parent-Thread-Id`; Claude children carry
`X-Claude-Code-Agent-Id`. Both clients otherwise reuse the parent's session
ID, so that ID alone is never child authorization. The proxy persists each
new UUID before its authenticated handshake and attaches its own ticket to
inference. Unknown and pre-opt-in resumed sessions fail closed; a compacted
session is blocked. See `capture/README.md` for installation and opt-in.

## Server configuration and rollout gates

Apply migration `0105_classifier-threads` before enabling. Set
`ROUTER_LLM_CLASSIFIER_CONFIG` to a server-owned JSON file containing:

| Field | Contract |
| --- | --- |
| `release` | Immutable `llm-classifier-vMAJOR.MINOR.PATCH` name |
| `release_sha256` | Lowercase SHA-256 of the classifier release manifest |
| `endpoint` | HTTPS origin, without query/userinfo; transport calls `/classify` |
| `selection_policy_file` | Local Go-selection-policy JSON |
| `selection_policy_sha256` | SHA-256 of that exact policy file |
| `installation_ids` | Nonempty server-owned enrollment allowlist |

Set `ROUTER_LLM_CLASSIFIER_BEARER` and
`ROUTER_LLM_CLASSIFIER_SIGNING_KEY` from secrets (at least 32 bytes each).
The policy must have schema `hmm_go_selection_policy_v1` and class order
`low`, `medium`, `high`, `maximum`. Startup validates these pins. The HTTPS
transport has a 25-second timeout, no redirect/retry/fallback, a 1 MB request
cap and a 16 KiB response cap. Responses must identify the pinned release and
report valid digit probabilities, historical-prediction provenance and at most
8,192 formatted tokens. The classifier validates the full ten-block input, then
fits the largest contiguous newest suffix by dropping whole oldest response
blocks. Empty blocks count; whole-prefix counters and historical complexities
do not change. The exact tokenizer counts the system prompt, JSON and chat
wrapper for each candidate. There is no six-block minimum.

If the current message alone exceeds the budget, the classifier keeps its
beginning and end with an explicit middle-omission marker. Response metadata
reports original/fitted token counts, received/retained/dropped history counts,
and current-message elision. The release manifest pins this fitting policy as
`recent_complete_responses_head_tail_v1`; older reject-on-overflow releases are
not equivalent. This is a classifier-only projection: causal identity, stored
history and the selected completion model's original conversation/context
window remain unchanged. Go alone selects the eligible model/provider.

This configuration deliberately refuses `ROUTER_SERVING_ASSERTION_KEY` managed
admission. A bearer-authenticated Modal endpoint does not satisfy the existing
Cloud Run IAM/revision/OCI attestation contract. Do not bypass deployment
ownership by enabling a legacy staging lane. A controller integration, full
trained-artifact quality gates, actual staging router decision/first-token
latency and replica-recovery checks remain required before production. Model
endpoint latency alone does not pass the
[policy release gates](POLICY_ROUTER_HARNESS.md#release-gates).

Local persistence validation on a migrated disposable database:

```sh
ROUTER_TEST_DATABASE_URL=postgres://... go run ./scripts/classifier_session_check
```
