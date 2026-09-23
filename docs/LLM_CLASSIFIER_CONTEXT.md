# Atomic classifier sessions (opt-in)

`llm_classifier` is an explicitly enrolled, release-bound strategy. It is off
unless configured at startup; neither a strategy header, an installation default,
nor a deployment default can enroll a conversation. This implementation is not
production activation. Managed gateway admission remains blocked until the
release controller can attest the classifier's deployment and authentication.

## Per-API-call boundary

Serving classifies **every new API-call prefix**, including tool continuations
within one human turn. Before dispatch, the input contains:

- the current user message;
- the last ten completed assistant text-response blocks, oldest first,
  including explicitly empty text blocks;
- counts of human messages (including the current one), tool invocations,
  and explicitly failed tool outcomes across the entire preceding prefix.

Reasoning, planning, tool arguments and result payloads are not response history.
Tool-only messages are not invented empty text responses. Tool continuations
retain the latest human request but immediately incorporate newly completed
response blocks and cumulative tool/error counts. A changed prediction can
select a different model on that call, without waiting for another human message.
Training labels were assigned per user turn; this checkpoint has not been
retrained with independently labeled API calls. Per-call serving quality must
therefore be evaluated separately from its user-turn training metrics.

`internal/proxy/classifier_context.go` builds that boundary from the existing
unclipped `translate.EscalationObservation`, independently of the clipped generic
policy wire. Native Responses must be parsed from the original request, never
the chat-completions projection. Text is not trimmed or truncated. Token limits
remain the classifier tokenizer's responsibility.

`router.ClassifierContext.WithHistoricalPredictions` constructs the V3
`/classify` JSON. Each response is joined to the latest committed API-call
checkpoint preceding its message position. Multiple output items or text blocks
from one invocation share that call's recorded prediction. Missing predictions,
gapped suffixes and invalid classes fail closed. Selected model tiers, offline
labels and retrospective classifications are not historical predictions.

## Identity, persistence and recovery

A call digest is **not** a session or authorization key. The authenticated
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

Postgres `classifier_threads` and `classifier_predictions` store hashes,
counters and classification facts, not prompts. A primary-database row lock
serializes inference and prediction commit before provider dispatch. Retries
of the exact call prefix reuse committed facts; another replica reads the same
row. Tool-loop calls append new facts even when the human-message count is
unchanged. The legacy `turn_digest` column now identifies the complete call
prefix; `input_message_count` locates its causal boundary. Conflicting histories
at an already committed call position are rejected. Ordinary
unticketed traffic retains its current strategy.

Claude Code's native one-shot WebSearch helper inherits the parent's process-wide
header. Go recognizes its exact helper system instruction, single search query
and sole native-search tool before deriving the session key. It validates the
persisted parent, then creates an isolated child bound to the same credential,
release, selection policy and expiry. A parent-scoped root digest makes identical
one-shot requests reuse that child across retries/replicas, including after the
parent advances. Different queries or parents get separate children. The child
does not inherit or update parent predictions, counters, checkpoints or pins;
provider eligibility still goes through the normal Go policy. This exception
does not infer new chats from shortened history and does not support arbitrary
subagents or compaction resets.

Each replica admits at most two concurrent classifier transactions, including
row-lock waiters. Admission happens before acquiring a shared database connection;
saturation returns 503 immediately without inference or provider dispatch. This
leaves four of the router's six connections available to other traffic. Capacity
is released after commit, rollback or cancellation; no additional pool is opened.

System/developer instructions participate in the causal digest without entering
the classifier prompt or feature counts.
Consecutive user messages before any assistant event form one input boundary:
all their text is retained, each still counts as a user message, and no prediction
is fabricated for setup-only messages. Trailing system/developer messages at
that boundary enter its digest before the first assistant event. Append-only
instruction events during a tool loop change the next API-call digest, just
like appended tool events, and trigger a new classification. Root
identity is the first committed boundary, not necessarily user-message count one.
Every admitted request also commits its complete prefix's message count and
rolling digest under the same thread lock. Subsequent requests must extend
that exact prefix, so changes to already admitted instructions, responses or
tool results fail before inference. Shortened histories (including stale retries
after a newer request commits) fail closed. Only hashes and counts are stored.
Legacy user-turn predictions (`input_message_count=0`) cannot be reused as
per-call predictions. Those threads must start fresh; the server does not
invent API-call boundaries for previously stored facts.
One replay canonicalization is Claude Code's exact transient parallel-tool
hint following a terminal `<total_tokens>` instruction, optionally followed by
Claude's numeric USD-budget line. Other instructions before that budget remain
identity-bearing, including periodic progress reminders. Claude omits that hint
on replay, so it is excluded from the persisted prefix, not from the provider
request. A second recognizes the leading system block's structured Git snapshot
and the first user block's wrapped `currentDate` field, which the client refreshes
on resume. Only those metadata fields are canonicalized for identity; surrounding
repository/user instructions remain identity-bearing. Neither provider bytes nor
classifier input text is rewritten. The token-budget instruction remains
identity-bearing; arbitrary text, modified hints, unrecognized suffixes and
history rewrites are not normalized away.
Responses `instructions` must be a string or null; unsupported shapes are
rejected instead of silently disappearing from that identity.

The pure builder rejects partial Responses continuations/item references,
unmatched or repeated tool IDs/results, open calls at a user boundary or at the
end of history, and mixed human/tool-result messages
whose boundary cannot be determined. ID-less legacy/Gemini tool calls require
an explicit pairing contract before admission. Unknown outcomes do not increment
the error count; text heuristics from spiral telemetry are deliberately not used.

Responses native `web_search_call` items with terminal `completed` or `failed`
status become a paired tool invocation/result: the result retains the entire
native event for prefix identity. They increment tool counts, and only `failed`
increments the error count. They are not response history. Older clients omit
native IDs on replay; those events receive an observation-only ID based on their
stable input position. Identical repeated searches still count separately, and
the complete original event remains identity-bearing. Explicit malformed IDs,
missing actions and unfinished search statuses are rejected rather than treated
as completed calls. The original provider conversation is never rewritten.

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

## Server configuration and rollout gates

Apply migrations through `0110_classifier-per-call` before enabling. Deploy
the updated V3 inference validator as well: older workers reject prior responses
while `user_message_count=1`. Publish the router/serving change under a new
release and enroll fresh threads; do not silently reuse a human-turn release.
The down migration refuses to discard per-call predictions to restore the old
unique-user-count constraint. Rollback requires draining those threads and an
explicit history-retention decision, not automatic deletion. Set
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
