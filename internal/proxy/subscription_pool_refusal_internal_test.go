package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/refusal"
	"weave-os/router/internal/router"
	"weave-os/router/internal/subscriptions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type poolRefusalPin struct {
	err      func(context.Context) error
	sentinel error
	kind     refusal.Kind
	text     string
}

func poolRefusalPins() map[string]poolRefusalPin {
	now := time.Now()
	cooling := now.Add(time.Hour)
	throttledSeat := &auth.SubscriptionAccount{ID: "seat", Provider: auth.SubscriptionProviderClaude, Enabled: true, State: auth.SubscriptionAccountStateCooldown, CooldownUntil: &cooling}
	svc := &Service{now: func() time.Time { return now }}
	enrollmentUnknown := func(ctx context.Context) context.Context {
		return context.WithValue(WithManagedSubscriptionUsage(ctx), ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)
	}
	callerPlanThrottledCtx := func(ctx context.Context) context.Context {
		ctx = WithManagedSubscriptionUsage(ctx)
		state, _ := ctx.Value(ManagedSubscriptionUsageContextKey{}).(*ManagedSubscriptionUsage)
		state.ThrottledProviders = map[subscriptions.Provider]time.Time{subscriptions.ProviderClaude: cooling}
		return ctx
	}
	return map[string]poolRefusalPin{
		"lease with unknown enrollment": {
			err: func(ctx context.Context) error {
				_, _, managed, err := svc.leaseManagedSubscription(enrollmentUnknown(ctx), providers.ProviderAnthropic, "claude-sonnet-4-5")
				if !managed {
					return nil
				}
				return err
			},
			sentinel: ErrSubscriptionPoolUnavailable, kind: refusal.SubscriptionPoolUnavailable,
			text: "subscription account pool unavailable",
		},
		"funding with unknown enrollment": {
			err: func(ctx context.Context) error {
				return svc.authorizeSubscriptionAPI(enrollmentUnknown(ctx), subscriptions.ProviderClaude, "", "", clientAppRefusalFunding{})
			},
			sentinel: ErrSubscriptionPoolUnavailable, kind: refusal.SubscriptionPoolUnavailable,
			text: "subscription account pool unavailable: subscription enrollment could not be verified",
		},
		"funding with every blocking seat throttled": {
			err: func(ctx context.Context) error {
				return svc.authorizeSubscriptionAPI(WithManagedSubscriptionCandidates(WithManagedSubscriptionUsage(ctx), []*auth.SubscriptionAccount{throttledSeat}), subscriptions.ProviderClaude, "", "", clientAppRefusalFunding{})
			},
			sentinel: ErrSubscriptionPoolThrottled, kind: refusal.SubscriptionPoolThrottled,
			text: "subscription account pool unavailable: every blocking account is rate-limited",
		},
		"funding with an unproven caller plan": {
			err: func(ctx context.Context) error {
				return callerPlanRefusal(ctx, subscriptions.ProviderClaude, true, now)
			},
			sentinel: ErrSubscriptionPoolUnavailable, kind: refusal.SubscriptionPoolUnavailable,
			text: "subscription account pool unavailable: caller subscription quota is available or unknown",
		},
		"funding with a throttled caller plan": {
			err: func(ctx context.Context) error {
				return callerPlanRefusal(callerPlanThrottledCtx(ctx), subscriptions.ProviderClaude, true, now)
			},
			sentinel: errCallerPlanThrottled, kind: refusal.SubscriptionPoolThrottled,
			text: "subscription account pool unavailable: every blocking account is rate-limited: caller plan is rate-limited",
		},
		"rotation budget spent while the pool is unavailable": {
			err: func(ctx context.Context) error {
				return svc.afterSubscriptionRotationBudget(ctx, router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-5"}, 1, ErrSubscriptionPoolUnavailable)
			},
			sentinel: ErrSubscriptionPoolUnavailable, kind: refusal.SubscriptionPoolUnavailable,
			text: "subscription account pool unavailable",
		},
	}
}

func TestSubscriptionPoolUnavailableAndThrottledRefusalsIgnoreTheLaneMark(t *testing.T) {
	for mark, lane := range subscriptionLaneRefusalCases() {
		for name, pin := range poolRefusalPins() {
			t.Run(mark+"/"+name, func(t *testing.T) {
				err := pin.err(lane.ctx)
				require.ErrorIs(t, err, pin.sentinel)
				assert.Equal(t, pin.text, err.Error())
				assert.Equal(t, pin.kind.Contract(), classifiedRefusal(t, err))
				assert.False(t, subscriptionLaneRefused(err))
			})
		}
	}
}

func TestOnlyACallerPlanThrottleIsCustomerOwned(t *testing.T) {
	for name, pin := range poolRefusalPins() {
		owner := classifyFailureOwner(failureOwnerInput{Err: pin.err(context.Background())})
		assert.Equal(t, errors.Is(pin.sentinel, errCallerPlanThrottled), owner == FailureOwnerClient, name)
	}
}

func TestManagedSubscriptionCommitMarksTheTurnServed(t *testing.T) {
	for mark, lane := range subscriptionLaneRefusalCases() {
		ctx := WithManagedSubscriptionUsage(lane.ctx)
		require.False(t, managedSubscriptionServed(ctx), mark)

		(&Service{}).markManagedSubscriptionServed(ctx, ctx, subscriptions.Lease{AccountID: "seat"})

		assert.True(t, managedSubscriptionServed(ctx), mark)
		assert.True(t, servedOnSubscription(ctx), mark)
	}
}

func TestEveryPoolVerdictSentinelClassifiesAsItsRefusedState(t *testing.T) {
	for on, verdict := range subscriptionPoolVerdicts {
		kind, refused := verdict.refused.Refusal()
		require.True(t, refused, "%s", on)
		assert.Equal(t, kind.Contract(), classifiedRefusal(t, verdict.sentinel), "%s", on)
		assert.Equal(t, verdict.sentinel, subscriptionPoolRefusal(context.Background(), on), "%s", on)
	}
}

func TestEveryPoolRefusalIsAFundingFailureThatSparesTheModel(t *testing.T) {
	for mark, lane := range subscriptionLaneRefusalCases() {
		for name, pin := range poolRefusalPins() {
			assert.True(t, subscriptionFundingFailure(pin.err(lane.ctx)), "%s/%s", mark, name)
		}
		for on := range subscriptionPoolVerdicts {
			assert.True(t, subscriptionFundingFailure(subscriptionPoolRefusal(lane.ctx, on)), "%s/%s", mark, on)
		}
	}
}
