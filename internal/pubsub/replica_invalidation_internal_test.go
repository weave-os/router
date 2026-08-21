package pubsub

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"workweave/router/internal/auth"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// newTestReplicaInvalidation builds a ReplicaInvalidation with injected seams
// and instant sleeps so tests never wait on real backoff.
func newTestReplicaInvalidation(
	create func(ctx context.Context) (string, func(), error),
	sub subscriberReceiver,
	caches ...auth.InstallationInvalidator,
) *ReplicaInvalidation {
	r := &ReplicaInvalidation{
		create:    create,
		subscribe: func(string) subscriberReceiver { return sub },
		caches:    caches,
		sleep:     func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		bgDone:    make(chan struct{}),
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	return r
}

func TestStartReplicaInvalidation_BootSuccessWiresListener(t *testing.T) {
	sub := &fakeSubscriber{messages: []*gcppubsub.Message{{Data: []byte("install-1")}}}
	cache := &fakeAPIKeyCache{}
	var cleaned atomic.Bool
	r := newTestReplicaInvalidation(func(_ context.Context) (string, func(), error) {
		return "projects/p/subscriptions/s-1", func() { cleaned.Store(true) }, nil
	}, sub, cache)

	require.NoError(t, r.start())

	require.Eventually(t, func() bool {
		return len(cache.Invalidated()) == 1
	}, time.Second, 5*time.Millisecond, "listener must forward messages after a successful boot")
	assert.Equal(t, []string{"install-1"}, cache.Invalidated())

	r.Stop()
	assert.True(t, cleaned.Load(), "Stop must delete the subscription")
}

func TestStartReplicaInvalidation_PermanentBootErrorFailsFast(t *testing.T) {
	permErr := fmt.Errorf("create per-replica subscription: %w", status.Error(codes.NotFound, "topic missing"))
	var calls atomic.Int32
	r := newTestReplicaInvalidation(func(_ context.Context) (string, func(), error) {
		calls.Add(1)
		return "", nil, permErr
	}, &fakeSubscriber{})

	err := r.start()

	require.Error(t, err)
	assert.ErrorIs(t, err, permErr)
	assert.Equal(t, int32(1), calls.Load(), "a permanent error must not be retried")
	assert.NotPanics(t, r.Stop, "Stop must be safe after a failed boot")
}

func TestStartReplicaInvalidation_TransientBootFailureRetriesInBackground(t *testing.T) {
	sub := &fakeSubscriber{messages: []*gcppubsub.Message{{Data: []byte("install-9")}}}
	cache := &fakeAPIKeyCache{}
	var calls atomic.Int32
	r := newTestReplicaInvalidation(func(_ context.Context) (string, func(), error) {
		if calls.Add(1) <= bootCreateAttempts+1 {
			return "", nil, status.Error(codes.DeadlineExceeded, "slow admin api")
		}
		return "projects/p/subscriptions/s-2", func() {}, nil
	}, sub, cache)

	require.NoError(t, r.start(), "a transient failure must not fail boot")

	require.Eventually(t, func() bool {
		return len(cache.Invalidated()) == 1
	}, time.Second, 5*time.Millisecond, "listener must attach once the background retry succeeds")
	r.Stop()
}

func TestStartReplicaInvalidation_StopHaltsBackgroundRetry(t *testing.T) {
	r := newTestReplicaInvalidation(func(_ context.Context) (string, func(), error) {
		return "", nil, status.Error(codes.Unavailable, "unreachable")
	}, &fakeSubscriber{})
	// Keep boot-attempt backoffs instant, then block the retry loop in its
	// backoff wait so the test asserts Stop interrupts a sleeping loop
	// rather than racing a hot one.
	var sleeps atomic.Int32
	r.sleep = func(ctx context.Context, _ time.Duration) error {
		if sleeps.Add(1) < bootCreateAttempts {
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	}

	require.NoError(t, r.start())

	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop must halt the background retry loop")
	}
}

func TestStartReplicaInvalidation_StopDuringCreateDeletesFreshSubscription(t *testing.T) {
	var cleaned atomic.Bool
	var calls atomic.Int32
	creating := make(chan struct{})
	r := newTestReplicaInvalidation(func(ctx context.Context) (string, func(), error) {
		if calls.Add(1) <= bootCreateAttempts {
			return "", nil, status.Error(codes.DeadlineExceeded, "boot attempt fails")
		}
		// Simulate the create succeeding server-side just as shutdown begins.
		close(creating)
		<-ctx.Done()
		return "projects/p/subscriptions/s-3", func() { cleaned.Store(true) }, nil
	}, &fakeSubscriber{})

	require.NoError(t, r.start())
	select {
	case <-creating:
	case <-time.After(2 * time.Second):
		t.Fatal("the background retry loop never reached the in-flight create")
	}

	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop must return even when a create is in flight")
	}
	assert.True(t, cleaned.Load(), "a subscription created during shutdown must be deleted immediately")
}

func TestPermanentSubscriptionError(t *testing.T) {
	assert.True(t, permanentSubscriptionError(status.Error(codes.NotFound, "topic missing")))
	assert.True(t, permanentSubscriptionError(fmt.Errorf("create per-replica subscription: %w", status.Error(codes.PermissionDenied, "iam"))))
	assert.True(t, permanentSubscriptionError(errMissingPrefix))
	assert.False(t, permanentSubscriptionError(status.Error(codes.DeadlineExceeded, "slow admin api")))
	assert.False(t, permanentSubscriptionError(status.Error(codes.Unavailable, "backend down")))
	assert.False(t, permanentSubscriptionError(context.DeadlineExceeded))
	assert.False(t, permanentSubscriptionError(errors.New("plain error")))
}
