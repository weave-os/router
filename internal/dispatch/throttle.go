package dispatch

import (
	"time"

	"weave-os/router/internal/providers"
)

const (
	// DefaultThrottleRetryAfterCap bounds the Retry-After a same-target retry
	// honours; a longer wait is a dead arm for this turn and goes to rescue.
	DefaultThrottleRetryAfterCap = 10 * time.Second
)

// DefaultThrottleBackoff spaces same-target retries of a 429 that carries no
// Retry-After: a per-minute quota is not going to clear in 250 ms.
var DefaultThrottleBackoff = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

// ThrottleDecision is one ThrottlePolicy verdict, reported to Observe.
type ThrottleDecision struct {
	// RetryAfter is the parsed header value; HasRetryAfter is false when the
	// upstream sent none (or an unparseable one).
	RetryAfter    time.Duration
	HasRetryAfter bool
	// Honoured is true when the retry waited for RetryAfter itself.
	Honoured bool
	// Delay is the wait chosen; Retry is false when the same target is not
	// tried again (Retry-After above the cap).
	Delay time.Duration
	Retry bool
}

// ThrottlePolicy is a Transport.RetryDelay that treats an upstream 429 as
// throttling rather than a transient fault: a Retry-After at or under Cap is
// honoured verbatim, one above it ends same-target retries, and a 429 without
// the header waits Backoff[retry] (the last entry repeats). Every other error
// keeps the executor's default schedule.
type ThrottlePolicy struct {
	Cap     time.Duration
	Backoff []time.Duration
	Now     func() time.Time
	Observe func(ThrottleDecision)
}

// RetryDelay implements Transport.RetryDelay.
func (p ThrottlePolicy) RetryDelay(attempt Attempt, err error, backoff time.Duration) (time.Duration, bool) {
	if !providers.IsUpstreamRateLimited(err) {
		return backoff, true
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	limit := p.Cap
	if limit <= 0 {
		limit = DefaultThrottleRetryAfterCap
	}
	schedule := p.Backoff
	if len(schedule) == 0 {
		schedule = DefaultThrottleBackoff
	}
	decision := ThrottleDecision{Retry: true}
	decision.RetryAfter, decision.HasRetryAfter = providers.RetryAfter(err, now())
	switch {
	case decision.HasRetryAfter && decision.RetryAfter > limit:
		decision.Retry = false
	case decision.HasRetryAfter:
		decision.Honoured = true
		decision.Delay = decision.RetryAfter
	default:
		decision.Delay = schedule[min(attempt.SameBindingRetry, len(schedule)-1)]
	}
	if p.Observe != nil {
		p.Observe(decision)
	}
	return decision.Delay, decision.Retry
}
