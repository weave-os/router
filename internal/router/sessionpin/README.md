# internal/router/sessionpin

Inner-ring contract for session-sticky routing pins: pure types plus the `Store` interfaces. The Postgres adapter lives in `internal/postgres` (`session_pin_repo.go`); the proxy's golden fake mirrors its semantics.

A `Pin` is one row in the session-pin table and mirrors a routing decision so a hit rehydrates `router.Decision` without re-running the scorer. Rows are keyed by `(SessionKey, Role)`. `SessionKey` is a sha256 digest truncated to `SessionKeyLen` (16) bytes, which makes collisions astronomically rare at per-(API key, session) granularity. `DefaultRole` is the only role emitted today; the column exists so a turn-type detector can land non-breakingly.

## Pin field lifecycle

| Field | Written by | Rule |
| --- | --- | --- |
| `Effort` | `Upsert` | The `:level` suffix of `/force-model` or `x-weave-force-model`; empty means the model default. Persisted so later turns served from the pin carry it. |
| `ForcedThreadKey` | `Upsert` | Restricts an explicit pin to one thread digest; empty means the force applies to every thread in the conversation. |
| `PairedProvider`/`PairedModel` | `Upsert` | The scorer's runner-up. Refreshed on a genuine scorer re-run, preserved on a same-model refresh, cleared on a model change without a fresh pair (force-model, loop-break, eviction). Empty for non-scorer pins and single-candidate routing. A per-turn policy swaps within the pair without re-scoring. |
| `Strategy` | `Upsert` | The routing strategy that produced the pin. During rollout non-beta stable strategies may accept an empty legacy value; beta never does. |
| `PolicyGroup` | `Upsert` | The HMM complexity cluster of the pinned decision (`RoutingMetadata.PolicyGroup`); compared with the fresh decision's group to tell a within-cluster reroute from a cluster change. |
| `Last*Tokens`, `LastTurnEndedAt` | `UpdateUsage` | Previous turn's usage. `Upsert` leaves them so a start-of-turn refresh can't clobber them with zeros. |
| `LastOutputLimitAt` | `UpdateUsage` | Marks a confirmed output cap only while nonzero and equal to `LastTurnEndedAt`; an older writer that refreshes usage without it retires the stale marker by the mismatch. Cleared on healthy writes; `Upsert` preserves it within a strategy and clears it on replacement. Set from `Usage.OutputLimitReached`, which is true only when the upstream terminal payload reported an output-limit stop reason; token count alone never sets it. |
| `ConsecutiveUpstreamErrors` | `IncrementUpstreamErrors` | Consecutive non-retryable upstream errors (4xx other than 408/429); the turn loop evicts at two strikes. `Upsert` preserves it on a same-model refresh and resets it on a model switch. |
| `ConsecutiveOverloadErrors` | `IncrementOverloadErrors` | Consecutive 529-exhausted turns on the pinned provider. A 529 is retryable in-turn, so it never trips the upstream-error counter. |
| `ConsecutiveDowngradeVotes`/`ConsecutiveUpgradeVotes` | `Upsert` | Consecutive held HMM downgrade (cheaper model) or same-group upgrade proposals. Unlike the error counters, `Upsert` takes the incoming value: the turn loop derives it from the pin it read at turn start. |
| `LastServedModel` | `UpdateUsage` | Compared with the next target to detect a mid-session switch, so stale Anthropic thinking signatures that would 400 on the new model are stripped. |
| `HasEverSwitched` | `UpdateUsage` | Latches true the first time the session serves a model other than `LastServedModel` and stays set for the pin's life; the emit path ORs it into `ModelSwitched` so blocks from an earlier excursion are stripped on every later turn. |
| `DisabledProviders` | `DisableProvider` | Providers struck out after repeated 529 exhaustion. Only grows; `Upsert` never touches it. |
| `DemotedModels` | `ExpireAndDemoteModel` | Models struck out after an upstream failure. Only grows; `Upsert` never touches it. |
| `DemotionCooldowns` | `ExpireAndCoolDownModel` | Models withdrawn only until the recorded instant: a rescued rate limit is throttling, not a dead arm. A model in `DemotedModels` is out regardless. `ActiveCooldowns` lists the unexpired ones, sorted for deterministic exclusion order. |
| `TurnStrikes` | `StrikeTurnModel` | Models that failed and were rescued during the current agent turn; out of the session until the next real user prompt (`ClearTurnStrikes`). |
| `TokenizerCalibration` | `UpdateTokenizerCalibration` | Only `TokenizerCalibrationStore` writes it; `Upsert` and `UpdateUsage` leave it. |

### `UpdateUsage`

