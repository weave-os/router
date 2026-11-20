# Turn signals, stuck-session detectors and telemetry rows

Contributor reference for the per-turn signals `internal/proxy` derives, the
detectors built on them, and how they land in telemetry. Thresholds live as
named constants next to each detector; this page records why they are shaped
the way they are.

## Telemetry row conventions

- A nullable column group is all-or-nothing. `NULL` means "not measured on this
  turn"; a stored zero is a measured zero. Helpers that copy a verdict onto the
  row (`applyPlannerTelemetry`, `applyEffortTelemetry`,
  `applyAuthorityShadowTelemetry`, `applyTurnSignalTelemetry`) return early
  rather than write zero values, because a stored `0.0` would read as evidence
  the planner, gate or shadow ran.
- An authority-shadow early exit (`no_pin`, `no_prior_usage`, `same_model`,
  `pricing_missing`) keeps its outcome and reason but leaves the EV columns
  `NULL`. The corrected-economics shadow verdict is pre-gate: the HMM cost gate
  can override the served outcome without mirroring it onto the shadow.
- `AuthorityShadow*` and other `*Shadow*` columns never describe what was
  served; an authoritative turn always serves `decision_model`.
- Costs are integer USD micros (`$1.00 = 1,000,000`) so sums never accumulate
  float error. `Actual` is the router's chosen binding; `Requested` is the
  client's originally requested model.
- `credential_key_prefix`/`credential_key_suffix` are the only safe display
  parts of the serving credential. Equal values across distinct router users
  reveal one subscription paying for many seats.
- `session_key` plus `role` join a telemetry row to `session_pins` offline.
- The per-turn spiral signal snapshot (`Spiral*` columns) is captured only
  when the installation allows training use **and** content capture is
  `hashed` or `full`; an unknown capture mode parses as `off`, so both gates
  fail closed. Same-file thrash records a truncated SHA-256 of the path, never
  the path.
- A turn refused because its honoured policy pin could not be served still
  writes a row (`requested=true`, `honoured=false`) so replay analysis sees it.
  Unauthorized pins and other routing failures write no row.
- Durable side writes (turn strikes, inference attempts, router feedback,
  tokenizer calibration) use `context.WithoutCancel`: the request context is often
  already cancelled once the stream ends.

## Stuck-session detectors

Text repetition and spiral signals are pure functions of the
request envelope, which carries the full history on every turn. The others need
earlier turns: no-progress keeps a per-replica ring of recent fingerprints per
session, compaction detection compares against the last counts it cached. Replay tests for those
must drive a sequence of requests. A zero session key never gets a pin row
written: such a row would be shared by every zero-keyed session.

| Detector | Catches | Acts? |
|---|---|---|
| No-progress | The same `(model, provider, tool-progress marker, prompt prefix)` fingerprint repeating inside a short window | Breaks the turn and expires the pin |
| Text repetition | The assistant narrating the same substantive line again while issuing fresh tool calls | Breaks the turn and expires the pin (`ROUTER_TEXT_REPETITION_BREAK_ENABLED`) |
| Spiral signals | Error streaks, same-file edit thrash, fuzzy repetition, monologue, A/B ping-pong, steps since last good edit | Recorded on the telemetry row only (`ROUTER_TURN_SIGNAL_CAPTURE_ENABLED`) |

- **No-progress fingerprint.** The tool-progress marker (assistant tool-call
  count plus the last call's signature) is the primary stuck signal. Message
  count is folded in only when there is no marker, because retries append
  messages even while stuck and Claude Code sub-agents hold the top-level
  count flat.
- **Compaction detection** watches for the message count dropping (full
  compaction) or the tool-call count shrinking on a flat window (rolling
  trimming). Both are gated by `compactionMinHistoryMessages`: fresh
  sub-agents and the Explore sub-agent's flat window would otherwise look like
  drops, and a false fire hands an in-flight sub-agent a lossy summary.
- No-progress recovery never clears an explicit `/force-model` pin.
- Sessions pinned by the retired cyclic-loop escalation keep their
  `loop_escalation` pin until it expires; nothing writes new ones.
- The retired escalation tables (`loop_escalation_events`,
  `spiral_shadow_events`, `struggle_shadow_events`,
  `struggle_escalation_events`) are archived: kept for posterity, written by
  nothing, and queried by no SQLC query
  (`TestArchivedEscalationTablesHaveNoQueries`).

## Cross-tokenizer usage reporting

When a thread is served by a model whose tokenizer differs from the requested
model's, the router reports client-facing input tokens in the requested model's
tokenizer so the client's auto-compaction tracks the window it believes it is
in. Billing and telemetry keep the provider's own counts.

- Only the measured Anthropic and OpenAI families calibrate; other bindings
  report provider counts unchanged.
- Anthropic reports fresh input separately from cache categories; OpenAI input
  already includes cached tokens, so the full billed prompt is computed per
  family.
- Only main-loop and tool-result turns record an observation. Utility,
  compaction and sub-agent prompts are different prompts from the history the
  next turn resends, so comparing them would measure prompts, not tokenizers.
- A requested `<provider>/<model>` is stripped to the catalog model before
  lookup, but a catalog id that itself contains a slash is kept whole.
- The thread's own billed usage also rules out models whose window the request
  no longer fits; the body-size estimate runs low for a tokenizer that bills
  history denser than its divisor assumes.

## Feedback footer

The rating footer is appended only for terminal agents (Claude Code, Codex,
Opencode) on main-loop and tool-result turns, only when durable feedback
storage is wired, and never for headless user agents. It never embeds a link.
Codex gets its own wording. The footer text must stay matched by the ingress
pattern that strips echoed footers from the next request's history;
`TestFeedbackFooter_TextIsRecognisedOnIngress` pins that pairing.
