package pubsub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	gcppubsub "cloud.google.com/go/pubsub/v2"
	"github.com/stretchr/testify/require"
)

func TestPolicyReleaseListenerTriggersEveryInvalidation(t *testing.T) {
	subscriber := &fakeSubscriber{messages: []*gcppubsub.Message{{Data: []byte("staging-01/stable/release")}, {}}}
	var triggers atomic.Int64
	listener := NewPolicyReleaseListener(nil, func() { triggers.Add(1) })
	listener.subscriber = subscriber
	ctx, cancel := context.WithCancel(context.Background())
	go listener.Run(ctx)

	require.Eventually(t, func() bool { return triggers.Load() == 2 }, time.Second, 5*time.Millisecond)
	cancel()
	listener.Wait()
}
