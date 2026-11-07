package subscriptions

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/sync/singleflight"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy/usage"
)

type subscriptionPlanReading struct {
	plan      providers.SubscriptionPlan
	err       error
	expiresAt time.Time
}

// PlanResolver owns credential-scoped provider identity and lookup freshness.
type PlanResolver struct {
	reader  providers.SubscriptionPlanReader
	now     func() time.Time
	salt    []byte
	entries *lru.Cache[usage.CredentialKey, subscriptionPlanReading]
	lookups singleflight.Group
}

// NewPlanResolver keeps successful readings for five minutes and failures
// for fifteen seconds. Cache keys use the subscription observer's keyed index.
func NewPlanResolver(reader providers.SubscriptionPlanReader, now func() time.Time) *PlanResolver {
	if now == nil {
		now = time.Now
	}
	entries, _ := lru.New[usage.CredentialKey, subscriptionPlanReading](4096)
	return &PlanResolver{reader: reader, now: now, salt: []byte(uuid.NewString()), entries: entries}
}

// Resolve returns only provider-verified identity; quota observations never
// classify a plan. Caller cancellation is not cached across requests.
func (r *PlanResolver) Resolve(ctx context.Context, token []byte) (providers.SubscriptionPlan, error) {
	if err := ctx.Err(); err != nil {
		return providers.SubscriptionPlanUnknown, err
	}
	key := usage.KeyFor(r.salt, token)
	if reading, cached := r.entries.Get(key); cached && reading.expiresAt.After(r.now()) {
		return reading.plan, reading.err
	}
	result := r.lookups.DoChan(string(key), func() (value any, lookupErr error) {
		defer func() {
			if recover() != nil {
				observability.FromContext(ctx).Error("Claude subscription profile lookup panicked")
				reading := subscriptionPlanReading{plan: providers.SubscriptionPlanUnknown, err: errors.New("subscription profile lookup panicked"), expiresAt: r.now().Add(15 * time.Second)}
				r.entries.Add(key, reading)
				value, lookupErr = reading, nil
			}
		}()
		if reading, cached := r.entries.Get(key); cached && reading.expiresAt.After(r.now()) {
			return reading, nil
		}
		profileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		plan, err := r.reader.SubscriptionPlan(profileCtx, token)
		if err != nil {
			observability.FromContext(ctx).Warn("Claude subscription profile unavailable", "err", err)
		}
		ttl := 5 * time.Minute
		if err != nil || plan == providers.SubscriptionPlanUnknown {
			plan = providers.SubscriptionPlanUnknown
			ttl = 15 * time.Second
		}
		reading := subscriptionPlanReading{plan: plan, err: err, expiresAt: r.now().Add(ttl)}
		r.entries.Add(key, reading)
		return reading, nil
	})
	select {
	case <-ctx.Done():
		return providers.SubscriptionPlanUnknown, ctx.Err()
	case outcome := <-result:
		reading := outcome.Val.(subscriptionPlanReading)
		return reading.plan, reading.err
	}
}
