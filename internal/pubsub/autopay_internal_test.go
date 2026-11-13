package pubsub

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/billing"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePublishResult struct{ err error }

func (r fakePublishResult) Get(context.Context) (string, error) { return "server-id", r.err }

type fakeAutopayPublisher struct {
	published chan *gcppubsub.Message
	err       error
	stopped   chan struct{}
}

func newFakeAutopayPublisher() *fakeAutopayPublisher {
	return &fakeAutopayPublisher{published: make(chan *gcppubsub.Message, 4), stopped: make(chan struct{})}
}

func (f *fakeAutopayPublisher) Publish(_ context.Context, msg *gcppubsub.Message) autopayPublishResult {
	f.published <- msg
	return fakePublishResult{err: f.err}
}

func (f *fakeAutopayPublisher) Stop() { close(f.stopped) }

func (f *fakeAutopayPublisher) waitForMessage(t *testing.T) *gcppubsub.Message {
	t.Helper()
	select {
	case msg := <-f.published:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for autopay publish")
		return nil
	}
}

func TestNotifyRechargeNeeded_PublishesRawOrgIDBytes(t *testing.T) {
	fp := newFakeAutopayPublisher()
	n := &AutopayNotifier{publisher: fp}

	n.NotifyRechargeNeeded(billing.OrganizationOwner("org-123"))

	msg := fp.waitForMessage(t)
	assert.Equal(t, []byte("org-123"), msg.Data)
}

func TestNotifyRechargeNeeded_PublishesSubscriberJSON(t *testing.T) {
	fp := newFakeAutopayPublisher()
	n := &AutopayNotifier{publisher: fp}
	subscriberID := "9f4c5f28-5f9e-4d5e-8b3a-0d1a3c5e7f90"

	n.NotifyRechargeNeeded(billing.SubscriberOwner(subscriberID))

	msg := fp.waitForMessage(t)
	assert.JSONEq(t,
		`{"owner_kind":"subscriber","subscriber_id":"`+subscriberID+`"}`,
		string(msg.Data))
}

func TestNotifyRechargeNeeded_PublishErrorDoesNotPanic(t *testing.T) {
	fp := newFakeAutopayPublisher()
	fp.err = assert.AnError
	n := &AutopayNotifier{publisher: fp}

	require.NotPanics(t, func() {
		n.NotifyRechargeNeeded(billing.OrganizationOwner("org-123"))
	})
	fp.waitForMessage(t)
}
