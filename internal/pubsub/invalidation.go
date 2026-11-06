package pubsub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// notifyTimeout bounds the publish call so a slow Pub/Sub connection can't
// stall the request that triggered the settings change.
const notifyTimeout = 2 * time.Second

// replicaSubscriptionTTL reclaims subscriptions leaked by replicas that
// crash before running their deferred cleanup.
const replicaSubscriptionTTL = 24 * time.Hour

// subscriptionDeleteTimeout bounds the cleanup call during shutdown so a slow
// Pub/Sub admin API can't hold up termination indefinitely.
const subscriptionDeleteTimeout = 10 * time.Second

// publisher is the narrow seam NotifyInstallationChanged needs from a GCP
// Pub/Sub Publisher: publish installationID and synchronously wait for the
// result. Abstracting it behind an interface (rather than the concrete
// *gcppubsub.Publisher, whose Publish returns a SDK-internal PublishResult
// future) lets tests exercise the guard/retry logic with an in-memory fake
// instead of a real Pub/Sub connection.
type publisher interface {
	Publish(ctx context.Context, installationID string) error
}

// gcpPublisher adapts a real *gcppubsub.Publisher to the publisher interface.
type gcpPublisher struct {
	inner *gcppubsub.Publisher
}

func (p gcpPublisher) Publish(ctx context.Context, installationID string) error {
	result := p.inner.Publish(ctx, &gcppubsub.Message{Data: []byte(installationID)})
	_, err := result.Get(ctx)
	return err
}

// InvalidationNotifier publishes installation invalidation events over GCP Pub/Sub.
type InvalidationNotifier struct {
	publisher publisher
	stop      func()
}

// NewInvalidationNotifier constructs a notifier backed by the supplied Publisher.
func NewInvalidationNotifier(publisherClient *gcppubsub.Publisher) *InvalidationNotifier {
	return &InvalidationNotifier{
		publisher: gcpPublisher{inner: publisherClient},
		stop:      publisherClient.Stop,
	}
}

// NotifyInstallationChanged publishes installationID on the invalidation topic.
// Fire-and-forget: the write already committed, so failures just log and rely on cache TTL.
func (n *InvalidationNotifier) NotifyInstallationChanged(installationID string) {
	if installationID == "" {
		return
	}
	log := observability.Get().With("installation_id", installationID)
	observability.SafeGo(log, notifyTimeout, "NotifyInstallationChanged", func(ctx context.Context) {
		if err := n.publisher.Publish(ctx, installationID); err != nil {
			log.Warn("Failed to publish installation invalidation", "err", err)
		}
	})
}

// Stop flushes buffered messages and shuts the publisher down. Must be
// called on graceful shutdown — Client.Close() does not stop publishers.
func (n *InvalidationNotifier) Stop() {
	n.stop()
}

// subscriberReceiver is the narrow seam Run needs from a GCP Pub/Sub
// Subscriber. *gcppubsub.Subscriber already implements this directly, so no
// adapter is required in production; the interface exists purely so tests can
// substitute an in-memory fake for Run's message-handling logic.
type subscriberReceiver interface {
	Receive(ctx context.Context, f func(context.Context, *gcppubsub.Message)) error
	String() string
}

// InvalidationListener subscribes to the invalidation topic and forwards every
// payload to the local cache; cache TTL is the fallback under sustained outages.
type InvalidationListener struct {
	subscriber subscriberReceiver
	caches     []auth.InstallationInvalidator
	done       chan struct{}
}

// NewInvalidationListener wires a listener that drops entries from every supplied
// cache on publish. Variadic so one installation-changed message fans out to both
// the API-key cache and the per-user cluster-selection cache.
func NewInvalidationListener(subscriber *gcppubsub.Subscriber, caches ...auth.InstallationInvalidator) *InvalidationListener {
	return &InvalidationListener{
		subscriber: subscriber,
		caches:     caches,
		done:       make(chan struct{}),
	}
}

// Run blocks until ctx is canceled, forwarding invalidation messages to cache.
// done is closed via defer so Wait() can't hang if a caller wraps Run with
// panic recovery (e.g. safeGo) — the defer still fires as the panic unwinds.
func (l *InvalidationListener) Run(ctx context.Context) {
	defer close(l.done)
	log := observability.Get()
	log.Info("Invalidation listener active", "subscription", l.subscriber.String())
	err := l.subscriber.Receive(ctx, func(_ context.Context, msg *gcppubsub.Message) {
		installationID := string(msg.Data)
		if installationID != "" {
			for _, c := range l.caches {
				c.InvalidateInstallation(installationID)
			}
		}
		msg.Ack()
	})
	if err != nil && ctx.Err() == nil {
		log.Warn("Invalidation listener receive loop ended unexpectedly", "err", err)
	}
}

