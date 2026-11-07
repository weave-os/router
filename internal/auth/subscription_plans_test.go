package auth_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
)

type planReaderFunc func(context.Context, []byte) (providers.SubscriptionPlan, error)

func (f planReaderFunc) SubscriptionPlan(ctx context.Context, token []byte) (providers.SubscriptionPlan, error) {
	return f(ctx, token)
}

func TestSubscriptionPlansRetryUnknownIdentityAfterNegativeTTL(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	lookupErr := errors.New("synthetic profile failure")
	calls := 0
	plans := auth.NewSubscriptionPlans(planReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) {
		calls++
		if calls == 1 {
			return providers.SubscriptionPlanUnknown, lookupErr
		}
		return providers.SubscriptionPlanEnterprise, nil
	}), func() time.Time { return now })
	for range 2 {
		plan, err := plans.Resolve(context.Background(), []byte("synthetic-token"))
		require.ErrorIs(t, err, lookupErr)
		require.Equal(t, providers.SubscriptionPlanUnknown, plan)
	}
	now = now.Add(16 * time.Second)
	plan, err := plans.Resolve(context.Background(), []byte("synthetic-token"))
	require.NoError(t, err)
	require.Equal(t, providers.SubscriptionPlanEnterprise, plan)
	require.Equal(t, 2, calls)
}

func TestCanceledProfileLookupDoesNotPoisonOtherRequests(t *testing.T) {
	plans := auth.NewSubscriptionPlans(planReaderFunc(func(ctx context.Context, token []byte) (providers.SubscriptionPlan, error) {
		return providers.SubscriptionPlanMax, nil
	}), time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, err := plans.Resolve(ctx, []byte("synthetic-token"))
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, providers.SubscriptionPlanUnknown, plan)
	plan, err = plans.Resolve(context.Background(), []byte("synthetic-token"))
	require.NoError(t, err)
	require.Equal(t, providers.SubscriptionPlanMax, plan)
}
