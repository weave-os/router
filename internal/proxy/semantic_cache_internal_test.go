package proxy

import (
	"context"
	"testing"
	"weave-os/router/internal/router/catalog"

	"github.com/stretchr/testify/assert"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

func TestSemanticCacheStoreRejectsChangedCredential(t *testing.T) {
	service := &Service{}
	decision := router.Decision{Model: catalog.ModelIDClaudeHaiku45.String(), Provider: providers.ProviderAnthropic}
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

func TestSemanticCacheManagedIdentityRequiresServingTuple(t *testing.T) {
	service := &Service{}
	decision := router.Decision{Model: catalog.ModelIDClaudeHaiku45.String(), Provider: providers.ProviderAnthropic}
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "verified-key")
	ctx = requestcontext.WithCredentials(ctx, &Credentials{Source: credSourceBYOK, PrincipalID: "account"})
	identity := requestcontext.ServingIdentity{CredentialIdentity: "subject", ReleaseID: "release", BindingID: "binding"}
	assert.True(t, service.semanticCacheProvenance(requestcontext.WithServingIdentity(ctx, identity), decision).Valid())
	for name, remove := range map[string]func(*requestcontext.ServingIdentity){
		"release": func(i *requestcontext.ServingIdentity) { i.ReleaseID = "" },
		"binding": func(i *requestcontext.ServingIdentity) { i.BindingID = "" },
		"both":    func(i *requestcontext.ServingIdentity) { i.ReleaseID = ""; i.BindingID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			partial := identity
			remove(&partial)
			assert.False(t, service.semanticCacheProvenance(requestcontext.WithServingIdentity(ctx, partial), decision).Valid())
		})
	}
}

func TestSemanticCacheOriginalRequirementsAndAutomaticExclusionsBypass(t *testing.T) {
	service := &Service{}
	assert.True(t, service.semanticCacheRequestAllowed(context.Background(), router.Request{}))
	for _, requirements := range []router.TranslationRequirements{{StructuredOutput: true}, {UsageDetail: true}, {FunctionTools: true}} {
		ctx := context.WithValue(context.Background(), responsesRequirementsContextKey{}, requirements)
		assert.False(t, service.semanticCacheRequestAllowed(ctx, router.Request{}))
	}
	service.WithGlobalAutomaticExclusions(&stubGlobalExclusionStore{byModel: map[string]string{catalog.ModelIDClaudeOpus48.String(): "disabled"}})
	assert.False(t, service.semanticCacheRequestAllowed(context.Background(), router.Request{}))
}
