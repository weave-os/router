package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/credentials"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/requestcontext"
)

func TestServedCredentialLogValues(t *testing.T) {
	service := &Service{engine: dispatch.NewEngine(nil), strategyServing: hmmPorts()}

	source, tier := service.servedCredentialLogValues(context.Background())
	assert.Empty(t, source, "deployment key")
	assert.Empty(t, tier)

	byok := requestcontext.WithCredentials(context.Background(), &requestcontext.Credentials{APIKey: []byte("sk-byok-0123456789abcdef"), Source: requestcontext.SourceBYOK})
	source, tier = service.servedCredentialLogValues(byok)
	assert.Equal(t, requestcontext.SourceBYOK, source)
	assert.Empty(t, tier)

	pooled := context.WithValue(context.Background(), credentials.ManagedSubscriptionUsageContextKey{}, &credentials.ManagedSubscriptionUsage{
		Served:                true,
		Finished:              true,
		CredentialSource:      requestcontext.SourceCodexSubscription,
		SubscriptionAccountID: "account",
		SubscriptionTier:      auth.SubscriptionTierShared,
	})
	source, tier = service.servedCredentialLogValues(pooled)
	assert.Equal(t, requestcontext.SourceCodexSubscription, source)
	assert.Equal(t, string(auth.SubscriptionTierShared), tier)
}
