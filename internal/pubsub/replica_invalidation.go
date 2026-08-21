package pubsub

import (
	"context"
	"errors"
	"sync"
	"time"

	"workweave/router/internal/auth"
	"workweave/router/internal/observability"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Boot gets a bounded budget for creating the per-replica subscription; a
// replica that can't finish inside it serves with TTL-only cache invalidation
// while retryLoop keeps trying, so a slow Pub/Sub admin API during a cold
// start degrades cache freshness instead of crashing the instance.
const (
	createAttemptTimeout = 15 * time.Second
	bootCreateAttempts   = 2
	bootAttemptBackoff   = 2 * time.Second
	retryBackoffFloor    = 10 * time.Second
	retryBackoffCap      = 5 * time.Minute
)

// permanentSubscriptionError reports whether a CreateSubscription failure is
// misconfiguration that retrying cannot fix. Unknown codes count as
// transient: for this component the safe default is to keep the replica
// alive and retry, because the cache TTL already bounds staleness.
func permanentSubscriptionError(err error) bool {
	if errors.Is(err, errMissingPrefix) {
		return true
	}
	s, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch s.Code() {
	case codes.NotFound, codes.PermissionDenied, codes.Unauthenticated,
		codes.InvalidArgument, codes.FailedPrecondition, codes.Unimplemented:
		return true
	default:
		return false
	}
}

// ReplicaInvalidation owns this replica's invalidation pipeline: per-replica
// subscription creation (with background retry after a transient boot
// failure), the listener that fans messages out to caches, and teardown.
type ReplicaInvalidation struct {
	create    func(ctx context.Context) (subscriptionName string, cleanup func(), err error)
	subscribe func(subscriptionName string) subscriberReceiver
	caches    []auth.InstallationInvalidator
	sleep     func(ctx context.Context, d time.Duration) error

	ctx    context.Context
	cancel context.CancelFunc
	bgDone chan struct{}

	mu       sync.Mutex
	cleanup  func()
	listener *InvalidationListener
}

// StartReplicaInvalidation provisions the per-replica subscription and starts
// the listener that fans invalidation messages out to caches.
// Misconfiguration (missing topic, denied IAM, empty prefix) is returned so
// boot can fail fast; a transient failure (slow or unreachable Pub/Sub admin
// API during a cold start) degrades instead of failing — the replica serves
// with TTL-bounded staleness while a background loop retries and attaches the
// listener once creation succeeds.
func StartReplicaInvalidation(
	client *gcppubsub.Client,
	projectID string,
	topicID string,
	prefix string,
	caches ...auth.InstallationInvalidator,
) (*ReplicaInvalidation, error) {
	r := &ReplicaInvalidation{
		create: func(ctx context.Context) (string, func(), error) {
			return CreateReplicaSubscription(ctx, client, projectID, topicID, prefix)
		},
		subscribe: func(subscriptionName string) subscriberReceiver { return client.Subscriber(subscriptionName) },
		caches:    caches,
		sleep:     sleepCtx,
		bgDone:    make(chan struct{}),
	}
	r.ctx, r.cancel = context.WithCancel(context.Background())
	return r, r.start()
}

func (r *ReplicaInvalidation) start() error {
	log := observability.Get()
	backoff := bootAttemptBackoff
	var lastErr error
	for attempt := 1; attempt <= bootCreateAttempts; attempt++ {
		if attempt > 1 {
			if err := r.sleep(r.ctx, backoff); err != nil {
				break
			}
			backoff *= 2
		}
		subscriptionName, cleanup, err := r.createOnce()
		if err == nil {
			close(r.bgDone)
			r.attach(subscriptionName, cleanup)
			return nil
		}
		if permanentSubscriptionError(err) {
			close(r.bgDone)
			return err
		}
		lastErr = err
		log.Warn("Transient failure creating per-replica invalidation subscription", "attempt", attempt, "err", err)
	}
	log.Error("Could not create per-replica invalidation subscription at boot; serving with TTL-only cache invalidation while retrying in background", "err", lastErr)
	goRecovered("invalidation-subscription-retry", r.retryLoop)
	return nil
}

// retryLoop keeps attempting subscription creation after a transient boot
// failure, backing off exponentially up to retryBackoffCap, until creation
// succeeds, the error turns permanent, or Stop cancels the context.
func (r *ReplicaInvalidation) retryLoop() {
	defer close(r.bgDone)
	log := observability.Get()
	backoff := retryBackoffFloor
	for {
		if err := r.sleep(r.ctx, backoff); err != nil {
			return
		}
		subscriptionName, cleanup, err := r.createOnce()
		if err == nil {
			r.attach(subscriptionName, cleanup)
			return
		}
		if permanentSubscriptionError(err) {
			log.Error("Per-replica invalidation subscription creation failed permanently; serving with TTL-only cache invalidation", "err", err)
			return
		}
		log.Warn("Retrying per-replica invalidation subscription creation", "backoff_sec", int(backoff.Seconds()), "err", err)
		backoff = min(backoff*2, retryBackoffCap)
	}
}

func (r *ReplicaInvalidation) createOnce() (subscriptionName string, cleanup func(), err error) {
	ctx, cancel := context.WithTimeout(r.ctx, createAttemptTimeout)
	defer cancel()
	return r.create(ctx)
}

// attach wires the listener for a freshly created subscription, or deletes it
// right away if Stop won the race while creation was in flight.
func (r *ReplicaInvalidation) attach(subscriptionName string, cleanup func()) {
	r.mu.Lock()
	if r.ctx.Err() != nil {
		r.mu.Unlock()
		cleanup()
		return
	}
	r.cleanup = cleanup
	listener := &InvalidationListener{
		subscriber: r.subscribe(subscriptionName),
		caches:     r.caches,
		done:       make(chan struct{}),
	}
	r.listener = listener
	r.mu.Unlock()
	observability.Get().Info("Created per-replica invalidation subscription", "subscription", subscriptionName)
	goRecovered("invalidation-listener", func() { listener.Run(r.ctx) })
}

// Stop halts any background retry, stops the listener, and deletes the
// subscription (best effort — the expiration policy reclaims it otherwise).
// Safe to call even when boot-time creation failed.
func (r *ReplicaInvalidation) Stop() {
	r.cancel()
	<-r.bgDone
	r.mu.Lock()
	listener := r.listener
	cleanup := r.cleanup
	r.mu.Unlock()
	if listener != nil {
		listener.Wait()
	}
	if cleanup != nil {
		cleanup()
	}
}

// goRecovered mirrors cmd/router's safeGo for this package's long-running
// goroutines: a panic logs and kills the goroutine, not the process.
func goRecovered(name string, fn func()) {
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				observability.Get().Error("Background goroutine panicked", "goroutine", name, "panic", rec)
			}
		}()
		fn()
	}()
}

// sleepCtx blocks for d or until ctx is done, returning ctx.Err() in the
// latter case so callers can distinguish shutdown from a completed wait.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
