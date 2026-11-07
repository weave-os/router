package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

type subscriptionPlanReading struct {
	plan      providers.SubscriptionPlan
	err       error
	expiresAt time.Time
}

// SubscriptionPlans owns credential-scoped provider identity and lookup freshness.
type SubscriptionPlans struct {
	reader  providers.SubscriptionPlanReader
	now     func() time.Time
	salt    []byte
	entries *lru.Cache[usage.CredentialKey, subscriptionPlanReading]
}

// NewSubscriptionPlans keeps successful readings for five minutes and failures
// for fifteen seconds. Cache keys use the subscription observer's keyed index.
func NewSubscriptionPlans(reader providers.SubscriptionPlanReader, now func() time.Time) *SubscriptionPlans {
	if now == nil {
		now = time.Now
	}
	entries, _ := lru.New[usage.CredentialKey, subscriptionPlanReading](4096)
	return &SubscriptionPlans{reader: reader, now: now, salt: []byte(uuid.NewString()), entries: entries}
}

// Resolve returns only provider-verified identity; quota observations never
// classify a plan. Caller cancellation is not cached across requests.
func (r *SubscriptionPlans) Resolve(ctx context.Context, token []byte) (providers.SubscriptionPlan, error) {
	if err := ctx.Err(); err != nil {
		return providers.SubscriptionPlanUnknown, err
	}
	key := usage.KeyFor(r.salt, token)
	if reading, cached := r.entries.Get(key); cached && reading.expiresAt.After(r.now()) {
		return reading.plan, reading.err
	}
	profileCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	plan, err := r.reader.SubscriptionPlan(profileCtx, token)
	cancel()
	if ctx.Err() != nil {
		return providers.SubscriptionPlanUnknown, ctx.Err()
	}
	ttl := 5 * time.Minute
	if err != nil || plan == providers.SubscriptionPlanUnknown {
		plan = providers.SubscriptionPlanUnknown
		ttl = 15 * time.Second
	}
	r.entries.Add(key, subscriptionPlanReading{plan: plan, err: err, expiresAt: r.now().Add(ttl)})
	return plan, err
}
