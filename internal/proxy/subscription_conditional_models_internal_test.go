package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"workweave/router/internal/proxy/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const conditionalModelsSubscriptionToken = "sk-ant-oat01-conditional-models"
const conditionalModelsCodexToken = "chatgpt-jwt-conditional-models"

func conditionalModelsContext(active, inactive []string) context.Context {
	ctx := context.WithValue(context.Background(), AnthropicSubscriptionContextKey{}, conditionalModelsSubscriptionToken)
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenActiveContextKey{}, active)
	return context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, inactive)
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

func TestWithSubscriptionConditionalModels_SelectsActiveList(t *testing.T) {
	svc := &Service{usageObserver: conditionalModelsObserver(usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.50, WindowMinutes: 300},
	})}

	ctx := svc.withSubscriptionConditionalModels(
		conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"}),
		http.Header{},
	)

	assert.Equal(t, []string{"active-model"}, subscriptionConditionalModelsForRequest(ctx))
}

func TestWithSubscriptionConditionalModels_SelectsInactiveListWhenExhausted(t *testing.T) {
	svc := &Service{usageObserver: conditionalModelsObserver(usage.Snapshot{
		Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080},
	})}

	ctx := svc.withSubscriptionConditionalModels(
		conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"}),
		http.Header{},
	)

	assert.Equal(t, []string{"inactive-model"}, subscriptionConditionalModelsForRequest(ctx))
}

func TestWithSubscriptionConditionalModels_ColdStartUsesActiveList(t *testing.T) {
	observer := usage.NewObserver([]byte("conditional-models-salt"), time.Minute, time.Now)
	svc := &Service{usageObserver: observer}

	ctx := svc.withSubscriptionConditionalModels(
		conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"}),
		http.Header{},
	)

	assert.Equal(t, []string{"active-model"}, subscriptionConditionalModelsForRequest(ctx))
}

func TestWithSubscriptionConditionalModels_ExcludesModelsFromRouting(t *testing.T) {
	svc := &Service{
		usageObserver:   conditionalModelsObserver(usage.Snapshot{Primary: usage.Window{UsedPercent: 0.1, WindowMinutes: 300}}),
		availableModels: map[string]struct{}{"active-model": {}, "other-model": {}},
	}
	ctx := svc.withSubscriptionConditionalModels(
		conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"}),
		http.Header{},
	)

	excluded := svc.excludedModelsForRequest(ctx)
	assert.NotContains(t, excluded, "active-model")
	assert.Contains(t, excluded, "other-model")
}

func TestWithSubscriptionConditionalModels_DoesNothingWithoutSubscription(t *testing.T) {
	observer := conditionalModelsObserver(usage.Snapshot{Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}})
	svc := &Service{usageObserver: observer}
	ctx := context.WithValue(context.Background(), InstallationSubscriptionModelsWhenActiveContextKey{}, []string{"active-model"})
	ctx = context.WithValue(ctx, InstallationSubscriptionModelsWhenInactiveContextKey{}, []string{"inactive-model"})

	out := svc.withSubscriptionConditionalModels(ctx, http.Header{})
	_, ok := out.Value(InstallationSubscriptionConditionalModelsContextKey{}).([]string)
	require.False(t, ok)
}

func TestWithSubscriptionConditionalModels_UsesCoveringSubscriptionOnly(t *testing.T) {
	observer := conditionalModelsObserverFor(conditionalModelsSubscriptionToken, usage.Snapshot{
		Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080},
	})
	observer.Record(observer.Key([]byte(conditionalModelsCodexToken)), usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.1, WindowMinutes: 300},
	})
	svc := &Service{usageObserver: observer}
	ctx := conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"})
	ctx = context.WithValue(ctx, OpenAISubscriptionContextKey{}, conditionalModelsCodexToken)
	ctx = context.WithValue(ctx, OpenAIAccountIDContextKey{}, "acct_conditional-models")

	// The unrelated active Codex credential must not mask the exhausted Claude
	// credential on the Anthropic Messages endpoint.
	messagesCtx := svc.withSubscriptionConditionalModels(ctx, http.Header{}, routePathMessages)
	assert.Equal(t, []string{"inactive-model"}, subscriptionConditionalModelsForRequest(messagesCtx))

	// Conversely, the active Codex credential is the one that matters on OpenAI.
	chatCtx := svc.withSubscriptionConditionalModels(ctx, http.Header{}, routePathChatCompletions)
	assert.Equal(t, []string{"active-model"}, subscriptionConditionalModelsForRequest(chatCtx))
}
