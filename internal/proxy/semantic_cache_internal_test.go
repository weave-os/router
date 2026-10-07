package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

func TestSemanticCacheStoreRejectsChangedCredential(t *testing.T) {
	service := &Service{}
	decision := router.Decision{Model: "claude-haiku-4-5", Provider: providers.ProviderAnthropic}
	original := context.WithValue(context.Background(), APIKeyIDContextKey{}, "verified-key")
	original = requestcontext.WithCredentials(original, &Credentials{Source: credSourceBYOK, PrincipalID: "original-account"})
	provenance := service.semanticCacheProvenance(original, decision)
	assert.True(t, provenance.Valid())
	assert.True(t, service.semanticCacheStoreAllowed(original, provenance, decision, decision.Provider))
	changed := context.WithValue(original, ManagedSubscriptionUsageContextKey{}, &ManagedSubscriptionUsage{
		Finished: true, WinningCredentials: &Credentials{Source: credSourceBYOK, PrincipalID: "different-account"},
	})
	assert.False(t, service.semanticCacheStoreAllowed(changed, provenance, decision, decision.Provider))
}
