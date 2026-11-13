package pubsub

import (
	"context"
	"encoding/json"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"

	gcppubsub "cloud.google.com/go/pubsub/v2"
)

// AutopayNotifier publishes an "org needs a recharge" signal over GCP Pub/Sub
// when the router's debit hook detects a balance crossing below the org's
// autopay threshold. The Weave control-plane subscriber picks it up and charges
// the saved card; a reconciliation sweep backstops any dropped signal.
type AutopayNotifier struct {
	publisher autopayPublisher
}

// autopayPublisher is the narrow seam NotifyRechargeNeeded needs from a GCP
// Pub/Sub Publisher: publish a message and return a handle whose result can be
// awaited. Abstracting it behind an interface (rather than the concrete
// *gcppubsub.Publisher, whose Publish returns a SDK-internal PublishResult
// future) lets tests assert the exact wire payload with an in-memory fake.
type autopayPublisher interface {
	Publish(ctx context.Context, msg *gcppubsub.Message) autopayPublishResult
	Stop()
}

type autopayPublishResult interface {
	Get(ctx context.Context) (string, error)
}

// gcpAutopayPublisher adapts a real *gcppubsub.Publisher to autopayPublisher.
type gcpAutopayPublisher struct {
	inner *gcppubsub.Publisher
}

func (p gcpAutopayPublisher) Publish(ctx context.Context, msg *gcppubsub.Message) autopayPublishResult {
	return p.inner.Publish(ctx, msg)
}

func (p gcpAutopayPublisher) Stop() {
	p.inner.Stop()
}

// NewAutopayNotifier constructs a notifier backed by the supplied Publisher.
func NewAutopayNotifier(publisher *gcppubsub.Publisher) *AutopayNotifier {
	return &AutopayNotifier{publisher: gcpAutopayPublisher{inner: publisher}}
}

type subscriberRechargePayload struct {
	OwnerKind    billing.OwnerKind `json:"owner_kind"`
	SubscriberID string            `json:"subscriber_id"`
}

// NotifyRechargeNeeded publishes a compatible owner signal on the autopay topic.
// Fire-and-forget: the balance debit has already committed and the
// reconciliation sweep is the safety net, so a publish error is logged and
// dropped rather than propagated onto the already-served request.
func (n *AutopayNotifier) NotifyRechargeNeeded(owner billing.Owner) {
	if err := owner.Validate(); err != nil {
		return
	}
	payload := []byte(owner.OrganizationID)
	if owner.Kind == billing.OwnerKindSubscriber {
		var err error
		payload, err = json.Marshal(subscriberRechargePayload{
			OwnerKind:    owner.Kind,
			SubscriberID: owner.SubscriberID,
		})
		if err != nil {
			return
		}
	}
	log := observability.Get().With("owner_kind", owner.Kind)
	if owner.Kind == billing.OwnerKindOrganization {
		log = log.With("organization_id", owner.OrganizationID)
	} else {
		log = log.With("subscriber_id", owner.SubscriberID)
	}
	observability.SafeGo(log, notifyTimeout, "NotifyRechargeNeeded", func(ctx context.Context) {
		result := n.publisher.Publish(ctx, &gcppubsub.Message{Data: payload})
		if _, err := result.Get(ctx); err != nil {
			log.Warn("Failed to publish autopay recharge signal", "err", err)
		}
	})
}

// Stop flushes buffered messages and shuts the publisher's background
// goroutines down. Must be called during graceful shutdown — Client.Close()
// does not stop publishers.
func (n *AutopayNotifier) Stop() {
	n.publisher.Stop()
}
