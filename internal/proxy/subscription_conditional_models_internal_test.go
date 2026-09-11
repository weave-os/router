package proxy

import (
	"context"
	"net/http"
	"testing"
	"time"

	"weave-os/router/internal/proxy/usage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestWithSubscriptionStatePreferences_SelectsActiveList(t *testing.T) {
	svc := &Service{usageObserver: conditionalModelsObserver(usage.Snapshot{
		Primary: usage.Window{UsedPercent: 0.50, WindowMinutes: 300},
	})}

	ctx := svc.withSubscriptionStatePreferences(
		conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"}),
		http.Header{},
	)

	assert.Equal(t, []string{"active-model"}, subscriptionStatePreferredModelsFromContext(ctx))
}

func TestWithSubscriptionStatePreferences_SelectsInactiveListWhenExhausted(t *testing.T) {
	svc := &Service{usageObserver: conditionalModelsObserver(usage.Snapshot{
		Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080},
	})}

	ctx := svc.withSubscriptionStatePreferences(
		conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"}),
		http.Header{},
	)

	assert.Equal(t, []string{"inactive-model"}, subscriptionStatePreferredModelsFromContext(ctx))
}

func TestWithSubscriptionStatePreferences_EmptyInactiveListDoesNotRestrictEligibility(t *testing.T) {
	svc := &Service{usageObserver: conditionalModelsObserver(usage.Snapshot{
		Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080},
	})}

	ctx := svc.withSubscriptionStatePreferences(
		conditionalModelsContext([]string{"active-model"}, []string{}),
		http.Header{},
	)

	assert.Empty(t, subscriptionStatePreferredModelsFromContext(ctx))
	excluded := (&Service{availableModels: map[string]struct{}{"active-model": {}}}).excludedModelsForRequest(ctx)
	assert.NotContains(t, excluded, "active-model")
}

func TestWithSubscriptionStatePreferences_BothEmptyLeavesFeatureOff(t *testing.T) {
	svc := &Service{usageObserver: conditionalModelsObserver(usage.Snapshot{
		Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080},
	})}

	ctx := svc.withSubscriptionStatePreferences(
		conditionalModelsContext([]string{}, []string{}),
		http.Header{},
	)

	assert.Nil(t, ctx.Value(SubscriptionStatePreferredModelsContextKey{}))
}

func TestWithSubscriptionStatePreferences_ColdStartUsesActiveList(t *testing.T) {
	observer := usage.NewObserver([]byte("conditional-models-salt"), time.Minute, time.Now)
	svc := &Service{usageObserver: observer}

	ctx := svc.withSubscriptionStatePreferences(
		conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"}),
		http.Header{},
	)

	assert.Equal(t, []string{"active-model"}, subscriptionStatePreferredModelsFromContext(ctx))
}

func TestWithSubscriptionStatePreferences_DoesNotExcludeOtherProviders(t *testing.T) {
	svc := &Service{
		usageObserver:   conditionalModelsObserver(usage.Snapshot{Primary: usage.Window{UsedPercent: 0.1, WindowMinutes: 300}}),
		availableModels: map[string]struct{}{"active-model": {}, "other-model": {}},
	}
	ctx := svc.withSubscriptionStatePreferences(
		conditionalModelsContext([]string{"active-model"}, []string{"inactive-model"}),
		http.Header{},
	)

	excluded := svc.excludedModelsForRequest(ctx)
	assert.NotContains(t, excluded, "active-model")
	assert.NotContains(t, excluded, "other-model")
	assert.Equal(t, []string{"active-model"}, subscriptionStatePreferredModelsFromContext(ctx))
}

func TestWithSubscriptionStatePreferences_DoesNothingWithoutSubscription(t *testing.T) {
	observer := conditionalModelsObserver(usage.Snapshot{Secondary: usage.Window{UsedPercent: 1.0, WindowMinutes: 10080}})
	svc := &Service{usageObserver: observer}
	ctx := context.WithValue(context.Background(), InstallationSubscriptionPreferredModelsWhenActiveContextKey{}, []string{"active-model"})
	ctx = context.WithValue(ctx, InstallationSubscriptionPreferredModelsWhenInactiveContextKey{}, []string{"inactive-model"})

	out := svc.withSubscriptionStatePreferences(ctx, http.Header{})
	_, ok := out.Value(SubscriptionStatePreferredModelsContextKey{}).([]string)
	require.False(t, ok)
}

func TestWithSubscriptionStatePreferences_UsesCoveringSubscriptionOnly(t *testing.T) {
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
	messagesCtx := svc.withSubscriptionStatePreferences(ctx, http.Header{}, routePathMessages)
	assert.Equal(t, []string{"inactive-model"}, subscriptionStatePreferredModelsFromContext(messagesCtx))

	// Conversely, the active Codex credential is the one that matters on OpenAI.
	chatCtx := svc.withSubscriptionStatePreferences(ctx, http.Header{}, routePathChatCompletions)
	assert.Equal(t, []string{"active-model"}, subscriptionStatePreferredModelsFromContext(chatCtx))
}

func TestPreferredModelsForRequest_SeparatesInstallationAndSubscriptionPreferences(t *testing.T) {
	ctx := context.WithValue(context.Background(), InstallationPreferredModelsContextKey{}, []string{"grok-4.1-fast", "gpt-5.6-sol"})
	ctx = context.WithValue(ctx, SubscriptionStatePreferredModelsContextKey{}, []string{"gpt-5.6-sol", "claude-sonnet-5"})

	assert.Equal(t, []string{"grok-4.1-fast", "gpt-5.6-sol"}, (&Service{}).preferredModelsForRequest(ctx))
	assert.Equal(t, []string{"gpt-5.6-sol", "claude-sonnet-5"}, subscriptionStatePreferredModelsFromContext(ctx))
}
