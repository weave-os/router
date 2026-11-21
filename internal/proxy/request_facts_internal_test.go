package proxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
)

func TestRequestFactsFromReadsTheBoundaryKeys(t *testing.T) {
	installation := uuid.New()
	keys := []*auth.ExternalAPIKey{{ID: "key-1", Provider: providers.ProviderAnthropic}}
	client := requestcontext.ClientIdentity{SessionID: "session-1", ClientApp: requestcontext.ClientAppCodex}
	headers := http.Header{"Authorization": []string{"Bearer inbound"}}
	ctx := observability.WithRequestID(context.Background(), "req-1")
	ctx = context.WithValue(ctx, APIKeyIDContextKey{}, "api-key-1")
	ctx = context.WithValue(ctx, ExternalIDContextKey{}, "external-1")
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, installation.String())
	ctx = context.WithValue(ctx, credentials.ExternalAPIKeysContextKey{}, keys)
	ctx = context.WithValue(ctx, credentials.AnthropicSubscriptionContextKey{}, "claude-token")
	ctx = context.WithValue(ctx, credentials.OpenAISubscriptionContextKey{}, "codex-token")
	ctx = context.WithValue(ctx, credentials.OpenAIAccountIDContextKey{}, "account-1")
	ctx = requestcontext.WithClientIdentity(ctx, client)

	facts := requestFactsFrom(ctx, headers)

	assert.Equal(t, requestcontext.RequestFacts{
		RequestID:             "req-1",
		APIKeyID:              "api-key-1",
		ExternalID:            "external-1",
		InstallationID:        installation,
		Client:                client,
		Headers:               headers,
		ExternalKeys:          keys,
		AnthropicSubscription: "claude-token",
		OpenAISubscription:    "codex-token",
		OpenAIAccountID:       "account-1",
	}, facts)
	assert.True(t, facts.RouterKeyed())
}

func TestRequestFactsFromWithoutRouterKey(t *testing.T) {
	invalid := context.WithValue(context.Background(), InstallationIDContextKey{}, "not-a-uuid")
	for name, ctx := range map[string]context.Context{"absent": context.Background(), "unparseable installation": invalid} {
		t.Run(name, func(t *testing.T) {
			facts := requestFactsFrom(ctx, nil)
			assert.False(t, facts.RouterKeyed())
			assert.NotEmpty(t, facts.RequestID, "a request outside the HTTP middleware still gets a correlation id")
		})
	}
}
