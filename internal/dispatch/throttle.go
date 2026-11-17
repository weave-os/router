package dispatch

import (
	"time"

	"weave-os/router/internal/providers"
)

const (
	DefaultThrottleRetryAfterCap = 10 * time.Second
)

var DefaultThrottleBackoff = []time.Duration{500 * time.Millisecond, 1500 * time.Millisecond}

type ThrottleDecision struct {
	RetryAfter    time.Duration
	HasRetryAfter bool
	Honoured      bool
	Delay         time.Duration
	Retry         bool
}

type ThrottlePolicy struct {
	Cap     time.Duration
	Backoff []time.Duration
	Now     func() time.Time
	Observe func(ThrottleDecision)
}

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
