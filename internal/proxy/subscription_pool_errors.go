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
)

func isSubscriptionPoolError(err error) bool {
	return errors.Is(err, ErrSubscriptionPoolExhausted) || errors.Is(err, ErrSubscriptionPoolUnavailable) || errors.Is(err, ErrClaudeEnterpriseSubscriptionUnavailable)
}
