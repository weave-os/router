package pubsub

import (
	"context"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"

	gcppubsub "cloud.google.com/go/pubsub/v2"
)

type AutopayNotifier struct {
	publisher autopayPublisher
}

type autopayPublisher interface {
	Publish(ctx context.Context, msg *gcppubsub.Message) autopayPublishResult
	Stop()
}

type autopayPublishResult interface {
	Get(ctx context.Context) (string, error)
}

type gcpAutopayPublisher struct {
	inner *gcppubsub.Publisher
}

func (p gcpAutopayPublisher) Publish(ctx context.Context, msg *gcppubsub.Message) autopayPublishResult {
	return p.inner.Publish(ctx, msg)
}

func (p gcpAutopayPublisher) Stop() {
	p.inner.Stop()
}

func NewAutopayNotifier(publisher *gcppubsub.Publisher) *AutopayNotifier {
	return &AutopayNotifier{publisher: gcpAutopayPublisher{inner: publisher}}
}

func (n *AutopayNotifier) NotifyRechargeNeeded(owner billing.Owner) {
	if err := owner.Validate(); err != nil {
		return
	}
	payload := []byte(owner.OrganizationID)
	log := observability.Get().With("owner_kind", owner.Kind)
	if owner.Kind == billing.OwnerKindOrganization {
		log = log.With("organization_id", owner.OrganizationID)
	}
	observability.SafeGo(log, notifyTimeout, "NotifyRechargeNeeded", func(ctx context.Context) {
		result := n.publisher.Publish(ctx, &gcppubsub.Message{Data: payload})
		if _, err := result.Get(ctx); err != nil {
			log.Warn("Failed to publish autopay recharge signal", "err", err)
		}
	})
}

func (n *AutopayNotifier) Stop() {
	n.publisher.Stop()
}
