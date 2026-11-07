package subscriptions_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/subscriptions"
)

type planReaderFunc func(context.Context, []byte) (providers.SubscriptionPlan, error)

func (f planReaderFunc) SubscriptionPlan(ctx context.Context, token []byte) (providers.SubscriptionPlan, error) {
	return f(ctx, token)
}

func TestSubscriptionPlansRetryUnknownIdentityAfterNegativeTTL(t *testing.T) {
	now := time.Unix(1_790_000_000, 0)
	lookupErr := errors.New("synthetic profile failure")
	calls := 0
	plans := subscriptions.NewPlanResolver(planReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) {
		calls++
		if calls == 1 {
			return providers.SubscriptionPlanUnknown, lookupErr
		}
		return providers.SubscriptionPlanEnterprise, nil
	}), func() time.Time { return now })
	var logs bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
	for range 2 {
		plan := plans.Resolve(ctx, []byte("synthetic-token"))
		require.Equal(t, providers.SubscriptionPlanUnknown, plan)
	}
	require.Contains(t, logs.String(), lookupErr.Error())
	require.Equal(t, 1, strings.Count(logs.String(), "Claude subscription profile unavailable"), "negative-cache hits must not repeat the provider failure log")
	now = now.Add(16 * time.Second)
	plan := plans.Resolve(context.Background(), []byte("synthetic-token"))
	require.Equal(t, providers.SubscriptionPlanEnterprise, plan)
	require.Equal(t, 2, calls)
}

func TestCanceledProfileLookupDoesNotPoisonOtherRequests(t *testing.T) {
	plans := subscriptions.NewPlanResolver(planReaderFunc(func(ctx context.Context, token []byte) (providers.SubscriptionPlan, error) {
		return providers.SubscriptionPlanMax, nil
	}), time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan := plans.Resolve(ctx, []byte("synthetic-token"))
	require.Equal(t, providers.SubscriptionPlanUnknown, plan)
	plan = plans.Resolve(context.Background(), []byte("synthetic-token"))
	require.Equal(t, providers.SubscriptionPlanMax, plan)
}

func TestConcurrentProfileLookupsShareOneRequest(t *testing.T) {
	var calls atomic.Int32
	var once sync.Once
	started, release := make(chan struct{}), make(chan struct{})
	plans := subscriptions.NewPlanResolver(planReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return providers.SubscriptionPlanMax, nil
	}), time.Now)
	type outcome struct {
		plan providers.SubscriptionPlan
	}
	const callers = 12
	results := make(chan outcome, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	begin := make(chan struct{})
	for range callers {
		go func() {
			ready.Done()
			<-begin
			plan := plans.Resolve(context.Background(), []byte("synthetic-token"))
			results <- outcome{plan: plan}
		}()
	}
	ready.Wait()
	close(begin)
	<-started
	time.Sleep(25 * time.Millisecond)
	close(release)
	for range callers {
		got := <-results
		require.Equal(t, providers.SubscriptionPlanMax, got.plan)
	}
	require.EqualValues(t, 1, calls.Load())
}

func TestProfileReaderPanicReturnsUnknownWithoutLeakingPanicValue(t *testing.T) {
	var calls atomic.Int32
	var logs bytes.Buffer
	plans := subscriptions.NewPlanResolver(planReaderFunc(func(context.Context, []byte) (providers.SubscriptionPlan, error) {
		calls.Add(1)
		panic("synthetic-sensitive-panic")
	}), time.Now)
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
	for range 2 {
		plan := plans.Resolve(ctx, []byte("synthetic-token"))
		require.Equal(t, providers.SubscriptionPlanUnknown, plan)
	}
	require.EqualValues(t, 1, calls.Load(), "the recovered failure is cached briefly")
	require.Contains(t, logs.String(), "Claude subscription profile lookup panicked")
	require.NotContains(t, logs.String(), "synthetic-sensitive-panic")
}
