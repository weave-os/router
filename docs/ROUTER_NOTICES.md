# Router notices

`translate.RouterNotice` carries router provenance, a unique identity, a typed
kind, display text, a served model and an optional attempt identity. Proxy policy
owns wording and suppression. `NoticeDelivery` and `NewProtocolNoticeSink` own
serialization and client-wire presentation, below provider conversion.

| Client format | Presentation | Non-streaming |
| --- | --- | --- |
| Anthropic Messages | Leading text content block | Suppressed |
| OpenAI Responses | Assistant output item with replay provenance | Prefix on assistant text |
| OpenAI Chat Completions | Leading assistant content chunk | Suppressed |
| Gemini | Leading model text part | Suppressed |

All sinks use existing protocol encoders. Native and converted streams reach the
same sink. Gemini ingress currently serves native Gemini; it has no converted
upstream path. Unknown formats and hidden presentation suppress notices. Unknown
harnesses receive only standard protocol text, never private SSE event names.

A notice can be delivered before provider headers. `NoticeCapabilities.Eager`
commits its prelude while holding the serialization lock, when the client writer
supports `CommitPrelude`. Existing Anthropic and Gemini model-choice notices keep
their buffered placement; Chat and Responses keep eager delivery. This choice
belongs to request composition, independently of a notice's kind.

The delivery owner buffers fragmented SSE records and serializes provider writes
with notice writes. It suppresses additional notices after provider content starts
or a terminal record arrives. A producer must stop on cancellation or a delivery
error; a disconnect remains latched. Header configuration happens before concurrent
streaming writes.

`ResetAttempt(true)` discards an abandoned partial record while retaining visible
notice identities and protocol continuation. `ResetAttempt(false)` also resets
buffered notice encoding, for an attempt whose prelude was never visible. The
proxy's provider-commit buffer remains the retry gate. Notice bytes do not feed
provider output-progress callbacks, timing, usage, empty-completion validation or
attempt success accounting. Existing error renderers terminate a committed stream
in its client-native format.

Responses notices retain their item IDs, output indexes and text in terminal
snapshots, including multiple notices during a retry. Sequence and output-index
rewriting happens in the Responses encoder. Converted provider summaries are
collected above this sink, so router text never contributes to them.

Replay recognition lives in `notice_content.go`. Legacy branded markers retain
their wording. Other notice prose carries an invisible router-owned prefix. The
Messages, Responses and Gemini ingress cleaners share these recognition rules;
provider thinking, signatures, tools and usage stay outside notice cleanup.

The contract initially defines `NoticeModelSelection`. A new notice kind uses the
same delivery API and sinks; it does not require modifying provider translators.
Client statusline/title helpers remain separate lifecycle integrations.

## Validation evidence

Synthetic localhost checks on October 20, 2026 used Claude Code 2.1.294 with
Anthropic Messages SSE and Codex CLI 0.160.0 with Responses SSE. Both clients
accepted initial and corrected notices, suppressed notices, provider thinking,
tool calls followed by a tool-result request, and client-native errors after an
early notice. Claude's structured stream exposed the notice and provider text;
its plain print mode returned the final answer block. Codex exposed both notice
and answer items, plus native Responses reasoning. Claude retried the synthetic
server error before reporting it; Codex reported a failed turn.

These checks used generated synthetic wire output without provider calls. They
verify CLI parsing and output, not the full interactive screen or statusline.
Policy and protocol tests separately cover opt-outs, same-model suppression,
replay cleanup, retry continuation, provider-progress exclusion, concurrent
writes, cancellation, disconnects, and terminal output consistency.
