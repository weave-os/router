# Atomic classifier context (staged)

This is input-contract groundwork for the `llm_classifier` strategy, not a
registered serving strategy or a production rollout. No deployment default,
installation setting, network call, or persistence behavior changes here.

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

## Identity and recovery requirements before wiring

A turn digest is **not** a session or authorization key. Persistence must scope
it by authenticated installation, credential/thread identity and immutable
classifier release. Identical retry inputs produce the same digest. Branches
with different prefixes produce different digests. Persist the prediction before
dispatch, with atomic first-writer behavior for overlapping retries; do not store
raw prompts just to recover a digit.

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

It cannot detect a client silently replacing the entire prefix with a summary.
Nor can it recover predictions from sessions that predate this classifier.
Production therefore needs an admission/recovery policy and a stable,
subagent-safe identity across compaction. Never infer continuity solely from a
shared parent session ID, reset lifetime counts silently, or fall back to another
strategy after admitting a classifier session.

Remaining integration: durable prediction lookup and retry handling, authenticated
classifier transport and immutable release verification, Go-owned arm selection,
installation-scoped admission, compaction/continuation recovery, and staging
decision/first-token latency measurements. The ordinary
[policy release gates](POLICY_ROUTER_HARNESS.md#release-gates) still apply.
