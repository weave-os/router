package proxy

import (
	"errors"
	"fmt"
)

var (
	ErrSubscriptionPoolExhausted   = errors.New("subscription account pool exhausted")
	ErrSubscriptionPoolUnavailable = errors.New("subscription account pool unavailable")
	// ErrSubscriptionPoolThrottled is a pool-unavailable refusal whose only
	// blockers are rate-limit cooldowns: a client-retryable 429, not an outage.
	ErrSubscriptionPoolThrottled = fmt.Errorf("%w: every blocking account is rate-limited", ErrSubscriptionPoolUnavailable)
	// errCallerPlanThrottled is a throttled refusal caused by a rate limit on
	// the caller's own plan, which is the customer's capacity to own.
	errCallerPlanThrottled = fmt.Errorf("%w: caller plan is rate-limited", ErrSubscriptionPoolThrottled)
)

func isSubscriptionPoolError(err error) bool {
	return errors.Is(err, ErrSubscriptionPoolExhausted) || errors.Is(err, ErrSubscriptionPoolUnavailable) || errors.Is(err, ErrClaudeEnterpriseSubscriptionUnavailable)
}
