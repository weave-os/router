package proxy

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
)

// DefaultRateLimitCooldownSeconds is how long a rescued 429 keeps the primary
// arm out of a session's automatic selection when
// ROUTER_RATE_LIMIT_COOLDOWN_SECONDS is unset.
const DefaultRateLimitCooldownSeconds = 45

// rateLimitTurn is one turn's account of the transient-rate-limit paths it
// took, for the completion line. Nil (flag off) contributes no fields.
type rateLimitTurn struct {
	mu sync.Mutex
	// cooldownUntil is the expiry written for this turn's rescued 429, zero
	// when the turn demoted nothing or demoted permanently.
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

type rateLimitTurnContextKey struct{}

// withRateLimitTurn attaches a fresh rateLimitTurn to ctx.
func withRateLimitTurn(ctx context.Context) (context.Context, *rateLimitTurn) {
	turn := &rateLimitTurn{}
	return context.WithValue(ctx, rateLimitTurnContextKey{}, turn), turn
}

// rateLimitTurnFromContext returns the turn's rateLimitTurn, or nil when the
// flag is off for this request.
func rateLimitTurnFromContext(ctx context.Context) *rateLimitTurn {
	turn, _ := ctx.Value(rateLimitTurnContextKey{}).(*rateLimitTurn)
	return turn
}

func (t *rateLimitTurn) recordCooldown(until time.Time, cooldown time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cooldownUntil = until
	t.cooldownMs = cooldown.Milliseconds()
}

func (t *rateLimitTurn) recordThrottle(decision dispatch.ThrottleDecision) {
	if t == nil || !decision.Honoured {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.retryAfterHonoured = true
	t.retryAfterMs += decision.Delay.Milliseconds()
}

func (t *rateLimitTurn) recordRescuePoolExhausted(readmitted string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rescuePoolExhausted = true
	t.rescueReadmitted = append(t.rescueReadmitted, readmitted)
}

// completionLogFields is the completion line's account of the turn's
// rate-limit handling. Empty while the flag is off so the line is unchanged.
func (t *rateLimitTurn) completionLogFields() []any {
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

// throttleRetryDelay is the dispatch.Transport.RetryDelay for a turn under
// transient_rate_limit: a 429's Retry-After is honoured up to the cap, one
// above the cap ends same-binding retries so the turn goes to rescue, and a
// bare 429 backs off 500 ms then 1.5 s. Nil (flag off) keeps the executor's
// default schedule.
func (s *Service) throttleRetryDelay(ctx context.Context) func(dispatch.Attempt, error, time.Duration) (time.Duration, bool) {
	turn := rateLimitTurnFromContext(ctx)
	if turn == nil {
		return nil
	}
	return dispatch.ThrottlePolicy{
		Cap:     dispatch.DefaultThrottleRetryAfterCap,
		Backoff: dispatch.DefaultThrottleBackoff,
		Now:     s.clockNow,
		Observe: turn.recordThrottle,
	}.RetryDelay
}

// mergeDemotionCooldowns unions two pins' cooldown maps, keeping the later
// expiry when both name a model.
func mergeDemotionCooldowns(a, b map[string]time.Time) map[string]time.Time {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(map[string]time.Time, len(a)+len(b))
	for model, until := range a {
		merged[model] = until
	}
	for model, until := range b {
		if current, ok := merged[model]; !ok || until.After(current) {
			merged[model] = until
		}
	}
	return merged
}

// activeDemotionCooldowns narrows cooldowns to those still in force at now.
func activeDemotionCooldowns(cooldowns map[string]time.Time, now time.Time) map[string]time.Time {
	active := make(map[string]time.Time, len(cooldowns))
	for _, model := range sessionpin.ActiveCooldowns(cooldowns, now) {
		active[model] = cooldowns[model]
	}
	if len(active) == 0 {
		return nil
	}
	return active
}

// readmittableCooldowns narrows the active cooldowns to the arms an exhausted
// rescue may readmit this turn: a session-lifetime strike wins over a
// leftover cooldown on the same model, and a text-only arm cannot take an
// image-bearing turn the scorer already kept it out of.
func readmittableCooldowns(cooling map[string]time.Time, permanent []string, hasImages bool) map[string]time.Time {
	out := make(map[string]time.Time, len(cooling))
	for model, until := range cooling {
		if slices.Contains(permanent, model) {
			continue
		}
		if hasImages && !catalog.AcceptsImages(model) {
			continue
		}
		out[model] = until
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// cooldownsByExpiry lists the models in cooldowns soonest-to-recover first,
// ties broken by name, so an exhausted rescue readmits the arm that has
// cooled the longest before one that was throttled a moment ago.
func cooldownsByExpiry(cooldowns map[string]time.Time) []string {
	models := make([]string, 0, len(cooldowns))
	for model := range cooldowns {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool {
		if !cooldowns[models[i]].Equal(cooldowns[models[j]]) {
			return cooldowns[models[i]].Before(cooldowns[models[j]])
		}
		return models[i] < models[j]
	})
	return models
}

// isRateLimitedPrimaryFailure reports whether the rescued primary's failure
// is a buffered upstream 429: throttling of this caller, not a dead arm.
func isRateLimitedPrimaryFailure(err error) bool {
	return providers.IsUpstreamRateLimited(err)
}

// withRateLimitTurn attaches a turn account when transient_rate_limit is on
// for the request; off, ctx is returned as-is and the account is nil, so
// every consumer keeps the flag-off path.
func (s *Service) withRateLimitTurn(ctx context.Context) (context.Context, *rateLimitTurn) {
	if !s.ResolveTransientRateLimit(ctx) {
		return ctx, nil
	}
	return withRateLimitTurn(ctx)
}
