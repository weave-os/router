package proxy

import (
	"context"
	"testing"
	"time"

	"weave-os/router/internal/proxy/usage"

	"github.com/stretchr/testify/assert"
)

const conditionalModelsSubscriptionToken = "sk-ant-oat01-conditional-models"
const conditionalModelsCodexToken = "chatgpt-jwt-conditional-models"

func conditionalModelsContext(active, inactive []string) context.Context {
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, conditionalModelsSubscriptionToken)
	ctx = context.WithValue(ctx, InstallationSubscriptionPreferredModelsWhenActiveContextKey{}, active)
	return context.WithValue(ctx, InstallationSubscriptionPreferredModelsWhenInactiveContextKey{}, inactive)
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
	ctx := context.WithValue(context.Background(), InstallationPreferredModelsContextKey{}, []string{"grok-4.1-fast", "gpt-5.6-sol"})

	assert.Equal(t, []string{"grok-4.1-fast", "gpt-5.6-sol"}, (&Service{}).preferredModelsForRequest(ctx))
}
