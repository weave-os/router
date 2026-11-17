package dispatch

import (
	"context"
	"sync"
	"time"
)

type ThrottleLedger struct {
	mu                  sync.Mutex
	cooldownUntil       time.Time
	cooldownMs          int64
	retryAfterHonoured  bool
	retryAfterMs        int64
	rescuePoolExhausted bool
	rescueReadmitted    []string
}

type throttleLedgerContextKey struct{}

func WithThrottleLedger(ctx context.Context) (context.Context, *ThrottleLedger) {
	turn := &ThrottleLedger{}
	return context.WithValue(ctx, throttleLedgerContextKey{}, turn), turn
}

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
	if !decision.Honoured {
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