A `Usage` names the `Strategy` whose pin may receive it; the update is ignored when another strategy has since replaced the row. `ServedProvider` is persisted into `Provider` only when the pinned model is empty or matches `ServedModel` (including `model:effort` variants), so a cross-model rescue keeps the pinned binding. `PriorServedModel` is optional caller evidence used to latch `HasEverSwitched` when the row is new or has no `last_served_model`. `SessionEverSwitched` carries an already-latched sibling-row verdict so a fresh role row keeps switch history even when its model is unchanged.

## Store contracts

- `Get` returns `(zero, false, nil)` when no row exists and does not filter expired rows; the caller checks `PinnedUntil`.
- In-place mutations are no-ops on an evicted, missing or different-strategy pin, so a late request cannot mutate a replacement row.
- `Consume` atomically removes and returns one unexpired pin, for one-shot continuations where `Get` followed by an expiry write would let two concurrent requests reuse it.
- `IncrementUpstreamErrors`/`IncrementOverloadErrors` atomically bump and return the new count so the turn loop evicts without a cross-pod read-modify-write race; a missing pin returns `(0, nil)`. The matching `Reset*` calls run after a successful turn.
- `DisableProvider` appends (deduped) and resets `ConsecutiveOverloadErrors` in the same write.
- `ExpireAndDemoteModel` writes an already-expired marker for the `(SessionKey, Role)` row and appends the model to `DemotedModels` (deduped) in one write. It seeds an absent row, rewrites an existing row only while it still belongs to `expired.Strategy` (the same match as `Consume`), and records the `DemotionReason` on the eviction trail, not the row.
- `CooldownStore.ExpireAndCoolDownModel` is the time-limited counterpart: same seeding and strategy guard, but it records (overwriting) a `DemotionCooldowns` deadline. It is a separate interface so lifetime-only stores still satisfy `Store`; callers fall back to a lifetime demotion without it.
- `TurnStrikeStore` is optional the same way; callers skip the strike without it. `StrikeTurnModel` only touches an existing row.
- `RecoveryProbeStore` admits one half-open request per session/model across workers. The lease is measured on the store's clock so every worker agrees when an abandoned lease frees, and the token makes release safe after expiry and reacquisition. `ClearDemotionCooldown` removes a cooldown after a successful probe unless the stored deadline is later than `observedUntil`, so a newer cooldown from a concurrent failure survives.
- `TokenizerCalibrationStore` is optional: without it the router never calibrates and cross-tokenizer usage is reported in the served model's own tokens. A missing pin or strategy mismatch is a no-op, matching `UpdateUsage`.

### Demotion reasons

| `DemotionReason` | Withdrawal |
| --- | --- |
| `committed_stream_failure` | Stream died after the prelude committed, so the turn could neither retry nor fail over. |
| `rescued_failure` | Primary arm failed pre-commit and was handed to a same-cluster sibling. |
| `response_header_timeout` | Upstream timed out before sending response headers. |
| `unrescued_stall` | A watchdog failure exhausted the turn before output with no eligible rescue candidate. |
| `rate_limited` | Pre-commit 429 handed to a same-cluster sibling; time-limited. |
| `transient_failure` | Rescued transient gateway failure; withdrawn for the configured cooldown only. |

## Tokenizer calibration

Bindings sharing a `TokenizerFamily` bill the same history with the same count; across families the counts differ (the same Claude Code history bills roughly 1.5-1.8x more tokens on Claude than on GPT, varying with the mix of code, JSON and prose). `anthropic` covers every Anthropic-format binding including gateways; `openai` covers OpenAI and OpenAI-compatible gateways serving OpenAI models.

- A `TokenizerObservation` is the full prompt (input + cache reads + cache writes) one family billed on its latest served turn, so it measures the whole history regardless of cache state, plus the router's body-size `Estimate` for the same request.
- `Observe` returns a new calibration (the receiver is unchanged). When another family observed the thread within `TokenizerCalibrationMaxGap` and both prompts reach `TokenizerCalibrationMinPrompt`, the ratio is (re)measured in both directions. The gap keeps both counts on nearly the same history (a Claude Code thread grows a few thousand tokens per turn); the floor excludes short histories where per-request overhead (system framing, tool schemas) dominates.
- A measured ratio outlives its observations: on production thread switches a thread's own ratio predicted its next switch within 0.5% (median), against 4.3% for a fleet-wide average.
- `ProjectedPrompt` scales the newest estimate-bearing observation's tokens-per-estimate rate (so a compacted thread projects small again) by the measured ratio, or else by the floor of the spread (1.5x GPT->Claude, 1/1.8 Claude->GPT) so a projection used to rule models out never overstates the target family's count.
- `MergeTokenizerCalibrations` combines a thread's rows, keeping the newest observation per family and the newest measurement per pair.
