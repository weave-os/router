package pubsub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/observability"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/durationpb"
)

const notifyTimeout = 2 * time.Second

const replicaSubscriptionTTL = 24 * time.Hour

const subscriptionDeleteTimeout = 10 * time.Second

type publisher interface {
	Publish(ctx context.Context, installationID string) error
}

type gcpPublisher struct {
	inner *gcppubsub.Publisher
}

func (p gcpPublisher) Publish(ctx context.Context, installationID string) error {
	result := p.inner.Publish(ctx, &gcppubsub.Message{Data: []byte(installationID)})
	_, err := result.Get(ctx)
	return err
}

type InvalidationNotifier struct {
	publisher publisher
	stop      func()
}

func NewInvalidationNotifier(publisherClient *gcppubsub.Publisher) *InvalidationNotifier {
	return &InvalidationNotifier{
		publisher: gcpPublisher{inner: publisherClient},
		stop:      publisherClient.Stop,
	}
}

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

func (n *InvalidationNotifier) Stop() {
	n.stop()
}

type subscriberReceiver interface {
	Receive(ctx context.Context, f func(context.Context, *gcppubsub.Message)) error
	String() string
}

type InvalidationListener struct {
	subscriber subscriberReceiver
	caches     []auth.InstallationInvalidator
	done       chan struct{}
}

func NewInvalidationListener(subscriber *gcppubsub.Subscriber, caches ...auth.InstallationInvalidator) *InvalidationListener {
	return &InvalidationListener{
		subscriber: subscriber,
		caches:     caches,
		done:       make(chan struct{}),
	}
}

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

func (l *InvalidationListener) Wait() { <-l.done }

func CreateReplicaSubscription(
	ctx context.Context,
	client *gcppubsub.Client,
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

	_, err = client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  subscriptionName,
		Topic: topicName,
		ExpirationPolicy: &pubsubpb.ExpirationPolicy{
			Ttl: durationpb.New(replicaSubscriptionTTL),
		},
	})
	if err != nil {
		return "", nil, fmt.Errorf("create per-replica subscription: %w", err)
	}

	cleanup = func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), subscriptionDeleteTimeout)
		defer cancel()
		delErr := client.SubscriptionAdminClient.DeleteSubscription(cleanupCtx, &pubsubpb.DeleteSubscriptionRequest{
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
