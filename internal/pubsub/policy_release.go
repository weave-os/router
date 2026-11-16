package pubsub

import (
	"context"

	gcppubsub "cloud.google.com/go/pubsub/v2"

	"weave-os/router/internal/observability"
)

type PolicyReleaseListener struct {
	subscriber subscriberReceiver
	trigger    func()
	done       chan struct{}
}

func NewPolicyReleaseListener(subscriber *gcppubsub.Subscriber, trigger func()) *PolicyReleaseListener {
	return &PolicyReleaseListener{subscriber: subscriber, trigger: trigger, done: make(chan struct{})}
}

func (l *PolicyReleaseListener) Run(ctx context.Context) {
	defer close(l.done)
	err := l.subscriber.Receive(ctx, func(_ context.Context, message *gcppubsub.Message) {
		if l.trigger != nil {
			l.trigger()
		}
		message.Ack()
	})
	if err != nil && ctx.Err() == nil {
		observability.FromContext(ctx).Warn("Policy invalidation listener ended unexpectedly; polling remains authoritative", "err", err)
	}
}

func (l *PolicyReleaseListener) Wait() { <-l.done }
