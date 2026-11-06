package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/requestcontext"
)

func TestServedCredentialLogValues(t *testing.T) {
	service := &Service{}

	source, tier := service.servedCredentialLogValues(context.Background())
	assert.Empty(t, source, "deployment key")
	assert.Empty(t, tier)

	byok := requestcontext.WithCredentials(context.Background(), &Credentials{APIKey: []byte("sk-byok-0123456789abcdef"), Source: credSourceBYOK})
	source, tier = service.servedCredentialLogValues(byok)
	assert.Equal(t, credSourceBYOK, source)
	assert.Empty(t, tier)

	pooled := context.WithValue(context.Background(), ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{
		Served:                true,
		Finished:              true,
		CredentialSource:      credSourceCodexSubscription,
		SubscriptionAccountID: "account",
		SubscriptionTier:      auth.SubscriptionTierShared,
	})
	source, tier = service.servedCredentialLogValues(pooled)
	assert.Equal(t, credSourceCodexSubscription, source)
	assert.Equal(t, string(auth.SubscriptionTierShared), tier)
}