// Wait blocks until Run has returned. Intended for shutdown coordination.
func (l *InvalidationListener) Wait() { <-l.done }

// replicaSubscriptionAttemptTimeout bounds one CreateSubscription call so a
// single hung admin RPC leaves room in the caller's budget for a retry.
const replicaSubscriptionAttemptTimeout = 10 * time.Second

// replicaSubscriptionRetryBackoff is the pause between create attempts.
const replicaSubscriptionRetryBackoff = time.Second

// subscriptionAdmin is the narrow seam CreateReplicaSubscription needs from
// the Pub/Sub subscription admin client.
type subscriptionAdmin interface {
	CreateSubscription(ctx context.Context, req *pubsubpb.Subscription, opts ...gax.CallOption) (*pubsubpb.Subscription, error)
	DeleteSubscription(ctx context.Context, req *pubsubpb.DeleteSubscriptionRequest, opts ...gax.CallOption) error
}

type replicaSubscriptionRetry struct {
	attemptTimeout time.Duration
	backoff        time.Duration
}

// CreateReplicaSubscription provisions a per-replica subscription on topicID so
// every replica sees every invalidation message — a shared subscription would
// load-balance and defeat the broadcast. Transient admin API failures are
// retried until ctx expires. Returns the subscription name and a cleanup func;
// cleanup uses context.Background() so it still runs if the caller's context is
// already canceled at shutdown.
func CreateReplicaSubscription(
	ctx context.Context,
	client *gcppubsub.Client,
	projectID string,
	topicID string,
	prefix string,
) (subscriptionName string, cleanup func(), err error) {
	return createReplicaSubscription(ctx, client.SubscriptionAdminClient, replicaSubscriptionRetry{
		attemptTimeout: replicaSubscriptionAttemptTimeout,
		backoff:        replicaSubscriptionRetryBackoff,
	}, projectID, topicID, prefix)
}

func createReplicaSubscription(
	ctx context.Context,
	admin subscriptionAdmin,
	retry replicaSubscriptionRetry,
	projectID string,
	topicID string,
	prefix string,
) (subscriptionName string, cleanup func(), err error) {
	if prefix == "" {
		return "", nil, fmt.Errorf("subscription prefix is required")
	}
	subID := fmt.Sprintf("%s-%s", strings.TrimRight(prefix, "-"), uuid.NewString())
	subscriptionName = fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	subscription := &pubsubpb.Subscription{
		Name:  subscriptionName,
		Topic: topicName,
		ExpirationPolicy: &pubsubpb.ExpirationPolicy{
			Ttl: durationpb.New(replicaSubscriptionTTL),
		},
	}

	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, retry.attemptTimeout)
		_, err = admin.CreateSubscription(attemptCtx, subscription)
		cancel()
		// The name is reused across attempts, so AlreadyExists means an
		// earlier attempt that timed out client-side did create it.
		if err == nil || (attempt > 1 && status.Code(err) == codes.AlreadyExists) {
			break
		}
		if !retryableSubscriptionCreateError(err) || ctx.Err() != nil {
			return "", nil, fmt.Errorf("create per-replica subscription after %d attempt(s): %w", attempt, err)
		}
		observability.FromContext(ctx).Warn(
			"Retrying per-replica invalidation subscription create",
			"subscription", subscriptionName,
			"attempt", attempt,
			"err", err,
		)
		select {
		case <-ctx.Done():
			return "", nil, fmt.Errorf("create per-replica subscription after %d attempt(s): %w", attempt, err)
		case <-time.After(retry.backoff):
		}
	}

	cleanup = func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), subscriptionDeleteTimeout)
		defer cancel()
		delErr := admin.DeleteSubscription(cleanupCtx, &pubsubpb.DeleteSubscriptionRequest{
			Subscription: subscriptionName,
		})
		if delErr != nil {
			observability.Get().Warn(
				"Failed to delete per-replica invalidation subscription; relying on expiration policy",
				"subscription", subscriptionName,
				"err", delErr,
			)
		}
	}
	return subscriptionName, cleanup, nil
}

func retryableSubscriptionCreateError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.DeadlineExceeded, codes.Unavailable, codes.ResourceExhausted, codes.Aborted, codes.Internal:
		return true
	default:
		return false
	}
}
