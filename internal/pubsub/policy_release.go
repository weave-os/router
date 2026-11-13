package pubsub

import (
	"context"

	gcppubsub "cloud.google.com/go/pubsub/v2"

	"weave-os/router/internal/observability"
)

// PolicyReleaseListener triggers a local GCS lane-head refresh for every
// best-effort policy invalidation. Polling remains the authority and backstop.
type PolicyReleaseListener struct {
	subscriber subscriberReceiver
	trigger    func()
	done       chan struct{}
}

// NewPolicyReleaseListener constructs a broadcast listener for one router replica.
func NewPolicyReleaseListener(subscriber *gcppubsub.Subscriber, trigger func()) *PolicyReleaseListener {
	return &PolicyReleaseListener{subscriber: subscriber, trigger: trigger, done: make(chan struct{})}
}

// Run receives invalidations until cancellation and acknowledges each only
// after the in-memory refresh trigger has been queued.
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

// Wait blocks until Run exits.
func (l *PolicyReleaseListener) Wait() { <-l.done }
