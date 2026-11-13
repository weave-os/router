package dispatch

import (
	"context"
	"sync"
	"time"
)

// ThrottleLedger is one turn's account of the transient-rate-limit paths it
// took, for the completion line. Nil (flag off) contributes no fields.
type ThrottleLedger struct {
	mu sync.Mutex
	// cooldownUntil is the expiry written for this turn's rescued transient
	// failure, zero when the turn demoted nothing or demoted permanently.
	cooldownUntil time.Time
	cooldownMs    int64
	// retryAfterHonoured is true when a same-binding retry waited the
	// upstream's Retry-After; retryAfterMs is that wait.
	retryAfterHonoured bool
	retryAfterMs       int64
	// rescuePoolExhausted is true when every eligible rescue candidate failed
	// and the walk dispatched the cooling-down arms in rescueReadmitted.
	rescuePoolExhausted bool
	rescueReadmitted    []string
}

type throttleLedgerContextKey struct{}

// WithThrottleLedger attaches a fresh ThrottleLedger to ctx.
func WithThrottleLedger(ctx context.Context) (context.Context, *ThrottleLedger) {
	turn := &ThrottleLedger{}
	return context.WithValue(ctx, throttleLedgerContextKey{}, turn), turn
}

// ThrottleLedgerFromContext returns the turn's ThrottleLedger, or nil when the
// flag is off for this request.
func ThrottleLedgerFromContext(ctx context.Context) *ThrottleLedger {
	turn, _ := ctx.Value(throttleLedgerContextKey{}).(*ThrottleLedger)
	return turn
}

func (t *ThrottleLedger) RecordCooldown(until time.Time, cooldown time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cooldownUntil = until
	t.cooldownMs = cooldown.Milliseconds()
}

func (t *ThrottleLedger) recordThrottle(decision ThrottleDecision) {
	if t == nil || !decision.Honoured {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retryAfterHonoured = true
	t.retryAfterMs += decision.Delay.Milliseconds()
}

func (t *ThrottleLedger) RecordRescuePoolExhausted(readmitted string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rescuePoolExhausted = true
	t.rescueReadmitted = append(t.rescueReadmitted, readmitted)
}

// CompletionLogFields is the completion line's account of the turn's
// rate-limit handling. Empty while the flag is off so the line is unchanged.
func (t *ThrottleLedger) CompletionLogFields() []any {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	expiresAt := ""
	if !t.cooldownUntil.IsZero() {
		expiresAt = t.cooldownUntil.UTC().Format(time.RFC3339)
	}
	return []any{
		"demotion_expires_at", expiresAt,
		"cooldown_ms", t.cooldownMs,
		"retry_after_honoured", t.retryAfterHonoured,
		"retry_after_ms", t.retryAfterMs,
		"rescue_pool_exhausted", t.rescuePoolExhausted,
		"rescue_pool_readmitted", t.rescueReadmitted,
	}
}

// RetryDelay is the Transport.RetryDelay for a turn under
// transient_rate_limit: a 429's Retry-After is honoured up to the cap, one
// above the cap ends same-binding retries so the turn goes to rescue, and a
// bare 429 backs off 500 ms then 1.5 s. Nil (flag off) keeps the executor's
// default schedule.
func (t *ThrottleLedger) RetryDelay(now func() time.Time) func(Attempt, error, time.Duration) (time.Duration, bool) {
	if t == nil {
		return nil
	}
	return ThrottlePolicy{
		Cap:     DefaultThrottleRetryAfterCap,
		Backoff: DefaultThrottleBackoff,
		Now:     now,
		Observe: t.recordThrottle,
	}.RetryDelay
}
