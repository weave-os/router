# internal/dispatch

The single execution boundary between policy-resolved plans and provider
adapters. It owns the boot-time provider client registry, walks a plan's
targets with the shared retry and failover rules, checks that the prepared wire
request names the plan target before any upstream I/O, and emits ordered
attempt events and the operation summary. Feature packages depend on
`inference.Executor`; only this package dereferences `providers.Client` (see
[the inference boundary](../../docs/INFERENCE_BOUNDARY.md)).

## Client registry

`NewClients` copies the boot map, so later mutation of the caller's map cannot
change dispatch. A provider mapped to a nil client counts as registered for
eligibility (`Has`, `Names`) but `Client` refuses it with
`ErrProviderNotConfigured`.

## Executor rules

`Executor.Run` tries the selected target, then each policy alternative in order:

- A target is retried in place only when it is the sole target and the error is
  transient, at most `MaxSameBindingRetries` times and within a wall-clock
  budget, so a hung upstream does not burn a full header timeout per attempt.
- Otherwise a retryable, model-not-found or billing-blocked error fails over to
  the next target, but only while nothing has reached the client
  (`Transport.Committed`).
- `Budget().MaxAttempts`, when positive, bounds all attempts including
  same-target retries.
- `Transport.Terminal` ends the operation even when the error class would allow
  a retry or failover (for example, a credential lease could not be obtained).
- `Transport.Bound` keeps the operation on the attempted target: transient
  errors may retry there but never fail over (a caller-bound credential served
  it).
- `Transport.RetryDelay` picks the wait before a same-target retry; returning
  `false` ends same-target retries so the operation moves on to failover. The
  wall-clock budget still caps the delay.
- An `AttemptFunc` must not fall back on its own; it returns the upstream error
  unmodified so the executor can apply these rules.
- `AttemptSink` receives events in order and must not block dispatch on
  persistence failures. An empty `Transport.OperationID` falls back to the
  purpose, so set it when one request runs several operations of a purpose.
- The funding guard (`WithAuthorization`) is captured before attempt contexts
  are prepared and checked against the final credentials right before
  `Client.Proxy`. `ErrAuthorizationDenied` is a pre-I/O denial, never retried or
  failed over. A context without a guard is not checked.

## Target guard

`ValidatePreparedTarget` accepts a body whose top-level `model` is the target's
catalog ID or upstream ID (adapters rewrite catalog IDs). Bodies with no
top-level model (Gemini carries it in the URL path) pass; such callers use
`ValidateWireModel`. `GuardTarget` gives surfaces that prepare the body inside
their attempt closure the same pre-I/O check as `Buffered`, whose `Transport`
fails with `ErrTargetMismatch` before any upstream I/O.

## Throttling

`ThrottlePolicy` treats an upstream 429 as throttling, not a transient fault:

- A `Retry-After` at or under the cap (`DefaultThrottleRetryAfterCap`, 10 s) is
  honoured verbatim.
- A longer one ends same-target retries; the arm is dead for this turn and the
  turn goes to rescue.
- A 429 without the header waits `DefaultThrottleBackoff` (500 ms, then 1.5 s,
  the last step repeating), because a per-minute quota will not clear in the
  executor's 250 ms default.
- Every other error keeps the executor's default schedule.

`ThrottleLedger` is the per-turn record of those paths for the completion log
line. It exists only while the `transient_rate_limit` flag is on; a nil ledger
adds no log fields and returns a nil `RetryDelay`, so the executor keeps its
default schedule.

## Response writers

`PreludeBuffer` holds pre-upstream writes (the eager SSE prelude) so failover can
retry on another binding before the client sees bytes from a primary that
failed. `Seal` ends the prelude phase; the first write after it commits the
buffered status and body and switches to pass-through. `Committed` is the retry
gate. Before commit, `Flush` does nothing, and `Discard` restores the headers
captured at construction and runs `OnDiscard` so writers above drop their
output too. `CommitPrelude` makes the prelude client-visible without marking
the attempt committed, so a later provider error can still retry or fail over.

`EarlyStopGate` sits above `PreludeBuffer` on a Claude Code tool-result turn and
classifies the Anthropic stream. Armed, it withholds the stream until the first
`tool_use` block or a non-`end_turn` stop reason; a turn that ends on
`end_turn` without a tool call stays withheld so the caller can re-serve it.
In shadow mode it only observes. It releases after 1 MiB of held bytes or after
60 s on a timer (a reasoning upstream may write nothing). Claude Code drops a
stream after 180 s without bytes and a redo still has to stream inside that
window, so the hold must stay well under 180 s. Upstream bytes go out through
an `io.Writer` field rather than `ResponseWriter.Write` so CodeQL's
reflected-XSS model does not read the SSE proxy chain as an HTML response.
`ArmOutputProgress` forwards to the writer below so withheld bytes do not count
as client-visible progress for the stall watchdog.

## Gateway capabilities

`GatewayCapabilities` remembers, per endpoint key, that a customer gateway
serves no Responses API or rejects `prompt_cache_key`, so only the first turn
pays the probe. The empty key is never remembered; the zero value is ready to
use.
