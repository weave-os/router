package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/requestcontext"

	"github.com/stretchr/testify/assert"
)

const conditionalModelsSubscriptionToken = "sk-ant-oat01-conditional-models"

func conditionalModelsContext(active, inactive []string) context.Context {
	ctx := context.WithValue(context.Background(), credentials.AnthropicSubscriptionContextKey{}, conditionalModelsSubscriptionToken)
	ctx = context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenActiveContextKey{}, active)
	return context.WithValue(ctx, requestcontext.InstallationSubscriptionModelsWhenInactiveContextKey{}, inactive)
}

func conditionalModelsObserverFor(token string, snapshot usage.Snapshot) *usage.Observer {
	now := time.Unix(1_000_000, 0)
	observer := usage.NewObserver([]byte("conditional-models-salt"), time.Minute, func() time.Time { return now })
	observer.Record(observer.Key([]byte(token)), snapshot)
	return observer
}

func conditionalModelsObserver(snapshot usage.Snapshot) *usage.Observer {
	return conditionalModelsObserverFor(conditionalModelsSubscriptionToken, snapshot)
}

func TestPreferredModelsForRequest_SeparatesInstallationAndSubscriptionPreferences(t *testing.T) {
	ctx := context.WithValue(context.Background(), requestcontext.InstallationPreferredModelsContextKey{}, []string{"grok-4.1-fast", "gpt-5.6-sol"})

	assert.Equal(t, []string{"grok-4.1-fast", "gpt-5.6-sol"}, (&Service{engine: dispatch.NewEngine(nil), hmmServing: hmmPorts()}).preferredModelsForRequest(ctx))
}
